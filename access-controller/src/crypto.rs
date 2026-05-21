//! Thin AEAD wrapper used by `fob_store` and `settings`.
//!
//! Wraps `chacha20poly1305::ChaCha20Poly1305` with our deterministic
//! nonce convention so both stores can't accidentally diverge in how
//! they construct nonces. Available in both firmware (`esp32`) and host
//! (`sim`) builds — host builds are how we unit-test the round-trip.
//!
//! ## Nonce
//!
//! 96-bit (12-byte) nonce = `seq.to_le_bytes()[..8] || domain[..4]`.
//!
//! `seq` is the monotonic per-store sequence counter (u64) already used
//! by both `fob_store` and `settings` to pick the winning sector during
//! ping-pong recovery. Because:
//!   * `seq` is monotonic across saves within a single store,
//!   * `domain` is a fixed 4-byte tag distinct between stores
//!     (`b"FOB1"` vs `b"CFG1"`), and
//!   * sub-keys are per-store (HKDF-derived with distinct `info`),
//! nonce reuse is impossible by construction — no reliance on RNG
//! quality at save time, and no need for nonce-misuse-resistant
//! cipher choice.
//!
//! ## Record envelope (both stores)
//!
//! ```text
//!   0..4    magic            ("FOBS" or "CONW", per store)
//!   4..8    version  = 3     (encrypted)
//!   8..16   seq      u64 LE  (monotonic)
//!  16..18   payload_len u16  (plaintext byte count, LE)
//!  18..20   reserved (zero)
//!  20..32   nonce            = seq_le(8) || domain_tag(4)
//!  32..N+32 ciphertext       (ChaCha20)
//!  N+32 ..  poly1305 tag     (16 bytes, AAD = header[0..32])
//! ```
//!
//! The full 32-byte header is fed in as AAD so an attacker cannot splice
//! a header from one sector or partition onto another partition's
//! ciphertext (the nonce inside the header is also part of the AAD,
//! which is redundant with its use as the AEAD nonce but harmless).

use chacha20poly1305::aead::AeadInPlace;
use chacha20poly1305::{ChaCha20Poly1305, KeyInit};

/// AEAD tag length for ChaCha20-Poly1305.
pub const TAG_LEN: usize = 16;
/// Nonce length for ChaCha20-Poly1305.
pub const NONCE_LEN: usize = 12;
/// Per-store envelope overhead added to the plaintext payload.
pub const ENVELOPE_OVERHEAD: usize = HEADER_LEN + TAG_LEN;
/// Encrypted record header length (bytes 0..32 of every sector).
pub const HEADER_LEN: usize = 32;

/// Record format version. Bump if envelope layout changes (would require
/// a factory wipe to roll out — there is no plaintext fallback).
pub const RECORD_VERSION: u32 = 3;

/// Domain tag for the local fob store (4 bytes).
pub const DOMAIN_FOBS: [u8; 4] = *b"FOB1";
/// Domain tag for the network settings store (4 bytes).
pub const DOMAIN_SETTINGS: [u8; 4] = *b"CFG1";

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum CryptoError {
    /// AEAD `decrypt_in_place_detached` rejected the ciphertext (tag
    /// mismatch, including header AAD or domain tag mismatch).
    AuthFailed,
    /// The supplied output buffer is too small for ciphertext+tag (seal)
    /// or for plaintext (open).
    BufferTooSmall,
}

/// Build the 12-byte nonce from `(seq, domain)`. Exposed for tests; the
/// `seal`/`open` helpers call this internally.
#[inline]
pub fn nonce(seq: u64, domain: [u8; 4]) -> [u8; NONCE_LEN] {
    let mut n = [0u8; NONCE_LEN];
    n[..8].copy_from_slice(&seq.to_le_bytes());
    n[8..].copy_from_slice(&domain);
    n
}

/// Build the on-flash 32-byte record header. `payload_len` is the
/// plaintext byte count; ciphertext is the same length.
pub fn build_header(magic: u32, seq: u64, payload_len: u16, domain: [u8; 4]) -> [u8; HEADER_LEN] {
    let mut h = [0u8; HEADER_LEN];
    h[0..4].copy_from_slice(&magic.to_le_bytes());
    h[4..8].copy_from_slice(&RECORD_VERSION.to_le_bytes());
    h[8..16].copy_from_slice(&seq.to_le_bytes());
    h[16..18].copy_from_slice(&payload_len.to_le_bytes());
    h[18..20].copy_from_slice(&[0, 0]); // reserved
    h[20..32].copy_from_slice(&nonce(seq, domain));
    h
}

