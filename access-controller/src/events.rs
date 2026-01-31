//! Event buffer with peek/commit semantics for reliable event delivery.
//!
//! Events are only removed from the buffer after the server acknowledges receipt.
//! This ensures events are not lost if sync fails.

use embassy_sync::blocking_mutex::raw::CriticalSectionRawMutex;
use embassy_sync::mutex::Mutex;

pub const MAX_EVENTS: usize = 20;

/// Access event to report to Conway server.
#[derive(Clone, Copy, Default)]
pub struct AccessEvent {
    pub fob: u32,
    pub allowed: bool,
}

/// Event buffer state.
struct EventBufferInner {
    events: [AccessEvent; MAX_EVENTS],
    head: usize, // next write position
    tail: usize, // next read position
}

impl EventBufferInner {
    const fn new() -> Self {
        Self {
            events: [AccessEvent { fob: 0, allowed: false }; MAX_EVENTS],
            head: 0,
            tail: 0,
        }
    }

    fn len(&self) -> usize {
        if self.head >= self.tail {
            self.head - self.tail
        } else {
            MAX_EVENTS - self.tail + self.head
        }
    }

    fn is_full(&self) -> bool {
        (self.head + 1) % MAX_EVENTS == self.tail
    }
}

/// Thread-safe event buffer with peek/commit semantics.
pub struct EventBuffer {
    inner: Mutex<CriticalSectionRawMutex, EventBufferInner>,
}

impl EventBuffer {
    pub const fn new() -> Self {
        Self {
            inner: Mutex::new(EventBufferInner::new()),
        }
    }

    /// Push an event to the buffer.
    /// If the buffer is full, the oldest event is discarded.
    pub async fn push(&self, event: AccessEvent) {
        let mut guard = self.inner.lock().await;

        // If buffer is full, advance tail to discard oldest event
        if guard.is_full() {
            log::warn!("events: buffer full, dropping oldest event");
            guard.tail = (guard.tail + 1) % MAX_EVENTS;
        }

        let head = guard.head;
        guard.events[head] = event;
        guard.head = (head + 1) % MAX_EVENTS;
    }

    /// Peek at pending events without removing them.
    /// Returns (events, count, tail_snapshot).
    /// The tail_snapshot should be passed to commit() after successful sync.
    pub async fn peek(&self, out: &mut [AccessEvent; MAX_EVENTS]) -> (usize, usize) {
        let guard = self.inner.lock().await;
        let tail = guard.tail;
        let head = guard.head;

        let mut count = 0;
        let mut idx = tail;
        while idx != head && count < MAX_EVENTS {
            out[count] = guard.events[idx];
            count += 1;
            idx = (idx + 1) % MAX_EVENTS;
        }

        (count, tail)
    }

    /// Commit (remove) events from the buffer after successful transmission.
    /// Takes the tail_snapshot from peek(). If tail has changed (buffer overflow
    /// occurred during sync), this adjusts accordingly.
    pub async fn commit(&self, count: usize, expected_tail: usize) {
        let mut guard = self.inner.lock().await;

        // Calculate where tail should be after committing
        let new_tail = (expected_tail + count) % MAX_EVENTS;

        // Only update if tail hasn't been modified by overflow handling
        if guard.tail == expected_tail {
            guard.tail = new_tail;
            log::debug!("events: committed {} events", count);
        } else {
            // Tail was moved by overflow - only advance if we would move it forward
            // Calculate the distance from current tail to new_tail in circular space
            let distance_forward = if new_tail >= guard.tail {
                new_tail - guard.tail
            } else {
                MAX_EVENTS - guard.tail + new_tail
            };

            // If new_tail is ahead of current tail in circular space, advance to it
            // Otherwise, overflow already moved tail past where we would commit to
            if distance_forward < MAX_EVENTS / 2 {
                // new_tail is ahead - advance tail
                guard.tail = new_tail;
                log::debug!(
                    "events: committed {} events (adjusted after overflow moved tail from {} to {})",
                    count,
                    expected_tail,
                    new_tail
                );
            } else {
                // new_tail is behind or equal - overflow already discarded our events
                log::debug!(
                    "events: peeked events already removed by overflow (tail moved from {} to {}, would commit to {})",
                    expected_tail,
                    guard.tail,
                    new_tail
                );
            }
        }
    }

    /// Get current event count (for status display).
    pub async fn len(&self) -> usize {
        let guard = self.inner.lock().await;
        guard.len()
    }
}
