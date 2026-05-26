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
//! ## Encryption (v3)
//!
//! Each sector stores a record encrypted under ChaCha20-Poly1305 with a
//! per-device sub-key derived from eFuse BLOCK3 (see
//! [`crate::device_key`]). Envelope format is defined in
//! [`crate::crypto`]; the Poly1305 tag has fully replaced the v1/v2
//! CRC-32 (the tag also authenticates the 32-byte header as AAD,
//! preventing cross-sector / cross-partition splicing).
//!
//! Plaintext payload format is unchanged from v2 (all little-endian):
//! ```text
//!   ssid:      u8 length, then bytes (max 32)
//!   pass:      u8 length, then bytes (max 64)
//!   host_flag: u8     (0 = standalone / no Conway, 1 = Some)
//!   host:      4 bytes (IPv4 octets, only present if host_flag == 1)
//!   port:      u16    (always present; meaningless when host_flag == 0)
//! ```
//!
//! ## Migration note
//!
//! v1/v2 plaintext records are **not** accepted. Upgrading firmware on a
//! previously-provisioned device requires a factory wipe (long-press
//! CONFIG ≥ 5 s) and re-onboarding. This was an explicit design choice
//! over a backward-compatible plaintext-fallback codepath.
//!
//! ## Unprovisioned behavior
//!
//! [`load`] returns `None` (which the boot path already handles by
//! falling back to compile-time `option_env!` defaults / AP onboarding).
//! [`save`] returns an error. Provision via
//! `tools/provision-device-key.sh` once per unit.

use alloc::string::String;
use embedded_storage::{ReadStorage, Storage};
use esp_storage::FlashStorage;

use crate::device_key;
use access_controller::crypto;

/// First byte of the `nvs` partition (see `partitions.csv`).
const NVS_BASE: u32 = 0x9000;
/// Flash erase granularity / our sector size.
const SECTOR: u32 = 4096;
/// We use the first two sectors of the `nvs` partition for ping-pong.
const SLOTS: [u32; 2] = [NVS_BASE, NVS_BASE + SECTOR];

const MAGIC: u32 = 0x434F4E57; // "CONW"

pub const MAX_SSID: usize = 32;
pub const MAX_PASSWORD: usize = 64;

/// Plaintext payload upper bound: 1+32 (ssid) + 1+64 (pw) + 1 (flag)
/// + 4 (host) + 2 (port) = 105. Round up for safety/headroom.
const MAX_PLAINTEXT: usize = 128;

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

    fn serialize(&self, out: &mut alloc::vec::Vec<u8>) -> Result<(), &'static str> {
        if self.ssid.len() > MAX_SSID {
            return Err("ssid too long");
        }
        if self.password.len() > MAX_PASSWORD {
            return Err("password too long");
        }
        out.push(self.ssid.len() as u8);
        out.extend_from_slice(self.ssid.as_bytes());
        out.push(self.password.len() as u8);
        out.extend_from_slice(self.password.as_bytes());
        match self.conway_host {
            None => out.push(0),
            Some(h) => {
                out.push(1);
                out.extend_from_slice(&h);
            }
        }
        out.extend_from_slice(&self.conway_port.to_le_bytes());
        Ok(())
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

struct Record {
    seq: u64,
    payload: alloc::vec::Vec<u8>,
}

fn read_slot(flash: &mut FlashStorage, base: u32, key: &[u8; 32]) -> Option<Record> {
    let mut hdr = [0u8; crypto::HEADER_LEN];
    flash.read(base, &mut hdr).ok()?;
    let (seq, payload_len) = crypto::parse_header(&hdr, MAGIC, crypto::DOMAIN_SETTINGS)?;
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
    match crypto::open(key, MAGIC, crypto::DOMAIN_SETTINGS, &sealed, &mut plaintext) {
        Ok(_n) => Some(Record { seq, payload: plaintext }),
        Err(e) => {
            log::warn!("settings: slot @0x{:X} AEAD open failed: {:?}", base, e);
            None
        }
    }
}

