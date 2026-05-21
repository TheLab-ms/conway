//! Persistent local fob list, stored in the dedicated `fobs` partition.
//!
//! Same ping-pong + CRC design as `settings.rs`, but for a list of
//! `(fob_id, label)` records that the operator can edit via the HTTP UI.
//!
//! Local fobs are checked **before** the Conway-synced cache and always
//! grant access regardless of Conway state. They survive reboots; the
//! Conway cache does not.
//!
//! Partition (see `partitions.csv`):
//!   `fobs, data, nvs, 0x11000, 0xF000`  (60 KiB)
//!
//! We use only the first two 4 KiB sectors of that partition for the
//! ping-pong; the remaining 52 KiB is reserved for future growth (larger
//! lists, audit-log persistence, etc.).
//!
//! Record format inside a sector (all little-endian):
//! ```text
//!   0..4    magic = 0x46_4F_42_53  ("FOBS")
//!   4..8    version = 1
//!   8..12   seq (monotonic counter, wraps at u32::MAX)
//!   12..14  count (number of entries)
//!   14..16  reserved (0)
//!   16..20  crc32 (IEEE) over [0..16] + entries (with crc field as 0)
//!   20..    entries, each:
//!             id:        u32  (4 bytes)
//!             label_len: u8   (1 byte, 0..=MAX_LABEL_LEN)
//!             label:     UTF-8 bytes (label_len)
//! ```

use embedded_storage::{ReadStorage, Storage};
use esp_storage::FlashStorage;
use heapless::{String as HString, Vec as HVec};

/// Start of the `fobs` partition. Keep in sync with `partitions.csv`.
const FOBS_BASE: u32 = 0x11000;
/// Flash erase granularity / our logical slot size.
const SECTOR: u32 = 4096;
/// Ping-pong: first two sectors of the partition.
const SLOTS: [u32; 2] = [FOBS_BASE, FOBS_BASE + SECTOR];

const MAGIC: u32 = 0x46_4F_42_53; // "FOBS"
const VERSION: u32 = 1;
const HEADER_LEN: usize = 20;

/// Maximum number of local fobs. Each entry is at most 4 + 1 + 16 = 21
/// bytes, so 128 entries + 20 byte header = 2708 bytes, comfortably
/// inside one 4 KiB sector.
pub const MAX_LOCAL_FOBS: usize = 128;

/// Maximum label length in bytes (UTF-8).
pub const MAX_LABEL_LEN: usize = 16;

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct LocalFob {
    pub id: u32,
    pub label: HString<MAX_LABEL_LEN>,
}

fn crc32(bytes: &[u8]) -> u32 {
    // Same plain IEEE CRC-32 as settings.rs (table-less).
    let mut crc: u32 = 0xFFFF_FFFF;
    for &b in bytes {
        crc ^= b as u32;
        for _ in 0..8 {
            let mask = (crc & 1).wrapping_neg();
            crc = (crc >> 1) ^ (0xEDB8_8320 & mask);
        }
    }
    !crc
}

struct Record {
    seq: u32,
    count: usize,
    payload: alloc::vec::Vec<u8>,
}

fn serialize(fobs: &[LocalFob]) -> alloc::vec::Vec<u8> {
    let mut out = alloc::vec::Vec::with_capacity(fobs.len() * (4 + 1 + MAX_LABEL_LEN));
    for f in fobs {
        out.extend_from_slice(&f.id.to_le_bytes());
        let bytes = f.label.as_bytes();
        let len = bytes.len().min(MAX_LABEL_LEN) as u8;
        out.push(len);
        out.extend_from_slice(&bytes[..len as usize]);
    }
    out
}

fn deserialize(buf: &[u8], count: usize) -> Option<HVec<LocalFob, MAX_LOCAL_FOBS>> {
    let mut out: HVec<LocalFob, MAX_LOCAL_FOBS> = HVec::new();
    let mut p = 0usize;
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
        if out.push(LocalFob { id, label }).is_err() {
            log::warn!("fob_store: truncated load at MAX_LOCAL_FOBS={}", MAX_LOCAL_FOBS);
            break;
        }
    }
    Some(out)
}

