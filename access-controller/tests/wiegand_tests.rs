//! Unit tests for Wiegand decoding logic.
//!
//! Tests the pure decoding functions from wiegand.rs without requiring hardware.

/// Decoded Wiegand credential (mirrors the struct in wiegand.rs).
#[derive(Debug, Clone, Copy, PartialEq)]
struct WiegandRead {
    facility: u32,
    card: u32,
    raw_data: u32,
}

impl WiegandRead {
    /// Convert to H10301 fob format: facility code + 5-digit card ID.
    fn to_fob(&self) -> u32 {
        self.facility * 100_000 + self.card
    }

    /// Convert raw data to NFC UID (byte-reversed).
    fn to_nfc_uid(&self) -> u32 {
        self.raw_data.swap_bytes()
    }
}

/// Decode 26-bit Wiegand format.
/// Format: 1 even parity + 8 facility + 16 card + 1 odd parity = 26 bits
fn decode_26(raw: u64) -> Option<WiegandRead> {
    let raw = raw as u32;
    let leading = (raw >> 25) & 1;
    let trailing = raw & 1;
    let data = (raw >> 1) & 0xFF_FFFF;

    // Even parity on upper 12 bits, odd parity on lower 12 bits
    let upper = data >> 12;
    let lower = data & 0xFFF;
    let even_ok = (upper.count_ones() % 2) == leading;
    let odd_ok = (lower.count_ones() % 2) != trailing;
    if !even_ok || !odd_ok {
        return None;
    }

    let facility = (data >> 16) & 0xFF;
    let card = data & 0xFFFF;
    Some(WiegandRead {
        facility,
        card,
        raw_data: data,
    })
}

/// Decode 34-bit Wiegand format.
/// Format: 1 even parity + 16 facility + 16 card + 1 odd parity = 34 bits
/// Note: Uses 8-bit facility extraction for compatibility with existing database.
fn decode_34(raw: u64) -> Option<WiegandRead> {
    let leading = ((raw >> 33) & 1) as u32;
    let trailing = (raw & 1) as u32;
    let data = ((raw >> 1) & 0xFFFF_FFFF) as u32;

    // Even parity on upper 16 bits, odd parity on lower 16 bits
    let upper = data >> 16;
    let lower = data & 0xFFFF;
    let even_ok = (upper.count_ones() % 2) == leading;
    let odd_ok = (lower.count_ones() % 2) != trailing;
    if !even_ok || !odd_ok {
        return None;
    }

    // Match original implementation: 8-bit facility, 16-bit card
    let facility = (data >> 16) & 0xFF;
    let card = data & 0xFFFF;
    Some(WiegandRead {
        facility,
        card,
        raw_data: data,
    })
}

// ============================================================================
// Tests for decode_26
// ============================================================================

#[test]
fn test_decode_26_valid_card() {
    // Create a valid 26-bit Wiegand code
    // Facility: 100 (0x64), Card: 12345 (0x3039)
    // Data field (24 bits): facility(8) + card(16) = 0x643039
    let facility: u32 = 100;
    let card: u32 = 12345;
    let data = (facility << 16) | card;

    // Calculate parities
    let upper = data >> 12;
    let lower = data & 0xFFF;
    let even_parity = upper.count_ones() % 2;
    let odd_parity = if (lower.count_ones() % 2) == 0 { 1 } else { 0 };

    // Assemble: leading_parity(1) + data(24) + trailing_parity(1)
    let raw = ((even_parity as u64) << 25) | ((data as u64) << 1) | (odd_parity as u64);

    let result = decode_26(raw);
    assert!(result.is_some());
    let read = result.unwrap();
    assert_eq!(read.facility, facility);
    assert_eq!(read.card, card);
    assert_eq!(read.raw_data, data);
}

#[test]
fn test_decode_26_facility_0_card_0() {
    // Edge case: all zeros (except parity bits)
    let data: u32 = 0;
    let upper = data >> 12;
    let lower = data & 0xFFF;
    let even_parity = upper.count_ones() % 2; // 0
    let odd_parity = if (lower.count_ones() % 2) == 0 { 1 } else { 0 }; // 1

    let raw = ((even_parity as u64) << 25) | ((data as u64) << 1) | (odd_parity as u64);

    let result = decode_26(raw);
    assert!(result.is_some());
    let read = result.unwrap();
    assert_eq!(read.facility, 0);
    assert_eq!(read.card, 0);
}

