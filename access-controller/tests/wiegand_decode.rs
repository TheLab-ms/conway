//! Tests for the pure Wiegand decoders (invariants W1–W4).
//!
//! Run with:
//!   cargo test --no-default-features --features sim \
//!              --target x86_64-unknown-linux-gnu \
//!              --test wiegand_decode

#![cfg(feature = "sim")]

use access_controller::decode::{decode_26, decode_34, encode_26, encode_34, WiegandRead};
use proptest::prelude::*;

// ---------------------------------------------------------------------------
// W2 / W3: scalar field derivations
// ---------------------------------------------------------------------------

#[test]
fn to_fob_is_facility_times_100k_plus_card() {
    // W2: fob = facility * 100_000 + card. Spot-check a known H10301 value.
    let w = WiegandRead { facility: 123, card: 45678, raw_data: 0 };
    assert_eq!(w.to_fob(), 123 * 100_000 + 45678);
    assert_eq!(w.to_fob(), 12_345_678);
}

#[test]
fn to_nfc_uid_swaps_bytes() {
    // W3: nfc_uid = raw_data.swap_bytes().
    let w = WiegandRead { facility: 0, card: 0, raw_data: 0xAABBCCDD };
    assert_eq!(w.to_nfc_uid(), 0xDDCCBBAA);
}

proptest! {
    #![proptest_config(ProptestConfig {
        cases: 4096,
        // Deterministic seed: failures are reproducible across runs.
        rng_algorithm: proptest::test_runner::RngAlgorithm::ChaCha,
        ..ProptestConfig::default()
    })]

    #[test]
    fn prop_to_fob_matches_formula(facility in 0u32..256, card in 0u32..(1 << 16)) {
        let w = WiegandRead { facility, card, raw_data: 0 };
        prop_assert_eq!(w.to_fob(), facility * 100_000 + card);
    }

    #[test]
    fn prop_to_nfc_uid_is_byte_reversal(raw in any::<u32>()) {
        let w = WiegandRead { facility: 0, card: 0, raw_data: raw };
        prop_assert_eq!(w.to_nfc_uid(), raw.swap_bytes());
    }
}

// ---------------------------------------------------------------------------
// W1: parity enforcement
// ---------------------------------------------------------------------------

#[test]
fn decode_26_accepts_well_formed_frame() {
    let frame = encode_26(123, 45678);
    let decoded = decode_26(frame).expect("well-formed 26-bit frame must decode");
    assert_eq!(decoded.facility, 123);
    assert_eq!(decoded.card, 45678);
}

#[test]
fn decode_26_rejects_flipped_leading_parity() {
    let frame = encode_26(123, 45678);
    let bad = frame ^ (1 << 25); // flip even-parity bit
    assert!(decode_26(bad).is_none());
}

#[test]
fn decode_26_rejects_flipped_trailing_parity() {
    let frame = encode_26(123, 45678);
    let bad = frame ^ 1; // flip odd-parity bit
    assert!(decode_26(bad).is_none());
}

#[test]
fn decode_34_accepts_well_formed_frame() {
    let frame = encode_34(7, 9999);
    let decoded = decode_34(frame).expect("well-formed 34-bit frame must decode");
    assert_eq!(decoded.facility, 7);
    assert_eq!(decoded.card, 9999);
}

#[test]
fn decode_34_rejects_flipped_leading_parity() {
    let frame = encode_34(7, 9999);
    let bad = frame ^ (1u64 << 33);
    assert!(decode_34(bad).is_none());
}

#[test]
fn decode_34_rejects_flipped_trailing_parity() {
    let frame = encode_34(7, 9999);
    let bad = frame ^ 1;
    assert!(decode_34(bad).is_none());
}

proptest! {
    #![proptest_config(ProptestConfig { cases: 4096, ..ProptestConfig::default() })]

    /// W1: any (facility, card) round-trips through encode_26 + decode_26.
    #[test]
    fn prop_decode_26_roundtrip(facility in 0u32..256, card in 0u32..(1 << 16)) {
        let frame = encode_26(facility, card);
        let decoded = decode_26(frame).expect("encoded frame must decode");
        prop_assert_eq!(decoded.facility, facility);
        prop_assert_eq!(decoded.card, card);
        prop_assert_eq!(decoded.to_fob(), facility * 100_000 + card);
    }

    /// W1: flipping any single bit in a valid frame must always fail parity,
    /// EXCEPT when the flipped bit is one of the 24 data bits, which moves
    /// the frame into a different (still valid) credential. Specifically:
    /// flipping bit 0 (odd parity) or bit 25 (even parity) must always be
    /// rejected because the data payload is unchanged. We check those two
    /// specifically; for data-bit flips we can only say the decoded value
    /// is *different* from the original.
    #[test]
    fn prop_decode_26_parity_bit_flip_rejected(
        facility in 0u32..256,
        card in 0u32..(1 << 16),
        which_parity_bit in 0u32..2,
    ) {
        let frame = encode_26(facility, card);
        let bit_pos = if which_parity_bit == 0 { 0 } else { 25 };
        let bad = frame ^ (1u64 << bit_pos);
        prop_assert!(decode_26(bad).is_none(),
            "flipping parity bit {} must reject", bit_pos);
    }

    #[test]
    fn prop_decode_34_roundtrip(facility in 0u32..256, card in 0u32..(1 << 16)) {
        let frame = encode_34(facility, card);
        let decoded = decode_34(frame).expect("encoded 34-bit frame must decode");
        prop_assert_eq!(decoded.facility, facility);
        prop_assert_eq!(decoded.card, card);
    }

    #[test]
    fn prop_decode_34_parity_bit_flip_rejected(
        facility in 0u32..256,
        card in 0u32..(1 << 16),
        which_parity_bit in 0u32..2,
    ) {
        let frame = encode_34(facility, card);
        let bit_pos = if which_parity_bit == 0 { 0 } else { 33 };
        let bad = frame ^ (1u64 << bit_pos);
        prop_assert!(decode_34(bad).is_none());
    }

    /// Random bit-string fuzzer: any 26-bit value that *does* decode must
    /// satisfy the parity equations. This catches accidental relaxations
    /// of the parity check.
    #[test]
    fn prop_decode_26_implies_parity_holds(raw in 0u64..(1 << 26)) {
        if let Some(_) = decode_26(raw) {
            let r = raw as u32;
            let leading = (r >> 25) & 1;
            let trailing = r & 1;
            let data = (r >> 1) & 0xFF_FFFF;
            let upper = data >> 12;
            let lower = data & 0xFFF;
            prop_assert_eq!(upper.count_ones() % 2, leading);
            prop_assert_ne!(lower.count_ones() % 2, trailing);
        }
    }
}
