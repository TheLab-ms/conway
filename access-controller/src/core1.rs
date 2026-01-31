//! Core 1: WiFi, Conway sync, and HTTP server.
//!
//! This core handles network operations:
//! - WiFi connection management with automatic reconnection
//! - DHCP client for IP address acquisition
//! - Periodic synchronization with Conway server
//! - HTTP server for admin interface and remote unlock

use core::sync::atomic::Ordering;
use esp_radio::wifi::{ClientConfig, ModeConfig, WifiController};
use smoltcp::iface::{Config, Interface, SocketSet, SocketStorage};
use smoltcp::socket::dhcpv4::{Event as DhcpEvent, Socket as DhcpSocket};
use smoltcp::socket::tcp::{Socket as TcpSocket, SocketBuffer};
use smoltcp::time::Instant as SmoltcpInstant;
use smoltcp::wire::{HardwareAddress, IpCidr};

use crate::conway::{handle_http_server, sync_with_conway};
use crate::shared::SHARED;
use crate::storage::Storage;
use crate::{SYNC_REQUEST, WIFI_CONTROLLER, WIFI_DEVICE};

// Timing constants
const SYNC_INTERVAL_MS: u64 = 10_000;

// WiFi connection constants
const WIFI_CONNECT_TIMEOUT_MS: u64 = 10_000;
const WIFI_MAX_RETRIES_BEFORE_RESET: u8 = 3;
const WIFI_RESET_COOLDOWN_MS: u64 = 5_000;

/// Core 1 main loop: WiFi, sync, and HTTP server.
pub fn run() -> ! {
    log::info!("Core 1 started (admin tasks)");

    // Give Core 0 time to finish setup
    esp_radio_rtos_driver::usleep(100_000);

    // Take WiFi handles from statics
    let (mut wifi_device, mut wifi_controller) = critical_section::with(|cs| {
        let device = WIFI_DEVICE.borrow_ref_mut(cs).take().unwrap();
        let controller = WIFI_CONTROLLER.borrow_ref_mut(cs).take().unwrap();
        (device, controller)
    });

    // Load configuration from flash (or use defaults if not present)
    let config = crate::storage::Config::get();
    log::info!(
        "config: ssid={}, host={}:{}",
        config.ssid,
        config.conway_host,
        config.conway_port
    );

    // Initialize storage and warm the cache
    let mut storage = Storage::new();
    let cached_fobs = storage.load_fobs();
    let cached_etag = storage.load_etag();
    if !cached_fobs.is_empty() {
        log::info!("storage: loaded {} fobs from cache", cached_fobs.len());
        SHARED.update_fobs(&cached_fobs);
    }

    crate::heap_debug::log_heap_stats("core1:after_storage_init");

    // Conway sync state
    let mut etag = cached_etag;

    // WiFi connection state
    let mut wifi_state = WifiState::new();

    // Create smoltcp interface
    let mac = esp_radio::wifi::sta_mac();
    let hw_addr = HardwareAddress::Ethernet(smoltcp::wire::EthernetAddress(mac));
    let iface_config = Config::new(hw_addr);
    let mut iface = Interface::new(iface_config, &mut wifi_device, SmoltcpInstant::ZERO);

    // Socket storage for DHCP, client (Conway sync), and server (HTTP admin)
    let mut socket_storage: [SocketStorage; 8] = Default::default();
    let mut sockets = SocketSet::new(&mut socket_storage[..]);

    // Pre-allocate socket buffers
    static mut SERVER_RX: [u8; 512] = [0; 512];
    static mut SERVER_TX: [u8; 512] = [0; 512];

    // Create DHCP client socket
    let dhcp_socket = DhcpSocket::new();
    let dhcp_handle = sockets.add(dhcp_socket);

    // Create listening socket for HTTP server
    let server_rx = SocketBuffer::new(unsafe { &mut SERVER_RX[..] });
    let server_tx = SocketBuffer::new(unsafe { &mut SERVER_TX[..] });
    let mut server_socket = TcpSocket::new(server_rx, server_tx);
    server_socket.listen(80).ok();
    let server_handle = sockets.add(server_socket);

    // Timing state
    let mut last_sync: u64 = 0;
    let mut last_log: u64 = 0;
    let mut last_wdt_feed: u64 = 0;
    let mut ip_configured = false;

    loop {
        let now_ms = esp_hal::time::Instant::now().duration_since_epoch().as_millis();
        let smoltcp_now = SmoltcpInstant::from_millis(now_ms as i64);

        // 1. Maintain WiFi connection
        wifi_state.maintain(&mut wifi_controller, &config, now_ms, &mut ip_configured);

        if wifi_state.connected {
            // Poll the network interface
            iface.poll(smoltcp_now, &mut wifi_device, &mut sockets);

            // Handle DHCP events
            let dhcp_socket = sockets.get_mut::<DhcpSocket>(dhcp_handle);
            if let Some(event) = dhcp_socket.poll() {
                match event {
                    DhcpEvent::Configured(dhcp_config) => {
                        let addr = dhcp_config.address;
                        iface.update_ip_addrs(|addrs| {
                            addrs.clear();
                            addrs.push(IpCidr::Ipv4(addr)).ok();
                        });
                        if let Some(router) = dhcp_config.router {
                            iface.routes_mut().add_default_ipv4_route(router).ok();
                        }
                        log::info!("dhcp: IP={}", addr);
                        crate::heap_debug::log_heap_stats("core1:dhcp_configured");
                        ip_configured = true;
                    }
                    DhcpEvent::Deconfigured => {
                        log::warn!("dhcp: deconfigured");
                        iface.update_ip_addrs(|addrs| addrs.clear());
                        ip_configured = false;
                    }
                }
            }

            // Only do network operations once we have an IP
            if ip_configured {
                // 2. Check for sync request from Core 0 (on denied access)
                let sync_requested = SYNC_REQUEST.load(Ordering::Acquire);

                // 3. Periodic sync with Conway server (or on-demand)
                if sync_requested || (now_ms - last_sync >= SYNC_INTERVAL_MS) {
                    last_sync = now_ms;
                    crate::heap_debug::log_heap_stats("core1:before_sync");
                    sync_with_conway(
                        &mut iface,
                        &mut wifi_device,
                        &mut sockets,
                        &config,
                        &mut storage,
                        &mut etag,
                    );
                    crate::heap_debug::log_heap_stats("core1:after_sync");
                    // Clear sync request flag after completion
                    if sync_requested {
                        SYNC_REQUEST.store(false, Ordering::Release);
                    }
                }

                // 4. Handle HTTP server requests
                handle_http_server(&mut sockets, server_handle, &etag);
            }
        }

        // 5. Status logging
        if now_ms - last_log > 30_000 {
            last_log = now_ms;
            let fob_count = SHARED.fob_count.load(Ordering::Relaxed);
            log::info!(
                "status: {} fobs, wifi={}",
                fob_count,
                if wifi_state.connected { "up" } else { "down" }
            );
        }

        // 6. Feed watchdog from Core 1 as backup
        // Core 0 feeds every 10s, but if Core 1 operations block the system
        // (e.g., flash writes, WiFi driver issues), this provides redundancy
        if now_ms - last_wdt_feed >= 5000 {
            last_wdt_feed = now_ms;
            crate::feed_watchdog();
        }

        // Yield to scheduler - this is critical for WiFi to work!
        // usleep allows other RTOS tasks (like the WiFi driver) to run
        esp_radio_rtos_driver::usleep(10_000);
    }
}

