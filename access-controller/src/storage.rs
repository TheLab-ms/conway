//! Configuration and fob cache storage with flash persistence.
//!
//! Fob storage uses A/B double-buffering for atomic updates. This ensures that
//! a power loss during write never corrupts data - the previous valid slot
//! remains intact.
//!
//! Storage layout: Two slots at different offsets, each containing:
//!   [4 bytes: magic] [4 bytes: sequence] [4 bytes: CRC32]
//!   [4 bytes: etag_len] [etag_len bytes: etag]
//!   [4 bytes: fob_count] [fob_count * 4 bytes: fobs]
//!
//! On write: Always write to the slot with the lower sequence number.
//! On read: Use the slot with the higher sequence number that has valid CRC.
//!
//! Network configuration is embedded at compile time via environment variables.
//! Use build.sh to build with credentials:
//!
//!   ./build.sh --ssid "MyWiFi" --password "secret" --host 192.168.1.68
//!
//! ## Multi-core Flash Safety
//!
//! On dual-core ESP32, flash writes require special handling because:
//! 1. The SPI flash is memory-mapped for code execution (XIP)
//! 2. Writing to flash requires detaching it from the cache
//! 3. While detached, neither core can execute code from flash
//!
//! We use a cooperative handshake protocol with Core 0:
//! 1. Core 1 requests flash write via SHARED.request_flash_write()
//! 2. Core 0 sees the request and signals safe state (no critical sections held)
//! 3. Core 1 performs the flash write
//! 4. Core 1 signals completion, Core 0 resumes normal operation
//!
//! This avoids deadlocks that would occur if we tried to stall Core 0 while
//! it holds the critical_section spinlock.

use crate::shared::{MAX_FOBS, SHARED};
use embedded_storage::{ReadStorage, Storage as EmbeddedStorage};
use heapless::{String, Vec};

const STORAGE_MAGIC: u32 = 0x434F4E57; // "CONW"

// Flash storage offsets (in data partition, after app)
// Two 32KB slots within the reserved 64KB region for A/B double-buffering
const STORAGE_SLOT_A: u32 = 0x3D_0000; // 3.8125 MB offset
const STORAGE_SLOT_B: u32 = 0x3D_8000; // 3.84375 MB offset (32KB after slot A)
const SLOT_SIZE: usize = 0x8000; // 32KB per slot

// Timeout for Core 0 to enter safe state (ms)
const FLASH_HANDSHAKE_TIMEOUT_MS: u64 = 500;

/// Conway server configuration, embedded at compile time.
#[derive(Clone)]
pub struct Config {
    pub ssid: &'static str,
    pub password: &'static str,
    pub conway_host: &'static str,
    pub conway_port: u16,
}

impl Config {
    /// Get the compile-time configuration.
    pub fn get() -> Self {
        Self {
            ssid: option_env!("CONWAY_SSID").unwrap_or("unconfigured"),
            password: option_env!("CONWAY_PASSWORD").unwrap_or(""),
            conway_host: option_env!("CONWAY_HOST").unwrap_or("192.168.1.1"),
            conway_port: match option_env!("CONWAY_PORT") {
                Some(s) => parse_port(s),
                None => 8080,
            },
        }
    }
}

/// Parse port at compile time (const fn compatible).
const fn parse_port(s: &str) -> u16 {
    let bytes = s.as_bytes();
    let mut result: u16 = 0;
    let mut i = 0;
    while i < bytes.len() {
        let digit = bytes[i];
        if digit >= b'0' && digit <= b'9' {
            result = result * 10 + (digit - b'0') as u16;
        }
        i += 1;
    }
    if result == 0 { 8080 } else { result }
}

/// Compute CRC32 for data validation (same algorithm as Python binascii.crc32).
pub fn crc32(data: &[u8]) -> u32 {
    let mut crc = 0xFFFF_FFFFu32;
    for &byte in data {
        crc ^= byte as u32;
        for _ in 0..8 {
            crc = if crc & 1 != 0 {
                (crc >> 1) ^ 0xEDB8_8320
            } else {
                crc >> 1
            };
        }
    }
    !crc
}

/// Storage interface with flash persistence using A/B double-buffering.
pub struct Storage {
    // In-memory cache (always up-to-date)
    fobs: Vec<u32, MAX_FOBS>,
    etag: String<64>,
    dirty: bool,
    // Current sequence number for A/B slot selection
    sequence: u32,
}

