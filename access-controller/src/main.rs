//! Conway Access Controller - ESP32 firmware with Embassy async.
//!
//! Single-core async architecture using Embassy for:
//! - Wiegand RFID reading (async GPIO edge detection)
//! - WiFi connectivity
//! - Conway API sync

#![no_std]
#![no_main]
#![allow(static_mut_refs)] // Required for ESP32 heap initialization

use esp_bootloader_esp_idf::esp_app_desc;
esp_app_desc!();

mod dhcp_server;
mod device_key;
mod dns_server;
mod fob_store;
mod http;
mod ota;
mod settings;
mod sync;
mod wiegand;

extern crate alloc;

use alloc::boxed::Box;
use alloc::format;
use core::mem::MaybeUninit;
use embassy_net::{Config as NetConfig, Stack, StackResources, StaticConfigV4};
use embassy_sync::blocking_mutex::raw::CriticalSectionRawMutex;
use embassy_sync::channel::Channel;
use embassy_sync::mutex::Mutex;
use embassy_sync::signal::Signal;
use embassy_time::{Duration, Timer};
use esp_alloc as _;
use esp_hal::clock::CpuClock;
use esp_hal::gpio::{Input, InputConfig, Level, Output, OutputConfig, Pull};
use esp_hal::time::Duration as HalDuration;
use esp_hal::timer::timg::{MwdtStage, MwdtStageAction, TimerGroup, Wdt};
use esp_println::logger::init_logger;
use esp_radio::wifi::{
    AccessPointConfig, AuthMethod, ClientConfig, Config as WifiConfig, ModeConfig, WifiController,
};
use heapless::String as HString;
use static_cell::StaticCell;

use crate::fob_store::{LocalFob, MAX_LOCAL_FOBS};
use crate::settings::Settings;
use crate::sync::{AccessEvent, EventBuffer};
use crate::wiegand::{Wiegand, WiegandRead};
use access_controller::core::{AccessCore, CardRead, Effect, Input as CoreInput, Outcome};

// Configuration constants
pub const MAX_FOBS: usize = 512;

/// Runtime device mode chosen at boot. Determines which WiFi interface
/// embassy-net is bound to and whether DHCP/DNS servers run.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum DeviceMode {
    /// Joined a configured WiFi network. Normal operation.
    Station,
    /// First-boot / post-factory-reset onboarding. Broadcasts an open
    /// SSID, runs DHCP+DNS, captive-portal HTTP redirects.
    Onboarding,
}

/// Live settings + current mode, used by every task that needs them.
pub struct RuntimeConfig {
    pub settings: Mutex<CriticalSectionRawMutex, Settings>,
    pub mode: DeviceMode,
    /// SSID we are broadcasting in onboarding mode (for the UI to show).
    pub ap_ssid: HString<32>,
}

static CONFIG: StaticCell<RuntimeConfig> = StaticCell::new();

// Channel for Wiegand reads -> access control task
static WIEGAND_CHANNEL: Channel<CriticalSectionRawMutex, WiegandRead, 4> = Channel::new();

// Event buffer with peek/commit semantics for reliable delivery
pub static EVENT_BUFFER: EventBuffer = EventBuffer::new();

// Signal for on-demand sync (when access denied)
pub static SYNC_SIGNAL: Signal<CriticalSectionRawMutex, ()> = Signal::new();

// Signal sent when sync completes (success or failure)
pub static SYNC_COMPLETE: Signal<CriticalSectionRawMutex, ()> = Signal::new();

// Signal for door unlock (after successful auth)
pub static DOOR_SIGNAL: Signal<CriticalSectionRawMutex, ()> = Signal::new();

// Signal raised by `POST /unlock` to request a manual door pulse.
pub static MANUAL_UNLOCK: Signal<CriticalSectionRawMutex, ()> = Signal::new();

