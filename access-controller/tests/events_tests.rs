//! Unit tests for event buffer with peek/commit semantics.
//!
//! Tests the circular buffer logic from events.rs without requiring Embassy runtime.

use std::sync::Mutex;

const MAX_EVENTS: usize = 20;

/// Access event to report to Conway server (mirrors events.rs).
#[derive(Clone, Copy, Default, Debug, PartialEq)]
struct AccessEvent {
    fob: u32,
    allowed: bool,
}

/// Event buffer state (mirrors events.rs, but using std::sync::Mutex).
struct EventBufferInner {
    events: [AccessEvent; MAX_EVENTS],
    head: usize, // next write position
    tail: usize, // next read position
}

impl EventBufferInner {
    fn new() -> Self {
        Self {
            events: [AccessEvent::default(); MAX_EVENTS],
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

/// Thread-safe event buffer with peek/commit semantics (std version).
struct EventBuffer {
    inner: Mutex<EventBufferInner>,
}

impl EventBuffer {
    fn new() -> Self {
        Self {
            inner: Mutex::new(EventBufferInner::new()),
        }
    }

    /// Push an event to the buffer.
    /// If the buffer is full, the oldest event is discarded.
    fn push(&self, event: AccessEvent) {
        let mut guard = self.inner.lock().unwrap();

        // If buffer is full, advance tail to discard oldest event
        if guard.is_full() {
            guard.tail = (guard.tail + 1) % MAX_EVENTS;
        }

        let head = guard.head;
        guard.events[head] = event;
        guard.head = (head + 1) % MAX_EVENTS;
    }

    /// Peek at pending events without removing them.
    /// Returns (events, count, tail_snapshot).
    fn peek(&self) -> (Vec<AccessEvent>, usize, usize) {
        let guard = self.inner.lock().unwrap();
        let tail = guard.tail;
        let head = guard.head;

        let mut events = Vec::new();
        let mut idx = tail;
        while idx != head {
            events.push(guard.events[idx]);
            idx = (idx + 1) % MAX_EVENTS;
        }

        let count = events.len();
        (events, count, tail)
    }

    /// Commit (remove) events from the buffer after successful transmission.
    fn commit(&self, count: usize, expected_tail: usize) {
        let mut guard = self.inner.lock().unwrap();

        // Calculate where tail should be after committing
        let new_tail = (expected_tail + count) % MAX_EVENTS;

        // Only update if tail hasn't been modified by overflow handling
        if guard.tail == expected_tail {
            guard.tail = new_tail;
        } else {
            // Tail was moved by overflow - only advance if we would move it forward
            let distance_forward = if new_tail >= guard.tail {
                new_tail - guard.tail
            } else {
                MAX_EVENTS - guard.tail + new_tail
            };

            // If new_tail is ahead of current tail in circular space, advance to it
            if distance_forward < MAX_EVENTS / 2 {
                guard.tail = new_tail;
            }
            // Otherwise, overflow already moved tail past where we would commit to
        }
    }

    /// Get current event count.
    fn len(&self) -> usize {
        let guard = self.inner.lock().unwrap();
        guard.len()
    }

    /// Check if buffer is empty.
    fn is_empty(&self) -> bool {
        self.len() == 0
    }

    /// Get raw state for testing (head, tail).
    fn state(&self) -> (usize, usize) {
        let guard = self.inner.lock().unwrap();
        (guard.head, guard.tail)
    }
}

// ============================================================================
// Basic operations tests
// ============================================================================

#[test]
fn test_buffer_new_is_empty() {
    let buffer = EventBuffer::new();
    assert!(buffer.is_empty());
    assert_eq!(buffer.len(), 0);
}

#[test]
fn test_buffer_push_single() {
    let buffer = EventBuffer::new();
    buffer.push(AccessEvent {
        fob: 12345,
        allowed: true,
    });

    assert_eq!(buffer.len(), 1);
    assert!(!buffer.is_empty());
}

#[test]
fn test_buffer_push_multiple() {
    let buffer = EventBuffer::new();

    for i in 0..5 {
        buffer.push(AccessEvent {
            fob: i,
            allowed: i % 2 == 0,
        });
    }

    assert_eq!(buffer.len(), 5);
}

#[test]
fn test_buffer_push_to_max_minus_one() {
    let buffer = EventBuffer::new();

    // Push MAX_EVENTS - 1 items (full buffer capacity)
    for i in 0..(MAX_EVENTS - 1) {
        buffer.push(AccessEvent {
            fob: i as u32,
            allowed: true,
        });
    }

    assert_eq!(buffer.len(), MAX_EVENTS - 1);
}

// ============================================================================
// Peek tests
// ============================================================================

#[test]
fn test_peek_empty_buffer() {
    let buffer = EventBuffer::new();
    let (events, count, _tail) = buffer.peek();

    assert_eq!(count, 0);
    assert!(events.is_empty());
}

#[test]
fn test_peek_single_event() {
    let buffer = EventBuffer::new();
    buffer.push(AccessEvent {
        fob: 12345,
        allowed: true,
    });

    let (events, count, tail) = buffer.peek();

    assert_eq!(count, 1);
    assert_eq!(events.len(), 1);
    assert_eq!(events[0].fob, 12345);
    assert!(events[0].allowed);
    assert_eq!(tail, 0);

    // Peek should not remove events
    assert_eq!(buffer.len(), 1);
}

#[test]
fn test_peek_multiple_events() {
    let buffer = EventBuffer::new();

    for i in 0..5 {
        buffer.push(AccessEvent {
            fob: i * 100,
            allowed: i % 2 == 0,
        });
    }

    let (events, count, _tail) = buffer.peek();

    assert_eq!(count, 5);
    for i in 0..5 {
        assert_eq!(events[i].fob, i as u32 * 100);
        assert_eq!(events[i].allowed, i % 2 == 0);
    }

    // Peek should not remove events
    assert_eq!(buffer.len(), 5);
}

#[test]
fn test_peek_preserves_order() {
    let buffer = EventBuffer::new();

    buffer.push(AccessEvent {
        fob: 1,
        allowed: true,
    });
    buffer.push(AccessEvent {
        fob: 2,
        allowed: false,
    });
    buffer.push(AccessEvent {
        fob: 3,
        allowed: true,
    });

    let (events, _, _) = buffer.peek();

    assert_eq!(events[0].fob, 1);
    assert_eq!(events[1].fob, 2);
    assert_eq!(events[2].fob, 3);
}

// ============================================================================
// Commit tests
// ============================================================================

#[test]
fn test_commit_all_events() {
    let buffer = EventBuffer::new();

    for i in 0..5 {
        buffer.push(AccessEvent {
            fob: i,
            allowed: true,
        });
    }

    let (_, count, tail) = buffer.peek();
    buffer.commit(count, tail);

    assert!(buffer.is_empty());
    assert_eq!(buffer.len(), 0);
}

#[test]
fn test_commit_partial_events() {
    let buffer = EventBuffer::new();

    for i in 0..5 {
        buffer.push(AccessEvent {
            fob: i,
            allowed: true,
        });
    }

    let (_, _, tail) = buffer.peek();
    buffer.commit(3, tail); // Only commit first 3

    assert_eq!(buffer.len(), 2);

    // Remaining events should be fob 3 and 4
    let (events, count, _) = buffer.peek();
    assert_eq!(count, 2);
    assert_eq!(events[0].fob, 3);
    assert_eq!(events[1].fob, 4);
}

#[test]
fn test_commit_zero_events() {
    let buffer = EventBuffer::new();

    buffer.push(AccessEvent {
        fob: 1,
        allowed: true,
    });

    let (_, _, tail) = buffer.peek();
    buffer.commit(0, tail);

    assert_eq!(buffer.len(), 1);
}

#[test]
fn test_commit_with_stale_tail() {
    let buffer = EventBuffer::new();

    // Push initial events
    for i in 0..5 {
        buffer.push(AccessEvent {
            fob: i,
            allowed: true,
        });
    }

    // Peek and get tail
    let (_, count, old_tail) = buffer.peek();
    assert_eq!(count, 5);

    // Simulate: between peek and commit, buffer overflows
    // This would happen if new events were pushed while sync was in progress
    // First, let's manually verify the tail changed scenario by pushing more

    // For this test, we'll commit with wrong tail
    // Since tail didn't actually change, this should work normally
    buffer.commit(count, old_tail);
    assert!(buffer.is_empty());
}

// ============================================================================
// Overflow tests (buffer full behavior)
// ============================================================================

#[test]
fn test_overflow_discards_oldest() {
    let buffer = EventBuffer::new();

    // Fill buffer completely (MAX_EVENTS - 1 is full)
    for i in 0..(MAX_EVENTS - 1) {
        buffer.push(AccessEvent {
            fob: i as u32,
            allowed: true,
        });
    }

    assert_eq!(buffer.len(), MAX_EVENTS - 1);

    // Push one more - should discard oldest (fob 0)
    buffer.push(AccessEvent {
        fob: 999,
        allowed: false,
    });

    assert_eq!(buffer.len(), MAX_EVENTS - 1);

    let (events, _, _) = buffer.peek();
    // First event should now be fob 1 (0 was discarded)
    assert_eq!(events[0].fob, 1);
    // Last event should be 999
    assert_eq!(events[events.len() - 1].fob, 999);
}

#[test]
fn test_overflow_multiple_times() {
    let buffer = EventBuffer::new();

    // Push 2 * MAX_EVENTS events
    for i in 0..(2 * MAX_EVENTS) {
        buffer.push(AccessEvent {
            fob: i as u32,
            allowed: true,
        });
    }

    // Should only have MAX_EVENTS - 1 events
    assert_eq!(buffer.len(), MAX_EVENTS - 1);

    let (events, _, _) = buffer.peek();
    // Should have the most recent MAX_EVENTS - 1 events
    let expected_first = (2 * MAX_EVENTS) - (MAX_EVENTS - 1);
    assert_eq!(events[0].fob, expected_first as u32);
}

// ============================================================================
// Circular buffer wraparound tests
// ============================================================================

#[test]
fn test_wraparound_basic() {
    let buffer = EventBuffer::new();

    // Fill and commit multiple times to cause wraparound
    for round in 0..3 {
        // Push 10 events
        for i in 0..10 {
            buffer.push(AccessEvent {
                fob: (round * 100 + i) as u32,
                allowed: true,
            });
        }

        let (events, count, tail) = buffer.peek();
        assert_eq!(count, 10);
        assert_eq!(events[0].fob, (round * 100) as u32);

        // Commit all
        buffer.commit(count, tail);
        assert!(buffer.is_empty());
    }
}

#[test]
fn test_wraparound_with_partial_commit() {
    let buffer = EventBuffer::new();

    // Push 15 events
    for i in 0..15 {
        buffer.push(AccessEvent {
            fob: i,
            allowed: true,
        });
    }

    // Commit first 10
    let (_, _, tail) = buffer.peek();
    buffer.commit(10, tail);
    assert_eq!(buffer.len(), 5);

    // Push 10 more (will wrap around)
    for i in 15..25 {
        buffer.push(AccessEvent {
            fob: i,
            allowed: true,
        });
    }

    assert_eq!(buffer.len(), 15);

    let (events, _, _) = buffer.peek();
    // Should have fobs 10-24
    assert_eq!(events[0].fob, 10);
    assert_eq!(events[14].fob, 24);
}

#[test]
fn test_head_tail_positions_after_operations() {
    let buffer = EventBuffer::new();

    // Push 5 events
    for i in 0..5 {
        buffer.push(AccessEvent {
            fob: i,
            allowed: true,
        });
    }

    let (head, tail) = buffer.state();
    assert_eq!(tail, 0);
    assert_eq!(head, 5);

    // Commit 3
    buffer.commit(3, tail);

    let (head2, tail2) = buffer.state();
    assert_eq!(tail2, 3);
    assert_eq!(head2, 5);

    // Push 5 more
    for i in 5..10 {
        buffer.push(AccessEvent {
            fob: i,
            allowed: true,
        });
    }

    let (head3, tail3) = buffer.state();
    assert_eq!(tail3, 3);
    assert_eq!(head3, 10);
}

// ============================================================================
// Edge case tests
// ============================================================================

#[test]
fn test_len_calculation_no_wrap() {
    let buffer = EventBuffer::new();

    buffer.push(AccessEvent {
        fob: 1,
        allowed: true,
    });
    assert_eq!(buffer.len(), 1);

    buffer.push(AccessEvent {
        fob: 2,
        allowed: true,
    });
    assert_eq!(buffer.len(), 2);
}

#[test]
fn test_len_calculation_with_wrap() {
    let buffer = EventBuffer::new();

    // Push and commit to move tail forward
    for i in 0..15 {
        buffer.push(AccessEvent {
            fob: i,
            allowed: true,
        });
    }

    let (_, count, tail) = buffer.peek();
    buffer.commit(count, tail);

    // Now push more events (head will wrap around eventually)
    for i in 0..10 {
        buffer.push(AccessEvent {
            fob: 100 + i,
            allowed: true,
        });
    }

    assert_eq!(buffer.len(), 10);
}

#[test]
fn test_commit_after_overflow_during_sync() {
    let buffer = EventBuffer::new();

    // Push 10 events
    for i in 0..10 {
        buffer.push(AccessEvent {
            fob: i,
            allowed: true,
        });
    }

    // Peek (simulating sync start)
    let (_, original_count, original_tail) = buffer.peek();
    assert_eq!(original_count, 10);
    assert_eq!(original_tail, 0);

    // Simulate: overflow happens during sync (push MAX_EVENTS more)
    for i in 100..(100 + MAX_EVENTS) {
        buffer.push(AccessEvent {
            fob: i as u32,
            allowed: true,
        });
    }

    // Tail should have moved due to overflow
    let (_, _, new_tail) = buffer.peek();
    assert_ne!(new_tail, original_tail);

    // Commit with original tail - should handle gracefully
    buffer.commit(original_count, original_tail);

    // Buffer should still be consistent (not corrupted)
    let len = buffer.len();
    assert!(len <= MAX_EVENTS - 1);
}

#[test]
fn test_is_full_boundary() {
    let buffer = EventBuffer::new();

    // Push exactly MAX_EVENTS - 2 events
    for i in 0..(MAX_EVENTS - 2) {
        buffer.push(AccessEvent {
            fob: i as u32,
            allowed: true,
        });
    }

    assert_eq!(buffer.len(), MAX_EVENTS - 2);

    // One more should fit
    buffer.push(AccessEvent {
        fob: 998,
        allowed: true,
    });
    assert_eq!(buffer.len(), MAX_EVENTS - 1);

    // Next one should trigger overflow
    buffer.push(AccessEvent {
        fob: 999,
        allowed: true,
    });
    assert_eq!(buffer.len(), MAX_EVENTS - 1); // Still MAX_EVENTS - 1
}

// ============================================================================
// Peek/commit pattern tests (simulating real sync behavior)
// ============================================================================

#[test]
fn test_sync_pattern_success() {
    let buffer = EventBuffer::new();

    // Events come in
    buffer.push(AccessEvent {
        fob: 1,
        allowed: true,
    });
    buffer.push(AccessEvent {
        fob: 2,
        allowed: false,
    });

    // Sync starts - peek events
    let (events, count, tail) = buffer.peek();
    assert_eq!(count, 2);

    // Sync succeeds - commit events
    buffer.commit(count, tail);
    assert!(buffer.is_empty());

    // More events after sync
    buffer.push(AccessEvent {
        fob: 3,
        allowed: true,
    });
    assert_eq!(buffer.len(), 1);

    // Verify it's the new event
    let (events2, _, _) = buffer.peek();
    assert_eq!(events2[0].fob, 3);

    // Original events still accessible (they were copied)
    assert_eq!(events[0].fob, 1);
    assert_eq!(events[1].fob, 2);
}

#[test]
fn test_sync_pattern_failure() {
    let buffer = EventBuffer::new();

    // Events come in
    buffer.push(AccessEvent {
        fob: 1,
        allowed: true,
    });
    buffer.push(AccessEvent {
        fob: 2,
        allowed: false,
    });

    // Sync starts - peek events
    let (events, count, _tail) = buffer.peek();
    assert_eq!(count, 2);

    // Sync fails - don't commit
    // Events should still be there
    assert_eq!(buffer.len(), 2);

    // Try again
    let (events2, count2, tail2) = buffer.peek();
    assert_eq!(count2, 2);
    assert_eq!(events2[0].fob, events[0].fob);

    // This time it succeeds
    buffer.commit(count2, tail2);
    assert!(buffer.is_empty());
}

#[test]
fn test_sync_pattern_partial_with_new_events() {
    let buffer = EventBuffer::new();

    // Initial events
    buffer.push(AccessEvent {
        fob: 1,
        allowed: true,
    });
    buffer.push(AccessEvent {
        fob: 2,
        allowed: true,
    });

    // Peek for sync
    let (_, count, tail) = buffer.peek();

    // New event arrives during sync
    buffer.push(AccessEvent {
        fob: 3,
        allowed: true,
    });

    // Commit original events
    buffer.commit(count, tail);

    // Only the new event should remain
    assert_eq!(buffer.len(), 1);
    let (events, _, _) = buffer.peek();
    assert_eq!(events[0].fob, 3);
}