impl Storage {
    pub fn new() -> Self {
        let mut s = Self {
            fobs: Vec::new(),
            etag: String::new(),
            dirty: false,
            sequence: 0,
        };
        s.load_from_flash();
        s
    }

    /// Read and validate a single slot, returning (sequence, etag, fobs) if valid.
    #[cfg(feature = "esp32")]
    fn read_slot(offset: u32) -> Option<(u32, String<64>, Vec<u32, MAX_FOBS>)> {
        use alloc::vec;
        use esp_storage::FlashStorage;

        crate::heap_debug::log_heap_stats("read_slot:before");

        // Use heap allocation to avoid stack overflow (Core 1 stack is only 16KB)
        let mut buf = vec![0u8; SLOT_SIZE];

        crate::heap_debug::log_heap_stats("read_slot:after_alloc");
        let mut flash = FlashStorage::new();

        if flash.read(offset, &mut buf).is_err() {
            return None;
        }

        // Verify magic number
        let magic = u32::from_le_bytes([buf[0], buf[1], buf[2], buf[3]]);
        if magic != STORAGE_MAGIC {
            return None;
        }

        // Get sequence number
        let sequence = u32::from_le_bytes([buf[4], buf[5], buf[6], buf[7]]);

        // Get stored CRC
        let stored_crc = u32::from_le_bytes([buf[8], buf[9], buf[10], buf[11]]);

        // Get etag length and validate bounds
        let etag_len = u32::from_le_bytes([buf[12], buf[13], buf[14], buf[15]]) as usize;
        if etag_len > 64 {
            return None;
        }

        let etag_end = 16 + etag_len;
        let fob_count_offset = etag_end;

        if fob_count_offset + 4 > SLOT_SIZE {
            return None;
        }

        // Get fob count
        let fob_count = u32::from_le_bytes([
            buf[fob_count_offset],
            buf[fob_count_offset + 1],
            buf[fob_count_offset + 2],
            buf[fob_count_offset + 3],
        ]) as usize;

        let fobs_start = fob_count_offset + 4;
        let fobs_end = fobs_start + fob_count * 4;

        if fob_count > MAX_FOBS || fobs_end > SLOT_SIZE {
            return None;
        }

        // Verify CRC over the data portion (etag_len + etag + fob_count + fobs)
        let data_crc = crc32(&buf[12..fobs_end]);
        if data_crc != stored_crc {
            log::warn!(
                "storage: slot at 0x{:X} CRC mismatch (stored={:08X}, computed={:08X})",
                offset,
                stored_crc,
                data_crc
            );
            return None;
        }

        // Parse etag
        let mut etag = String::new();
        if let Ok(etag_str) = core::str::from_utf8(&buf[16..etag_end]) {
            let _ = etag.push_str(etag_str);
        }

        // Parse fobs
        let mut fobs = Vec::new();
        for i in 0..fob_count {
            let fob_offset = fobs_start + i * 4;
            let fob = u32::from_le_bytes([
                buf[fob_offset],
                buf[fob_offset + 1],
                buf[fob_offset + 2],
                buf[fob_offset + 3],
            ]);
            let _ = fobs.push(fob);
        }

        Some((sequence, etag, fobs))
    }

    /// Load cached data from flash storage using A/B double-buffering.
    /// Reads both slots and uses the one with the higher valid sequence number.
    fn load_from_flash(&mut self) {
        #[cfg(feature = "esp32")]
        {
            let slot_a = Self::read_slot(STORAGE_SLOT_A);
            let slot_b = Self::read_slot(STORAGE_SLOT_B);

            // Pick the slot with the higher sequence number
            let chosen = match (&slot_a, &slot_b) {
                (Some((seq_a, _, _)), Some((seq_b, _, _))) => {
                    if seq_b > seq_a {
                        log::info!("storage: using slot B (seq={})", seq_b);
                        slot_b
                    } else {
                        log::info!("storage: using slot A (seq={})", seq_a);
                        slot_a
                    }
                }
                (Some((seq, _, _)), None) => {
                    log::info!("storage: using slot A (seq={}), slot B invalid", seq);
                    slot_a
                }
                (None, Some((seq, _, _))) => {
                    log::info!("storage: using slot B (seq={}), slot A invalid", seq);
                    slot_b
                }
                (None, None) => {
                    log::info!("storage: no valid data in flash");
                    None
                }
            };

            if let Some((sequence, etag, fobs)) = chosen {
                self.sequence = sequence;
                self.etag = etag;
                self.fobs = fobs;
                log::info!("storage: loaded {} fobs from flash", self.fobs.len());
            }
        }

        #[cfg(not(feature = "esp32"))]
        {
            log::warn!("storage: flash not available on this platform");
        }
    }