fn write_slot(
    flash: &mut FlashStorage,
    base: u32,
    seq: u64,
    payload: &[u8],
    key: &[u8; 32],
) -> Result<(), &'static str> {
    let total = crypto::HEADER_LEN + payload.len() + crypto::TAG_LEN;
    if total > SECTOR as usize {
        return Err("payload too large");
    }
    let mut buf = alloc::vec![0xFFu8; SECTOR as usize];
    crypto::seal(key, MAGIC, seq, crypto::DOMAIN_SETTINGS, payload, &mut buf[..total])
        .map_err(|_| "crypto seal failed")?;
    flash.write(base, &buf).map_err(|_| "flash write failed")
}

fn erase_slot(flash: &mut FlashStorage, base: u32) -> Result<(), &'static str> {
    let blank = alloc::vec![0xFFu8; SECTOR as usize];
    flash.write(base, &blank).map_err(|_| "flash erase failed")
}

/// Read just the 32-byte envelope header from a slot and return its
/// `seq` if the header is structurally valid (magic / version / nonce
/// consistent), regardless of whether the AEAD body decrypts.
///
/// Used by [`save`] to derive `next_seq` so an interrupted prior save's
/// half-written slot — whose body fails to open but whose header is
/// intact — still bumps the counter. Without this, the retry could
/// reuse the same `(seq, domain)` nonce with different plaintext, which
/// is fatal for ChaCha20-Poly1305. See H1 in the security review.
fn peek_slot_seq(flash: &mut FlashStorage, base: u32) -> Option<u64> {
    let mut hdr = [0u8; crypto::HEADER_LEN];
    flash.read(base, &mut hdr).ok()?;
    crypto::parse_header(&hdr, MAGIC, crypto::DOMAIN_SETTINGS).map(|(seq, _)| seq)
}

/// Load the most recent valid settings record. Returns `None` if neither
/// sector contains a valid record (== never provisioned, or wiped), or
/// if the device key is unavailable.
pub fn load() -> Option<Settings> {
    let key = device_key::settings_key()?;
    let mut flash = FlashStorage::new();
    let a = read_slot(&mut flash, SLOTS[0], key);
    let b = read_slot(&mut flash, SLOTS[1], key);
    let winner = match (a, b) {
        (Some(a), Some(b)) => {
            if (a.seq.wrapping_sub(b.seq)) as i64 >= 0 {
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
/// Returns an error if the device is not yet provisioned with a key.
pub fn save(s: &Settings) -> Result<(), &'static str> {
    let Some(key) = device_key::settings_key() else {
        return Err("device not provisioned (eFuse BLOCK3 unset)");
    };
    let mut flash = FlashStorage::new();
    let a = read_slot(&mut flash, SLOTS[0], key);
    let b = read_slot(&mut flash, SLOTS[1], key);

    // Pick write slot from successfully-opened slots only (older one).
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

    // next_seq: max over ANY parseable header (even if AEAD open fails)
    // + 1. Defends against nonce reuse after an interrupted prior save.
    // See H1 in the security review and `peek_slot_seq` docs above.
    let seq_a = peek_slot_seq(&mut flash, SLOTS[0]);
    let seq_b = peek_slot_seq(&mut flash, SLOTS[1]);
    let max_hdr_seq = match (seq_a, seq_b) {
        (Some(x), Some(y)) => Some(if (x.wrapping_sub(y)) as i64 >= 0 { x } else { y }),
        (Some(x), None) | (None, Some(x)) => Some(x),
        (None, None) => None,
    };
    let next_seq = max_hdr_seq.map(|s| s.wrapping_add(1)).unwrap_or(1u64);

    let mut payload = alloc::vec::Vec::with_capacity(128);
    s.serialize(&mut payload)?;

    write_slot(&mut flash, SLOTS[write_idx as usize], next_seq, &payload, key)?;
    let other = (1 - write_idx) as usize;
    let _ = erase_slot(&mut flash, SLOTS[other]);

    log::info!(
        "settings: saved seq={} to slot {} ({} bytes plaintext, encrypted)",
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