/// Sentinel `fob` value logged for web-UI-initiated manual unlocks so the
/// Conway audit trail can distinguish them from real card swipes.
///
/// Chosen outside the Wiegand-26 card-number range: Wiegand-26 frames a
/// 24-bit card number (bits 1-24) with two parity bits, so any value
/// >= 2^24 cannot collide with a real swipe. `u32::MAX` is used for
/// obviousness in logs. The previous sentinel of `0` was ambiguous
/// because fob ID 0 is a legal Wiegand-26 transmission.
pub const MANUAL_UNLOCK_FOB: u32 = u32::MAX;

/// Most recent door event (swipe or manual unlock). Rendered on the
/// HTTP status page; not persisted across reboots.
#[derive(Debug, Clone, Copy)]
pub struct LastSwipe {
    pub fob: u32,
    pub allowed: bool,
    pub at_uptime_ms: u64,
    pub manual: bool,
}

/// Reader-side user feedback to play after an access decision.
#[derive(Debug, Clone, Copy)]
pub enum AccessOutcome {
    Granted,
    Denied,
}

// Signal to drive reader LED/beeper after each access decision.
pub static READER_FEEDBACK: Signal<CriticalSectionRawMutex, AccessOutcome> = Signal::new();

// Signal to request watchdog feed (proves access_task is responsive)
pub static WATCHDOG_FEED: Signal<CriticalSectionRawMutex, ()> = Signal::new();

// Static cells for 'static lifetime requirements
static FOBS: StaticCell<Mutex<CriticalSectionRawMutex, heapless::Vec<u32, MAX_FOBS>>> =
    StaticCell::new();
/// Locally-managed fob list, edited via the HTTP UI and persisted in the
/// `fobs` partition. Always wins over the Conway-synced cache, and is the
/// only authority when running standalone (no Conway host configured).
static LOCAL_FOBS: StaticCell<
    Mutex<CriticalSectionRawMutex, heapless::Vec<LocalFob, MAX_LOCAL_FOBS>>,
> = StaticCell::new();
static ETAG: StaticCell<Mutex<CriticalSectionRawMutex, HString<64>>> = StaticCell::new();
static LAST_SWIPE: StaticCell<Mutex<CriticalSectionRawMutex, Option<LastSwipe>>> =
    StaticCell::new();
static STACK_RESOURCES: StaticCell<StackResources<8>> = StaticCell::new();
static STACK: StaticCell<Stack<'static>> = StaticCell::new();

// Type alias for the watchdog timer
type WdtType = Wdt<esp_hal::peripherals::TIMG1<'static>>;
static WDT: StaticCell<Mutex<CriticalSectionRawMutex, WdtType>> = StaticCell::new();