    /// Read just the sequence number from a slot header (8 bytes: magic + seq).
    /// Much cheaper than read_slot() which allocates 32KB.
    #[cfg(feature = "esp32")]
    fn read_slot_sequence(offset: u32) -> Option<u32> {
        use esp_storage::FlashStorage;

        let mut header = [0u8; 8];
        let mut flash = FlashStorage::new();

        // Feed watchdog before flash read
        crate::feed_watchdog();

        if flash.read(offset, &mut header).is_err() {
            return None;
        }

        // Verify magic number
        let magic = u32::from_le_bytes([header[0], header[1], header[2], header[3]]);
        if magic != STORAGE_MAGIC {
            return None;
        }

        // Return sequence number
        Some(u32::from_le_bytes([header[4], header[5], header[6], header[7]]))
    }

    /// Save cached data to flash storage using A/B double-buffering.
    /// Writes to the slot with the lower sequence number (the older one).
    ///
    /// Uses cooperative handshake with Core 0 to ensure safe flash access:
    /// 1. Request flash write via SHARED
    /// 2. Wait for Core 0 to signal it's in a safe state
    /// 3. Disable watchdog and perform flash write
    /// 4. Signal completion so Core 0 can resume
    fn save_to_flash(&mut self) -> bool {
        #[cfg(feature = "esp32")]
        {
            use alloc::vec;
            use esp_storage::FlashStorage;

            // Feed watchdog at entry
            crate::feed_watchdog();
            crate::heap_debug::log_heap_stats("save_to_flash:entry");

            // Determine which slot to write to based on current sequence numbers
            // Use read_slot_sequence() instead of read_slot() to avoid allocating 32KB per slot
            let slot_a_seq = Self::read_slot_sequence(STORAGE_SLOT_A);
            let slot_b_seq = Self::read_slot_sequence(STORAGE_SLOT_B);

            // Write to the slot with the lower sequence number (older data)
            // If both are None (fresh device), start with slot A
            let (target_offset, slot_name) = match (slot_a_seq, slot_b_seq) {
                (Some(a), Some(b)) if b < a => (STORAGE_SLOT_B, "B"),
                (Some(a), Some(b)) if a < b => (STORAGE_SLOT_A, "A"),
                (Some(_), Some(_)) => (STORAGE_SLOT_A, "A"), // Equal (shouldn't happen)
                (None, Some(_)) => (STORAGE_SLOT_A, "A"),
                (Some(_), None) => (STORAGE_SLOT_B, "B"),
                (None, None) => (STORAGE_SLOT_A, "A"),
            };

            // Increment sequence number for the new write
            self.sequence = self.sequence.saturating_add(1);

            // Calculate actual buffer size needed:
            // Header: 16 bytes (magic + seq + crc + etag_len)
            // Etag: up to 64 bytes
            // Fob count: 4 bytes
            // Fobs: fob_count * 4 bytes
            let etag_bytes = self.etag.as_bytes();
            let buf_size = 16 + etag_bytes.len() + 4 + self.fobs.len() * 4;

            log::debug!(
                "storage: allocating {} bytes for slot {} write ({} fobs)",
                buf_size,
                slot_name,
                self.fobs.len()
            );
            crate::heap_debug::log_heap_stats("save_to_flash:before_alloc");

            // Allocate only what we need, not the full 32KB slot size
            let mut buf = vec![0u8; buf_size];

            crate::heap_debug::log_heap_stats("save_to_flash:after_alloc");

            log::info!("storage: filling buffer...");

            // Magic number
            buf[0..4].copy_from_slice(&STORAGE_MAGIC.to_le_bytes());

            // Sequence number
            buf[4..8].copy_from_slice(&self.sequence.to_le_bytes());

            // CRC placeholder at bytes 8..12 (filled after computing data)

            // Etag length and data
            buf[12..16].copy_from_slice(&(etag_bytes.len() as u32).to_le_bytes());
            buf[16..16 + etag_bytes.len()].copy_from_slice(etag_bytes);

            let fob_count_offset = 16 + etag_bytes.len();

            // Fob count and data
            buf[fob_count_offset..fob_count_offset + 4]
                .copy_from_slice(&(self.fobs.len() as u32).to_le_bytes());

            let fobs_start = fob_count_offset + 4;
            for (i, &fob) in self.fobs.iter().enumerate() {
                let offset = fobs_start + i * 4;
                buf[offset..offset + 4].copy_from_slice(&fob.to_le_bytes());
            }

            let fobs_end = fobs_start + self.fobs.len() * 4;

            // Compute and store CRC over data portion (etag_len + etag + fob_count + fobs)
            let data_crc = crc32(&buf[12..fobs_end]);
            buf[8..12].copy_from_slice(&data_crc.to_le_bytes());

            log::info!("storage: buffer filled, requesting flash write handshake...");

            // ================================================================
            // Cooperative handshake with Core 0
            // ================================================================
            // Request flash write - Core 0 will see this and enter safe state
            if !SHARED.request_flash_write() {
                log::error!("storage: flash write already in progress");
                self.sequence = self.sequence.saturating_sub(1);
                return false;
            }

            // Wait for Core 0 to signal it's in a safe state (no spinlocks held)
            // Core 0 checks for this at the top of its main loop, so latency is
            // bounded by its loop iteration time (~5ms)
            if !SHARED.wait_for_flash_safe(FLASH_HANDSHAKE_TIMEOUT_MS) {
                log::error!("storage: Core 0 did not enter safe state");
                self.sequence = self.sequence.saturating_sub(1);
                return false;
            }


            // Feed watchdog before flash write, then disable it during the write.
            // Flash operations on ESP32 disable the CPU cache and block both cores,
            // preventing watchdog feeding. We disable the watchdog to prevent reset
            // during the blocking write, then re-enable immediately after.
            //
            // NOTE: These critical_section calls are safe because Core 0 is now
            // in its safe state (spin-waiting on is_flash_done), not holding any
            // spinlocks and not attempting to acquire any.
            crate::feed_watchdog();
            crate::disable_watchdog();

            // Perform the flash write
            // Core 0 is safely spin-waiting, not executing any critical sections
            let mut flash = FlashStorage::new();

            log::info!("storage: Core 0 safe, writing {} bytes to 0x{:X}...", fobs_end, target_offset);
            let write_result = flash.write(target_offset, &buf[..fobs_end]);
            log::info!("storage: done writing {} bytes to 0x{:X}...", fobs_end, target_offset);

            // Re-enable watchdog
            crate::enable_watchdog();
            crate::feed_watchdog();

            // Signal to Core 0 that flash write is complete
            SHARED.signal_flash_done();

            log::info!("storage: flash.write() completed");

            match write_result {
                Ok(_) => {
                    log::info!(
                        "storage: saved {} fobs to slot {} (seq={})",
                        self.fobs.len(),
                        slot_name,
                        self.sequence
                    );
                    true
                }
                Err(e) => {
                    log::error!("storage: flash write to slot {} failed: {:?}", slot_name, e);
                    // Revert sequence on failure so next attempt uses same slot
                    self.sequence = self.sequence.saturating_sub(1);
                    false
                }
            }
        }

        #[cfg(not(feature = "esp32"))]
        {
            log::warn!("storage: flash not available on this platform");
            false
        }
    }

    pub fn load_fobs(&self) -> Vec<u32, MAX_FOBS> {
        self.fobs.clone()
    }

    pub fn save_fobs(&mut self, fobs: &[u32]) -> bool {
        self.fobs.clear();
        for &fob in fobs.iter().take(MAX_FOBS) {
            let _ = self.fobs.push(fob);
        }
        self.dirty = true;
        self.flush()
    }

    pub fn load_etag(&self) -> String<64> {
        self.etag.clone()
    }

    pub fn save_etag(&mut self, etag: &str) -> bool {
        self.etag.clear();
        let _ = self.etag.push_str(etag);
        self.dirty = true;
        // Don't flush immediately - wait for save_fobs which usually follows
        true
    }

    /// Flush any pending changes to flash.
    pub fn flush(&mut self) -> bool {
        if self.dirty {
            self.dirty = false;
            self.save_to_flash()
        } else {
            true
        }
    }
}
