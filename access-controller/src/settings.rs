//! Persistent device settings stored in the `nvs` partition.
//!
//! Layout: the 24 KiB `nvs` partition (`partitions.csv`) is treated as two
//! independent 4 KiB sectors at offsets `NVS_BASE + 0` and `NVS_BASE + 4096`.
//! Each write goes to the sector whose last record had the lower sequence
//! number (or the one that is blank/invalid), then the previous sector is
//! erased. On boot, both sectors are read and the valid record with the
//! highest sequence number wins. This gives single-shot wear leveling and
//! crash-safe atomic updates without pulling in a full K/V store.
//!
//! Record format inside a sector (all little-endian):
//! ```text
//!   0..4    magic = 0x434F4E57  ("CONW")
//!   4..8    version = 1
//!   8..12   seq (monotonic counter, wraps at u32::MAX)
//!   12..14  payload length (bytes)
//!   14..16  reserved (0)
//!   16..20  crc32 (IEEE) over [0..16] + payload (with crc field as 0)
//!   20..    payload (Settings serialization)
//! ```
//!
//! Settings serialization (all little-endian):
//! ```text
//!   ssid:      u8 length, then bytes (max 32)
//!   pass:      u8 length, then bytes (max 64)
//!   host_flag: u8     (0 = standalone / no Conway, 1 = Some)
//!   host:      4 bytes (IPv4 octets, only present if host_flag == 1)
//!   port:      u16    (always present; meaningless when host_flag == 0)
//! ```
//!
//! Version note: v1 stored `host` unconditionally without `host_flag`. v2
//! makes Conway host optional. v1 records are ignored on load (a
//! reflash-required migration anyway, since this change ships with the
//! `fobs` partition table addition).
//!
//! Empty / unprovisioned state: a zero-length SSID. The boot path treats
//! this (and "no valid record found") as "not provisioned" and brings up
//! the device in AP onboarding mode.

use alloc::string::String;
use embedded_storage::{ReadStorage, Storage};
use esp_storage::FlashStorage;

/// First byte of the `nvs` partition (see `partitions.csv`).
const NVS_BASE: u32 = 0x9000;
/// Flash erase granularity / our sector size.
const SECTOR: u32 = 4096;
/// We use the first two sectors of the `nvs` partition for ping-pong.
const SLOTS: [u32; 2] = [NVS_BASE, NVS_BASE + SECTOR];

const MAGIC: u32 = 0x434F4E57; // "CONW"
const VERSION: u32 = 2;
const HEADER_LEN: usize = 20;

pub const MAX_SSID: usize = 32;
pub const MAX_PASSWORD: usize = 64;

#[derive(Clone, Debug)]
pub struct Settings {
    pub ssid: String,
    pub password: String,
    /// IPv4 address of the Conway server. `None` => standalone mode: the
    /// device boots, joins WiFi, serves the UI, and authorizes against the
    /// local fob list only. `sync_task` is not spawned in that case.
    pub conway_host: Option<[u8; 4]>,
    pub conway_port: u16,
}

impl Settings {
    /// Settings that boot the device into AP onboarding mode (empty SSID).
    pub fn defaults_from_env() -> Self {
        let host = option_env!("CONWAY_HOST").and_then(parse_ipv4);
        Self {
            ssid: option_env!("CONWAY_SSID").unwrap_or("").into(),
            password: option_env!("CONWAY_PASSWORD").unwrap_or("").into(),
            conway_host: host,
            conway_port: 8080,
        }
    }

    pub fn is_provisioned(&self) -> bool {
        !self.ssid.is_empty()
    }

    /// `true` when a Conway server is configured and `sync_task` should run.
    pub fn conway_enabled(&self) -> bool {
        self.conway_host.is_some()
    }

    pub fn conway_host_str(&self) -> alloc::string::String {
        use core::fmt::Write;
        let mut s = alloc::string::String::new();
        match self.conway_host {
            None => {
                s.push_str("(standalone)");
            }
            Some(h) => {
                let _ = write!(s, "{}.{}.{}.{}", h[0], h[1], h[2], h[3]);
            }
        }
        s
    }

