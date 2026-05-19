//! Async Wiegand 26/34-bit decoder.
//!
//! Uses async edge detection instead of interrupt handlers.

use embassy_time::{Duration, Instant, with_timeout};
use esp_hal::gpio::Input;

// Re-export the pure decoder types so existing callers (`use crate::wiegand::WiegandRead`)
// continue to compile unchanged.
pub use access_controller::decode::{decode_26, decode_34, WiegandRead};

const DEBOUNCE: Duration = Duration::from_micros(500);
const BIT_TIMEOUT: Duration = Duration::from_millis(25);

pub struct Wiegand<'a> {
    d0: Input<'a>,
    d1: Input<'a>,
}

impl<'a> Wiegand<'a> {
    pub fn new(d0: Input<'a>, d1: Input<'a>) -> Self {
        Self { d0, d1 }
    }

    /// Read a complete Wiegand transmission asynchronously.
    ///
    /// Waits for the first bit, then collects bits until no more arrive
    /// within the timeout period.
    pub async fn read(&mut self) -> Option<WiegandRead> {
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
            26 => decode_26(bits),
            34 => decode_34(bits),
            _ => {
                log::warn!("wiegand: unknown format ({} bits)", count);
                None
            }
        }
    }

    /// Wait for either D0 or D1 edge and return the bit value.
    ///
    /// The as-built PCB uses a non-inverting SN74LVC2G17 dual Schmitt buffer
    /// (Vcc=3V3, 5V-tolerant inputs) between the reader and the ESP32. The
    /// reader's native Wiegand signaling is idle-HIGH with a brief LOW pulse
    /// per bit, and the buffer preserves that polarity, so the ESP32 sees the
    /// reader's true falling edges directly.
    async fn wait_for_bit(&mut self) -> u8 {
        use embassy_futures::select::Either;

        // D0 falling edge = 0 bit, D1 falling edge = 1 bit.
        match embassy_futures::select::select(
            self.d0.wait_for_falling_edge(),
            self.d1.wait_for_falling_edge(),
        )
        .await
        {
            Either::First(()) => 0,
            Either::Second(()) => 1,
        }
    }
}
