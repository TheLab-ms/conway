//! Core 0: Real-time RFID reading and door control.
//!
//! This core handles time-critical operations:
//! - Polling the Wiegand reader for card scans
//! - Checking fob authorization against the shared fob list
//! - Controlling the door relay
//! - Feeding the watchdog timer
//! - Cooperating with Core 1 for safe flash writes

use core::sync::atomic::Ordering;
use esp_hal::{delay::Delay, gpio::Output};

use crate::shared::SHARED;
use crate::wiegand::Wiegand;
use crate::{PENDING_RECHECK, SYNC_REQUEST, WATCHDOG};

// Timing constants
const DOOR_PULSE_MS: u32 = 200;
const POLL_INTERVAL_MS: u32 = 5;
const WATCHDOG_FEED_MS: u64 = 10_000;

/// Core 0 main loop: Wiegand polling and door control.
pub fn run(wiegand: Wiegand, mut door: Output<'static>) -> ! {
    let delay = Delay::new();
    let mut backoff_until: u64 = 0;
    let mut failed_attempts: u8 = 0;
    let mut last_watchdog_feed: u64 = 0;

    loop {
        let now = esp_hal::time::Instant::now().duration_since_epoch().as_millis();

        // ====================================================================
        // Flash write coordination - check FIRST before any critical sections
        // ====================================================================
        // This must happen at the TOP of the loop, before we enter any
        // critical_section::with() blocks. Core 1 is waiting for us to signal
        // that we're in a "safe" state (no spinlocks held).
        if SHARED.is_flash_requested() {
            // Signal that we're safe (not holding any spinlocks)
            SHARED.signal_flash_safe();

            // Spin-wait while Core 1 performs the flash write.
            // We must NOT enter any critical sections during this time!
            // The flash write disables the CPU cache, so we also cannot
            // execute code from flash - but this code is in IRAM (no_std).
            while !SHARED.is_flash_done() {
                // Brief delay to avoid hammering the atomic
                for _ in 0..100 {
                    core::hint::spin_loop();
                }
            }

            // Acknowledge completion and return to normal operation
            SHARED.acknowledge_flash_done();
            log::debug!("core0: flash write complete, resuming");
        }

        // ====================================================================
        // Normal operation (may use critical sections)
        // ====================================================================

        // Check for HTTP unlock request
        if SHARED.take_unlock_request() {
            log::info!("door: unlock via HTTP");
            pulse_door(&mut door, &delay);
        }

        // Check if we have a pending recheck and if sync has completed
        // Do both check and take in a single critical section to avoid race
        let pending = critical_section::with(|cs| {
            let sync_done = !SYNC_REQUEST.load(Ordering::Acquire);
            if sync_done {
                PENDING_RECHECK.borrow_ref_mut(cs).take()
            } else {
                None
            }
        });

        if let Some((fob, nfc, scan_time)) = pending {
            // Sync completed - recheck access
            let fob_allowed = SHARED.check_fob(fob);
            let nfc_allowed = !fob_allowed && SHARED.check_fob(nfc);
            let allowed = fob_allowed || nfc_allowed;
            let credential = if fob_allowed { fob } else { nfc };
            SHARED.push_event(credential, allowed);

            if allowed {
                log::info!("access GRANTED (after sync)");
                failed_attempts = 0;
                pulse_door(&mut door, &delay);
            } else {
                log::warn!("access DENIED (after sync)");
                failed_attempts = failed_attempts.saturating_add(1);
                let delay_ms = (1u64 << failed_attempts.min(3)) * 1000;
                backoff_until = scan_time + delay_ms;
            }
        }

        // Poll Wiegand reader
        if let Some(read) = wiegand.poll() {
            if now < backoff_until {
                log::debug!("card ignored (backoff)");
            } else {
                let fob = read.to_fob();
                let nfc = read.to_nfc_uid();
                log::info!("scan: fob={} nfc={:08X}", fob, nfc);

                let fob_allowed = SHARED.check_fob(fob);
                let nfc_allowed = !fob_allowed && SHARED.check_fob(nfc);
                let allowed = fob_allowed || nfc_allowed;

                // If denied, request sync from Core 1 (non-blocking)
                if !allowed {
                    log::info!("access denied, requesting sync...");

                    // Only store credentials for recheck if no other recheck is pending
                    // This prevents overwriting a pending recheck that hasn't been processed yet
                    // Set both PENDING_RECHECK and SYNC_REQUEST atomically to avoid race
                    let stored = critical_section::with(|cs| {
                        let mut pending = PENDING_RECHECK.borrow_ref_mut(cs);
                        if pending.is_none() {
                            *pending = Some((fob, nfc, now));
                            // Set sync request inside critical section for atomicity
                            SYNC_REQUEST.store(true, Ordering::Release);
                            true
                        } else {
                            false
                        }
                    });

                    if !stored {
                        log::warn!(
                            "RECHECK BLOCKED: fob={} nfc={:08X} - another recheck is pending, this scan will NOT trigger sync",
                            fob, nfc
                        );
                    }
                    // Don't block - continue polling Wiegand in the next iteration
                } else {
                    let credential = if fob_allowed { fob } else { nfc };
                    SHARED.push_event(credential, allowed);
                    log::info!("access GRANTED");
                    failed_attempts = 0;
                    pulse_door(&mut door, &delay);
                }
            }
        }

        // Feed watchdog to prove this loop isn't deadlocked
        if now - last_watchdog_feed >= WATCHDOG_FEED_MS {
            last_watchdog_feed = now;
            critical_section::with(|cs| {
                if let Some(ref mut wdt) = *WATCHDOG.borrow_ref_mut(cs) {
                    wdt.feed();
                }
            });
        }

        delay.delay_millis(POLL_INTERVAL_MS);
    }
}

fn pulse_door(door: &mut Output<'static>, delay: &Delay) {
    door.set_high();
    delay.delay_millis(DOOR_PULSE_MS);
    door.set_low();
}
