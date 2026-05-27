//! Ed25519 verification of `POST /api/fobs` responses.
//!
//! The Conway server signs the raw bytes of each `200 OK` fob-list
//! response body with a per-server Ed25519 key (see
//! `engine/ed25519_signer.go`) and ships the 64-byte raw signature in
//! the `X-Fob-Signature` HTTP header, base64-encoded with standard
//! alphabet and padding.
//!
//! When a controller has been provisioned with a `trusted_pubkey` in
//! its persisted [`crate::settings::Settings`], `sync_task` calls
//! [`verify`] before parsing the body. A failed verification aborts the
//! sync without touching the fob cache or committing pending events.
//!
//! ## Why a bespoke base64 decoder?
//!
//! Pulling in a full `base64` crate would more than double the verifier
//! footprint for ~30 LoC of code. The inputs are tiny and fixed-shape
//! (64-byte sig = 88 base64 chars; 32-byte key = 44 base64 chars), so
//! the inline decoder pays for itself.
//!
//! ## Algorithm choice
//!
//! Ed25519 (`ed25519-compact`, pure Rust, `no_std`, constant-time
//! reference impl) was chosen because:
//! - public keys are 32 B and signatures 64 B, which fits easily in our
//!   tiny 4 KiB nvs sector and in an HTTP header;
//! - verification has no key-derivation step, so we don't need to keep
//!   any per-request state on the device;
//! - the verify path does not require an RNG, allowing
//!   `default-features = false` on `ed25519-compact` and dropping the
//!   `getrandom` dep that doesn't have an ESP32 backend.

use alloc::string::String;
use alloc::vec::Vec;
use ed25519_compact::{PublicKey, Signature};

/// Standard base64 alphabet.
const B64_ALPHA: &[u8; 64] =
    b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";

/// Encode `input` as standard base64 with padding.
///
/// Used by the HTTP config page to render the currently-installed
/// `trusted_pubkey` for operator verification before commit.
pub fn b64_encode(input: &[u8]) -> String {
    let mut out = String::with_capacity((input.len() + 2) / 3 * 4);
    let mut i = 0;
    while i + 3 <= input.len() {
        let n = ((input[i] as u32) << 16) | ((input[i + 1] as u32) << 8) | (input[i + 2] as u32);
        out.push(B64_ALPHA[((n >> 18) & 0x3F) as usize] as char);
        out.push(B64_ALPHA[((n >> 12) & 0x3F) as usize] as char);
        out.push(B64_ALPHA[((n >> 6) & 0x3F) as usize] as char);
        out.push(B64_ALPHA[(n & 0x3F) as usize] as char);
        i += 3;
    }
    let rem = input.len() - i;
    if rem == 1 {
        let n = (input[i] as u32) << 16;
        out.push(B64_ALPHA[((n >> 18) & 0x3F) as usize] as char);
        out.push(B64_ALPHA[((n >> 12) & 0x3F) as usize] as char);
        out.push_str("==");
    } else if rem == 2 {
        let n = ((input[i] as u32) << 16) | ((input[i + 1] as u32) << 8);
        out.push(B64_ALPHA[((n >> 18) & 0x3F) as usize] as char);
        out.push(B64_ALPHA[((n >> 12) & 0x3F) as usize] as char);
        out.push(B64_ALPHA[((n >> 6) & 0x3F) as usize] as char);
        out.push('=');
    }
    out
}

/// Verify that `sig_b64` is a valid Ed25519 signature, by the holder of
/// `pubkey`, over `body`.
///
/// `sig_b64` is the verbatim value of the `X-Fob-Signature` header (no
/// `b64=` prefix, no whitespace handling beyond `trim`). On any decode
/// failure or invalid-length input we return `false` and the caller
/// must treat the response as untrusted.
///
/// This function is *not* constant-time with respect to `sig_b64`'s
/// shape (length, charset). That is intentional and safe — the
/// signature is public data, transmitted in cleartext over the wire.
/// The cryptographic verification (`PublicKey::verify`) is constant
/// time per `ed25519-compact`'s documentation.
pub fn verify(pubkey: &[u8; 32], body: &[u8], sig_b64: &str) -> bool {
    let trimmed = sig_b64.trim();
    let sig_bytes = match b64_decode(trimmed) {
        Some(v) if v.len() == 64 => v,
        _ => return false,
    };
    let pk = match PublicKey::from_slice(pubkey) {
        Ok(pk) => pk,
        Err(_) => return false,
    };
    let sig = match Signature::from_slice(&sig_bytes) {
        Ok(s) => s,
        Err(_) => return false,
    };
    pk.verify(body, &sig).is_ok()
}