/// Parse a 32-byte header. Returns `None` if magic / version don't match
/// or the nonce isn't internally consistent with `(seq, domain)`.
pub fn parse_header(buf: &[u8], magic: u32, domain: [u8; 4]) -> Option<(u64, u16)> {
    if buf.len() < HEADER_LEN {
        return None;
    }
    if u32::from_le_bytes([buf[0], buf[1], buf[2], buf[3]]) != magic {
        return None;
    }
    if u32::from_le_bytes([buf[4], buf[5], buf[6], buf[7]]) != RECORD_VERSION {
        return None;
    }
    let seq = u64::from_le_bytes([
        buf[8], buf[9], buf[10], buf[11], buf[12], buf[13], buf[14], buf[15],
    ]);
    let payload_len = u16::from_le_bytes([buf[16], buf[17]]);
    let expected_nonce = nonce(seq, domain);
    if buf[20..32] != expected_nonce[..] {
        return None;
    }
    Some((seq, payload_len))
}

/// Encrypt `plaintext` in place into `out`.
///
/// Writes `[header(32) || ciphertext(plaintext.len()) || tag(16)]` and
/// returns the total number of bytes written.
pub fn seal(
    key: &[u8; 32],
    magic: u32,
    seq: u64,
    domain: [u8; 4],
    plaintext: &[u8],
    out: &mut [u8],
) -> Result<usize, CryptoError> {
    let total = HEADER_LEN + plaintext.len() + TAG_LEN;
    if out.len() < total {
        return Err(CryptoError::BufferTooSmall);
    }
    if plaintext.len() > u16::MAX as usize {
        return Err(CryptoError::BufferTooSmall);
    }

    // Header first — it doubles as AAD, so it must be byte-stable before
    // we hand it to the AEAD.
    let header = build_header(magic, seq, plaintext.len() as u16, domain);
    out[..HEADER_LEN].copy_from_slice(&header);

    // Copy plaintext into the ciphertext slot, encrypt in place.
    out[HEADER_LEN..HEADER_LEN + plaintext.len()].copy_from_slice(plaintext);
    let (ct_region, tag_region) = out[HEADER_LEN..total].split_at_mut(plaintext.len());

    let cipher = ChaCha20Poly1305::new(key.into());
    let n = nonce(seq, domain);
    let tag = cipher
        .encrypt_in_place_detached((&n).into(), &header, ct_region)
        // Only happens on extremely large inputs (>= ~256 GiB) per
        // ChaCha20 spec — impossible at our sizes.
        .map_err(|_| CryptoError::BufferTooSmall)?;
    tag_region.copy_from_slice(&tag);

    Ok(total)
}

/// Verify + decrypt a record read from flash. On success returns the
/// plaintext slice length written into `out` (plaintext is `out[..n]`).
///
/// `record` must be `[header(32) || ciphertext || tag(16)]`. Any tampering
/// with header, ciphertext, or tag — or use of a wrong key / wrong
/// domain — produces `AuthFailed`.
pub fn open(
    key: &[u8; 32],
    magic: u32,
    domain: [u8; 4],
    record: &[u8],
    out: &mut [u8],
) -> Result<usize, CryptoError> {
    if record.len() < HEADER_LEN + TAG_LEN {
        return Err(CryptoError::AuthFailed);
    }
    let (seq, payload_len) =
        parse_header(record, magic, domain).ok_or(CryptoError::AuthFailed)?;
    let pt_len = payload_len as usize;
    if record.len() != HEADER_LEN + pt_len + TAG_LEN {
        return Err(CryptoError::AuthFailed);
    }
    if out.len() < pt_len {
        return Err(CryptoError::BufferTooSmall);
    }
    let header = &record[..HEADER_LEN];
    let ct = &record[HEADER_LEN..HEADER_LEN + pt_len];
    let tag = &record[HEADER_LEN + pt_len..];

    out[..pt_len].copy_from_slice(ct);
    let cipher = ChaCha20Poly1305::new(key.into());
    let n = nonce(seq, domain);
    cipher
        .decrypt_in_place_detached((&n).into(), header, &mut out[..pt_len], tag.into())
        .map_err(|_| CryptoError::AuthFailed)?;
    Ok(pt_len)
}