#[esp_rtos::main]
async fn main(spawner: embassy_executor::Spawner) {
    init_logger(log::LevelFilter::Info);
    log::info!("Conway Access Controller starting...");

    // Initialize heap
    const HEAP_SIZE: usize = 72 * 1024;
    static mut HEAP: MaybeUninit<[u8; HEAP_SIZE]> = MaybeUninit::uninit();
    unsafe {
        esp_alloc::HEAP.add_region(esp_alloc::HeapRegion::new(
            HEAP.as_mut_ptr() as *mut u8,
            HEAP_SIZE,
            esp_alloc::MemoryCapability::Internal.into(),
        ));
    }

    // Hardware init
    let hal_config = esp_hal::Config::default().with_cpu_clock(CpuClock::max());
    let peripherals = esp_hal::init(hal_config);

    // Start the esp-rtos scheduler with a timer - MUST happen before esp_radio::init()
    // The scheduler requires a hardware timer for task scheduling and time management.
    let timg0 = TimerGroup::new(peripherals.TIMG0);
    esp_rtos::start(timg0.timer0);

    // Initialize hardware watchdog timer using TIMG1
    // The watchdog will reset the system if not fed within 30 seconds.
    // Feeding is done by access_task to prove it's not blocked.
    let timg1 = TimerGroup::new(peripherals.TIMG1);
    let mut wdt = timg1.wdt;
    wdt.set_timeout(MwdtStage::Stage0, HalDuration::from_secs(30));
    wdt.set_stage_action(MwdtStage::Stage0, MwdtStageAction::ResetSystem);
    wdt.enable();
    let wdt = WDT.init(Mutex::new(wdt));
    log::info!("watchdog: initialized with 30s timeout");

    // Load persisted settings. Empty / missing => first boot or post-
    // factory-reset, so we come up in AP onboarding mode.
    //
    // NOTE: `settings::load()` requires the per-device key, which in turn
    // requires `esp_radio::init()` to have been called first (so the MAC,
    // used as HKDF salt, is reliably readable). We therefore initialize
    // the radio stack first, then derive the key, then load settings.
    // This is a deliberate reorder vs. previous firmware where settings
    // were loaded before radio init.

    // Initialize esp-radio for WiFi.
    // NOTE: esp_rtos::start() must be called before this (done above
    // after peripherals init).
    let esp_radio_ctrl = esp_radio::init().unwrap();

    // Derive per-device storage key from eFuse BLOCK3. If unprovisioned,
    // this logs a loud warning and `settings::load()`/`fob_store::load()`
    // both return empty -- the device falls through to compile-time env
    // defaults / onboarding mode just as on a freshly-wiped unit. See
    // `src/device_key.rs` for the full threat model + provisioning steps.
    device_key::init();
    if !device_key::is_ready() {
        log::warn!(
            "boot: device is UNPROVISIONED -- at-rest encryption disabled. \
             Persistent storage will not be written. \
             Run tools/provision-device-key.sh to enable."
        );
    }

    let loaded = settings::load().unwrap_or_else(Settings::defaults_from_env);
    let mode = if loaded.is_provisioned() {
        DeviceMode::Station
    } else {
        DeviceMode::Onboarding
    };
    log::info!(
        "config: mode={:?} ssid={} host={} port={}",
        mode,
        if loaded.ssid.is_empty() {
            "<unset>"
        } else {
            loaded.ssid.as_str()
        },
        loaded.conway_host_str(),
        loaded.conway_port,
    );

    // Initialize shared state (fobs and etag start empty, populated by sync)
    let fobs = FOBS.init(Mutex::new(heapless::Vec::new()));
    let etag = ETAG.init(Mutex::new(HString::new()));
    let last_swipe = LAST_SWIPE.init(Mutex::new(None));

    // Load locally-managed fobs from flash. Empty on first boot / after a
    // factory reset / when the device is unprovisioned.
    let local_fobs_loaded = fob_store::load();
    log::info!(
        "storage: loaded {} local fobs from flash",
        local_fobs_loaded.len()
    );
    let local_fobs = LOCAL_FOBS.init(Mutex::new(local_fobs_loaded));

    log::info!("storage: fob cache initialized (empty, will sync from server)");

    // Leak the radio controller to get 'static lifetime before creating WiFi.
    let esp_radio_ctrl: &'static _ = Box::leak(Box::new(esp_radio_ctrl));

    let wifi_config = WifiConfig::default();
    let (wifi_controller, interfaces) =
        esp_radio::wifi::new(esp_radio_ctrl, peripherals.WIFI, wifi_config).unwrap();

    // Setup Embassy network stack. STA mode runs DHCP client; AP mode
    // uses our static 192.168.4.1/24 + our DHCP server.
    let stack_resources = STACK_RESOURCES.init(StackResources::new());

    // MAC-derived RNG seed (also drives the onboarding SSID).
    let mac = esp_radio::wifi::sta_mac();
    let seed = u64::from_le_bytes([mac[0], mac[1], mac[2], mac[3], mac[4], mac[5], 0, 0]);
    log::info!("rng: seed={:016X}", seed);

    // Build the AP SSID up front so both the WiFi task and the UI can
    // see it (the latter via RuntimeConfig).
    let ap_ssid_str = format!(
        "conway-{:02X}{:02X}{:02X}",
        mac[3], mac[4], mac[5]
    );
    let mut ap_ssid_hs: HString<32> = HString::new();
    let _ = ap_ssid_hs.push_str(&ap_ssid_str);
    log::info!("onboarding: AP SSID = {}", ap_ssid_str);

    // Pick the right interface for the chosen mode and produce the
    // matching IP config.
    let (wifi_device, net_config) = match mode {
        DeviceMode::Station => {
            let dev: esp_radio::wifi::WifiDevice<'static> =
                unsafe { core::mem::transmute(interfaces.sta) };
            (dev, NetConfig::dhcpv4(Default::default()))
        }
        DeviceMode::Onboarding => {
            let dev: esp_radio::wifi::WifiDevice<'static> =
                unsafe { core::mem::transmute(interfaces.ap) };
            let static_cfg = StaticConfigV4 {
                address: embassy_net::Ipv4Cidr::new(
                    embassy_net::Ipv4Address::new(
                        dhcp_server::AP_IP[0],
                        dhcp_server::AP_IP[1],
                        dhcp_server::AP_IP[2],
                        dhcp_server::AP_IP[3],
                    ),
                    24,
                ),
                gateway: Some(embassy_net::Ipv4Address::new(
                    dhcp_server::AP_IP[0],
                    dhcp_server::AP_IP[1],
                    dhcp_server::AP_IP[2],
                    dhcp_server::AP_IP[3],
                )),
                dns_servers: heapless::Vec::new(),
            };
            (dev, NetConfig::ipv4_static(static_cfg))
        }
    };
    let wifi_controller: WifiController<'static> =
        unsafe { core::mem::transmute(wifi_controller) };

    let (stack, runner) = embassy_net::new(wifi_device, net_config, stack_resources, seed);
    let stack: &'static Stack<'static> = STACK.init(stack);

    // Publish shared runtime config (settings + mode) for all tasks.
    let rt_config = CONFIG.init(RuntimeConfig {
        settings: Mutex::new(loaded.clone()),
        mode,
        ap_ssid: ap_ssid_hs.clone(),
    });

    // Setup GPIO pins (see HARDWARE.md for full pin map).
    //
    // Wiegand inputs: driven by SN74LVC2G17 non-inverting Schmitt buffer
    // (3V3 output, actively driven), so no internal pull is required.
    let d0 = Input::new(
        peripherals.GPIO25,
        InputConfig::default().with_pull(Pull::None),
    );
    let d1 = Input::new(
        peripherals.GPIO33,
        InputConfig::default().with_pull(Pull::None),
    );

    // Output drivers: SS8050 NPN low-side switches, so GPIO HIGH = load energized.
    let door = Output::new(peripherals.GPIO12, Level::Low, OutputConfig::default());
    let reader_led = Output::new(peripherals.GPIO26, Level::Low, OutputConfig::default());
    let reader_beep = Output::new(peripherals.GPIO27, Level::Low, OutputConfig::default());
    let status_led = Output::new(peripherals.GPIO14, Level::Low, OutputConfig::default());

    // CONFIG button: external 10k pull-up to 3V3 + 100nF debounce cap.
    // GPIO35 is input-only on the ESP32, so we rely on the external pull-up.
    let config_btn = Input::new(
        peripherals.GPIO35,
        InputConfig::default().with_pull(Pull::None),
    );

    // Create Wiegand reader
    let wiegand = Wiegand::new(d0, d1);

    // Spawn tasks
    spawner.spawn(net_task(runner)).unwrap();
    spawner.spawn(wifi_task(wifi_controller, rt_config)).unwrap();
    spawner.spawn(wiegand_task(wiegand)).unwrap();
    spawner
        .spawn(access_task(fobs, local_fobs, last_swipe, wdt, rt_config))
        .unwrap();
    spawner.spawn(door_task(door)).unwrap();
    spawner
        .spawn(reader_feedback_task(reader_led, reader_beep))
        .unwrap();
    spawner
        .spawn(status_and_config_task(status_led, config_btn, stack))
        .unwrap();
    // Sync task only makes sense in station mode AND when a Conway host
    // is configured. Standalone mode (no host) skips it entirely.
    let conway_enabled = {
        let s = rt_config.settings.lock().await;
        s.conway_enabled()
    };
    if mode == DeviceMode::Station && conway_enabled {
        spawner.spawn(sync_task(stack, fobs, etag, rt_config)).unwrap();
    } else if mode == DeviceMode::Station {
        log::info!("sync: disabled (standalone mode, no Conway host configured)");
    }
    spawner
        .spawn(http::http_server_task(
            stack, fobs, local_fobs, etag, last_swipe, rt_config,
        ))
        .unwrap();
    spawner.spawn(watchdog_feed_task()).unwrap();

    // Onboarding-only services.
    if mode == DeviceMode::Onboarding {
        spawner.spawn(dhcp_server::dhcp_server_task(stack)).unwrap();
        spawner.spawn(dns_server::dns_server_task(stack)).unwrap();
    }
}

