//! Wiegand 26/34-bit decoder using GPIO interrupts.

use core::cell::RefCell;
use core::sync::atomic::Ordering;
use critical_section::Mutex;
use esp_hal::gpio::{Event, Input, InputConfig, InputPin, Io, Pull};
use esp_hal::handler;
use esp_hal::interrupt::Priority;
use esp_hal::peripherals::IO_MUX;
use portable_atomic::AtomicU64;

const DEBOUNCE_US: u64 = 200;
const END_OF_TX_US: u64 = 25_000;

// ISR-accessible state (single instance)
// WIEGAND_STATE packs both count and bits into a single 64-bit atomic to avoid races.
// Format: upper 8 bits = bit count (0-64), lower 56 bits = accumulated bits
// This ensures atomic updates of both values together in the CAS loop.
static WIEGAND_STATE: AtomicU64 = AtomicU64::new(0);
static LAST_BIT_US: AtomicU64 = AtomicU64::new(0);

const COUNT_SHIFT: u32 = 56;
const BITS_MASK: u64 = (1u64 << COUNT_SHIFT) - 1;

// Pin storage for interrupt handler access
static D0_PIN: Mutex<RefCell<Option<Input<'static>>>> = Mutex::new(RefCell::new(None));
static D1_PIN: Mutex<RefCell<Option<Input<'static>>>> = Mutex::new(RefCell::new(None));

/// GPIO interrupt handler for Wiegand D0 and D1 pins.
/// Priority3 is used to ensure low-latency bit capture.
#[handler(priority = Priority::Priority3)]
fn gpio_handler() {
    // Determine which bits to record while in critical section (minimal time)
    let (d0_triggered, d1_triggered) = critical_section::with(|cs| {
        let mut d0 = false;
        let mut d1 = false;

        if let Some(ref mut pin) = *D0_PIN.borrow_ref_mut(cs) {
            if pin.is_interrupt_set() {
                pin.clear_interrupt();
                d0 = true;
            }
        }

        if let Some(ref mut pin) = *D1_PIN.borrow_ref_mut(cs) {
            if pin.is_interrupt_set() {
                pin.clear_interrupt();
                d1 = true;
            }
        }

        (d0, d1)
    });

    // Record bits outside critical section (lock-free)
    if d0_triggered {
        record_bit(0);
    }
    if d1_triggered {
        record_bit(1);
    }
}

fn record_bit(bit: u8) {
    let now = esp_hal::time::Instant::now().duration_since_epoch().as_micros();
    let last = LAST_BIT_US.load(Ordering::Relaxed);

    // Debounce
    if last != 0 && now.saturating_sub(last) < DEBOUNCE_US {
        return;
    }

    // Use CAS loop to atomically update both count and bits together
    loop {
        let state = WIEGAND_STATE.load(Ordering::Acquire);
        let count = (state >> COUNT_SHIFT) as u32;
        if count >= 64 {
            return; // Buffer full
        }

        let bits = state & BITS_MASK;
        let new_bits = (bits << 1) | (bit as u64);
        let new_count = count + 1;
        let new_state = ((new_count as u64) << COUNT_SHIFT) | (new_bits & BITS_MASK);

        // Try to atomically update the packed state (count + bits together)
        if WIEGAND_STATE
            .compare_exchange(state, new_state, Ordering::AcqRel, Ordering::Acquire)
            .is_ok()
        {
            // We won the race - update timestamp
            LAST_BIT_US.store(now, Ordering::Release);
            return;
        }
        // Another interrupt beat us - retry with fresh values
    }
}

/// Wiegand reader state.
pub struct Wiegand {
    // Pins are stored in statics for ISR access, but we track initialization
    _initialized: bool,
}

