//! Async Wiegand 26/34-bit decoder using Embassy GPIO.
//!
//! Uses async edge detection instead of interrupt handlers.

use embassy_time::{Duration, Instant, with_timeout};
use esp_hal::gpio::Input;

// Debounce time accounts for optocoupler propagation delay and slower edge transitions.
// Typical optocouplers (e.g., PC817, 6N137) have 3-18µs rise/fall times which can cause
// ringing or multiple edge detections. Wiegand pulse width is typically 50-100µs with
// 1-2ms between pulses, so 500µs debounce is safe and eliminates duplicate bits.
const DEBOUNCE: Duration = Duration::from_micros(500);
const BIT_TIMEOUT: Duration = Duration::from_millis(25);

/// Async Wiegand reader.
pub struct Wiegand<'a> {
    d0: Input<'a>,
    d1: Input<'a>,
}

impl<'a> Wiegand<'a> {
    /// Create a new Wiegand reader on specified D0 and D1 pins.
    pub fn new(d0: Input<'a>, d1: Input<'a>) -> Self {
        Self { d0, d1 }
    }

    /// Read a complete Wiegand transmission asynchronously.
    ///
    /// Waits for the first bit, then collects bits until no more arrive
    /// within the timeout period.
    pub async fn read(&mut self) -> Option<WiegandRead> {
        // Wait for first bit
        let first_bit = self.wait_for_bit().await;

        // Set timestamp after first bit for debouncing subsequent bits
        let mut last_bit = Instant::now();
        let mut bits: u64 = first_bit as u64;
        let mut count: u32 = 1;

        // Collect remaining bits until timeout
        loop {
            match with_timeout(BIT_TIMEOUT, self.wait_for_bit()).await {
                Ok(bit) => {
                    let now = Instant::now();
                    if now.duration_since(last_bit) < DEBOUNCE {
                        continue; // Debounce
                    }
                    last_bit = now;

                    if count >= 64 {
                        break; // Buffer full
                    }
                    bits = (bits << 1) | (bit as u64);
                    count += 1;
                }
                Err(_) => break, // Timeout - transmission complete
            }
        }

        // Decode based on bit count
        match count {
            26 => Self::decode_26(bits),
            34 => Self::decode_34(bits),
            _ => {
                log::warn!("wiegand: unknown format ({} bits)", count);
                None
            }
        }
    }

    /// Wait for either D0 or D1 edge and return the bit value.
    ///
    /// NOTE: With optocoupler isolation (PC817), the signal is inverted:
    /// - Reader idle (HIGH) -> LED on -> transistor on -> ESP32 sees LOW
    /// - Reader pulse (LOW) -> LED off -> transistor off -> ESP32 sees HIGH
    /// Therefore we detect RISING edges (the inverted falling edge from reader).
    async fn wait_for_bit(&mut self) -> u8 {
        use embassy_futures::select::Either;

        // D0 rising edge = 0 bit, D1 rising edge = 1 bit
        // (inverted from reader's falling edge by optocoupler)
        match embassy_futures::select::select(
            self.d0.wait_for_rising_edge(),
            self.d1.wait_for_rising_edge(),
        )
        .await
        {
            Either::First(()) => 0,
            Either::Second(()) => 1,
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
        Some(WiegandRead {
            facility,
            card,
            raw_data: data,
        })
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

        // Match original implementation: 8-bit facility, 16-bit card
        // (This is technically incorrect for H10304 which has 16-bit facility,
        // but we depend on this behavior for compatibility with existing fob database)
        let facility = (data >> 16) & 0xFF;
        let card = data & 0xFFFF;
        Some(WiegandRead {
            facility,
            card,
            raw_data: data,
        })
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