/// Runs the embassy-net stack, processing incoming/outgoing packets and maintaining network state.
/// Must run continuously for the network stack to function.
#[embassy_executor::task]
async fn net_task(mut runner: embassy_net::Runner<'static, esp_radio::wifi::WifiDevice<'static>>) {
    runner.run().await;
}

/// WiFi connection management.
///
/// In `Station` mode, retries connection every 5 seconds. In `Onboarding`
/// mode brings up the AP exactly once and then idles - the AP is a
/// background service that doesn't need re-application unless the radio
/// firmware crashes (in which case the hardware watchdog will reboot us).
#[embassy_executor::task]
async fn wifi_task(mut controller: WifiController<'static>, rt: &'static RuntimeConfig) {
    use alloc::string::ToString;

    match rt.mode {
        DeviceMode::Onboarding => {
            let ssid = rt.ap_ssid.as_str().to_string();
            log::info!("wifi: starting AP SSID={} (open)", ssid);

            let ap_config = AccessPointConfig::default()
                .with_ssid(ssid.as_str().into())
                .with_auth_method(AuthMethod::None)
                .with_channel(6)
                .with_max_connections(4);

            if let Err(e) = controller.set_config(&ModeConfig::AccessPoint(ap_config)) {
                log::error!("wifi: AP set_config failed: {:?}", e);
            }
            if let Err(e) = controller.start() {
                log::error!("wifi: AP start failed: {:?}", e);
            }
            log::info!("wifi: AP up");

            // Idle - the AP runs autonomously in the radio.
            loop {
                Timer::after(Duration::from_secs(60)).await;
            }
        }
        DeviceMode::Station => {
            // Snapshot the credentials once at boot. Changes go through
            // settings::save() + software_reset() so we don't need to
            // hot-reload here.
            let (ssid, password) = {
                let s = rt.settings.lock().await;
                (s.ssid.clone(), s.password.clone())
            };

            loop {
                if !controller.is_connected().unwrap_or(false) {
                    log::info!("wifi: connecting to {}", ssid);

                    let _ = controller.stop();
                    Timer::after(Duration::from_millis(100)).await;

                    let client_config = ClientConfig::default()
                        .with_ssid(ssid.to_string())
                        .with_password(password.to_string());

                    if let Err(e) = controller.set_config(&ModeConfig::Client(client_config)) {
                        log::error!("wifi: set_config failed: {:?}", e);
                    }
                    if let Err(e) = controller.start() {
                        log::error!("wifi: start failed: {:?}", e);
                    }
                    if let Err(e) = controller.connect() {
                        log::error!("wifi: connect failed: {:?}", e);
                    }

                    for _ in 0..100 {
                        if controller.is_connected().unwrap_or(false) {
                            log::info!("wifi: connected");
                            break;
                        }
                        Timer::after(Duration::from_millis(200)).await;
                    }
                }

                Timer::after(Duration::from_secs(5)).await;
            }
        }
    }
}