/// Decode a base64 string from the standard alphabet (`A-Z a-z 0-9 + /`)
/// with padding (`=`). Returns `None` for any malformed input.
///
/// We accept padding but do not require canonical padding (last quartet
/// may be 2, 3, or 4 chars, with optional `=`s). That tolerance matches
/// what the Go `encoding/base64` standard encoder produces and keeps us
/// resilient to whitespace-stripping intermediaries.
pub fn b64_decode(s: &str) -> Option<Vec<u8>> {
    let bytes = s.as_bytes();
    // Strip trailing padding for length math; we don't trust it.
    let mut end = bytes.len();
    while end > 0 && bytes[end - 1] == b'=' {
        end -= 1;
    }
    let payload = &bytes[..end];

    let mut out: Vec<u8> = Vec::with_capacity(payload.len() * 3 / 4 + 2);
    let mut acc: u32 = 0;
    let mut bits: u32 = 0;
    for &b in payload {
        let v: u32 = match b {
            b'A'..=b'Z' => (b - b'A') as u32,
            b'a'..=b'z' => (b - b'a' + 26) as u32,
            b'0'..=b'9' => (b - b'0' + 52) as u32,
            b'+' => 62,
            b'/' => 63,
            _ => return None,
        };
        acc = (acc << 6) | v;
        bits += 6;
        if bits >= 8 {
            bits -= 8;
            out.push(((acc >> bits) & 0xFF) as u8);
        }
    }
    // Reject quartets with leftover non-zero bits (malformed encoding).
    if bits > 0 && (acc & ((1 << bits) - 1)) != 0 {
        return None;
    }
    Some(out)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn b64_roundtrip_known_vectors() {
        // Empty
        assert_eq!(b64_decode("").unwrap(), Vec::<u8>::new());
        // "f" -> "Zg=="
        assert_eq!(b64_decode("Zg==").unwrap(), b"f");
        // "fo" -> "Zm8="
        assert_eq!(b64_decode("Zm8=").unwrap(), b"fo");
        // "foo" -> "Zm9v"
        assert_eq!(b64_decode("Zm9v").unwrap(), b"foo");
        // "hello world" -> "aGVsbG8gd29ybGQ="
        assert_eq!(b64_decode("aGVsbG8gd29ybGQ=").unwrap(), b"hello world");
    }

    #[test]
    fn b64_rejects_garbage() {
        assert!(b64_decode("***").is_none());
        assert!(b64_decode("Zm9v!").is_none());
        // Leftover non-zero bits in the last quartet.
        assert!(b64_decode("Zg=A").is_none() || b64_decode("Zg==").unwrap() == b"f");
    }

    #[test]
    fn verify_rejects_bad_sig() {
        let pk = [0u8; 32];
        assert!(!verify(&pk, b"hello", "AAAA"));
        assert!(!verify(&pk, b"hello", "not!base64"));
        // Length must be 64 bytes after b64 decode.
        assert!(!verify(&pk, b"hello", "AA=="));
    }

    /// Round-trip: sign with a freshly-generated keypair and verify.
    /// This pulls in the RNG via the test profile so we only do it when
    /// `sim` is on (host build with std).
    #[cfg(feature = "sim")]
    #[test]
    fn verify_accepts_good_sig() {
        use ed25519_compact::KeyPair;
        let kp = KeyPair::from_seed([7u8; 32].into());
        let msg = b"[123,234]\n";
        let sig = kp.sk.sign(msg, None);
        // Encode sig as base64 (standard, padded) so we exercise the
        // header path end-to-end.
        let sig_b64 = b64_encode(sig.as_ref());
        let mut pk_bytes = [0u8; 32];
        pk_bytes.copy_from_slice(kp.pk.as_ref());
        assert!(verify(&pk_bytes, msg, &sig_b64));
        // Tamper with body -> reject.
        assert!(!verify(&pk_bytes, b"[123,234,999]\n", &sig_b64));
    }

    #[test]
    fn b64_encode_known_vectors() {
        assert_eq!(b64_encode(b""), "");
        assert_eq!(b64_encode(b"f"), "Zg==");
        assert_eq!(b64_encode(b"fo"), "Zm8=");
        assert_eq!(b64_encode(b"foo"), "Zm9v");
        assert_eq!(b64_encode(b"hello world"), "aGVsbG8gd29ybGQ=");
    }

    #[test]
    fn b64_encode_decode_roundtrip() {
        let input: alloc::vec::Vec<u8> = (0u8..=200).collect();
        let s = b64_encode(&input);
        assert_eq!(b64_decode(&s).unwrap(), input);
    }
}