#[test]
fn test_decode_26_max_values() {
    // Maximum values: facility 255, card 65535
    let facility: u32 = 255;
    let card: u32 = 65535;
    let data = (facility << 16) | card;

    let upper = data >> 12;
    let lower = data & 0xFFF;
    let even_parity = upper.count_ones() % 2;
    let odd_parity = if (lower.count_ones() % 2) == 0 { 1 } else { 0 };

    let raw = ((even_parity as u64) << 25) | ((data as u64) << 1) | (odd_parity as u64);

    let result = decode_26(raw);
    assert!(result.is_some());
    let read = result.unwrap();
    assert_eq!(read.facility, 255);
    assert_eq!(read.card, 65535);
}

#[test]
fn test_decode_26_even_parity_failure() {
    // Create valid data then flip the even parity bit
    let facility: u32 = 100;
    let card: u32 = 12345;
    let data = (facility << 16) | card;

    let upper = data >> 12;
    let lower = data & 0xFFF;
    let even_parity = upper.count_ones() % 2;
    let odd_parity = if (lower.count_ones() % 2) == 0 { 1 } else { 0 };

    // Flip the even parity bit to make it invalid
    let wrong_even_parity = 1 - even_parity;
    let raw = ((wrong_even_parity as u64) << 25) | ((data as u64) << 1) | (odd_parity as u64);

    let result = decode_26(raw);
    assert!(result.is_none());
}

#[test]
fn test_decode_26_odd_parity_failure() {
    // Create valid data then flip the odd parity bit
    let facility: u32 = 100;
    let card: u32 = 12345;
    let data = (facility << 16) | card;

    let upper = data >> 12;
    let lower = data & 0xFFF;
    let even_parity = upper.count_ones() % 2;
    let odd_parity = if (lower.count_ones() % 2) == 0 { 1 } else { 0 };

    // Flip the odd parity bit to make it invalid
    let wrong_odd_parity = 1 - odd_parity;
    let raw = ((even_parity as u64) << 25) | ((data as u64) << 1) | (wrong_odd_parity as u64);

    let result = decode_26(raw);
    assert!(result.is_none());
}

#[test]
fn test_decode_26_both_parity_failure() {
    // Flip both parity bits
    let facility: u32 = 100;
    let card: u32 = 12345;
    let data = (facility << 16) | card;

    let upper = data >> 12;
    let lower = data & 0xFFF;
    let even_parity = upper.count_ones() % 2;
    let odd_parity = if (lower.count_ones() % 2) == 0 { 1 } else { 0 };

    let wrong_even_parity = 1 - even_parity;
    let wrong_odd_parity = 1 - odd_parity;
    let raw =
        ((wrong_even_parity as u64) << 25) | ((data as u64) << 1) | (wrong_odd_parity as u64);

    let result = decode_26(raw);
    assert!(result.is_none());
}

#[test]
fn test_decode_26_single_bit_error_in_data() {
    // Valid card, then flip one bit in the data (should fail parity)
    let facility: u32 = 100;
    let card: u32 = 12345;
    let data = (facility << 16) | card;

    let upper = data >> 12;
    let lower = data & 0xFFF;
    let even_parity = upper.count_ones() % 2;
    let odd_parity = if (lower.count_ones() % 2) == 0 { 1 } else { 0 };

    let raw = ((even_parity as u64) << 25) | ((data as u64) << 1) | (odd_parity as u64);

    // Flip bit 10 in the raw value (within data region)
    let corrupted = raw ^ (1 << 10);

    let result = decode_26(corrupted);
    assert!(result.is_none());
}

// ============================================================================
// Tests for decode_34
// ============================================================================

#[test]
fn test_decode_34_valid_card() {
    // Create a valid 34-bit Wiegand code
    // Full 16-bit facility: 0x1234, but we extract only lower 8 bits (0x34)
    // Card: 0x5678
    let full_facility: u32 = 0x1234;
    let card: u32 = 0x5678;
    let data = (full_facility << 16) | card;

    // Calculate parities on 16-bit halves
    let upper = data >> 16;
    let lower = data & 0xFFFF;
    let even_parity = upper.count_ones() % 2;
    let odd_parity = if (lower.count_ones() % 2) == 0 { 1 } else { 0 };

    // Assemble: leading_parity(1) + data(32) + trailing_parity(1)
    let raw = ((even_parity as u64) << 33) | ((data as u64) << 1) | (odd_parity as u64);

    let result = decode_34(raw);
    assert!(result.is_some());
    let read = result.unwrap();
    // Note: only lower 8 bits of facility are extracted (compatibility mode)
    assert_eq!(read.facility, full_facility & 0xFF);
    assert_eq!(read.card, card);
    assert_eq!(read.raw_data, data);
}