/// Wiegand reader task - reads cards and sends to channel.
#[embassy_executor::task]
async fn wiegand_task(mut wiegand: Wiegand<'static>) {
    loop {
        if let Some(read) = wiegand.read().await {
            log::info!("scan: fob={} nfc={:08X}", read.to_fob(), read.to_nfc_uid());
            if WIEGAND_CHANNEL.try_send(read).is_err() {
                log::warn!("wiegand: channel full, read dropped");
            }
        }
    }
}

/// Access control task - checks authorization and triggers door/events.
///
/// CRITICAL: This task must NEVER block on networking. All authorization checks
/// use only local cached data. Network sync happens asynchronously in sync_task.
///
/// This task also handles watchdog feeding. When WATCHDOG_FEED is signaled,
/// this task feeds the hardware watchdog, proving it is not blocked.
///
/// All actual decision logic lives in the pure `AccessCore` state machine
/// (`access_controller::core`) so it can be exercised deterministically from
/// host tests. This function is now a thin adapter that:
///   1. selects on the three input signals,
///   2. snapshots the fob cache + current time,
///   3. calls `core.step(...)` to get a list of `Effect`s,
///   4. dispatches each effect to the appropriate Embassy primitive.
#[embassy_executor::task]
async fn access_task(
    fobs: &'static Mutex<CriticalSectionRawMutex, heapless::Vec<u32, MAX_FOBS>>,
    local_fobs: &'static Mutex<CriticalSectionRawMutex, heapless::Vec<LocalFob, MAX_LOCAL_FOBS>>,
    last_swipe: &'static Mutex<CriticalSectionRawMutex, Option<LastSwipe>>,
    wdt: &'static Mutex<CriticalSectionRawMutex, WdtType>,
    rt: &'static RuntimeConfig,
) {
    let mut core = AccessCore::new();

    loop {
        // Select across all firmware-level inputs: card reads, sync
        // completion, watchdog feed ticks, and operator-initiated
        // manual unlocks from the HTTP server.
        let event = embassy_futures::select::select4(
            WIEGAND_CHANNEL.receive(),
            SYNC_COMPLETE.wait(),
            WATCHDOG_FEED.wait(),
            MANUAL_UNLOCK.wait(),
        )
        .await;

        let now = embassy_time::Instant::now().as_millis();

        // Manual unlock is handled entirely in the firmware adapter -
        // it doesn't run through AccessCore because there's no
        // authorization decision to make.
        if let embassy_futures::select::Either4::Fourth(()) = event {
            log::warn!("access MANUAL UNLOCK via HTTP");
            DOOR_SIGNAL.signal(());
            READER_FEEDBACK.signal(AccessOutcome::Granted);
            EVENT_BUFFER
                .push(AccessEvent {
                    fob: MANUAL_UNLOCK_FOB,
                    allowed: true,
                })
                .await;
            *last_swipe.lock().await = Some(LastSwipe {
                fob: MANUAL_UNLOCK_FOB,
                allowed: true,
                at_uptime_ms: now,
                manual: true,
            });
            continue;
        }

        let input = match event {
            embassy_futures::select::Either4::First(read) => CoreInput::Card(CardRead {
                fob: read.to_fob(),
                nfc: read.to_nfc_uid(),
            }),
            embassy_futures::select::Either4::Second(()) => CoreInput::SyncComplete,
            embassy_futures::select::Either4::Third(()) => CoreInput::WatchdogFeed,
            embassy_futures::select::Either4::Fourth(()) => unreachable!(),
        };

        // Snapshot both caches once and pass them as slices. Local list is
        // checked first by AccessCore; the conway_enabled flag controls
        // whether denials trigger a RequestSync or apply backoff immediately.
        let conway_enabled = rt.settings.lock().await.conway_enabled();
        let effects = {
            let fob_list = fobs.lock().await;
            let local_list = local_fobs.lock().await;
            // Project LocalFob -> u32 ids into a small stack buffer so
            // AccessCore stays oblivious to label metadata.
            let mut local_ids: heapless::Vec<u32, MAX_LOCAL_FOBS> = heapless::Vec::new();
            for f in local_list.iter() {
                let _ = local_ids.push(f.id);
            }
            core.step(
                now,
                local_ids.as_slice(),
                fob_list.as_slice(),
                conway_enabled,
                input,
            )
        };

        for effect in effects.iter() {
            match effect {
                Effect::OpenDoor => {
                    log::info!("access GRANTED");
                    DOOR_SIGNAL.signal(());
                }
                Effect::Feedback(Outcome::Granted) => {
                    READER_FEEDBACK.signal(AccessOutcome::Granted);
                }
                Effect::Feedback(Outcome::Denied) => {
                    log::warn!("access DENIED");
                    READER_FEEDBACK.signal(AccessOutcome::Denied);
                }
                Effect::Record(ev) => {
                    EVENT_BUFFER
                        .push(AccessEvent {
                            fob: ev.fob,
                            allowed: ev.allowed,
                        })
                        .await;
                    // Mirror the record into the UI's last-swipe slot.
                    *last_swipe.lock().await = Some(LastSwipe {
                        fob: ev.fob,
                        allowed: ev.allowed,
                        at_uptime_ms: now,
                        manual: false,
                    });
                }
                Effect::RequestSync => {
                    SYNC_SIGNAL.signal(());
                }
                Effect::FeedWatchdog => {
                    wdt.lock().await.feed();
                    log::debug!("watchdog: fed");
                }
            }
        }
    }
}