fn read_slot(flash: &mut FlashStorage, base: u32) -> Option<Record> {
    let mut hdr = [0u8; HEADER_LEN];
    flash.read(base, &mut hdr).ok()?;
    let magic = u32::from_le_bytes([hdr[0], hdr[1], hdr[2], hdr[3]]);
    if magic != MAGIC {
        return None;
    }
    let version = u32::from_le_bytes([hdr[4], hdr[5], hdr[6], hdr[7]]);
    if version != VERSION {
        return None;
    }
    let seq = u32::from_le_bytes([hdr[8], hdr[9], hdr[10], hdr[11]]);
    let count = u16::from_le_bytes([hdr[12], hdr[13]]) as usize;
    let stored_crc = u32::from_le_bytes([hdr[16], hdr[17], hdr[18], hdr[19]]);

    // Payload upper bound for capacity check.
    let max_payload = SECTOR as usize - HEADER_LEN;
    let est_payload = count.saturating_mul(4 + 1 + MAX_LABEL_LEN);
    if est_payload > max_payload {
        return None;
    }

    // We can't know exact payload length without parsing, so read up to
    // the per-record upper bound. Trim later during deserialization.
    let read_len = est_payload.min(max_payload);
    let mut payload = alloc::vec![0u8; read_len];
    if read_len > 0 {
        flash.read(base + HEADER_LEN as u32, &mut payload).ok()?;
    }

    // CRC is computed over header (with crc zeroed) + the actual variable
    // payload. We don't know the *actual* length without parsing, so we
    // compute progressively: parse, then verify CRC against the parsed
    // prefix length.
    let parsed = deserialize(&payload, count)?;
    let used = serialized_len(&parsed);
    if used > payload.len() {
        return None;
    }
    let mut hdr_for_crc = hdr;
    hdr_for_crc[16..20].copy_from_slice(&[0, 0, 0, 0]);
    let mut crc_input = alloc::vec::Vec::with_capacity(HEADER_LEN + used);
    crc_input.extend_from_slice(&hdr_for_crc);
    crc_input.extend_from_slice(&payload[..used]);
    if crc32(&crc_input) != stored_crc {
        log::warn!("fob_store: slot @0x{:X} CRC mismatch", base);
        return None;
    }

    Some(Record {
        seq,
        count,
        payload: payload[..used].to_vec(),
    })
}

fn serialized_len(fobs: &[LocalFob]) -> usize {
    fobs.iter().map(|f| 4 + 1 + f.label.len()) .sum()
}

fn write_slot(
    flash: &mut FlashStorage,
    base: u32,
    seq: u32,
    fobs: &[LocalFob],
) -> Result<(), &'static str> {
    let payload = serialize(fobs);
    if HEADER_LEN + payload.len() > SECTOR as usize {
        return Err("payload too large");
    }
    if fobs.len() > u16::MAX as usize {
        return Err("too many fobs");
    }

    let mut buf = alloc::vec![0xFFu8; SECTOR as usize];
    buf[0..4].copy_from_slice(&MAGIC.to_le_bytes());
    buf[4..8].copy_from_slice(&VERSION.to_le_bytes());
    buf[8..12].copy_from_slice(&seq.to_le_bytes());
    buf[12..14].copy_from_slice(&(fobs.len() as u16).to_le_bytes());
    buf[14..16].copy_from_slice(&[0, 0]);
    buf[16..20].copy_from_slice(&[0, 0, 0, 0]); // crc placeholder
    buf[HEADER_LEN..HEADER_LEN + payload.len()].copy_from_slice(&payload);

    let crc = crc32(&buf[..HEADER_LEN + payload.len()]);
    buf[16..20].copy_from_slice(&crc.to_le_bytes());

    flash
        .write(base, &buf)
        .map_err(|_| "flash write failed")?;
    Ok(())
}

fn erase_slot(flash: &mut FlashStorage, base: u32) -> Result<(), &'static str> {
    let blank = alloc::vec![0xFFu8; SECTOR as usize];
    flash.write(base, &blank).map_err(|_| "flash erase failed")
}

/// Load the most recent valid local-fob list. Returns an empty list if
/// neither slot contains a valid record (== never written, or wiped).
pub fn load() -> HVec<LocalFob, MAX_LOCAL_FOBS> {
    let mut flash = FlashStorage::new();
    let a = read_slot(&mut flash, SLOTS[0]);
    let b = read_slot(&mut flash, SLOTS[1]);
    let winner = match (a, b) {
        (Some(a), Some(b)) => {
            if a.seq.wrapping_sub(b.seq) as i32 >= 0 {
                a
            } else {
                b
            }
        }
        (Some(a), None) => a,
        (None, Some(b)) => b,
        (None, None) => return HVec::new(),
    };
    deserialize(&winner.payload, winner.count).unwrap_or_default()
}

/// Persist new fob list. Writes to the older slot, then erases the other.
pub fn save(fobs: &[LocalFob]) -> Result<(), &'static str> {
    let mut flash = FlashStorage::new();
    let a = read_slot(&mut flash, SLOTS[0]);
    let b = read_slot(&mut flash, SLOTS[1]);

    let (write_idx, next_seq) = match (&a, &b) {
        (None, _) => (0, b.as_ref().map(|r| r.seq.wrapping_add(1)).unwrap_or(1)),
        (Some(_), None) => (1, a.as_ref().unwrap().seq.wrapping_add(1)),
        (Some(ra), Some(rb)) => {
            if ra.seq.wrapping_sub(rb.seq) as i32 >= 0 {
                (1, ra.seq.wrapping_add(1))
            } else {
                (0, rb.seq.wrapping_add(1))
            }
        }
    };

    write_slot(&mut flash, SLOTS[write_idx], next_seq, fobs)?;
    let other = 1 - write_idx;
    let _ = erase_slot(&mut flash, SLOTS[other]);

    log::info!(
        "fob_store: saved seq={} to slot {} ({} fobs)",
        next_seq,
        write_idx,
        fobs.len()
    );
    Ok(())
}

/// Wipe both slots.
pub fn erase() -> Result<(), &'static str> {
    let mut flash = FlashStorage::new();
    erase_slot(&mut flash, SLOTS[0])?;
    erase_slot(&mut flash, SLOTS[1])?;
    log::warn!("fob_store: wiped");
    Ok(())
}
