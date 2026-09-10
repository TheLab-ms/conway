//! Persistent Conway-synced fob cache, stored in the dedicated `cache`
//! partition.
//!
//! This is the "last-good" remote fob list: the set of fob IDs the last
//! successful Conway sync authorized. It is written to flash on every
//! change during a sync, and loaded at boot into the in-RAM [`super::FOBS`]
//! cache. That way a reboot during a WiFi outage (power blip, watchdog
//! reset, OTA flash) still boots with the previously-known-good list and
//! keeps authorizing those fobs until a fresh sync lands — instead of
//! authorizing nothing until WiFi returns.
//!
//! ## Layout
//!
//! Same ping-pong + monotonic-seq design as `settings.rs` / `fob_store.rs`:
//! two 4 KiB sectors written alternately so a power loss mid-write always
//! leaves the previous-good sector intact.
//!
//! ## Confidentiality
//!
//! Each sector stores an encrypted record under ChaCha20-Poly1305 with the
//! `cache` per-device sub-key derived from eFuse BLOCK3 (see
//! [`crate::device_key`] for the threat model). Envelope is defined by
//! [`crate::crypto`]: `[header(32)] [ciphertext(N)] [tag(16)]`, with
//! `magic = "CCH1"` and domain tag `"FCH1"` — distinct from both the
//! `fobs` and `nvs` stores (cross-partition splicing defence).
//!
//! ## Cache-only authorization stays intact
//!
//! Persisting the cache does **not** change the authorization model: a
//! fob is still only granted if it is present in the local list (`fobs`
//! partition, operator-managed) or in the synced cache. Persisting just
//! makes that cache survive reboots. The server remains the source of
//! truth; a 304 (not modified) or a verified signature both count as
//! "the cached list is current".
//!
//! ## Behavior when device key is not provisioned
//!
//! [`load`] returns an empty `Vec`. [`save`] returns
//! `Err("device not provisioned")`. The device still works — the synced
//! cache just lives in RAM until a sync succeeds, matching pre-persist
//! behaviour.

use embedded_storage::{ReadStorage, Storage};
use esp_storage::FlashStorage;
use heapless::Vec as HVec;

use crate::device_key;
use crate::MAX_FOBS;
use access_controller::crypto;

/// Start of the `cache` partition. Keep in sync with `partitions.csv`.
const CACHE_BASE: u32 = 0x3E0000;
/// Flash erase granularity / our logical slot size.
const SECTOR: u32 = 4096;
/// Ping-pong: first two sectors of the partition.
const SLOTS: [u32; 2] = [CACHE_BASE, CACHE_BASE + SECTOR];

/// Per-store magic: "CCH1" (preserved across format versions for log clarity).
const MAGIC: u32 = 0x43_43_48_31;

/// Plaintext payload upper bound: count prefix (2) + max fobs × 4 bytes.
const MAX_PLAINTEXT: usize = 2 + MAX_FOBS * 4;

// ---------- plaintext serialization -----------------------------------
//
// Format: `count u16 LE`, then `count` × `fob_id u32 LE`. No labels — this
// is the raw Conway cache, not the operator-managed local list.

fn serialize(fobs: &[u32]) -> alloc::vec::Vec<u8> {
    let mut out = alloc::vec::Vec::with_capacity(2 + fobs.len() * 4);
    let n = fobs.len().min(MAX_FOBS) as u16;
    out.extend_from_slice(&n.to_le_bytes());
    for f in fobs.iter().take(n as usize) {
        out.extend_from_slice(&f.to_le_bytes());
    }
    out
}

fn deserialize(buf: &[u8]) -> Option<HVec<u32, MAX_FOBS>> {
    if buf.len() < 2 {
        return None;
    }
    let count = u16::from_le_bytes([buf[0], buf[1]]) as usize;
    if count > MAX_FOBS {
        return None;
    }
    if buf.len() != 2 + count * 4 {
        return None;
    }
    let mut out: HVec<u32, MAX_FOBS> = HVec::new();
    for i in 0..count {
        let off = 2 + i * 4;
        let id = u32::from_le_bytes([buf[off], buf[off + 1], buf[off + 2], buf[off + 3]]);
        // Push cannot fail because count <= MAX_FOBS.
        let _ = out.push(id);
    }
    Some(out)
}