/// Door control task - pulses relay when signaled.
#[embassy_executor::task]
async fn door_task(mut door: Output<'static>) {
    const DOOR_PULSE_MS: u64 = 200;

    loop {
        DOOR_SIGNAL.wait().await;
        door.set_high();
        Timer::after(Duration::from_millis(DOOR_PULSE_MS)).await;
        door.set_low();
    }
}

/// Conway API sync task.
#[embassy_executor::task]
async fn sync_task(
    stack: &'static Stack<'static>,
    fobs: &'static Mutex<CriticalSectionRawMutex, heapless::Vec<u32, MAX_FOBS>>,
    etag: &'static Mutex<CriticalSectionRawMutex, HString<64>>,
    rt: &'static RuntimeConfig,
) {
    // Wait for network
    loop {
        if stack.is_link_up() && stack.config_v4().is_some() {
            break;
        }
        Timer::after(Duration::from_millis(100)).await;
    }
    log::info!("sync: network ready");

    loop {
        // Wait for periodic timer or on-demand signal
        let _ = embassy_futures::select::select(
            Timer::after(Duration::from_secs(10)),
            SYNC_SIGNAL.wait(),
        )
        .await;

        if stack.config_v4().is_none() {
            log::warn!("sync: no IP, skipping");
            continue;
        }

        crate::sync::sync_with_conway(stack, fobs, etag, rt).await;
    }
}

