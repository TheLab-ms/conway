//! Pure Wiegand frame decoders.
//!
//! The hardware-driven async reader lives in `src/wiegand.rs`. Everything
//! testable in isolation (parity checks, field extraction, credential
//! derivation) lives here so it can be exercised from host tests.

/// Decoded Wiegand credential.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct WiegandRead {
    pub facility: u32,
    pub card: u32,
    pub raw_data: u32,
}

impl WiegandRead {
    /// H10301 fob format: facility code + 5-digit card ID.
    pub fn to_fob(&self) -> u32 {
        self.facility * 100_000 + self.card
    }

    /// NFC UID derived by byte-reversing the raw data field.
    pub fn to_nfc_uid(&self) -> u32 {
        self.raw_data.swap_bytes()
    }
}

/// Decode a 26-bit Wiegand frame (H10301).
///
/// Frame layout (MSB first):
/// - bit 25: even-parity over upper 12 data bits
/// - bits 24..1: 24 data bits (8-bit facility + 16-bit card)
/// - bit 0: odd-parity over lower 12 data bits
///
/// Returns `None` on parity failure.
pub fn decode_26(raw: u64) -> Option<WiegandRead> {
    let raw = raw as u32;
    let leading = (raw >> 25) & 1;
    let trailing = raw & 1;
    let data = (raw >> 1) & 0xFF_FFFF;

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

/// Decode a 34-bit Wiegand frame.
///
/// Frame layout (MSB first):
/// - bit 33: even-parity over upper 16 data bits
/// - bits 32..1: 32 data bits
/// - bit 0: odd-parity over lower 16 data bits
///
/// NOTE: We intentionally pull facility from bits 23..16 (8-bit), not bits
/// 31..16 as strict H10304 would specify. This matches the legacy fob
/// database that pre-dated this firmware. Tests below pin this behavior.
pub fn decode_34(raw: u64) -> Option<WiegandRead> {
    let leading = ((raw >> 33) & 1) as u32;
    let trailing = (raw & 1) as u32;
    let data = ((raw >> 1) & 0xFFFF_FFFF) as u32;

    let upper = data >> 16;
    let lower = data & 0xFFFF;
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

/// Build a syntactically valid 26-bit frame for a given facility/card pair,
/// with correct parity bits. Useful for tests and for round-tripping known
/// credentials through `decode_26`. Truncates `facility` to 8 bits and
/// `card` to 16 bits per H10301.
pub fn encode_26(facility: u32, card: u32) -> u64 {
    let facility = facility & 0xFF;
    let card = card & 0xFFFF;
    let data = (facility << 16) | card;
    let upper = data >> 12;
    let lower = data & 0xFFF;
    // even parity bit: chosen so upper.count_ones() + leading is even.
    let leading = upper.count_ones() & 1;
    // odd parity bit: chosen so lower.count_ones() + trailing is odd.
    let trailing = (lower.count_ones() & 1) ^ 1;
    ((leading as u64) << 25) | ((data as u64) << 1) | (trailing as u64)
}

/// Build a syntactically valid 34-bit frame, matching the legacy 8-bit
/// facility layout that `decode_34` understands.
pub fn encode_34(facility: u32, card: u32) -> u64 {
    let facility = facility & 0xFF;
    let card = card & 0xFFFF;
    let data: u32 = (facility << 16) | card;
    let upper = data >> 16;
    let lower = data & 0xFFFF;
    let leading = upper.count_ones() & 1;
    let trailing = (lower.count_ones() & 1) ^ 1;
    ((leading as u64) << 33) | ((data as u64) << 1) | (trailing as u64)
}
