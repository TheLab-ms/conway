//! Offline persistent log of fob swipes.
//!
//! ## Purpose
//!
//! In Conway mode every access decision is uploaded to the server (see
//! [`crate::sync`]) and persisted there, so no local history is needed.
//! In **standalone mode** (no Conway host configured) there is nowhere to
//! send events, and `EVENT_BUFFER` is a small RAM ring that is lost on
//! reboot. This module gives standalone units a durable, bounded audit
//! trail on flash so swipes can be reviewed later (`GET /swipes`).
//!
//! ## Storage
//!
//! Entries are stored with [`sequential_storage`]'s `queue` (FIFO)
//! abstraction, which provides wear-levelling and power-loss safety over
//! raw NOR flash for free — we do not hand-roll a sector layout the way
//! [`crate::fob_store`] does. The queue lives in the **tail** of the
//! `fobs` partition, after the two 4 KiB ping-pong sectors `fob_store`
//! uses:
//!
//! ```text
//!   0x11000 .. 0x13000   fob_store ping-pong (2 sectors)   [not ours]
//!   0x13000 .. 0x20000   swipe-log queue (13 sectors, 52 KiB)
//! ```
//!
//! Both bounds are 4 KiB-sector aligned, as `sequential_storage`
//! requires.
//!
//! ## "Limit on the number of entries"
//!
//! [`append`] pushes with `allow_overwrite_old_data = true`, turning the
//! queue into a **bounded ring**: once the 52 KiB region is full the
//! oldest entries are evicted to make room for new ones. The effective
//! cap is therefore the region capacity — each entry is 13 bytes plus a
//! few bytes of per-item framing, so on the order of a couple of
//! thousand swipes are retained before the oldest roll off. The log can
//! never grow without bound or exhaust flash.
//!
//! ## Confidentiality
//!
//! Plaintext. Unlike `fob_store`/`settings`, swipe records are not
//! encrypted: they hold only a fob number, an allow/deny bit, and an
//! uptime timestamp — no secrets — and keeping them plain lets an
//! operator read them back trivially. If at-rest confidentiality of the
//! audit trail is ever required, wrap each entry with [`crate::crypto`]
//! as the other stores do.
//!
//! ## Blocking
//!
//! All functions here touch flash and therefore block the CPU for the
//! duration of a read/erase/program. They must NOT be called from
//! `access_task` (the latency-critical decision loop). Instead
//! `access_task` hands entries to `SWIPE_LOG_CHANNEL` with a non-blocking
//! `try_send`, and the dedicated `swipe_log_task` drains that channel and
//! calls [`append`] here — exactly the decoupling pattern `sync_task`
//! uses for networking.

use core::ops::Range;

use embassy_embedded_hal::adapter::BlockingAsync;
use embedded_storage::nor_flash::NorFlash;
use esp_storage::FlashStorage;
use sequential_storage::cache::NoCache;
use sequential_storage::queue;

/// Start of the swipe-log region (first sector after `fob_store`'s two
/// ping-pong slots). Keep in sync with `partitions.csv` and
/// [`crate::fob_store`].
const SWIPE_LOG_BASE: u32 = 0x13000;
/// End of the `fobs` partition.
const SWIPE_LOG_END: u32 = 0x20000;

/// Flash range owned by the queue. `sequential_storage` requires the
/// bounds to be erase-sector (4 KiB) aligned, which both are.
const fn region() -> Range<u32> {
    SWIPE_LOG_BASE..SWIPE_LOG_END
}

/// Serialised size of one [`SwipeLogEntry`]: `fob`(4) + `allowed`(1) +
/// `at_ms`(8), little-endian.
const ENTRY_LEN: usize = 13;

/// Scratch buffer size for queue reads. Must be at least the largest
/// stored item; rounded up to give `sequential_storage` headroom for its
/// internal framing.
const BUF_LEN: usize = 32;

