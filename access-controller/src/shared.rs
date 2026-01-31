use core::sync::atomic::{AtomicBool, AtomicU8, AtomicU16, AtomicU32, Ordering};

pub const MAX_FOBS: usize = 512;
pub const MAX_EVENTS: usize = 20;

/// Flash write coordination state machine.
///
/// Protocol for safe dual-core flash writes:
/// 1. Core 1 sets state to `Requested`
/// 2. Core 0 sees `Requested`, finishes current work, sets state to `Safe`
/// 3. Core 1 sees `Safe`, performs flash write, sets state to `Done`
/// 4. Core 0 sees `Done`, resumes normal operation, sets state to `Idle`
///
/// This ensures Core 0 is never holding a spinlock when flash write occurs.
#[repr(u8)]
#[derive(Clone, Copy, PartialEq, Eq)]
pub enum FlashState {
    /// No flash operation pending
    Idle = 0,
    /// Core 1 is requesting a flash write
    Requested = 1,
    /// Core 0 has entered a safe state (no locks held)
    Safe = 2,
    /// Core 1 has completed the flash write
    Done = 3,
}

impl From<u8> for FlashState {
    fn from(v: u8) -> Self {
        match v {
            1 => FlashState::Requested,
            2 => FlashState::Safe,
            3 => FlashState::Done,
            _ => FlashState::Idle,
        }
    }
}

/// Access event to report to Conway server.
#[derive(Clone, Copy, Default)]
pub struct AccessEvent {
    pub fob: u32,
    pub allowed: bool,
}

/// Shared state between Core 0 (RFID) and Core 1 (network).
pub struct Shared {
    // Fob list - written by Core 1, read by Core 0
    pub fobs: [AtomicU32; MAX_FOBS],
    pub fob_count: AtomicU16,
    pub update_seq: AtomicU32, // Sequence counter: odd = update in progress, even = stable

    // Events ring buffer - written by Core 0, read by Core 1
    // Format: bit 31 = allowed, bits 30:0 = fob (fob=0 is reserved as "not ready" sentinel)
    pub events: [AtomicU32; MAX_EVENTS],
    pub event_head: AtomicU16, // next write position
    pub event_tail: AtomicU16, // next read position

    // Door unlock request from HTTP server
    pub unlock_request: AtomicBool,

    // Flash write coordination between cores
    pub flash_state: AtomicU8,
}

impl Shared {
    pub const fn new() -> Self {
        const ZERO: AtomicU32 = AtomicU32::new(0);
        Self {
            fobs: [ZERO; MAX_FOBS],
            fob_count: AtomicU16::new(0),
            update_seq: AtomicU32::new(0),
            events: [ZERO; MAX_EVENTS],
            event_head: AtomicU16::new(0),
            event_tail: AtomicU16::new(0),
            unlock_request: AtomicBool::new(false),
            flash_state: AtomicU8::new(FlashState::Idle as u8),
        }
    }

    /// Check if a fob is authorized (called from Core 0).
    /// Uses a seqlock pattern with a retry limit to prevent livelock.
    pub fn check_fob(&self, fob: u32) -> bool {
        const MAX_RETRIES: u32 = 100;
        let mut retries = 0;
        let mut update_spins = 0u32; // Track spins waiting for update to complete

        loop {
            // Read sequence number - if odd, update is in progress
            let seq1 = self.update_seq.load(Ordering::Acquire);
            if seq1 & 1 != 0 {
                // Update in progress, spin briefly and retry
                core::hint::spin_loop();
                retries += 1;
                update_spins += 1;
                if retries >= MAX_RETRIES {
                    // Safety fallback: if we can't get a consistent read,
                    // deny access (fail closed for security)
                    log::error!(
                        "check_fob: SEQLOCK CONTENTION - fob={} retries={} update_spins={} seq={}",
                        fob, retries, update_spins, seq1
                    );
                    return false;
                }
                continue;
            }

            let count = self.fob_count.load(Ordering::Relaxed) as usize;
            let mut found = false;

            for i in 0..count.min(MAX_FOBS) {
                if self.fobs[i].load(Ordering::Relaxed) == fob {
                    found = true;
                    break;
                }
            }

            // Verify sequence hasn't changed during the scan
            let seq2 = self.update_seq.load(Ordering::Acquire);
            if seq1 == seq2 {
                // Log if we had any contention (even if we succeeded)
                if retries > 0 {
                    log::debug!(
                        "check_fob: seqlock contention resolved - fob={} retries={} update_spins={}",
                        fob, retries, update_spins
                    );
                }
                return found;
            }

            // Sequence changed during read, retry the entire search
            retries += 1;
            if retries >= MAX_RETRIES {
                log::error!(
                    "check_fob: SEQLOCK CONTENTION - fob={} retries={} update_spins={} seq1={} seq2={}",
                    fob, retries, update_spins, seq1, seq2
                );
                return false;
            }
        }
    }

