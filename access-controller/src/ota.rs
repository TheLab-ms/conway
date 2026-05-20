//! Over-the-air firmware update support.
//!
//! Streams a new firmware image into the inactive OTA application slot,
//! then flips the `otadata` partition pointer so the bootloader runs the
//! new image on the next reset.
//!
//! Verification is intentionally minimal (matches the deployment policy
//! for this device):
//!   - first byte of the image must be `0xE9` (ESP image magic);
//!   - the image must fit inside the target app partition;
//!   - total bytes received must equal the declared `Content-Length`.
//!
//! There is no signing, no SHA-256 check and no bootloader auto-rollback
//! dance. The whole device is intended for LAN-only operation.
//!
//! Flash writes are buffered to a 4 KiB sector boundary because
//! `FlashStorage::write` does a read-modify-erase-write of the entire
//! sector on every call - issuing many small writes to the same sector
//! would erase data we just wrote.

use alloc::boxed::Box;

use embedded_storage::Storage;
use esp_bootloader_esp_idf::{
    ota::{Ota, Slot},
    partitions::{
        read_partition_table, AppPartitionSubType, DataPartitionSubType, PartitionType,
        PARTITION_TABLE_MAX_LEN,
    },
};
use esp_storage::FlashStorage;

/// Flash sector size (also the erase granularity).
const SECTOR: usize = 4096;

/// Errors that can be raised during an OTA operation. Each variant maps
/// to an HTTP status the caller should return to the client.
#[derive(Debug, Clone, Copy)]
pub enum OtaError {
    /// No `otadata` partition found - device was not built for OTA.
    NoOtadata,
    /// No target `ota_0` / `ota_1` app slot in the partition table.
    NoAppSlot,
    /// Failed to read or parse the partition table.
    PartTable,
    /// Failure inside `esp_bootloader_esp_idf::ota`.
    Ota,
    /// Declared `Content-Length` exceeds the app slot capacity.
    TooLarge,
    /// Bytes received did not match `Content-Length`.
    SizeMismatch,
    /// First byte of the image was not `0xE9`.
    BadMagic,
    /// Underlying flash read/write failed.
    Flash,
}

impl OtaError {
    /// HTTP status line appropriate for this error.
    pub fn http_status(&self) -> &'static str {
        match self {
            OtaError::TooLarge | OtaError::BadMagic | OtaError::SizeMismatch => {
                "400 Bad Request"
            }
            OtaError::NoOtadata | OtaError::NoAppSlot => "501 Not Implemented",
            OtaError::PartTable | OtaError::Ota | OtaError::Flash => "500 Internal Server Error",
        }
    }
}

impl core::fmt::Display for OtaError {
    fn fmt(&self, f: &mut core::fmt::Formatter<'_>) -> core::fmt::Result {
        let s = match self {
            OtaError::NoOtadata => "no otadata partition",
            OtaError::NoAppSlot => "no target app slot",
            OtaError::PartTable => "partition table error",
            OtaError::Ota => "otadata write failed",
            OtaError::TooLarge => "image larger than slot",
            OtaError::SizeMismatch => "received bytes != Content-Length",
            OtaError::BadMagic => "first byte != 0xE9",
            OtaError::Flash => "flash i/o error",
        };
        f.write_str(s)
    }
}

/// Snapshot of OTA state for the status page.
#[derive(Debug, Clone, Copy)]
pub struct OtaStatus {
    /// Which slot we are currently booted from.
    pub current: Slot,
    /// Offset of the currently active app partition.
    #[allow(dead_code)]
    pub current_offset: u32,
    /// Offset of the slot a new OTA would be written to.
    #[allow(dead_code)]
    pub next_offset: u32,
    /// Size of the slot a new OTA would be written to.
    pub next_size: u32,
}

/// Read the OTA partition state. Cheap enough to call from the status
/// page request handler.
pub fn status() -> Result<OtaStatus, OtaError> {
    let mut flash = FlashStorage::new();
    let mut pt_buf = Box::new([0u8; PARTITION_TABLE_MAX_LEN]);
    let pt = read_partition_table(&mut flash, pt_buf.as_mut_slice())
        .map_err(|_| OtaError::PartTable)?;

    let otadata = pt
        .find_partition(PartitionType::Data(DataPartitionSubType::Ota))
        .map_err(|_| OtaError::PartTable)?
        .ok_or(OtaError::NoOtadata)?;

    let current = {
        let mut region = otadata.as_embedded_storage(&mut flash);
        let mut ota = Ota::new(&mut region).map_err(|_| OtaError::Ota)?;
        effective_current(ota.current_slot().map_err(|_| OtaError::Ota)?)
    };

    let next = current.next();
    let (cur_app, next_app) = (app_subtype_for(current), app_subtype_for(next));

    let cur_part = pt
        .find_partition(PartitionType::App(cur_app))
        .map_err(|_| OtaError::PartTable)?
        .ok_or(OtaError::NoAppSlot)?;
    let next_part = pt
        .find_partition(PartitionType::App(next_app))
        .map_err(|_| OtaError::PartTable)?
        .ok_or(OtaError::NoAppSlot)?;

    Ok(OtaStatus {
        current,
        current_offset: cur_part.offset(),
        next_offset: next_part.offset(),
        next_size: next_part.len(),
    })
}