#[test]
fn test_decode_34_zeros() {
    let data: u32 = 0;
    let upper = data >> 16;
    let lower = data & 0xFFFF;
    let even_parity = upper.count_ones() % 2;
    let odd_parity = if (lower.count_ones() % 2) == 0 { 1 } else { 0 };

    let raw = ((even_parity as u64) << 33) | ((data as u64) << 1) | (odd_parity as u64);

    let result = decode_34(raw);
    assert!(result.is_some());
    let read = result.unwrap();
    assert_eq!(read.facility, 0);
    assert_eq!(read.card, 0);
}

#[test]
fn test_decode_34_max_values() {
    // Max 16-bit facility and card
    let full_facility: u32 = 0xFFFF;
    let card: u32 = 0xFFFF;
    let data = (full_facility << 16) | card;

    let upper = data >> 16;
    let lower = data & 0xFFFF;
    let even_parity = upper.count_ones() % 2;
    let odd_parity = if (lower.count_ones() % 2) == 0 { 1 } else { 0 };

    let raw = ((even_parity as u64) << 33) | ((data as u64) << 1) | (odd_parity as u64);

    let result = decode_34(raw);
    assert!(result.is_some());
    let read = result.unwrap();
    assert_eq!(read.facility, 0xFF); // Only lower 8 bits
    assert_eq!(read.card, 0xFFFF);
}

#[test]
fn test_decode_34_parity_failure() {
    let full_facility: u32 = 0x1234;
    let card: u32 = 0x5678;
    let data = (full_facility << 16) | card;

    let upper = data >> 16;
    let lower = data & 0xFFFF;
    let even_parity = upper.count_ones() % 2;
    let odd_parity = if (lower.count_ones() % 2) == 0 { 1 } else { 0 };

    // Flip the even parity bit
    let wrong_even = 1 - even_parity;
    let raw = ((wrong_even as u64) << 33) | ((data as u64) << 1) | (odd_parity as u64);

    let result = decode_34(raw);
    assert!(result.is_none());
}

// ============================================================================
// Tests for to_fob conversion
// ============================================================================

#[test]
fn test_to_fob_basic() {
    let read = WiegandRead {
        facility: 100,
        card: 12345,
        raw_data: 0,
    };
    // 100 * 100000 + 12345 = 10012345
    assert_eq!(read.to_fob(), 10012345);
}

#[test]
fn test_to_fob_zero() {
    let read = WiegandRead {
        facility: 0,
        card: 0,
        raw_data: 0,
    };
    assert_eq!(read.to_fob(), 0);
}

#[test]
fn test_to_fob_max_facility() {
    let read = WiegandRead {
        facility: 255,
        card: 0,
        raw_data: 0,
    };
    // 255 * 100000 = 25500000
    assert_eq!(read.to_fob(), 25500000);
}

#[test]
fn test_to_fob_max_card() {
    let read = WiegandRead {
        facility: 0,
        card: 65535,
        raw_data: 0,
    };
    assert_eq!(read.to_fob(), 65535);
}

#[test]
fn test_to_fob_max_both() {
    let read = WiegandRead {
        facility: 255,
        card: 65535,
        raw_data: 0,
    };
    // 255 * 100000 + 65535 = 25565535
    assert_eq!(read.to_fob(), 25565535);
}

// ============================================================================
// Tests for to_nfc_uid conversion
// ============================================================================

#[test]
fn test_to_nfc_uid_swap() {
    let read = WiegandRead {
        facility: 0,
        card: 0,
        raw_data: 0x12345678,
    };
    // Byte swap: 0x12345678 -> 0x78563412
    assert_eq!(read.to_nfc_uid(), 0x78563412);
}

#[test]
fn test_to_nfc_uid_zero() {
    let read = WiegandRead {
        facility: 0,
        card: 0,
        raw_data: 0,
    };
    assert_eq!(read.to_nfc_uid(), 0);
}

