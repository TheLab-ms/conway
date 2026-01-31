//! Conway Access Controller - ESP32 dual-core firmware for door access control.
//!
//! Architecture:
//! - Core 0 (`core0`): Real-time RFID reading via Wiegand protocol and door relay control
//! - Core 1 (`core1`): WiFi networking, Conway server sync, and HTTP admin interface
//!
//! Cross-core communication uses lock-free atomics and the `shared` module.

#![no_std]
#![no_main]

use esp_bootloader_esp_idf::esp_app_desc;
esp_app_desc!();

mod conway;
mod core0;
mod core1;
mod heap_debug;
mod shared;
mod storage;
mod wiegand;

extern crate alloc;

use alloc::boxed::Box;
use core::cell::RefCell;
use core::mem::MaybeUninit;
use critical_section::Mutex;
use esp_alloc as _;
use esp_hal::{
    clock::CpuClock,
    gpio::{Level, Output, OutputConfig},
    interrupt::software::SoftwareInterruptControl,
    main,
    system::Stack,
    time::Duration,
    timer::timg::{TimerGroup, Wdt},
};
use esp_println::logger::init_logger;
use esp_radio::wifi::{Config as WifiConfig, WifiController, WifiDevice};

use crate::wiegand::Wiegand;

// Core 1 stack (needs to be larger for WiFi/smoltcp)
// 32KB is required to handle WiFi + smoltcp + HTTP server + Conway sync
// with HString buffers. 16KB caused stack overflow panics.
static mut CORE1_STACK: Stack<32768> = Stack::new();

// WiFi handles passed from Core 0 to Core 1
pub(crate) static WIFI_DEVICE: Mutex<RefCell<Option<WifiDevice<'static>>>> =
    Mutex::new(RefCell::new(None));
pub(crate) static WIFI_CONTROLLER: Mutex<RefCell<Option<WifiController<'static>>>> =
    Mutex::new(RefCell::new(None));

// Flag to signal sync request from Core 0 to Core 1
pub(crate) static SYNC_REQUEST: core::sync::atomic::AtomicBool =
    core::sync::atomic::AtomicBool::new(false);

// Pending recheck state: (fob, nfc, timestamp_ms)
// Core 0 stores credentials here after requesting sync, then rechecks after sync completes.
// Only one recheck can be pending at a time to prevent race conditions where a new scan
// overwrites a pending recheck before the sync completes.
pub(crate) static PENDING_RECHECK: Mutex<RefCell<Option<(u32, u32, u64)>>> =
    Mutex::new(RefCell::new(None));

// Watchdog timer (shared between cores)
pub(crate) static WATCHDOG: Mutex<RefCell<Option<Wdt<esp_hal::peripherals::TIMG1>>>> =
    Mutex::new(RefCell::new(None));

/// Feed the watchdog timer. Safe to call from any core.
/// This should be called during long-running operations to prevent watchdog reset.
pub fn feed_watchdog() {
    critical_section::with(|cs| {
        if let Some(ref mut wdt) = *WATCHDOG.borrow_ref_mut(cs) {
            wdt.feed();
        }
    });
}

/// Disable the watchdog timer temporarily.
/// SAFETY: Only use this around operations that block both cores (like flash writes).
/// Must be paired with `enable_watchdog()`.
pub fn disable_watchdog() {
    critical_section::with(|cs| {
        if let Some(ref mut wdt) = *WATCHDOG.borrow_ref_mut(cs) {
            wdt.disable();
        }
    });
}

/// Re-enable the watchdog timer after it was disabled.
/// Must be paired with a previous `disable_watchdog()` call.
pub fn enable_watchdog() {
    critical_section::with(|cs| {
        if let Some(ref mut wdt) = *WATCHDOG.borrow_ref_mut(cs) {
            wdt.enable();
        }
    });
}

#[main]
fn main() -> ! {
    // Initialize logging
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
    let config = esp_hal::Config::default().with_cpu_clock(CpuClock::max());
    let peripherals = esp_hal::init(config);

    // Initialize timer for esp-rtos scheduler
    let timg0 = TimerGroup::new(peripherals.TIMG0);

    // Start the esp-rtos scheduler (required before esp_radio::init)
    esp_rtos::start(timg0.timer0);

    // Initialize esp-radio
    let esp_radio_ctrl = esp_radio::init().unwrap();

    // Create WiFi device and controller with station mode config
    let wifi_config = WifiConfig::default();
    let (wifi_controller, interfaces) =
        esp_radio::wifi::new(&esp_radio_ctrl, peripherals.WIFI, wifi_config).unwrap();

    // Store WiFi handles in statics for Core 1 access
    // SAFETY: wifi_device and wifi_controller borrow from esp_radio_ctrl.
    // We will leak esp_radio_ctrl to 'static below, making these borrows valid for 'static.
    // We store these before starting Core 1, and Core 1 takes exclusive ownership.
    critical_section::with(|cs| {
        WIFI_DEVICE
            .borrow_ref_mut(cs)
            .replace(unsafe { core::mem::transmute(interfaces.sta) });
        WIFI_CONTROLLER
            .borrow_ref_mut(cs)
            .replace(unsafe { core::mem::transmute(wifi_controller) });
    });

    // Keep esp_radio_ctrl alive (it owns the WiFi state)
    // SAFETY: We leak it to 'static since it needs to live for the entire program.
    // This validates the transmutes above - the borrowed references are now truly 'static.
    let _esp_radio_ctrl: &'static _ = Box::leak(Box::new(unsafe {
        core::mem::transmute::<_, esp_radio::Controller<'static>>(esp_radio_ctrl)
    }));

    // Initialize watchdog timer on TIMG1 (TIMG0 is used by WiFi)
    let timg1 = TimerGroup::new(peripherals.TIMG1);
    let mut wdt = timg1.wdt;
    wdt.enable();
    wdt.set_timeout(
        esp_hal::timer::timg::MwdtStage::Stage0,
        Duration::from_secs(30),
    );
    critical_section::with(|cs| {
        WATCHDOG
            .borrow_ref_mut(cs)
            .replace(unsafe { core::mem::transmute(wdt) });
    });

    // Door relay output (GPIO25)
    let door = Output::new(peripherals.GPIO25, Level::Low, OutputConfig::default());

    // Wiegand reader pins (GPIO14=D0, GPIO27=D1)
    let wiegand = Wiegand::new(peripherals.GPIO14, peripherals.GPIO27, peripherals.IO_MUX);

    // Software interrupts for esp-rtos multi-core scheduler
    let sw_ints = SoftwareInterruptControl::new(peripherals.SW_INTERRUPT);

    // Start Core 1 with esp-rtos scheduler (required for usleep and other RTOS functions)
    esp_rtos::start_second_core(
        peripherals.CPU_CTRL,
        sw_ints.software_interrupt0,
        sw_ints.software_interrupt1,
        unsafe { &mut *core::ptr::addr_of_mut!(CORE1_STACK) },
        || {
            core1::run();
        },
    );

    // Core 0 main loop: Wiegand + door control
    core0::run(wiegand, door);
}

#[panic_handler]
fn panic(info: &core::panic::PanicInfo) -> ! {
    critical_section::with(|_| {
        log::error!("PANIC: {}", info);
    });

    // Spin without feeding watchdog. The 30s timeout will trigger a full system reset.
    loop {
        core::hint::spin_loop();
    }
}
