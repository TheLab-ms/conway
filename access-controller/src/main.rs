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

mod http;
mod ota;
mod sync;
mod wiegand;

extern crate alloc;

use alloc::boxed::Box;
use core::mem::MaybeUninit;
use embassy_net::{Config as NetConfig, Stack, StackResources};
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
use esp_radio::wifi::{ClientConfig, Config as WifiConfig, ModeConfig, WifiController};
use heapless::String as HString;
use static_cell::StaticCell;

use crate::sync::{AccessEvent, EventBuffer};
use crate::wiegand::{Wiegand, WiegandRead};
use access_controller::core::{AccessCore, CardRead, Effect, Input as CoreInput, Outcome};

// Configuration constants
pub const MAX_FOBS: usize = 512;

pub const SSID: &str = match option_env!("CONWAY_SSID") {
    Some(s) => s,
    None => "unconfigured",
};
pub const PASSWORD: &str = match option_env!("CONWAY_PASSWORD") {
    Some(s) => s,
    None => "",
};
pub const CONWAY_HOST: &str = match option_env!("CONWAY_HOST") {
    Some(s) => s,
    None => "192.168.1.1",
};
pub const CONWAY_PORT: u16 = 8080;

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
static ETAG: StaticCell<Mutex<CriticalSectionRawMutex, HString<64>>> = StaticCell::new();
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

    log::info!(
        "config: ssid={}, host={}:{}",
        SSID,
        CONWAY_HOST,
        CONWAY_PORT
    );

    // Initialize shared state (fobs and etag start empty, populated by sync)
    let fobs = FOBS.init(Mutex::new(heapless::Vec::new()));
    let etag = ETAG.init(Mutex::new(HString::new()));

    log::info!("storage: fob cache initialized (empty, will sync from server)");

    // Initialize esp-radio for WiFi
    // NOTE: esp_rtos::start() must be called before this (done above after peripherals init).
    let esp_radio_ctrl = esp_radio::init().unwrap();

    // Leak the controller to get 'static lifetime before creating WiFi
    let esp_radio_ctrl: &'static _ = Box::leak(Box::new(esp_radio_ctrl));

    let wifi_config = WifiConfig::default();
    let (wifi_controller, interfaces) =
        esp_radio::wifi::new(esp_radio_ctrl, peripherals.WIFI, wifi_config).unwrap();

    // Setup Embassy network stack
    let stack_resources = STACK_RESOURCES.init(StackResources::new());
    let net_config = NetConfig::dhcpv4(Default::default());

    // Use MAC address as seed for network stack RNG
    // Not cryptographically secure, but sufficient for TCP sequence numbers
    let mac = esp_radio::wifi::sta_mac();
    let seed = u64::from_le_bytes([mac[0], mac[1], mac[2], mac[3], mac[4], mac[5], 0, 0]);
    log::info!("rng: seed={:016X}", seed);

    let wifi_device: esp_radio::wifi::WifiDevice<'static> =
        unsafe { core::mem::transmute(interfaces.sta) };
    let wifi_controller: WifiController<'static> =
        unsafe { core::mem::transmute(wifi_controller) };

    let (stack, runner) = embassy_net::new(wifi_device, net_config, stack_resources, seed);
    let stack: &'static Stack<'static> = STACK.init(stack);

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
    let door = Output::new(peripherals.GPIO32, Level::Low, OutputConfig::default());
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
    spawner.spawn(wifi_task(wifi_controller)).unwrap();
    spawner.spawn(wiegand_task(wiegand)).unwrap();
    spawner.spawn(access_task(fobs, wdt)).unwrap();
    spawner.spawn(door_task(door)).unwrap();
    spawner
        .spawn(reader_feedback_task(reader_led, reader_beep))
        .unwrap();
    spawner
        .spawn(status_and_config_task(status_led, config_btn, stack))
        .unwrap();
    spawner.spawn(sync_task(stack, fobs, etag)).unwrap();
    spawner.spawn(http::http_server_task(stack, fobs, etag)).unwrap();
    spawner.spawn(watchdog_feed_task()).unwrap();
}

/// Runs the embassy-net stack, processing incoming/outgoing packets and maintaining network state.
/// Must run continuously for the network stack to function.
#[embassy_executor::task]
async fn net_task(mut runner: embassy_net::Runner<'static, esp_radio::wifi::WifiDevice<'static>>) {
    runner.run().await;
}

/// WiFi connection management.
/// Tries to connect every 5 seconds, with a 20 second timeout.
#[embassy_executor::task]
async fn wifi_task(mut controller: WifiController<'static>) {
    use alloc::string::ToString;

    loop {
        if !controller.is_connected().unwrap_or(false) {
            log::info!("wifi: connecting to {}", SSID);

            let _ = controller.stop();
            Timer::after(Duration::from_millis(100)).await;

            let client_config = ClientConfig::default()
                .with_ssid(SSID.to_string())
                .with_password(PASSWORD.to_string());

            if let Err(e) = controller.set_config(&ModeConfig::Client(client_config)) {
                log::error!("wifi: set_config failed: {:?}", e);
            }
            if let Err(e) = controller.start() {
                log::error!("wifi: start failed: {:?}", e);
            }
            if let Err(e) = controller.connect() {
                log::error!("wifi: connect failed: {:?}", e);
            }

            // Wait for connection
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
    wdt: &'static Mutex<CriticalSectionRawMutex, WdtType>,
) {
    let mut core = AccessCore::new();

    loop {
        // Use select3 to handle card reads, sync completion, AND watchdog feed requests.
        // This ensures we can service all events without blocking.
        let event = embassy_futures::select::select3(
            WIEGAND_CHANNEL.receive(),
            SYNC_COMPLETE.wait(),
            WATCHDOG_FEED.wait(),
        )
        .await;

        let now = embassy_time::Instant::now().as_millis();

        let input = match event {
            embassy_futures::select::Either3::First(read) => CoreInput::Card(CardRead {
                fob: read.to_fob(),
                nfc: read.to_nfc_uid(),
            }),
            embassy_futures::select::Either3::Second(()) => CoreInput::SyncComplete,
            embassy_futures::select::Either3::Third(()) => CoreInput::WatchdogFeed,
        };

        // Snapshot the cache once and pass it as a slice; mirrors the
        // single-lock-acquisition behavior of the original code.
        let effects = {
            let fob_list = fobs.lock().await;
            core.step(now, fob_list.as_slice(), input)
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

        crate::sync::sync_with_conway(stack, fobs, etag).await;
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

                    // Long hold - factory reset placeholder.
                    Either::Second(()) => {
                        log::warn!("config: factory reset placeholder - rebooting");
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