/// One recorded access decision.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct SwipeLogEntry {
    /// Fob number, or [`crate::MANUAL_UNLOCK_FOB`] for a web-UI unlock.
    pub fob: u32,
    /// Whether the door was opened.
    pub allowed: bool,
    /// Milliseconds since boot (`Instant::now().as_millis()`) at the time
    /// of the decision. Not wall-clock — standalone units have no RTC —
    /// but lets a reviewer order events and see relative timing within an
    /// uptime session.
    pub at_ms: u64,
}

impl SwipeLogEntry {
    fn encode(&self) -> [u8; ENTRY_LEN] {
        let mut b = [0u8; ENTRY_LEN];
        b[0..4].copy_from_slice(&self.fob.to_le_bytes());
        b[4] = self.allowed as u8;
        b[5..13].copy_from_slice(&self.at_ms.to_le_bytes());
        b
    }

    fn decode(b: &[u8]) -> Option<Self> {
        if b.len() < ENTRY_LEN {
            return None;
        }
        let fob = u32::from_le_bytes([b[0], b[1], b[2], b[3]]);
        let allowed = b[4] != 0;
        let at_ms = u64::from_le_bytes([b[5], b[6], b[7], b[8], b[9], b[10], b[11], b[12]]);
        Some(Self { fob, allowed, at_ms })
    }
}

/// Append one swipe to the flash log.
///
/// Pushes with `allow_overwrite_old_data = true` so a full region evicts
/// the oldest entry rather than failing — see the module-level note on
/// the entry limit. Blocks for the flash write (and, occasionally, a
/// 4 KiB sector erase as the ring advances); call only from
/// `swipe_log_task`, never from `access_task`.
pub async fn append(entry: &SwipeLogEntry) -> Result<(), &'static str> {
    let mut flash = BlockingAsync::new(FlashStorage::new());
    let mut cache = NoCache::new();
    let data = entry.encode();
    queue::push(&mut flash, region(), &mut cache, &data, true)
        .await
        .map_err(|e| {
            log::warn!("swipe_log: push failed: {:?}", e);
            "swipe_log push failed"
        })
}

/// Read back up to `N` of the most-recent entries, oldest-first.
///
/// The on-flash queue iterates oldest→newest; when more than `N` entries
/// are stored we drop from the front so the returned slice is the newest
/// `N` (still in chronological order). Intended for the `GET /swipes`
/// operator view, not a hot path — it scans the whole region.
pub async fn read_recent<const N: usize>() -> heapless::Vec<SwipeLogEntry, N> {
    let mut out: heapless::Vec<SwipeLogEntry, N> = heapless::Vec::new();
    if N == 0 {
        return out;
    }

    let mut flash = BlockingAsync::new(FlashStorage::new());
    let mut cache = NoCache::new();
    let mut iter = match queue::iter(&mut flash, region(), &mut cache).await {
        Ok(it) => it,
        Err(e) => {
            log::warn!("swipe_log: iter failed: {:?}", e);
            return out;
        }
    };

    let mut buf = [0u8; BUF_LEN];
    loop {
        match iter.next(&mut buf).await {
            Ok(Some(entry)) => {
                if let Some(e) = SwipeLogEntry::decode(&entry[..]) {
                    if out.is_full() {
                        // Keep only the newest N: drop the oldest.
                        out.remove(0);
                    }
                    let _ = out.push(e);
                }
            }
            Ok(None) => break,
            Err(e) => {
                log::warn!("swipe_log: iter next failed: {:?}", e);
                break;
            }
        }
    }
    out
}

/// Wipe the entire swipe-log region back to a fresh, empty queue.
///
/// Erasing NOR flash to all-`0xFF` is exactly the blank state
/// `sequential_storage` expects, so the next [`append`] starts a new
/// queue. Synchronous (mirrors [`crate::fob_store::erase`]); called from
/// the factory-reset path. Always attempts the erase even if a prior
/// step failed, so a reset works on damaged units.
pub fn erase() -> Result<(), &'static str> {
    let mut flash = FlashStorage::new();
    NorFlash::erase(&mut flash, SWIPE_LOG_BASE, SWIPE_LOG_END).map_err(|_| "swipe_log erase failed")?;
    log::warn!("swipe_log: wiped");
    Ok(())
}
