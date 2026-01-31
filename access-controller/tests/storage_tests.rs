//! Unit tests for storage serialization logic.
//!
//! Tests the serialization/deserialization of FobList, ETagValue, and StorageKey,
//! as well as the parse_port function.

/// Storage keys for the map (mirrors storage.rs).
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
#[repr(u8)]
enum StorageKey {
    Fobs = 0,
    ETag = 1,
}

/// Serialization error type.
#[derive(Debug, PartialEq)]
enum SerializationError {
    BufferTooSmall,
    InvalidFormat,
}

impl StorageKey {
    fn serialize_into(&self, buffer: &mut [u8]) -> Result<usize, SerializationError> {
        *buffer
            .first_mut()
            .ok_or(SerializationError::BufferTooSmall)? = *self as u8;
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

const MAX_FOBS: usize = 512;

/// Wrapper for fob list serialization (mirrors storage.rs).
struct FobList(Vec<u32>);

impl FobList {
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

    fn deserialize_from(buffer: &[u8]) -> Result<Self, SerializationError> {
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
            fobs.push(u32::from_le_bytes(bytes));
        }
        Ok(FobList(fobs))
    }
}

/// Wrapper for ETag serialization (mirrors storage.rs).
struct ETagValue(String);

impl ETagValue {
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