#[test]
fn test_to_nfc_uid_max() {
    let read = WiegandRead {
        facility: 0,
        card: 0,
        raw_data: 0xFFFFFFFF,
    };
    assert_eq!(read.to_nfc_uid(), 0xFFFFFFFF);
}

#[test]
fn test_to_nfc_uid_single_byte() {
    // 0x000000AB -> 0xAB000000
    let read = WiegandRead {
        facility: 0,
        card: 0,
        raw_data: 0x000000AB,
    };
    assert_eq!(read.to_nfc_uid(), 0xAB000000);
}

// ============================================================================
// Integration tests combining decode and conversion
// ============================================================================

#[test]
fn test_decode_26_and_to_fob() {
    // Simulate a real card: Facility 45, Card 9876
    let facility: u32 = 45;
    let card: u32 = 9876;
    let data = (facility << 16) | card;

    let upper = data >> 12;
    let lower = data & 0xFFF;
    let even_parity = upper.count_ones() % 2;
    let odd_parity = if (lower.count_ones() % 2) == 0 { 1 } else { 0 };

    let raw = ((even_parity as u64) << 25) | ((data as u64) << 1) | (odd_parity as u64);

    let read = decode_26(raw).unwrap();
    // Expected fob: 45 * 100000 + 9876 = 4509876
    assert_eq!(read.to_fob(), 4509876);
}

#[test]
fn test_decode_34_and_to_fob() {
    // 34-bit card: Full facility 0x0123, Card 0x4567
    // Extracted facility: 0x23 = 35
    let full_facility: u32 = 0x0123;
    let card: u32 = 0x4567;
    let data = (full_facility << 16) | card;

    let upper = data >> 16;
    let lower = data & 0xFFFF;
    let even_parity = upper.count_ones() % 2;
    let odd_parity = if (lower.count_ones() % 2) == 0 { 1 } else { 0 };

    let raw = ((even_parity as u64) << 33) | ((data as u64) << 1) | (odd_parity as u64);

    let read = decode_34(raw).unwrap();
    // Expected: facility(35) * 100000 + card(17767) = 3517767
    assert_eq!(read.facility, 0x23); // 35
    assert_eq!(read.card, 0x4567); // 17767
    assert_eq!(read.to_fob(), 35 * 100_000 + 17767);
}

// ============================================================================
// Property-based style tests (exhaustive edge cases)
// ============================================================================

#[test]
fn test_decode_26_preserves_roundtrip() {
    // Test several facility/card combinations
    let test_cases = [
        (0, 0),
        (1, 1),
        (127, 32768),
        (255, 65535),
        (100, 12345),
        (50, 50000),
    ];

    for (facility, card) in test_cases {
        let data: u32 = (facility << 16) | card;
        let upper: u32 = data >> 12;
        let lower: u32 = data & 0xFFF;
        let even_parity = upper.count_ones() % 2;
        let odd_parity = if (lower.count_ones() % 2) == 0 { 1 } else { 0 };

        let raw = ((even_parity as u64) << 25) | ((data as u64) << 1) | (odd_parity as u64);

        let result = decode_26(raw);
        assert!(
            result.is_some(),
            "Failed to decode facility={}, card={}",
            facility,
            card
        );
        let read = result.unwrap();
        assert_eq!(read.facility, facility);
        assert_eq!(read.card, card);
    }
}

#[test]
fn test_decode_34_preserves_roundtrip() {
    // Test several full_facility/card combinations
    let test_cases = [
        (0, 0),
        (0x00FF, 0x0001),
        (0x0100, 0xFFFF), // Upper bits of facility should be ignored
        (0xFFFF, 0xFFFF),
        (0x1234, 0x5678),
    ];

    for (full_facility, card) in test_cases {
        let data: u32 = (full_facility << 16) | card;
        let upper: u32 = data >> 16;
        let lower: u32 = data & 0xFFFF;
        let even_parity = upper.count_ones() % 2;
        let odd_parity = if (lower.count_ones() % 2) == 0 { 1 } else { 0 };

        let raw = ((even_parity as u64) << 33) | ((data as u64) << 1) | (odd_parity as u64);

        let result = decode_34(raw);
        assert!(
            result.is_some(),
            "Failed to decode full_facility={:#x}, card={:#x}",
            full_facility,
            card
        );
        let read = result.unwrap();
        assert_eq!(read.facility, full_facility & 0xFF);
        assert_eq!(read.card, card);
    }
}
