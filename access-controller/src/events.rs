//! Access event reported to the Conway server.

/// A single swipe event: which credential was presented and whether the
/// local cache authorized it. Buffered locally and POSTed to Conway during
/// the next sync; only removed from the buffer after the server ACKs.
#[derive(Clone, Copy, Default, Debug, PartialEq, Eq)]
pub struct AccessEvent {
    pub fob: u32,
    pub allowed: bool,
}