/// Watchdog feed task - periodically signals access_task to feed the watchdog.
///
/// This task runs on a 10-second interval and sends a signal to access_task
/// requesting it to feed the hardware watchdog. If access_task is blocked and
/// cannot respond, the watchdog will not be fed and will eventually reset the system.
///
/// The 10-second interval with a 30-second watchdog timeout provides 3 feed
/// opportunities before reset, allowing for some timing variance.
#[embassy_executor::task]
async fn watchdog_feed_task() {
    loop {
        Timer::after(Duration::from_secs(10)).await;
        WATCHDOG_FEED.signal(());
    }
}

/// Reader-side feedback task - drives the reader LED and beeper after
/// each access decision.
///
/// - Granted: LED on for 200ms with a 100ms beep at the start.
/// - Denied:  three 100ms beeps (100ms gap), LED stays off.
#[embassy_executor::task]
async fn reader_feedback_task(mut led: Output<'static>, mut beep: Output<'static>) {
    loop {
        match READER_FEEDBACK.wait().await {
            AccessOutcome::Granted => {
                led.set_high();
                beep.set_high();
                Timer::after(Duration::from_millis(100)).await;
                beep.set_low();
                Timer::after(Duration::from_millis(100)).await;
                led.set_low();
            }
            AccessOutcome::Denied => {
                for _ in 0..3 {
                    beep.set_high();
                    Timer::after(Duration::from_millis(100)).await;
                    beep.set_low();
                    Timer::after(Duration::from_millis(100)).await;
                }
            }
        }
    }
}