#[cfg(test)]
mod tests {
    use super::*;

    const KEY: [u8; 32] = [
        0x42, 0x9b, 0x9a, 0x9e, 0x33, 0x32, 0xd0, 0x1f, 0x05, 0xab, 0xcd, 0xef, 0x00, 0x11, 0x22,
        0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0x01, 0x02,
        0x03, 0x04,
    ];
    const MAGIC: u32 = 0x46_4F_42_53; // "FOBS"

    #[test]
    fn roundtrip_basic() {
        let pt = b"the quick brown fox jumps over the lazy dog";
        let mut sealed = alloc::vec![0u8; HEADER_LEN + pt.len() + TAG_LEN];
        let n = seal(&KEY, MAGIC, 42, DOMAIN_FOBS, pt, &mut sealed).unwrap();
        assert_eq!(n, sealed.len());

        let mut opened = alloc::vec![0u8; pt.len()];
        let m = open(&KEY, MAGIC, DOMAIN_FOBS, &sealed, &mut opened).unwrap();
        assert_eq!(m, pt.len());
        assert_eq!(&opened, pt);
    }

    #[test]
    fn roundtrip_empty_payload() {
        let pt: &[u8] = &[];
        let mut sealed = alloc::vec![0u8; HEADER_LEN + TAG_LEN];
        seal(&KEY, MAGIC, 0, DOMAIN_FOBS, pt, &mut sealed).unwrap();
        let mut opened = [0u8; 0];
        let m = open(&KEY, MAGIC, DOMAIN_FOBS, &sealed, &mut opened).unwrap();
        assert_eq!(m, 0);
    }

    #[test]
    fn wrong_key_rejected() {
        let pt = b"secret";
        let mut sealed = alloc::vec![0u8; HEADER_LEN + pt.len() + TAG_LEN];
        seal(&KEY, MAGIC, 1, DOMAIN_FOBS, pt, &mut sealed).unwrap();
        let mut bad = KEY;
        bad[0] ^= 1;
        let mut opened = alloc::vec![0u8; pt.len()];
        assert_eq!(
            open(&bad, MAGIC, DOMAIN_FOBS, &sealed, &mut opened),
            Err(CryptoError::AuthFailed)
        );
    }

    #[test]
    fn wrong_domain_rejected() {
        // Sealed under DOMAIN_FOBS, opened as DOMAIN_SETTINGS — must fail
        // (this is the cross-partition splicing defence).
        let pt = b"x";
        let mut sealed = alloc::vec![0u8; HEADER_LEN + pt.len() + TAG_LEN];
        seal(&KEY, MAGIC, 7, DOMAIN_FOBS, pt, &mut sealed).unwrap();
        let mut opened = alloc::vec![0u8; pt.len()];
        assert_eq!(
            open(&KEY, MAGIC, DOMAIN_SETTINGS, &sealed, &mut opened),
            Err(CryptoError::AuthFailed)
        );
    }

    #[test]
    fn wrong_magic_rejected() {
        let pt = b"x";
        let mut sealed = alloc::vec![0u8; HEADER_LEN + pt.len() + TAG_LEN];
        seal(&KEY, MAGIC, 7, DOMAIN_FOBS, pt, &mut sealed).unwrap();
        let mut opened = alloc::vec![0u8; pt.len()];
        assert_eq!(
            open(&KEY, 0xdead_beef, DOMAIN_FOBS, &sealed, &mut opened),
            Err(CryptoError::AuthFailed)
        );
    }

    #[test]
    fn flipped_ciphertext_byte_rejected() {
        let pt = b"hello world";
        let mut sealed = alloc::vec![0u8; HEADER_LEN + pt.len() + TAG_LEN];
        seal(&KEY, MAGIC, 99, DOMAIN_FOBS, pt, &mut sealed).unwrap();
        sealed[HEADER_LEN + 2] ^= 0x01;
        let mut opened = alloc::vec![0u8; pt.len()];
        assert_eq!(
            open(&KEY, MAGIC, DOMAIN_FOBS, &sealed, &mut opened),
            Err(CryptoError::AuthFailed)
        );
    }