// ---------- sector I/O ------------------------------------------------

struct Record {
    seq: u64,
    payload: alloc::vec::Vec<u8>,
}

fn read_slot(flash: &mut FlashStorage, base: u32, key: &[u8; 32]) -> Option<Record> {
    // Read header first to learn payload_len, then read the rest.
    let mut hdr = [0u8; crypto::HEADER_LEN];
    flash.read(base, &mut hdr).ok()?;
    let (seq, payload_len) = crypto::parse_header(&hdr, MAGIC, crypto::DOMAIN_FOBS_CACHE)?;
    let pt_len = payload_len as usize;
    if pt_len > MAX_PLAINTEXT
        || crypto::HEADER_LEN + pt_len + crypto::TAG_LEN > SECTOR as usize
    {
        return None;
    }

    let total = crypto::HEADER_LEN + pt_len + crypto::TAG_LEN;
    let mut sealed = alloc::vec![0u8; total];
    flash.read(base, &mut sealed).ok()?;

    let mut plaintext = alloc::vec![0u8; pt_len];
    match crypto::open(key, MAGIC, crypto::DOMAIN_FOBS_CACHE, &sealed, &mut plaintext) {
        Ok(_n) => Some(Record { seq, payload: plaintext }),
        Err(e) => {
            log::warn!("cache_store: slot @0x{:X} AEAD open failed: {:?}", base, e);
            None
        }
    }
}

fn write_slot(
    flash: &mut FlashStorage,
    base: u32,
    seq: u64,
    fobs: &[u32],
    key: &[u8; 32],
) -> Result<(), &'static str> {
    let plaintext = serialize(fobs);
    if plaintext.len() > MAX_PLAINTEXT {
        return Err("payload too large");
    }
    let total = crypto::HEADER_LEN + plaintext.len() + crypto::TAG_LEN;
    if total > SECTOR as usize {
        return Err("payload too large");
    }
    if fobs.len() > u16::MAX as usize {
        return Err("too many fobs");
    }

    // Build full sector buffer so the underlying FlashStorage write is a
    // single sector-aligned erase+program. Unused tail stays 0xFF so a
    // future shorter record's read past payload_len cannot leak stale
    // ciphertext (the AEAD never reads past the declared len anyway).
    let mut buf = alloc::vec![0xFFu8; SECTOR as usize];
    crypto::seal(
        key,
        MAGIC,
        seq,
        crypto::DOMAIN_FOBS_CACHE,
        &plaintext,
        &mut buf[..total],
    )
    .map_err(|_| "crypto seal failed")?;

    flash.write(base, &buf).map_err(|_| "flash write failed")?;
    Ok(())
}

fn erase_slot(flash: &mut FlashStorage, base: u32) -> Result<(), &'static str> {
    let blank = alloc::vec![0xFFu8; SECTOR as usize];
    flash.write(base, &blank).map_err(|_| "flash erase failed")
}

/// Read just the 32-byte envelope header from a slot and return its
/// `seq` if the header is structurally valid (magic / version / nonce
/// consistent), regardless of whether the AEAD body decrypts.
///
/// Used by [`save`] to derive `next_seq`: if a previous save was
/// interrupted mid-write, the half-written slot's tag/ciphertext will
/// fail to open and `read_slot` returns `None`, but the header itself
/// is the first thing written and is almost always intact. Skipping
/// such a slot when picking `next_seq` would let a retry reuse the same
/// nonce with different plaintext — catastrophic for ChaCha20-Poly1305.
/// Parsing the header recovers the seq cheaply and closes that gap.
fn peek_slot_seq(flash: &mut FlashStorage, base: u32) -> Option<u64> {
    let mut hdr = [0u8; crypto::HEADER_LEN];
    flash.read(base, &mut hdr).ok()?;
    crypto::parse_header(&hdr, MAGIC, crypto::DOMAIN_FOBS_CACHE).map(|(seq, _)| seq)
}

