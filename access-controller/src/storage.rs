//! Configuration and fob cache storage with flash persistence.
//!
//! Network configuration is embedded at compile time via environment variables.
//! Fob storage uses sequential-storage for wear-leveled key-value storage.

use heapless::{String, Vec};
use sequential_storage::map::{Key, SerializationError, Value};

pub const MAX_FOBS: usize = 512;

/// Flash storage region: 64KB starting at 0x3D0000 (last ~256KB of 4MB flash)
/// Using 16 pages of 4KB each for wear leveling.
#[cfg(feature = "esp32")]
const FLASH_RANGE: core::ops::Range<u32> = 0x3D_0000..0x3E_0000;

/// Storage keys for the map.
#[derive(Clone, Copy, PartialEq, Eq)]
#[repr(u8)]
pub enum StorageKey {
    Fobs = 0,
    ETag = 1,
}

impl Key for StorageKey {
    fn serialize_into(&self, buffer: &mut [u8]) -> Result<usize, SerializationError> {
        *buffer.first_mut().ok_or(SerializationError::BufferTooSmall)? = *self as u8;
        Ok(1)
    }

    fn deserialize_from(buffer: &[u8]) -> Result<(Self, usize), SerializationError> {
        match buffer.first() {
            Some(0) => Ok((Self::Fobs, 1)),
            Some(1) => Ok((Self::ETag, 1)),
            Some(_) => Err(SerializationError::InvalidFormat),
            None => Err(SerializationError::BufferTooSmall),
        }
    }
}

/// Wrapper for fob list that implements Value trait.
pub struct FobList(pub Vec<u32, MAX_FOBS>);

impl<'a> Value<'a> for FobList {
    fn serialize_into(&self, buffer: &mut [u8]) -> Result<usize, SerializationError> {
        let needed = 2 + self.0.len() * 4;
        if buffer.len() < needed {
            return Err(SerializationError::BufferTooSmall);
        }

        buffer[..2].copy_from_slice(&(self.0.len() as u16).to_le_bytes());
        for (i, fob) in self.0.iter().enumerate() {
            buffer[2 + i * 4..][..4].copy_from_slice(&fob.to_le_bytes());
        }
        Ok(needed)
    }

    fn deserialize_from(buffer: &'a [u8]) -> Result<Self, SerializationError> {
        let count = u16::from_le_bytes(
            buffer
                .get(..2)
                .ok_or(SerializationError::BufferTooSmall)?
                .try_into()
                .unwrap(),
        ) as usize;

        if count > MAX_FOBS {
            return Err(SerializationError::InvalidFormat);
        }

        let mut fobs = Vec::new();
        for i in 0..count {
            let offset = 2 + i * 4;
            let bytes: [u8; 4] = buffer
                .get(offset..offset + 4)
                .ok_or(SerializationError::BufferTooSmall)?
                .try_into()
                .unwrap();
            let _ = fobs.push(u32::from_le_bytes(bytes));
        }
        Ok(FobList(fobs))
    }
}

/// Wrapper for ETag that implements Value trait.
pub struct ETagValue(pub String<64>);

impl<'a> Value<'a> for ETagValue {
    fn serialize_into(&self, buffer: &mut [u8]) -> Result<usize, SerializationError> {
        let bytes = self.0.as_bytes();
        let needed = 1 + bytes.len();
        if buffer.len() < needed {
            return Err(SerializationError::BufferTooSmall);
        }

        buffer[0] = bytes.len() as u8;
        buffer[1..needed].copy_from_slice(bytes);
        Ok(needed)
    }

    fn deserialize_from(buffer: &'a [u8]) -> Result<Self, SerializationError> {
        let len = *buffer.first().ok_or(SerializationError::BufferTooSmall)? as usize;
        if len > 64 {
            return Err(SerializationError::InvalidFormat);
        }

        let mut etag = String::new();
        let _ = etag.push_str(
            core::str::from_utf8(
                buffer
                    .get(1..1 + len)
                    .ok_or(SerializationError::BufferTooSmall)?,
            )
            .map_err(|_| SerializationError::InvalidFormat)?,
        );
        Ok(ETagValue(etag))
    }
}