    fn deserialize_from(buffer: &[u8]) -> Result<Self, SerializationError> {
        let len = *buffer.first().ok_or(SerializationError::BufferTooSmall)? as usize;
        if len > 64 {
            return Err(SerializationError::InvalidFormat);
        }

        let etag_str = std::str::from_utf8(
            buffer
                .get(1..1 + len)
                .ok_or(SerializationError::BufferTooSmall)?,
        )
        .map_err(|_| SerializationError::InvalidFormat)?;

        Ok(ETagValue(etag_str.to_string()))
    }
}

/// Parse port from string (const fn in original, regular fn here for testing).
/// Uses wrapping arithmetic to match const fn behavior.
fn parse_port(s: &str) -> u16 {
    let bytes = s.as_bytes();
    let mut result: u16 = 0;
    let mut i = 0;
    while i < bytes.len() {
        let digit = bytes[i];
        if digit >= b'0' && digit <= b'9' {
            result = result.wrapping_mul(10).wrapping_add((digit - b'0') as u16);
        }
        i += 1;
    }
    if result == 0 {
        8080
    } else {
        result
    }
}

// ============================================================================
// Tests for StorageKey
// ============================================================================

#[test]
fn test_storage_key_serialize_fobs() {
    let mut buffer = [0u8; 10];
    let result = StorageKey::Fobs.serialize_into(&mut buffer);
    assert_eq!(result, Ok(1));
    assert_eq!(buffer[0], 0);
}

#[test]
fn test_storage_key_serialize_etag() {
    let mut buffer = [0u8; 10];
    let result = StorageKey::ETag.serialize_into(&mut buffer);
    assert_eq!(result, Ok(1));
    assert_eq!(buffer[0], 1);
}

#[test]
fn test_storage_key_serialize_empty_buffer() {
    let mut buffer = [0u8; 0];
    let result = StorageKey::Fobs.serialize_into(&mut buffer);
    assert_eq!(result, Err(SerializationError::BufferTooSmall));
}

#[test]
fn test_storage_key_deserialize_fobs() {
    let buffer = [0u8];
    let result = StorageKey::deserialize_from(&buffer);
    assert_eq!(result, Ok((StorageKey::Fobs, 1)));
}

#[test]
fn test_storage_key_deserialize_etag() {
    let buffer = [1u8];
    let result = StorageKey::deserialize_from(&buffer);
    assert_eq!(result, Ok((StorageKey::ETag, 1)));
}

#[test]
fn test_storage_key_deserialize_invalid() {
    let buffer = [2u8];
    let result = StorageKey::deserialize_from(&buffer);
    assert_eq!(result, Err(SerializationError::InvalidFormat));
}

#[test]
fn test_storage_key_deserialize_empty() {
    let buffer: [u8; 0] = [];
    let result = StorageKey::deserialize_from(&buffer);
    assert_eq!(result, Err(SerializationError::BufferTooSmall));
}

#[test]
fn test_storage_key_roundtrip() {
    for key in [StorageKey::Fobs, StorageKey::ETag] {
        let mut buffer = [0u8; 10];
        let len = key.serialize_into(&mut buffer).unwrap();
        let (deserialized, read_len) = StorageKey::deserialize_from(&buffer[..len]).unwrap();
        assert_eq!(deserialized, key);
        assert_eq!(read_len, len);
    }
}

// ============================================================================
// Tests for FobList
// ============================================================================

#[test]
fn test_fob_list_serialize_empty() {
    let fobs = FobList(vec![]);
    let mut buffer = [0u8; 100];
    let result = fobs.serialize_into(&mut buffer);
    assert_eq!(result, Ok(2));
    assert_eq!(&buffer[..2], &[0, 0]); // count = 0 in little endian
}

#[test]
fn test_fob_list_serialize_single() {
    let fobs = FobList(vec![12345678]);
    let mut buffer = [0u8; 100];
    let result = fobs.serialize_into(&mut buffer);
    assert_eq!(result, Ok(6)); // 2 bytes count + 4 bytes fob

    // Check count
    let count = u16::from_le_bytes([buffer[0], buffer[1]]);
    assert_eq!(count, 1);

    // Check fob value
    let fob = u32::from_le_bytes([buffer[2], buffer[3], buffer[4], buffer[5]]);
    assert_eq!(fob, 12345678);
}

#[test]
fn test_fob_list_serialize_multiple() {
    let fobs = FobList(vec![100, 200, 300]);
    let mut buffer = [0u8; 100];
    let result = fobs.serialize_into(&mut buffer);
    assert_eq!(result, Ok(14)); // 2 + 3*4 = 14

    let count = u16::from_le_bytes([buffer[0], buffer[1]]);
    assert_eq!(count, 3);
}

#[test]
fn test_fob_list_serialize_buffer_too_small() {
    let fobs = FobList(vec![1, 2, 3]);
    let mut buffer = [0u8; 5]; // Need 14, only have 5
    let result = fobs.serialize_into(&mut buffer);
    assert_eq!(result, Err(SerializationError::BufferTooSmall));
}

#[test]
fn test_fob_list_deserialize_empty() {
    let buffer = [0u8, 0u8]; // count = 0
    let result = FobList::deserialize_from(&buffer);
    assert!(result.is_ok());
    assert_eq!(result.unwrap().0.len(), 0);
}

#[test]
fn test_fob_list_deserialize_single() {
    let mut buffer = [0u8; 6];
    buffer[0..2].copy_from_slice(&1u16.to_le_bytes()); // count = 1
    buffer[2..6].copy_from_slice(&12345678u32.to_le_bytes());

    let result = FobList::deserialize_from(&buffer);
    assert!(result.is_ok());
    let fobs = result.unwrap().0;
    assert_eq!(fobs.len(), 1);
    assert_eq!(fobs[0], 12345678);
}

#[test]
fn test_fob_list_deserialize_multiple() {
    let mut buffer = [0u8; 14];
    buffer[0..2].copy_from_slice(&3u16.to_le_bytes());
    buffer[2..6].copy_from_slice(&100u32.to_le_bytes());
    buffer[6..10].copy_from_slice(&200u32.to_le_bytes());
    buffer[10..14].copy_from_slice(&300u32.to_le_bytes());

    let result = FobList::deserialize_from(&buffer);
    assert!(result.is_ok());
    let fobs = result.unwrap().0;
    assert_eq!(fobs, vec![100, 200, 300]);
}

#[test]
fn test_fob_list_deserialize_buffer_too_small_for_count() {
    let buffer = [0u8]; // Need 2 bytes for count
    let result = FobList::deserialize_from(&buffer);
    assert_eq!(result.err(), Some(SerializationError::BufferTooSmall));
}

#[test]
fn test_fob_list_deserialize_buffer_too_small_for_fobs() {
    let mut buffer = [0u8; 4];
    buffer[0..2].copy_from_slice(&2u16.to_le_bytes()); // count = 2, but only room for ~0.5 fobs
    let result = FobList::deserialize_from(&buffer);
    assert_eq!(result.err(), Some(SerializationError::BufferTooSmall));
}

#[test]
fn test_fob_list_deserialize_count_exceeds_max() {
    let mut buffer = [0u8; 4];
    buffer[0..2].copy_from_slice(&(MAX_FOBS as u16 + 1).to_le_bytes());
    let result = FobList::deserialize_from(&buffer);
    assert_eq!(result.err(), Some(SerializationError::InvalidFormat));
}

#[test]
fn test_fob_list_roundtrip() {
    let test_cases: Vec<Vec<u32>> = vec![
        vec![],
        vec![1],
        vec![0, u32::MAX],
        vec![100, 200, 300, 400, 500],
        (0..100).collect(),
    ];

    for fobs in test_cases {
        let original = FobList(fobs.clone());
        let mut buffer = [0u8; 2 + 100 * 4];
        let len = original.serialize_into(&mut buffer).unwrap();
        let deserialized = FobList::deserialize_from(&buffer[..len]).unwrap();
        assert_eq!(deserialized.0, fobs);
    }
}

#[test]
fn test_fob_list_max_capacity() {
    // Test with exactly MAX_FOBS entries
    let fobs: Vec<u32> = (0..MAX_FOBS as u32).collect();
    let original = FobList(fobs.clone());
    let mut buffer = vec![0u8; 2 + MAX_FOBS * 4];
    let len = original.serialize_into(&mut buffer).unwrap();
    let deserialized = FobList::deserialize_from(&buffer[..len]).unwrap();
    assert_eq!(deserialized.0.len(), MAX_FOBS);
}

// ============================================================================
// Tests for ETagValue
// ============================================================================

#[test]
fn test_etag_serialize_empty() {
    let etag = ETagValue(String::new());
    let mut buffer = [0u8; 100];
    let result = etag.serialize_into(&mut buffer);
    assert_eq!(result, Ok(1));
    assert_eq!(buffer[0], 0); // length = 0
}

#[test]
fn test_etag_serialize_simple() {
    let etag = ETagValue("abc123".to_string());
    let mut buffer = [0u8; 100];
    let result = etag.serialize_into(&mut buffer);
    assert_eq!(result, Ok(7)); // 1 byte length + 6 bytes string
    assert_eq!(buffer[0], 6);
    assert_eq!(&buffer[1..7], b"abc123");
}

#[test]
fn test_etag_serialize_with_quotes() {
    // ETag values often have quotes like W/"abc"
    let etag = ETagValue("W/\"abc\"".to_string());
    let mut buffer = [0u8; 100];
    let result = etag.serialize_into(&mut buffer);
    assert!(result.is_ok());
    assert_eq!(buffer[0], 7); // W/"abc" is 7 characters
}

#[test]
fn test_etag_serialize_buffer_too_small() {
    let etag = ETagValue("toolongstring".to_string());
    let mut buffer = [0u8; 5];
    let result = etag.serialize_into(&mut buffer);
    assert_eq!(result, Err(SerializationError::BufferTooSmall));
}

#[test]
fn test_etag_deserialize_empty() {
    let buffer = [0u8]; // length = 0
    let result = ETagValue::deserialize_from(&buffer);
    assert!(result.is_ok());
    assert_eq!(result.unwrap().0, "");
}

#[test]
fn test_etag_deserialize_simple() {
    let mut buffer = [0u8; 10];
    buffer[0] = 5;
    buffer[1..6].copy_from_slice(b"hello");

    let result = ETagValue::deserialize_from(&buffer);
    assert!(result.is_ok());
    assert_eq!(result.unwrap().0, "hello");
}

#[test]
fn test_etag_deserialize_buffer_too_small_for_length() {
    let buffer: [u8; 0] = [];
    let result = ETagValue::deserialize_from(&buffer);
    assert_eq!(result.err(), Some(SerializationError::BufferTooSmall));
}

#[test]
fn test_etag_deserialize_buffer_too_small_for_string() {
    let mut buffer = [0u8; 3];
    buffer[0] = 10; // Claims 10 bytes, but only 2 available
    let result = ETagValue::deserialize_from(&buffer);
    assert_eq!(result.err(), Some(SerializationError::BufferTooSmall));
}

#[test]
fn test_etag_deserialize_length_exceeds_64() {
    let buffer = [65u8]; // length = 65, exceeds max
    let result = ETagValue::deserialize_from(&buffer);
    assert_eq!(result.err(), Some(SerializationError::InvalidFormat));
}

#[test]
fn test_etag_deserialize_invalid_utf8() {
    let mut buffer = [0u8; 5];
    buffer[0] = 4;
    buffer[1..5].copy_from_slice(&[0xFF, 0xFE, 0xFF, 0xFE]); // Invalid UTF-8

    let result = ETagValue::deserialize_from(&buffer);
    assert_eq!(result.err(), Some(SerializationError::InvalidFormat));
}

#[test]
fn test_etag_roundtrip() {
    let test_cases = [
        "",
        "simple",
        "W/\"abc123\"",
        "\"0123456789abcdef0123456789abcdef\"",
        "special-chars-!@#$%",
    ];

    for etag_str in test_cases {
        let original = ETagValue(etag_str.to_string());
        let mut buffer = [0u8; 100];
        let len = original.serialize_into(&mut buffer).unwrap();
        let deserialized = ETagValue::deserialize_from(&buffer[..len]).unwrap();
        assert_eq!(deserialized.0, etag_str);
    }
}

#[test]
fn test_etag_max_length() {
    // Test with exactly 64 character string (max allowed)
    let etag_str: String = (0..64).map(|i| char::from(b'a' + (i % 26) as u8)).collect();
    let original = ETagValue(etag_str.clone());
    let mut buffer = [0u8; 100];
    let len = original.serialize_into(&mut buffer).unwrap();
    let deserialized = ETagValue::deserialize_from(&buffer[..len]).unwrap();
    assert_eq!(deserialized.0, etag_str);
}

// ============================================================================
// Tests for parse_port
// ============================================================================

#[test]
fn test_parse_port_valid() {
    assert_eq!(parse_port("8080"), 8080);
    assert_eq!(parse_port("80"), 80);
    assert_eq!(parse_port("443"), 443);
    assert_eq!(parse_port("3000"), 3000);
    assert_eq!(parse_port("65535"), 65535);
}

#[test]
fn test_parse_port_single_digit() {
    assert_eq!(parse_port("1"), 1);
    assert_eq!(parse_port("9"), 9);
}

#[test]
fn test_parse_port_leading_zeros() {
    assert_eq!(parse_port("0080"), 80);
    assert_eq!(parse_port("008080"), 8080);
}

#[test]
fn test_parse_port_empty_returns_default() {
    assert_eq!(parse_port(""), 8080);
}

#[test]
fn test_parse_port_zero_returns_default() {
    assert_eq!(parse_port("0"), 8080);
    assert_eq!(parse_port("000"), 8080);
}

#[test]
fn test_parse_port_with_non_digits() {
    // Non-digit characters are ignored
    assert_eq!(parse_port("abc"), 8080); // No digits -> 0 -> default
    assert_eq!(parse_port("80ab"), 80); // Stops at non-digit? No, skips non-digits
    assert_eq!(parse_port("a80b"), 80); // Digits are extracted
    assert_eq!(parse_port("8a0b8c0"), 8080); // All digits concatenated
}

#[test]
fn test_parse_port_whitespace() {
    // Whitespace is ignored (not a digit)
    assert_eq!(parse_port(" 80 "), 80);
    assert_eq!(parse_port("  443  "), 443);
}

#[test]
fn test_parse_port_overflow_wraps() {
    // 65536 wraps: 6553 * 10 + 6 = 65536, wrapping gives 0, which returns default 8080
    assert_eq!(parse_port("65536"), 8080);
}

#[test]
fn test_parse_port_large_number() {
    // 100000 wraps: 10000 * 10 = 100000, wrapping gives 34464 (100000 & 0xFFFF)
    assert_eq!(parse_port("100000"), 34464);
}