impl Wiegand {
    /// Create a new Wiegand reader on specified D0 and D1 pins.
    ///
    /// # Arguments
    /// * `d0` - GPIO pin for Wiegand D0 (bit value 0)
    /// * `d1` - GPIO pin for Wiegand D1 (bit value 1)
    /// * `io_mux` - IO_MUX peripheral for interrupt handler registration
    pub fn new<D0, D1>(d0: D0, d1: D1, io_mux: IO_MUX<'static>) -> Self
    where
        D0: InputPin + 'static,
        D1: InputPin + 'static,
    {
        // Create input pins with pull-ups
        let input_config = InputConfig::default().with_pull(Pull::Up);
        let mut d0_pin: Input<'static> = Input::new(d0, input_config);
        let mut d1_pin: Input<'static> = Input::new(d1, input_config);

        // Enable falling edge interrupts
        d0_pin.listen(Event::FallingEdge);
        d1_pin.listen(Event::FallingEdge);

        // Store pins in statics for ISR access
        critical_section::with(|cs| {
            D0_PIN.borrow_ref_mut(cs).replace(d0_pin);
            D1_PIN.borrow_ref_mut(cs).replace(d1_pin);
        });

        // Register the GPIO interrupt handler
        let mut io = Io::new(io_mux);
        io.set_interrupt_handler(gpio_handler);

        Self { _initialized: true }
    }

    /// Poll for a complete Wiegand read. Returns decoded credential if ready.
    ///
    /// Uses lock-free CAS to avoid blocking interrupts during the check-and-reset.
    pub fn poll(&self) -> Option<WiegandRead> {
        // Lock-free read, check timeout, and reset using CAS
        loop {
            let state = WIEGAND_STATE.load(Ordering::Acquire);
            let count = (state >> COUNT_SHIFT) as u32;
            if count == 0 {
                return None;
            }

            let last = LAST_BIT_US.load(Ordering::Acquire);
            let now = esp_hal::time::Instant::now().duration_since_epoch().as_micros();
            if now.saturating_sub(last) < END_OF_TX_US {
                return None; // Transmission still in progress
            }

            // Try to atomically claim this read by resetting the state
            // If an ISR adds bits between our load and CAS, CAS fails and we retry
            if WIEGAND_STATE
                .compare_exchange(state, 0, Ordering::AcqRel, Ordering::Acquire)
                .is_ok()
            {
                // Successfully claimed - reset timestamp too
                // (No race here: ISR only updates timestamp after successful state CAS,
                // and we just set state to 0, so ISR will start fresh)
                LAST_BIT_US.store(0, Ordering::Release);

                let bits = state & BITS_MASK;
                return match count {
                    26 => Self::decode_26(bits),
                    34 => Self::decode_34(bits),
                    _ => {
                        log::warn!("wiegand: unknown format ({} bits)", count);
                        None
                    }
                };
            }
            // CAS failed - state changed (ISR added bits), retry with fresh values
        }
    }

    fn decode_26(raw: u64) -> Option<WiegandRead> {
        let raw = raw as u32;
        let leading = (raw >> 25) & 1;
        let trailing = raw & 1;
        let data = (raw >> 1) & 0xFF_FFFF;

        // Even parity on upper 12 bits, odd parity on lower 12 bits
        let upper = data >> 12;
        let lower = data & 0xFFF;
        let even_ok = (upper.count_ones() % 2) == leading;
        let odd_ok = (lower.count_ones() % 2) != trailing;
        if !even_ok || !odd_ok {
            log::warn!("wiegand: 26-bit parity failed");
            return None;
        }

        let facility = (data >> 16) & 0xFF;
        let card = data & 0xFFFF;
        Some(WiegandRead { facility, card, raw_data: data })
    }

    fn decode_34(raw: u64) -> Option<WiegandRead> {
        let leading = ((raw >> 33) & 1) as u32;
        let trailing = (raw & 1) as u32;
        let data = ((raw >> 1) & 0xFFFF_FFFF) as u32;

        // Even parity on upper 16 bits, odd parity on lower 16 bits
        let upper = data >> 16;
        let lower = data & 0xFFFF;
        let even_ok = (upper.count_ones() % 2) == leading;
        let odd_ok = (lower.count_ones() % 2) != trailing;
        if !even_ok || !odd_ok {
            log::warn!("wiegand: 34-bit parity failed");
            return None;
        }

        let facility = (data >> 16) & 0xFF;
        let card = data & 0xFFFF;
        Some(WiegandRead { facility, card, raw_data: data })
    }
}

/// Decoded Wiegand credential.
#[derive(Debug, Clone, Copy)]
pub struct WiegandRead {
    pub facility: u32,
    pub card: u32,
    pub raw_data: u32,
}

impl WiegandRead {
    /// Convert to H10301 fob format: facility code + 5-digit card ID.
    pub fn to_fob(&self) -> u32 {
        self.facility * 100_000 + self.card
    }

    /// Convert raw data to NFC UID (byte-reversed).
    pub fn to_nfc_uid(&self) -> u32 {
        self.raw_data.swap_bytes()
    }
}
