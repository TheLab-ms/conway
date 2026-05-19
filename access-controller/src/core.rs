//! Pure access-control decision state machine.
//!
//! This is a mechanical extraction of the body of `access_task` in
//! `src/main.rs`. All side effects (door pulse, reader feedback, event
//! buffer push, sync request, watchdog feed) are returned as an `Effect`
//! enum instead of being performed directly, and all time is taken as an
//! explicit `u64` (milliseconds since boot) parameter rather than read from
//! `embassy_time::Instant::now()`. The cache is taken as a slice rather than
//! a `Mutex` lock.
//!
//! This makes the firmware's authorization decisions deterministically
//! testable from host tests under `cargo test --features sim`, without
//! changing observable runtime behavior in the firmware.
//!
//! The firmware adapter in `main.rs` is responsible for:
//! - selecting on `WIEGAND_CHANNEL` / `SYNC_COMPLETE` / `WATCHDOG_FEED` and
//!   mapping each to the corresponding `Input` variant,
//! - calling `embassy_time::Instant::now().as_millis()` and passing it in,
//! - locking the `FOBS` mutex and passing a slice,
//! - dispatching each returned `Effect` to the corresponding `Signal` or
//!   the global `EVENT_BUFFER`.

use heapless::Vec as HVec;

use crate::events::AccessEvent;

/// Window during which a sync completion can retroactively grant a
/// previously-denied credential. Matches `main.rs` (10 seconds).
pub const RECHECK_DEADLINE_MS: u64 = 10_000;

/// Number of effects emitted by a single `step()` call. The current
/// implementation emits at most 3 (Record + Feedback + OpenDoor on grant;
/// Record + Feedback + RequestSync on denial); 4 leaves headroom.
pub const MAX_EFFECTS_PER_STEP: usize = 4;

/// A credential read off the Wiegand reader. Already decoded into both the
/// H10301 fob form and the byte-swapped NFC UID form so the core does not
/// need to know about Wiegand framing.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct CardRead {
    pub fob: u32,
    pub nfc: u32,
}

/// Inputs that drive the access-control state machine.
#[derive(Clone, Copy, Debug)]
pub enum Input {
    /// A new credential was decoded by the Wiegand reader.
    Card(CardRead),
    /// The sync task finished a round-trip with the Conway server (success
    /// or failure). The fob cache slice passed to `step()` reflects any
    /// updates that resulted.
    SyncComplete,
    /// The 10-second tick that proves `access_task` is responsive; mapped
    /// to a hardware watchdog feed by the firmware adapter.
    WatchdogFeed,
}

/// The decision a `Card` step produced (used to drive reader LED/beeper).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Outcome {
    Granted,
    Denied,
}

/// Side effects emitted by `step()`. The firmware adapter is the sole
/// consumer; tests inspect them directly.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Effect {
    /// Pulse the door relay (200ms in firmware).
    OpenDoor,
    /// Drive the reader LED/beeper.
    Feedback(Outcome),
    /// Push an entry into the event buffer for later upload.
    Record(AccessEvent),
    /// Ask the sync task to attempt an on-demand round-trip with Conway.
    RequestSync,
    /// Feed the hardware watchdog.
    FeedWatchdog,
}

/// Pure decision state for the access controller. Mirrors the locals
/// inside `access_task`.
#[derive(Clone, Debug)]
pub struct AccessCore {
    /// `(fob, nfc, deadline_ms)` — a previously denied credential whose
    /// authorization will be re-checked when the next sync completes.
    pending_recheck: Option<(u32, u32, u64)>,
    /// Card reads received before this timestamp are silently dropped.
    backoff_until: u64,
    /// Number of consecutive denials. Drives exponential backoff (1, 2, 4,
    /// then 8s thereafter). Reset to 0 on any grant.
    failed_attempts: u8,
}

impl Default for AccessCore {
    fn default() -> Self {
        Self::new()
    }
}

impl AccessCore {
    pub const fn new() -> Self {
        Self {
            pending_recheck: None,
            backoff_until: 0,
            failed_attempts: 0,
        }
    }

    /// Read-only access to the pending recheck window, for tests.
    pub fn pending_recheck(&self) -> Option<(u32, u32, u64)> {
        self.pending_recheck
    }

    /// Read-only access to the backoff deadline, for tests.
    pub fn backoff_until(&self) -> u64 {
        self.backoff_until
    }

    /// Read-only access to the consecutive-denial counter, for tests.
    pub fn failed_attempts(&self) -> u8 {
        self.failed_attempts
    }

    /// Step the state machine.
    ///
    /// - `now_ms`: virtual wall clock (milliseconds).
    /// - `fobs`: snapshot of authorized credential IDs at this instant.
    /// - `input`: the event being delivered.
    ///
    /// Returns the ordered list of effects the firmware adapter must apply.
    pub fn step(
        &mut self,
        now_ms: u64,
        fobs: &[u32],
        input: Input,
    ) -> HVec<Effect, MAX_EFFECTS_PER_STEP> {
        let mut out: HVec<Effect, MAX_EFFECTS_PER_STEP> = HVec::new();

        match input {
            Input::WatchdogFeed => {
                let _ = out.push(Effect::FeedWatchdog);
            }

            Input::SyncComplete => {
                if let Some((fob, nfc, deadline)) = self.pending_recheck.take() {
                    if now_ms > deadline {
                        // Recheck expired; do nothing.
                        return out;
                    }
                    let allowed =
                        fobs.iter().any(|&f| f == fob) || fobs.iter().any(|&f| f == nfc);
                    if allowed {
                        // Mirror main.rs:336-340 — failed_attempts is reset
                        // to 0 here, but `backoff_until` is intentionally
                        // *not* cleared. A grant after sync therefore still
                        // honors any outstanding backoff window for future
                        // card reads. Tests pin this behavior.
                        self.failed_attempts = 0;
                        let _ = out.push(Effect::Feedback(Outcome::Granted));
                        let _ = out.push(Effect::OpenDoor);
                    } else {
                        self.failed_attempts = self.failed_attempts.saturating_add(1);
                        let delay_ms = (1u64 << self.failed_attempts.min(3)) * 1000;
                        self.backoff_until = now_ms + delay_ms;
                        let _ = out.push(Effect::Feedback(Outcome::Denied));
                    }
                }
            }

            Input::Card(read) => {
                if now_ms < self.backoff_until {
                    // Card ignored during backoff window; no effects.
                    return out;
                }

                let fob = read.fob;
                let nfc = read.nfc;

                let fob_ok = fobs.iter().any(|&f| f == fob);
                let nfc_ok = !fob_ok && fobs.iter().any(|&f| f == nfc);
                let allowed = fob_ok || nfc_ok;

                if allowed {
                    self.failed_attempts = 0;
                    let credential = if fob_ok { fob } else { nfc };
                    let _ = out.push(Effect::Record(AccessEvent {
                        fob: credential,
                        allowed: true,
                    }));
                    let _ = out.push(Effect::Feedback(Outcome::Granted));
                    let _ = out.push(Effect::OpenDoor);
                } else {
                    let _ = out.push(Effect::Record(AccessEvent { fob, allowed: false }));
                    let _ = out.push(Effect::Feedback(Outcome::Denied));
                    let _ = out.push(Effect::RequestSync);
                    self.pending_recheck = Some((fob, nfc, now_ms + RECHECK_DEADLINE_MS));
                }
            }
        }

        out
    }
}