/// WiFi connection state machine.
struct WifiState {
    connected: bool,
    connecting: bool,
    connect_started: u64,
    retry_count: u8,
    cooldown_until: u64,
}

impl WifiState {
    fn new() -> Self {
        Self {
            connected: false,
            connecting: false,
            connect_started: 0,
            retry_count: 0,
            cooldown_until: 0,
        }
    }

    fn maintain(
        &mut self,
        controller: &mut WifiController<'_>,
        config: &crate::storage::Config,
        now_ms: u64,
        ip_configured: &mut bool,
    ) {
        use alloc::string::ToString;

        if !self.connected {
            // Check if we're in cooldown after a radio reset
            if now_ms < self.cooldown_until {
                // Still cooling down, skip connection attempt
            } else if !self.connecting {
                log::info!("wifi: connecting to {}", config.ssid);

                // Ensure WiFi is stopped before (re)configuring to avoid ESP-IDF errors
                let _ = controller.stop();
                esp_radio_rtos_driver::usleep(10_000);

                let client_config = ClientConfig::default()
                    .with_ssid(config.ssid.to_string())
                    .with_password(config.password.to_string());
                if let Err(e) = controller.set_config(&ModeConfig::Client(client_config)) {
                    log::error!("wifi: set_config failed: {:?}", e);
                }
                if let Err(e) = controller.start() {
                    log::error!("wifi: start failed: {:?}", e);
                }
                if let Err(e) = controller.connect() {
                    log::error!("wifi: connect failed: {:?}", e);
                }
                self.connecting = true;
                self.connect_started = now_ms;
            } else {
                // Currently connecting - check for success or timeout
                if controller.is_connected().unwrap_or(false) {
                    log::info!("wifi: connected");
                    self.connected = true;
                    self.connecting = false;
                    self.retry_count = 0;
                } else if now_ms - self.connect_started > WIFI_CONNECT_TIMEOUT_MS {
                    // Connection attempt timed out
                    self.retry_count = self.retry_count.saturating_add(1);
                    log::warn!(
                        "wifi: connection timeout (attempt {}/{})",
                        self.retry_count,
                        WIFI_MAX_RETRIES_BEFORE_RESET
                    );

                    if self.retry_count >= WIFI_MAX_RETRIES_BEFORE_RESET {
                        // Power-cycle the radio to recover from potential stuck state
                        log::warn!("wifi: power-cycling radio after {} failures", self.retry_count);
                        if let Err(e) = controller.disconnect() {
                            log::warn!("wifi: disconnect failed: {:?}", e);
                        }
                        if let Err(e) = controller.stop() {
                            log::warn!("wifi: stop failed: {:?}", e);
                        }
                        crate::feed_watchdog(); // Feed before blocking sleep
                        esp_radio_rtos_driver::usleep(100_000);
                        self.retry_count = 0;
                        self.cooldown_until = now_ms + WIFI_RESET_COOLDOWN_MS;
                    }

                    self.connecting = false;
                }
            }
        } else if !controller.is_connected().unwrap_or(false) {
            // Was connected but now disconnected
            log::warn!("wifi: disconnected, will power-cycle radio");
            self.connected = false;
            self.connecting = false;
            *ip_configured = false;

            // Power-cycle the radio on unexpected disconnect
            if let Err(e) = controller.disconnect() {
                log::warn!("wifi: disconnect failed: {:?}", e);
            }
            if let Err(e) = controller.stop() {
                log::warn!("wifi: stop failed: {:?}", e);
            }
            crate::feed_watchdog(); // Feed before blocking sleep
            esp_radio_rtos_driver::usleep(100_000);
            self.cooldown_until = now_ms + WIFI_RESET_COOLDOWN_MS;
        }
    }
}
