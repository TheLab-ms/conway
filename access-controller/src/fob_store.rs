//! Persistent local fob list, stored in the dedicated `fobs` partition.
//!
//! ## Layout
//!
//! Same ping-pong + monotonic-seq design as `settings.rs`: two 4 KiB
//! sectors at the start of the `fobs` partition, written alternately so
//! a power loss mid-write always leaves the previous-good sector intact.
//!
//! ## Confidentiality
//!
//! Each sector now stores an encrypted record under
//! ChaCha20-Poly1305 with a per-device key derived from eFuse BLOCK3
//! (see [`crate::device_key`] for threat model). The CRC-32 of the
//! v1 layout has been replaced by the Poly1305 tag, which is strictly
//! stronger (it also authenticates the header, defending against
//! cross-sector and cross-partition splicing attacks).
//!
//! Envelope is defined by [`crate::crypto`]:
//! ```text
//!   [header(32)] [ciphertext(N)] [tag(16)]
//!   header = magic("FOBS") | version=3 | seq u64 | payload_len u16
//!          | reserved(2) | nonce(12 = seq_le8 || "FOB1")
//! ```
//!
//! ## Plaintext payload
//!
//! Identical to the previous (v1) format, sans the outer 20-byte CRC
//! header. A repeated sequence of `(id: u32 LE, label_len: u8, label: utf8)`:
//!
//! ```text
//!   count u16 LE
//!   repeat count times:
//!     id u32 LE
//!     label_len u8
//!     label utf8[label_len]
//! ```
//!
//! ## Behavior when device key is not provisioned
//!
//! [`load`] returns an empty `Vec`. [`save`] returns
//! `Err("device not provisioned")`. Operator is responsible for running
//! `tools/provision-device-key.sh` once per unit. See
//! `crate::device_key` docs for the full provisioning story.
//!
//! ## Migration from older firmware
//!
//! There is none. Bumping from v1 to v3 is a breaking change — operators
//! must perform a factory wipe (long-press CONFIG ≥ 5 s) after upgrading
//! firmware on a previously-provisioned device. This was an explicit
//! design choice to keep this module simple and to eliminate a
//! plaintext-fallback codepath that would silently degrade security.

use embedded_storage::{ReadStorage, Storage};
use esp_storage::FlashStorage;
use heapless::{String as HString, Vec as HVec};

use crate::device_key;
use access_controller::crypto;

/// Start of the `fobs` partition. Keep in sync with `partitions.csv`.
const FOBS_BASE: u32 = 0x11000;
/// Flash erase granularity / our logical slot size.
const SECTOR: u32 = 4096;
/// Ping-pong: first two sectors of the partition.
const SLOTS: [u32; 2] = [FOBS_BASE, FOBS_BASE + SECTOR];

/// Per-store magic (preserved across format versions for log clarity).
const MAGIC: u32 = 0x46_4F_42_53; // "FOBS"

/// Maximum number of local fobs. Each entry is at most 4 + 1 + 16 = 21
/// bytes; the count prefix adds 2; envelope adds 48; total worst case
/// 2 + 128·21 + 48 = 2738 B, comfortably inside a 4 KiB sector.
pub const MAX_LOCAL_FOBS: usize = 128;

/// Maximum label length in bytes (UTF-8).
pub const MAX_LABEL_LEN: usize = 16;

/// Plaintext payload upper bound (count prefix + max entries).
const MAX_PLAINTEXT: usize = 2 + MAX_LOCAL_FOBS * (4 + 1 + MAX_LABEL_LEN);

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct LocalFob {
    pub id: u32,
    pub label: HString<MAX_LABEL_LEN>,
}

// ---------- plaintext serialization -----------------------------------