    /// Push an access event (called from Core 0).
    /// If the buffer is full, the oldest event is discarded.
    ///
    /// Uses a two-phase write pattern to avoid race conditions:
    /// 1. Claim slot by advancing head
    /// 2. Write data with "not ready" sentinel (fob=0) first
    /// 3. Write actual data with Release ordering
    ///
    /// Readers in peek_events spin-wait if they see fob=0, ensuring they never
    /// read stale data from a slot that was just claimed but not yet written.
    pub fn push_event(&self, fob: u32, allowed: bool) {
        // Fob value 0 is reserved as "not ready" sentinel - map it to 1
        // (fob 0 would be facility=0, card=0 which is invalid anyway)
        let safe_fob = if fob == 0 { 1 } else { fob };
        let packed = (safe_fob & 0x7FFF_FFFF) | ((allowed as u32) << 31);

        loop {
            let head = self.event_head.load(Ordering::Acquire) as usize;
            let tail = self.event_tail.load(Ordering::Acquire) as usize;
            let next_head = (head + 1) % MAX_EVENTS;

            // If buffer is full, advance tail to discard oldest event
            if next_head == tail {
                // Try to advance tail; if it fails, another thread changed it, so retry
                let _ = self.event_tail.compare_exchange(
                    tail as u16,
                    ((tail + 1) % MAX_EVENTS) as u16,
                    Ordering::AcqRel,
                    Ordering::Relaxed,
                );
                // Continue to retry the whole operation with fresh head/tail
                continue;
            }

            // Try to atomically claim the slot by advancing head
            if self
                .event_head
                .compare_exchange(head as u16, next_head as u16, Ordering::AcqRel, Ordering::Relaxed)
                .is_ok()
            {
                // We won the slot. Write the actual event data.
                // The slot previously contained either:
                // - 0 (initial state or cleared by commit_events)
                // - Valid data from a previous event that was already read
                //
                // Readers seeing this slot will spin if fob==0, so we're safe as long
                // as we write with Release ordering to ensure visibility.
                self.events[head % MAX_EVENTS].store(packed, Ordering::Release);
                break;
            }
            // CAS failed - another thread claimed the slot, retry with fresh values
        }
    }

    /// Peek at pending events without removing them (called from Core 1).
    /// Returns (count, tail_snapshot) - the tail_snapshot should be passed to commit_events.
    /// Events remain in the queue until `commit_events` is called.
    ///
    /// If a slot contains fob=0 (not ready sentinel), this function spin-waits briefly
    /// for the writer to complete. This handles the race where head was advanced but
    /// data hasn't been written yet.
    pub fn peek_events(&self, out: &mut [AccessEvent]) -> (usize, u16) {
        let tail = self.event_tail.load(Ordering::Acquire);
        let head = self.event_head.load(Ordering::Acquire);

        let mut count = 0;
        let mut idx = tail as usize;
        while idx != head as usize && count < out.len() {
            // Spin-wait if slot contains sentinel (writer claimed slot but hasn't written yet)
            let mut spins = 0;
            let packed = loop {
                let value = self.events[idx % MAX_EVENTS].load(Ordering::Acquire);
                let fob_part = value & 0x7FFF_FFFF;
                if fob_part != 0 {
                    break Some(value);
                }
                // Slot not ready - writer is still writing
                spins += 1;
                if spins > 1000 {
                    // Safety limit: if we spin too long, something is wrong.
                    // Skip this slot entirely rather than sending fob=0 to server.
                    log::warn!("peek_events: spin limit on slot {}, skipping", idx);
                    break None;
                }
                core::hint::spin_loop();
            };

            if let Some(packed) = packed {
                out[count] = AccessEvent {
                    fob: packed & 0x7FFF_FFFF,
                    allowed: (packed >> 31) != 0,
                };
                count += 1;
            }
            idx = (idx + 1) % MAX_EVENTS;
        }
        (count, tail)
    }

    /// Commit (remove) events from the queue after successful transmission.
    /// Takes the tail_snapshot from peek_events. If tail has changed (buffer overflow
    /// occurred during sync), this is a no-op since events were already dropped.
    /// Must be called after `peek_events` once events have been acknowledged by the server.
    pub fn commit_events(&self, count: usize, expected_tail: u16) {
        let new_tail = ((expected_tail as usize + count) % MAX_EVENTS) as u16;
        // Only update if tail hasn't been modified by push_event overflow handling
        let _ = self.event_tail.compare_exchange(
            expected_tail,
            new_tail,
            Ordering::AcqRel,
            Ordering::Relaxed,
        );
    }