/// Flip the otadata pointer back to the previous slot and return the
/// slot that is now selected. Caller is responsible for triggering a
/// reset (we do not do it here so the HTTP response can flush first).
pub fn rollback() -> Result<Slot, OtaError> {
    let mut flash = FlashStorage::new();
    let mut pt_buf = Box::new([0u8; PARTITION_TABLE_MAX_LEN]);
    let pt = read_partition_table(&mut flash, pt_buf.as_mut_slice())
        .map_err(|_| OtaError::PartTable)?;

    let otadata = pt
        .find_partition(PartitionType::Data(DataPartitionSubType::Ota))
        .map_err(|_| OtaError::PartTable)?
        .ok_or(OtaError::NoOtadata)?;

    let mut region = otadata.as_embedded_storage(&mut flash);
    let mut ota = Ota::new(&mut region).map_err(|_| OtaError::Ota)?;
    let cur = effective_current(ota.current_slot().map_err(|_| OtaError::Ota)?);
    let other = cur.next();
    ota.set_current_slot(other).map_err(|_| OtaError::Ota)?;
    Ok(other)
}

/// Streaming writer for an OTA image.
///
/// Usage:
///
/// ```ignore
/// let mut w = OtaWriter::begin(content_length)?;
/// while let Some(chunk) = next_chunk() { w.write(chunk)?; }
/// w.finish()?;
/// ```
pub struct OtaWriter {
    flash: FlashStorage,
    /// Absolute flash offset of the target app partition.
    base: u32,
    /// Size of the target app partition in bytes.
    #[allow(dead_code)]
    size: u32,
    /// Declared total size of the incoming image.
    expected: u32,
    /// Bytes already written to flash.
    flushed: u32,
    /// Bytes currently buffered (not yet written to flash).
    buf_len: usize,
    /// Pending sector buffer; padded with 0xFF for the final partial sector.
    buf: Box<[u8; SECTOR]>,
    /// Slot we will activate in `finish()`.
    next_slot: Slot,
}

impl OtaWriter {
    /// Locate the inactive slot and prepare to receive an image of
    /// exactly `content_length` bytes.
    pub fn begin(content_length: u32) -> Result<Self, OtaError> {
        if content_length == 0 {
            return Err(OtaError::SizeMismatch);
        }

        let mut flash = FlashStorage::new();
        let mut pt_buf = Box::new([0u8; PARTITION_TABLE_MAX_LEN]);
        let pt = read_partition_table(&mut flash, pt_buf.as_mut_slice())
            .map_err(|_| OtaError::PartTable)?;

        let otadata = pt
            .find_partition(PartitionType::Data(DataPartitionSubType::Ota))
            .map_err(|_| OtaError::PartTable)?
            .ok_or(OtaError::NoOtadata)?;

        let current = {
            let mut region = otadata.as_embedded_storage(&mut flash);
            let mut ota = Ota::new(&mut region).map_err(|_| OtaError::Ota)?;
            effective_current(ota.current_slot().map_err(|_| OtaError::Ota)?)
        };
        let next = current.next();

        let app = pt
            .find_partition(PartitionType::App(app_subtype_for(next)))
            .map_err(|_| OtaError::PartTable)?
            .ok_or(OtaError::NoAppSlot)?;

        let base = app.offset();
        let size = app.len();
        if content_length > size {
            return Err(OtaError::TooLarge);
        }

        log::info!(
            "ota: begin -> slot={:?} base=0x{:X} size={} image={}",
            next,
            base,
            size,
            content_length
        );

        Ok(Self {
            flash,
            base,
            size,
            expected: content_length,
            flushed: 0,
            buf_len: 0,
            buf: Box::new([0xFFu8; SECTOR]),
            next_slot: next,
        })
    }

    /// Slot this writer will activate when `finish()` succeeds.
    pub fn target_slot(&self) -> Slot {
        self.next_slot
    }