/// Conway server configuration, embedded at compile time.
#[derive(Clone)]
pub struct Config {
    pub ssid: &'static str,
    pub password: &'static str,
    pub conway_host: &'static str,
    pub conway_port: u16,
}

impl Config {
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

/// Flash operations helper module.
#[cfg(feature = "esp32")]
mod flash_ops {
    use super::*;
    use embassy_embedded_hal::adapter::BlockingAsync;
    use esp_storage::FlashStorage;
    use sequential_storage::{cache::NoCache, map};

    pub async fn fetch<V: for<'a> Value<'a>>(key: StorageKey, buf: &mut [u8]) -> Option<V> {
        let mut flash = BlockingAsync::new(FlashStorage::new());
        match map::fetch_item::<StorageKey, V, _>(&mut flash, FLASH_RANGE, &mut NoCache::new(), buf, &key)
            .await
        {
            Ok(value) => value,
            Err(e) => {
                log::error!("storage: flash fetch error: {:?}", e);
                None
            }
        }
    }

    pub async fn store<V: for<'a> Value<'a>>(key: StorageKey, value: &V, buf: &mut [u8]) -> bool {
        let mut flash = BlockingAsync::new(FlashStorage::new());
        match map::store_item::<StorageKey, V, _>(&mut flash, FLASH_RANGE, &mut NoCache::new(), buf, &key, value)
            .await
        {
            Ok(()) => true,
            Err(e) => {
                log::error!("storage: flash store error: {:?}", e);
                false
            }
        }
    }
}

/// Storage interface using sequential-storage for flash persistence.
pub struct Storage {
    fobs: Vec<u32, MAX_FOBS>,
    etag: String<64>,
}

impl Storage {
    /// Create new storage, loading cached data from flash.
    #[cfg(feature = "esp32")]
    pub async fn new() -> Self {
        let mut s = Self {
            fobs: Vec::new(),
            etag: String::new(),
        };

        let mut buf = [0u8; 2 + MAX_FOBS * 4 + 16];
        if let Some(FobList(fobs)) = flash_ops::fetch(StorageKey::Fobs, &mut buf).await {
            log::info!("storage: loaded {} fobs from flash", fobs.len());
            s.fobs = fobs;
        }

        let mut buf = [0u8; 72];
        if let Some(ETagValue(etag)) = flash_ops::fetch(StorageKey::ETag, &mut buf).await {
            s.etag = etag;
        }

        s
    }

    #[cfg(not(feature = "esp32"))]
    pub async fn new() -> Self {
        Self {
            fobs: Vec::new(),
            etag: String::new(),
        }
    }

    pub fn load_fobs(&self) -> Vec<u32, MAX_FOBS> {
        self.fobs.clone()
    }

    pub fn load_etag(&self) -> String<64> {
        self.etag.clone()
    }

    /// Save fobs to flash. Returns true on success.
    pub async fn save_fobs(&mut self, fobs: &[u32]) -> bool {
        self.fobs.clear();
        for &fob in fobs.iter().take(MAX_FOBS) {
            let _ = self.fobs.push(fob);
        }

        #[cfg(feature = "esp32")]
        {
            let mut buf = [0u8; 2 + MAX_FOBS * 4 + 16];
            let ok =
                flash_ops::store(StorageKey::Fobs, &FobList(self.fobs.clone()), &mut buf).await;
            if ok {
                log::info!("storage: saved {} fobs to flash", self.fobs.len());
            } else {
                log::error!("storage: failed to save fobs");
            }
            return ok;
        }

        #[cfg(not(feature = "esp32"))]
        false
    }

    /// Save etag to flash.
    pub async fn save_etag(&mut self, etag: &str) {
        self.etag.clear();
        let _ = self.etag.push_str(etag);

        #[cfg(feature = "esp32")]
        {
            let mut buf = [0u8; 72];
            if !flash_ops::store(StorageKey::ETag, &ETagValue(self.etag.clone()), &mut buf).await {
                log::error!("storage: failed to save etag");
            }
        }
    }
}