    /// Update the fob list (called from Core 1).
    /// Uses seqlock pattern: increment sequence to odd (unstable), update data, increment to even (stable).
    pub fn update_fobs(&self, fobs: &[u32]) {
        // Signal update starting (odd sequence = unstable).
        // AcqRel ordering ensures this increment is visible to readers BEFORE any
        // subsequent data writes could be reordered before it.
        self.update_seq.fetch_add(1, Ordering::AcqRel);

        // No fence needed here - the Release on fetch_add above prevents reordering
        // of subsequent stores before the sequence increment.

        let count = fobs.len().min(MAX_FOBS);
        for (i, &fob) in fobs.iter().take(count).enumerate() {
            self.fobs[i].store(fob, Ordering::Relaxed);
        }
        self.fob_count.store(count as u16, Ordering::Relaxed);

        // Release fence ensures all stores above are visible to other cores
        // BEFORE we increment the sequence to signal completion. This prevents
        // readers from seeing the stable (even) sequence before data is fully written.
        core::sync::atomic::fence(Ordering::Release);

        // Signal update complete (even sequence = stable).
        // Relaxed is fine here because the fence above already ensures ordering.
        self.update_seq.fetch_add(1, Ordering::Relaxed);
    }

    /// Request door unlock (called from HTTP server on Core 1).
    pub fn request_unlock(&self) {
        self.unlock_request.store(true, Ordering::Release);
    }

    /// Check and clear unlock request (called from Core 0).
    pub fn take_unlock_request(&self) -> bool {
        self.unlock_request.swap(false, Ordering::AcqRel)
    }

    // ========================================================================
    // Flash write coordination (lock-free protocol between cores)
    // ========================================================================

    /// Get current flash coordination state.
    #[inline]
    pub fn flash_state(&self) -> FlashState {
        FlashState::from(self.flash_state.load(Ordering::Acquire))
    }

    /// Request flash write (called from Core 1).
    /// Returns false if a flash operation is already in progress.
    pub fn request_flash_write(&self) -> bool {
        self.flash_state
            .compare_exchange(
                FlashState::Idle as u8,
                FlashState::Requested as u8,
                Ordering::AcqRel,
                Ordering::Relaxed,
            )
            .is_ok()
    }

    /// Signal that Core 0 is in a safe state for flash writes.
    /// Called by Core 0 after it has finished any critical sections.
    pub fn signal_flash_safe(&self) {
        // Only transition from Requested -> Safe
        let _ = self.flash_state.compare_exchange(
            FlashState::Requested as u8,
            FlashState::Safe as u8,
            Ordering::AcqRel,
            Ordering::Relaxed,
        );
    }

    /// Signal that flash write is complete (called from Core 1).
    pub fn signal_flash_done(&self) {
        self.flash_state.store(FlashState::Done as u8, Ordering::Release);
    }

    /// Acknowledge flash completion and return to idle (called from Core 0).
    pub fn acknowledge_flash_done(&self) {
        // Only transition from Done -> Idle
        let _ = self.flash_state.compare_exchange(
            FlashState::Done as u8,
            FlashState::Idle as u8,
            Ordering::AcqRel,
            Ordering::Relaxed,
        );
    }

    /// Check if flash write is requested (called from Core 0).
    #[inline]
    pub fn is_flash_requested(&self) -> bool {
        self.flash_state() == FlashState::Requested
    }

    /// Check if flash write is done and needs acknowledgment (called from Core 0).
    #[inline]
    pub fn is_flash_done(&self) -> bool {
        self.flash_state() == FlashState::Done
    }

    /// Wait for Core 0 to enter safe state (called from Core 1).
    /// Returns true if safe state achieved, false on timeout.
    pub fn wait_for_flash_safe(&self, timeout_ms: u64) -> bool {
        let start = esp_hal::time::Instant::now().duration_since_epoch().as_millis();
        loop {
            let state = self.flash_state();
            if state == FlashState::Safe {
                return true;
            }
            if state != FlashState::Requested {
                // State changed unexpectedly (e.g., reset to Idle)
                log::warn!("flash: unexpected state {:?} while waiting for Safe", state as u8);
                return false;
            }
            let now = esp_hal::time::Instant::now().duration_since_epoch().as_millis();
            if now - start > timeout_ms {
                log::error!("flash: timeout waiting for Core 0 safe state");
                // Reset to Idle on timeout so we don't get stuck
                self.flash_state.store(FlashState::Idle as u8, Ordering::Release);
                return false;
            }
            // Yield to RTOS scheduler while waiting
            esp_radio_rtos_driver::usleep(1_000);
        }
    }
}

/// Global shared state - static for cross-core access.
pub static SHARED: Shared = Shared::new();