    fn serialize(&self, out: &mut alloc::vec::Vec<u8>) {
        out.push(self.ssid.len().min(MAX_SSID) as u8);
        out.extend_from_slice(&self.ssid.as_bytes()[..self.ssid.len().min(MAX_SSID)]);
        out.push(self.password.len().min(MAX_PASSWORD) as u8);
        out.extend_from_slice(&self.password.as_bytes()[..self.password.len().min(MAX_PASSWORD)]);
        match self.conway_host {
            None => out.push(0),
            Some(h) => {
                out.push(1);
                out.extend_from_slice(&h);
            }
        }
        out.extend_from_slice(&self.conway_port.to_le_bytes());
    }

    fn deserialize(buf: &[u8]) -> Option<Self> {
        let mut p = 0usize;
        let ssid_len = *buf.get(p)? as usize;
        p += 1;
        if ssid_len > MAX_SSID || p + ssid_len > buf.len() {
            return None;
        }
        let ssid = core::str::from_utf8(&buf[p..p + ssid_len]).ok()?.into();
        p += ssid_len;

        let pw_len = *buf.get(p)? as usize;
        p += 1;
        if pw_len > MAX_PASSWORD || p + pw_len > buf.len() {
            return None;
        }
        let password = core::str::from_utf8(&buf[p..p + pw_len]).ok()?.into();
        p += pw_len;

        let host_flag = *buf.get(p)?;
        p += 1;
        let conway_host = match host_flag {
            0 => None,
            1 => {
                if p + 4 > buf.len() {
                    return None;
                }
                let h = [buf[p], buf[p + 1], buf[p + 2], buf[p + 3]];
                p += 4;
                Some(h)
            }
            _ => return None,
        };

        if p + 2 > buf.len() {
            return None;
        }
        let port = u16::from_le_bytes([buf[p], buf[p + 1]]);

        Some(Self {
            ssid,
            password,
            conway_host,
            conway_port: port,
        })
    }
}

fn crc32(bytes: &[u8]) -> u32 {
    // Plain IEEE 802.3 polynomial CRC-32, table-less so we don't pay any
    // .rodata cost. ~1us per record on the ESP32 - irrelevant.
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

/// One stored record (header + payload), in-memory.
struct Record {
    seq: u32,
    payload: alloc::vec::Vec<u8>,
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
    let len = u16::from_le_bytes([hdr[12], hdr[13]]) as usize;
    if len > (SECTOR as usize - HEADER_LEN) {
        return None;
    }
    let stored_crc = u32::from_le_bytes([hdr[16], hdr[17], hdr[18], hdr[19]]);

    let mut payload = alloc::vec![0u8; len];
    if len > 0 {
        flash.read(base + HEADER_LEN as u32, &mut payload).ok()?;
    }

    // Recompute CRC over header (with crc field zeroed) + payload.
    let mut hdr_for_crc = hdr;
    hdr_for_crc[16..20].copy_from_slice(&[0, 0, 0, 0]);
    let mut crc_input = alloc::vec::Vec::with_capacity(HEADER_LEN + len);
    crc_input.extend_from_slice(&hdr_for_crc);
    crc_input.extend_from_slice(&payload);
    let calc = crc32(&crc_input);
    if calc != stored_crc {
        log::warn!("settings: slot @0x{:X} CRC mismatch", base);
        return None;
    }

    Some(Record { seq, payload })
}

fn write_slot(
    flash: &mut FlashStorage,
    base: u32,
    seq: u32,
    payload: &[u8],
) -> Result<(), &'static str> {
    if HEADER_LEN + payload.len() > SECTOR as usize {
        return Err("payload too large");
    }

    // Build the full sector buffer so the underlying FlashStorage write is
    // a single sector-aligned operation (it would otherwise read-modify-
    // erase-write internally, which is fine but wasteful).
    let mut buf = alloc::vec![0xFFu8; SECTOR as usize];
    buf[0..4].copy_from_slice(&MAGIC.to_le_bytes());
    buf[4..8].copy_from_slice(&VERSION.to_le_bytes());
    buf[8..12].copy_from_slice(&seq.to_le_bytes());
    buf[12..14].copy_from_slice(&(payload.len() as u16).to_le_bytes());
    buf[14..16].copy_from_slice(&[0, 0]);
    buf[16..20].copy_from_slice(&[0, 0, 0, 0]); // crc placeholder
    buf[HEADER_LEN..HEADER_LEN + payload.len()].copy_from_slice(payload);

    let crc = crc32(&buf[..HEADER_LEN + payload.len()]);
    buf[16..20].copy_from_slice(&crc.to_le_bytes());

    flash
        .write(base, &buf)
        .map_err(|_| "flash write failed")?;
    Ok(())
}