    /// Total bytes accepted so far (buffered + flushed).
    pub fn bytes_accepted(&self) -> u32 {
        self.flushed + self.buf_len as u32
    }

    /// Declared total size.
    pub fn expected(&self) -> u32 {
        self.expected
    }

    /// Feed the next chunk of the image. Chunks may be of any size;
    /// internal buffering aligns writes to 4 KiB sector boundaries.
    pub fn write(&mut self, mut chunk: &[u8]) -> Result<(), OtaError> {
        if chunk.is_empty() {
            return Ok(());
        }
        // Magic check on the very first byte of the very first chunk.
        if self.flushed == 0 && self.buf_len == 0 && chunk[0] != 0xE9 {
            return Err(OtaError::BadMagic);
        }
        if self.bytes_accepted() as u64 + chunk.len() as u64 > self.expected as u64 {
            return Err(OtaError::SizeMismatch);
        }

        while !chunk.is_empty() {
            let space = SECTOR - self.buf_len;
            let n = chunk.len().min(space);
            self.buf[self.buf_len..self.buf_len + n].copy_from_slice(&chunk[..n]);
            self.buf_len += n;
            chunk = &chunk[n..];

            if self.buf_len == SECTOR {
                self.flash
                    .write(self.base + self.flushed, self.buf.as_slice())
                    .map_err(|_| OtaError::Flash)?;
                self.flushed += SECTOR as u32;
                self.buf_len = 0;
            }
        }
        Ok(())
    }

    /// Flush any partial trailing sector (padded with 0xFF) and flip
    /// the otadata pointer to the freshly written slot.
    pub fn finish(mut self) -> Result<Slot, OtaError> {
        if self.bytes_accepted() != self.expected {
            return Err(OtaError::SizeMismatch);
        }

        if self.buf_len > 0 {
            for b in &mut self.buf[self.buf_len..] {
                *b = 0xFF;
            }
            self.flash
                .write(self.base + self.flushed, self.buf.as_slice())
                .map_err(|_| OtaError::Flash)?;
            self.flushed += self.buf_len as u32;
            self.buf_len = 0;
        }

        // Re-read the partition table to flip otadata - cheaper than
        // dragging FlashRegion through the writer's lifetime.
        let mut pt_buf = Box::new([0u8; PARTITION_TABLE_MAX_LEN]);
        let pt = read_partition_table(&mut self.flash, pt_buf.as_mut_slice())
            .map_err(|_| OtaError::PartTable)?;
        let otadata = pt
            .find_partition(PartitionType::Data(DataPartitionSubType::Ota))
            .map_err(|_| OtaError::PartTable)?
            .ok_or(OtaError::NoOtadata)?;
        let mut region = otadata.as_embedded_storage(&mut self.flash);
        let mut ota = Ota::new(&mut region).map_err(|_| OtaError::Ota)?;
        ota.set_current_slot(self.next_slot)
            .map_err(|_| OtaError::Ota)?;

        log::info!(
            "ota: finish -> activated slot={:?} bytes={}",
            self.next_slot,
            self.flushed
        );
        Ok(self.next_slot)
    }
}

/// Short human-readable label for a slot.
pub fn slot_label(slot: Slot) -> &'static str {
    match slot {
        Slot::None => "none",
        Slot::Slot0 => "ota_0",
        Slot::Slot1 => "ota_1",
    }
}

/// Normalize `Slot::None` (returned when `otadata` is erased, e.g. on a
/// fresh USB flash) into the slot the IDF bootloader actually boots from
/// in that state.
///
/// This firmware is built without a factory partition, so when otadata
/// is invalid the bootloader falls back to `ota_0`. Crucially,
/// `Slot::None.next()` in `esp-bootloader-esp-idf` returns `Slot0`,
/// which would cause `OtaWriter::begin` to pick `ota_0` as the "next"
/// slot - i.e. write the new image directly over the currently
/// executing app and then point otadata at the half-written result.
/// Normalizing here ensures the first OTA after a USB flash lands in
/// `ota_1` instead of bricking the device.
fn effective_current(slot: Slot) -> Slot {
    match slot {
        Slot::None => Slot::Slot0,
        s => s,
    }
}

fn app_subtype_for(slot: Slot) -> AppPartitionSubType {
    match slot {
        Slot::Slot0 => AppPartitionSubType::Ota0,
        Slot::Slot1 => AppPartitionSubType::Ota1,
        // Callers must normalize `Slot::None` via `effective_current`
        // before reaching here; if they don't, fall back to ota_0 so we
        // at least describe a real partition rather than panicking.
        Slot::None => AppPartitionSubType::Ota0,
    }
}