fn serialize(fobs: &[LocalFob]) -> alloc::vec::Vec<u8> {
    let mut out = alloc::vec::Vec::with_capacity(2 + fobs.len() * (4 + 1 + MAX_LABEL_LEN));
    let n = fobs.len().min(MAX_LOCAL_FOBS) as u16;
    out.extend_from_slice(&n.to_le_bytes());
    for f in fobs.iter().take(n as usize) {
        out.extend_from_slice(&f.id.to_le_bytes());
        let bytes = f.label.as_bytes();
        let len = bytes.len().min(MAX_LABEL_LEN) as u8;
        out.push(len);
        out.extend_from_slice(&bytes[..len as usize]);
    }
    out
}

fn deserialize(buf: &[u8]) -> Option<HVec<LocalFob, MAX_LOCAL_FOBS>> {
    if buf.len() < 2 {
        return None;
    }
    let count = u16::from_le_bytes([buf[0], buf[1]]) as usize;
    if count > MAX_LOCAL_FOBS {
        return None;
    }
    let mut out: HVec<LocalFob, MAX_LOCAL_FOBS> = HVec::new();
    let mut p = 2usize;
    for _ in 0..count {
        if p + 5 > buf.len() {
            return None;
        }
        let id = u32::from_le_bytes([buf[p], buf[p + 1], buf[p + 2], buf[p + 3]]);
        p += 4;
        let label_len = buf[p] as usize;
        p += 1;
        if label_len > MAX_LABEL_LEN || p + label_len > buf.len() {
            return None;
        }
        let label_str = core::str::from_utf8(&buf[p..p + label_len]).ok()?;
        p += label_len;
        let mut label: HString<MAX_LABEL_LEN> = HString::new();
        label.push_str(label_str).ok()?;
        // Push cannot fail because count <= MAX_LOCAL_FOBS.
        let _ = out.push(LocalFob { id, label });
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
    let (seq, payload_len) = crypto::parse_header(&hdr, MAGIC, crypto::DOMAIN_FOBS)?;
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
    match crypto::open(key, MAGIC, crypto::DOMAIN_FOBS, &sealed, &mut plaintext) {
        Ok(_n) => Some(Record { seq, payload: plaintext }),
        Err(e) => {
            log::warn!("fob_store: slot @0x{:X} AEAD open failed: {:?}", base, e);
            None
        }
    }
}

fn write_slot(
    flash: &mut FlashStorage,
    base: u32,
    seq: u64,
    fobs: &[LocalFob],
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
    crypto::seal(key, MAGIC, seq, crypto::DOMAIN_FOBS, &plaintext, &mut buf[..total])
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
    crypto::parse_header(&hdr, MAGIC, crypto::DOMAIN_FOBS).map(|(seq, _)| seq)
}

// ---------- public API ------------------------------------------------

/// Load the most recent valid local-fob list. Returns an empty list if
/// neither slot contains a valid encrypted record, or if the device is
/// not yet provisioned with a per-device key.
pub fn load() -> HVec<LocalFob, MAX_LOCAL_FOBS> {
    let Some(key) = device_key::fobs_key() else {
        if device_key::state() != device_key::KeyState::Uninit {
            log::warn!("fob_store: device unprovisioned, skipping load");
        }
        return HVec::new();
    };
    let mut flash = FlashStorage::new();
    let a = read_slot(&mut flash, SLOTS[0], key);
    let b = read_slot(&mut flash, SLOTS[1], key);
    let winner = match (a, b) {
        (Some(a), Some(b)) => {
            // Signed diff handles u64 wraparound (irrelevant in practice
            // — saves are operator-driven — but free correctness).
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

/// Persist new fob list. Writes to the older slot, then erases the other.
/// Returns an error if the device is not yet provisioned.
pub fn save(fobs: &[LocalFob]) -> Result<(), &'static str> {
    let Some(key) = device_key::fobs_key() else {
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
    // See H1 in the security review and `peek_slot_seq` docs above.
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
        "fob_store: saved seq={} to slot {} ({} fobs, encrypted)",
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
    log::warn!("fob_store: wiped");
    Ok(())
}