/// Combined status-LED heartbeat + CONFIG button handler.
///
/// One task owns the status LED so the factory-reset acknowledgement can
/// flash it without any cross-task synchronization.
///
/// Heartbeat:
///   - 1Hz toggle (500ms period) when the network stack has an IPv4 lease.
///   - 5Hz toggle (100ms period) otherwise, indicating "no IP / not ready".
///
/// CONFIG button (active-low, external pull-up):
///   - Short press (>=50ms, <5s): signal SYNC_SIGNAL for an on-demand sync.
///   - Long hold (>=5s):          flash the status LED 5x, log a placeholder
///                                factory-reset message, then software-reset.
#[embassy_executor::task]
async fn status_and_config_task(
    mut led: Output<'static>,
    mut btn: Input<'static>,
    stack: &'static Stack<'static>,
) {
    use embassy_futures::select::{Either, select};

    const DEBOUNCE_MS: u64 = 50;
    const LONG_HOLD_MS: u64 = 5_000;

    loop {
        // Choose heartbeat cadence based on network readiness each tick.
        let period_ms = if stack.config_v4().is_some() {
            500
        } else {
            100
        };

        match select(
            btn.wait_for_falling_edge(),
            Timer::after(Duration::from_millis(period_ms)),
        )
        .await
        {
            // Heartbeat tick.
            Either::Second(()) => {
                led.toggle();
            }

            // Button pressed (active-low edge).
            Either::First(()) => {
                Timer::after(Duration::from_millis(DEBOUNCE_MS)).await;
                if btn.is_high() {
                    // Bounce / glitch - ignore.
                    continue;
                }

                // Wait for release or long-hold timeout.
                match select(
                    btn.wait_for_rising_edge(),
                    Timer::after(Duration::from_millis(LONG_HOLD_MS - DEBOUNCE_MS)),
                )
                .await
                {
                    // Short press - released before long-hold threshold.
                    Either::First(()) => {
                        log::info!("config: manual sync requested");
                        SYNC_SIGNAL.signal(());
                    }

                    // Long hold - factory reset. Wipe persisted settings
                    // so the next boot comes up in AP onboarding mode.
                    Either::Second(()) => {
                        log::warn!("config: factory reset - wiping NVS and rebooting");
                        if let Err(e) = settings::erase() {
                            log::error!("config: settings::erase failed: {}", e);
                        }
                        if let Err(e) = fob_store::erase() {
                            log::error!("config: fob_store::erase failed: {}", e);
                        }
                        for _ in 0..5 {
                            led.set_high();
                            Timer::after(Duration::from_millis(100)).await;
                            led.set_low();
                            Timer::after(Duration::from_millis(100)).await;
                        }
                        esp_hal::system::software_reset();
                    }
                }
            }
        }
    }
}

#[panic_handler]
fn panic(info: &core::panic::PanicInfo) -> ! {
    log::error!("PANIC: {}", info);
    esp_hal::system::software_reset()
}