// ---------- public API ------------------------------------------------

/// Load the persisted last-good synced fob list. Returns an empty list if
/// neither slot contains a valid encrypted record (first boot / factory
/// wipe), or if the device is not yet provisioned with a per-device key.
pub fn load() -> HVec<u32, MAX_FOBS> {
    let Some(key) = device_key::cache_key() else {
        if device_key::state() != device_key::KeyState::Uninit {
            log::warn!("cache_store: device unprovisioned, skipping load");
        }
        return HVec::new();
    };
    let mut flash = FlashStorage::new();
    let a = read_slot(&mut flash, SLOTS[0], key);
    let b = read_slot(&mut flash, SLOTS[1], key);
    let winner = match (a, b) {
        (Some(a), Some(b)) => {
            // Signed diff handles u64 wraparound (irrelevant in practice
            // but free correctness).
            if (a.seq.wrapping_sub(b.seq)) as i64 >= 0 {
                a
            } else {
                b
            }
        }
        (Some(a), None) => a,
        (None, Some(b)) => b,
        (None, None) => return HVec::new(),
    };
    deserialize(&winner.payload).unwrap_or_default()
}

/// Persist a newly-synced fob list. Writes to the older slot, then erases
/// the other. Returns an error if the device is not yet provisioned.
///
/// Callers should only call this when the list actually changed (the
/// in-RAM cache already holds the new list) — repeated identical saves
/// would otherwise burn flash wear every 10 s sync tick.
///
/// The caller may pass a list that is already in RAM (`fobs`); this
/// function does not coordinate with the in-RAM [`super::FOBS`] mutex, so
/// it is safe to call after releasing that lock.
pub fn save(fobs: &[u32]) -> Result<(), &'static str> {
    let Some(key) = device_key::cache_key() else {
        return Err("device not provisioned (eFuse BLOCK3 unset)");
    };
    let mut flash = FlashStorage::new();
    let a = read_slot(&mut flash, SLOTS[0], key);
    let b = read_slot(&mut flash, SLOTS[1], key);

    // Pick write slot based on which successfully-opened slot is older
    // (or use slot 0 if neither opens).
    let write_idx: u8 = match (&a, &b) {
        (None, None) => 0,
        (None, Some(_)) => 0,
        (Some(_), None) => 1,
        (Some(ra), Some(rb)) => {
            if (ra.seq.wrapping_sub(rb.seq)) as i64 >= 0 {
                1
            } else {
                0
            }
        }
    };

    // Compute next_seq from ANY parseable header (open success not
    // required) to avoid nonce reuse after an interrupted prior save.
    // See `peek_slot_seq` docs above and H1 in the security review.
    let seq_a = peek_slot_seq(&mut flash, SLOTS[0]);
    let seq_b = peek_slot_seq(&mut flash, SLOTS[1]);
    let max_hdr_seq = match (seq_a, seq_b) {
        (Some(x), Some(y)) => Some(if (x.wrapping_sub(y)) as i64 >= 0 { x } else { y }),
        (Some(x), None) | (None, Some(x)) => Some(x),
        (None, None) => None,
    };
    let next_seq = max_hdr_seq.map(|s| s.wrapping_add(1)).unwrap_or(1u64);

    write_slot(&mut flash, SLOTS[write_idx as usize], next_seq, fobs, key)?;
    let other = (1 - write_idx) as usize;
    let _ = erase_slot(&mut flash, SLOTS[other]);

    log::info!(
        "cache_store: saved seq={} to slot {} ({} fobs, encrypted)",
        next_seq,
        write_idx,
        fobs.len()
    );
    Ok(())
}

/// Wipe both slots. Always succeeds even if the device is unprovisioned
/// (factory reset must work on broken units too).
pub fn erase() -> Result<(), &'static str> {
    let mut flash = FlashStorage::new();
    erase_slot(&mut flash, SLOTS[0])?;
    erase_slot(&mut flash, SLOTS[1])?;
    log::warn!("cache_store: wiped");
    Ok(())
}