    #[test]
    fn flipped_header_byte_rejected() {
        let pt = b"hello";
        let mut sealed = alloc::vec![0u8; HEADER_LEN + pt.len() + TAG_LEN];
        seal(&KEY, MAGIC, 100, DOMAIN_FOBS, pt, &mut sealed).unwrap();
        // Twiddle the seq field — also invalidates the nonce, but the
        // AEAD's AAD check is what we're verifying here.
        sealed[8] ^= 0x10;
        let mut opened = alloc::vec![0u8; pt.len()];
        // parse_header may already reject (nonce mismatch) — either way
        // we expect AuthFailed.
        assert_eq!(
            open(&KEY, MAGIC, DOMAIN_FOBS, &sealed, &mut opened),
            Err(CryptoError::AuthFailed)
        );
    }

    #[test]
    fn different_seq_distinct_ciphertext() {
        // Sanity: nonce is determined by seq, so two seals of the same
        // plaintext at different seqs must produce distinct ciphertexts.
        let pt = b"identical plaintext";
        let mut s1 = alloc::vec![0u8; HEADER_LEN + pt.len() + TAG_LEN];
        let mut s2 = alloc::vec![0u8; HEADER_LEN + pt.len() + TAG_LEN];
        seal(&KEY, MAGIC, 1, DOMAIN_FOBS, pt, &mut s1).unwrap();
        seal(&KEY, MAGIC, 2, DOMAIN_FOBS, pt, &mut s2).unwrap();
        assert_ne!(s1[HEADER_LEN..], s2[HEADER_LEN..]);
    }

    #[test]
    fn truncated_record_rejected() {
        let pt = b"hello";
        let mut sealed = alloc::vec![0u8; HEADER_LEN + pt.len() + TAG_LEN];
        seal(&KEY, MAGIC, 1, DOMAIN_FOBS, pt, &mut sealed).unwrap();
        let truncated = &sealed[..sealed.len() - 1];
        let mut opened = alloc::vec![0u8; pt.len()];
        assert_eq!(
            open(&KEY, MAGIC, DOMAIN_FOBS, truncated, &mut opened),
            Err(CryptoError::AuthFailed)
        );
    }

    /// H1 regression: a half-written sector (header intact, tag/CT
    /// truncated or garbage) must still expose its `seq` via
    /// `parse_header` so that `*_store::save()` can advance `next_seq`
    /// past it and not reuse a (key, nonce) pair.
    #[test]
    fn parse_header_recovers_seq_from_partial_write() {
        let pt = b"interrupted-payload";
        let mut sealed = alloc::vec![0u8; HEADER_LEN + pt.len() + TAG_LEN];
        seal(&KEY, MAGIC, 0xDEAD_BEEF, DOMAIN_FOBS, pt, &mut sealed).unwrap();

        // Simulate a power loss after the header was committed but
        // before the tag landed: corrupt the tag bytes.
        let tag_off = HEADER_LEN + pt.len();
        for b in &mut sealed[tag_off..tag_off + TAG_LEN] {
            *b ^= 0xFF;
        }

        // Open must fail (tag invalid)...
        let mut opened = alloc::vec![0u8; pt.len()];
        assert_eq!(
            open(&KEY, MAGIC, DOMAIN_FOBS, &sealed, &mut opened),
            Err(CryptoError::AuthFailed)
        );
        // ...but the header is still parseable and yields the original
        // seq, which is what peek_slot_seq() relies on.
        let (seq, payload_len) =
            parse_header(&sealed[..HEADER_LEN], MAGIC, DOMAIN_FOBS).expect("header parseable");
        assert_eq!(seq, 0xDEAD_BEEF);
        assert_eq!(payload_len as usize, pt.len());

        // And further: even if the ciphertext is entirely truncated
        // away (only the 32-byte header survived flush), the header
        // still parses.
        let header_only = &sealed[..HEADER_LEN];
        let (seq2, _) = parse_header(header_only, MAGIC, DOMAIN_FOBS).expect("hdr-only parseable");
        assert_eq!(seq2, 0xDEAD_BEEF);
    }
}