fn erase_slot(flash: &mut FlashStorage, base: u32) -> Result<(), &'static str> {
    // Writing all-0xFF performs an erase + write under the hood for
    // FlashStorage, but to be explicit we just write a blank sector so
    // the magic check fails on next read.
    let blank = alloc::vec![0xFFu8; SECTOR as usize];
    flash.write(base, &blank).map_err(|_| "flash erase failed")
}

/// Load the most recent valid settings record. Returns `None` if neither
/// sector contains a valid record (== never provisioned, or wiped).
pub fn load() -> Option<Settings> {
    let mut flash = FlashStorage::new();
    let a = read_slot(&mut flash, SLOTS[0]);
    let b = read_slot(&mut flash, SLOTS[1]);
    let winner = match (a, b) {
        (Some(a), Some(b)) => {
            // Choose by signed seq diff so wraparound is handled.
            if a.seq.wrapping_sub(b.seq) as i32 >= 0 {
                a
            } else {
                b
            }
        }
        (Some(a), None) => a,
        (None, Some(b)) => b,
        (None, None) => return None,
    };
    Settings::deserialize(&winner.payload)
}

/// Persist new settings. Writes to the older slot, then erases the other.
pub fn save(s: &Settings) -> Result<(), &'static str> {
    let mut flash = FlashStorage::new();
    let a = read_slot(&mut flash, SLOTS[0]);
    let b = read_slot(&mut flash, SLOTS[1]);

    let (write_idx, next_seq) = match (&a, &b) {
        (None, _) => (0, b.as_ref().map(|r| r.seq.wrapping_add(1)).unwrap_or(1)),
        (Some(_), None) => (1, a.as_ref().unwrap().seq.wrapping_add(1)),
        (Some(ra), Some(rb)) => {
            // Write into the older one.
            if ra.seq.wrapping_sub(rb.seq) as i32 >= 0 {
                (1, ra.seq.wrapping_add(1))
            } else {
                (0, rb.seq.wrapping_add(1))
            }
        }
    };

    let mut payload = alloc::vec::Vec::with_capacity(128);
    s.serialize(&mut payload);

    write_slot(&mut flash, SLOTS[write_idx], next_seq, &payload)?;

    // Erase the other slot so a future power loss can't pick it up.
    let other = 1 - write_idx;
    let _ = erase_slot(&mut flash, SLOTS[other]);

    log::info!(
        "settings: saved seq={} to slot {} ({} bytes)",
        next_seq,
        write_idx,
        payload.len()
    );
    Ok(())
}

/// Wipe both sectors. Next `load()` will return `None` and the device
/// will boot into onboarding (AP) mode.
pub fn erase() -> Result<(), &'static str> {
    let mut flash = FlashStorage::new();
    erase_slot(&mut flash, SLOTS[0])?;
    erase_slot(&mut flash, SLOTS[1])?;
    log::warn!("settings: NVS wiped (factory reset)");
    Ok(())
}

/// Parse an IPv4 dotted-quad string into 4 octets.
pub fn parse_ipv4(s: &str) -> Option<[u8; 4]> {
    let mut octets = [0u8; 4];
    let mut idx = 0;
    for part in s.split('.') {
        if idx >= 4 {
            return None;
        }
        octets[idx] = part.parse().ok()?;
        idx += 1;
    }
    if idx == 4 {
        Some(octets)
    } else {
        None
    }
}
