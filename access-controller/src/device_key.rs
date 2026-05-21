//! Per-device root key + HKDF-derived per-partition sub-keys for at-rest
//! encryption of the `nvs` (settings) and `fobs` partitions.
//!
//! ## Threat model
//!
//! Defends against an attacker that has physical custody of a powered-down
//! device and can run `espflash read-flash` to dump the SPI flash chip.
//! With this module enabled, the dump yields only ciphertext for the two
//! protected partitions; the WiFi PSK and local fob list cannot be
//! recovered from flash alone.
//!
//! Does **NOT** defend against:
//!   * `espefuse.py summary` over UART download mode — this returns the
//!     BLOCK3 root key in cleartext because BLOCK3 read-protection (RD_DIS
//!     bit) is *not* set (it cannot be, because we need the CPU to read
//!     the bytes to derive the AEAD key — the ESP32 classic AES peripheral
//!     does not have a BLOCK3 key-feeder, unlike S2/S3/C3).
//!   * Anyone with code execution on the running device (they can read the
//!     derived keys from RAM).
//!   * Modified/malicious firmware (OTA is unsigned — separate issue).
//!
//! For threat models that include UART-bootloader attackers, enable the
//! ESP32 native flash-encryption + Secure-Boot-v1 stack instead; that
//! moves the root key into BLOCK1/BLOCK2 where even the CPU cannot read
//! it (only the flash-encryption peripheral consumes it).
//!
//! ## Provisioning
//!
//! Per-device key lives in eFuse BLOCK3 (32 bytes, 256 bits). Burning
//! eFuse from firmware is **not** done — esp-hal exposes reads only, and
//! a buggy firmware build mis-burning BLOCK3 would brick the device
//! irreversibly. Instead, provisioning is an external operator step:
//!
//! ```sh
//! ./tools/provision-device-key.sh /dev/ttyUSB0
//! ```
//!
//! which generates 32 bytes of OS entropy and writes them with
//! `espefuse.py burn_block_data BLOCK3 ...`. See `tools/README.md`.
//!
//! On boot this module reads BLOCK3 via the PAC. If it is all-zero
//! (unprovisioned), [`state()`] returns [`KeyState::Unprovisioned`] and
//! both stores degrade safely: loads return empty, saves return an error
//! that is surfaced in the HTTP UI and logs.
//!
//! ## Derivation
//!
//! Per-partition sub-keys are derived via HKDF-SHA256, with the MAC as
//! salt (public, but provides domain separation between identical
//! firmware builds on different units) and a per-partition `info` label:
//!
//! ```text
//! K_fobs     = HKDF-SHA256(ikm = BLOCK3, salt = mac6, info = "conway/fobs/v1")
//! K_settings = HKDF-SHA256(ikm = BLOCK3, salt = mac6, info = "conway/settings/v1")
//! ```
//!
//! Sub-keys are cached at boot in a pair of `static mut [u8; 32]` arrays
//! guarded by an `AtomicU8` state flag with Release/Acquire ordering.
//! [`init`] is a single-shot called pre-task-spawn during boot: it writes
//! the bytes, then publishes `ST_READY` with a Release store; accessor
//! functions ([`fobs_key`], [`settings_key`]) load the state with Acquire
//! and only dereference the statics once they observe `ST_READY`. Once
//! published the bytes are immutable for the lifetime of the process.
//! This is logically equivalent to a write-once cell but avoids pulling
//! in `OnceCell`/`once_cell` for two fixed-size byte arrays.
//! The BLOCK3 IKM is held in a `Zeroizing<[u8; 32]>` and wiped as soon
//! as HKDF returns.

#![cfg(feature = "esp32")]

use core::sync::atomic::{AtomicU8, Ordering};

use hkdf::Hkdf;
use sha2::Sha256;
use zeroize::Zeroizing;

/// Symmetric key used by [`crate::crypto`] — 32 bytes for ChaCha20-Poly1305.
pub type Key = [u8; 32];

/// Provisioning state of the device key. Set once during `init()`.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum KeyState {
    /// `init()` has not run yet.
    Uninit,
    /// BLOCK3 is all zero — device has not been provisioned with
    /// `tools/provision-device-key.sh`. Persistent stores will refuse to
    /// save and return empty on load. The device still boots; the
    /// operator will see a "device not provisioned" banner in the HTTP
    /// UI and a loud log line at startup.
    Unprovisioned,
    /// Sub-keys are derived and cached; encrypted load/save is available.
    Ready,
}

// Encoded as u8 so we can use atomics (no need for a mutex around the
// state — it's written exactly once during single-threaded boot and only
// read thereafter).
const ST_UNINIT: u8 = 0;
const ST_UNPROVISIONED: u8 = 1;
const ST_READY: u8 = 2;
static STATE: AtomicU8 = AtomicU8::new(ST_UNINIT);

// Cached derived sub-keys. Set exactly once by `init()` when the device
// is provisioned. Held in static mutables guarded by `STATE` — only
// dereferenced when STATE == Ready, which is set with `Release` ordering
// after the bytes are written, so the AcquireLoad in the accessors
// observes the data.
static mut K_FOBS: Key = [0u8; 32];
static mut K_SETTINGS: Key = [0u8; 32];

const INFO_FOBS: &[u8] = b"conway/fobs/v1";
const INFO_SETTINGS: &[u8] = b"conway/settings/v1";

/// HKDF salt — the WiFi STA MAC (6 bytes). Public, not a secret; serves
/// only as domain separation between physically distinct devices that
/// happen to have identical BLOCK3 contents (which should be impossible
/// given correct provisioning, but defense in depth costs us nothing).
fn salt_from_mac() -> [u8; 6] {
    esp_radio::wifi::sta_mac()
}

/// Read all 256 bits of eFuse BLOCK3 via the PAC readback registers.
///
/// On a chip whose BLOCK3 has never been programmed, every readback word
/// is zero. This is the cue we use to detect "device not provisioned".
///
/// We use the PAC directly because `esp_hal::efuse` only exposes named
/// sub-fields of BLOCK3 (CUSTOM_MAC, SECURE_VERSION, ADC1_TP_*, …) and
/// has no helper for "read me the whole 32 bytes". Reading the readback
/// registers is side-effect-free.
fn read_block3() -> Zeroizing<Key> {
    // EFUSE_RDATA registers are read-only; reading them has no side
    // effects on chip state. `EFUSE::regs()` returns the PAC register
    // block without consuming a singleton, which matches how esp-hal's
    // own efuse code accesses these registers (efuse/esp32/mod.rs).
    let efuse = esp_hal::peripherals::EFUSE::regs();
    let mut out = Zeroizing::new([0u8; 32]);
    let words = [
        efuse.blk3_rdata0().read().bits(),
        efuse.blk3_rdata1().read().bits(),
        efuse.blk3_rdata2().read().bits(),
        efuse.blk3_rdata3().read().bits(),
        efuse.blk3_rdata4().read().bits(),
        efuse.blk3_rdata5().read().bits(),
        efuse.blk3_rdata6().read().bits(),
        efuse.blk3_rdata7().read().bits(),
    ];
    for (i, w) in words.iter().enumerate() {
        out[i * 4..(i + 1) * 4].copy_from_slice(&w.to_le_bytes());
    }
    out
}

/// Initialize the device key. Call exactly once, after `esp_radio::init()`
/// (so the MAC is reliably readable) and before any call to
/// [`crate::settings::load`] / [`crate::fob_store::load`].
///
/// Idempotent: subsequent calls are no-ops.
pub fn init() {
    if STATE.load(Ordering::Acquire) != ST_UNINIT {
        return;
    }

    let ikm = read_block3();
    if ikm.iter().all(|&b| b == 0) {
        log::warn!(
            "device_key: BLOCK3 is all-zero — device is UNPROVISIONED. \
             Encrypted at-rest storage is DISABLED. \
             Run tools/provision-device-key.sh against this unit to enable."
        );
        STATE.store(ST_UNPROVISIONED, Ordering::Release);
        return;
    }

    let salt = salt_from_mac();
    let hk = Hkdf::<Sha256>::new(Some(&salt), &*ikm);

    let mut k_fobs = Zeroizing::new([0u8; 32]);
    let mut k_settings = Zeroizing::new([0u8; 32]);

    // HKDF expand of length 32 bytes for SHA-256 only fails if `okm.len()`
    // exceeds 255 * HashLen = 8160 bytes — impossible at 32 bytes, so
    // unwrap is sound and would only fire on a programmer error.
    hk.expand(INFO_FOBS, &mut *k_fobs).expect("hkdf expand fobs");
    hk.expand(INFO_SETTINGS, &mut *k_settings)
        .expect("hkdf expand settings");

    // Publish keys, then state. The Release on STATE pairs with Acquire
    // in the accessors; readers either see Uninit/Unprovisioned (and
    // bail) or see Ready *with* the bytes already written.
    //
    // SAFETY: We are still in single-threaded boot (init() must be called
    // before any task is spawned that touches storage). No other thread
    // can be reading these statics yet.
    unsafe {
        K_FOBS = *k_fobs;
        K_SETTINGS = *k_settings;
    }
    STATE.store(ST_READY, Ordering::Release);

    log::info!("device_key: per-device key provisioned, sub-keys derived");
    // ikm / k_fobs / k_settings stack copies drop here -> Zeroizing wipes.
}

/// Current provisioning state.
pub fn state() -> KeyState {
    match STATE.load(Ordering::Acquire) {
        ST_READY => KeyState::Ready,
        ST_UNPROVISIONED => KeyState::Unprovisioned,
        _ => KeyState::Uninit,
    }
}

/// Convenience: `true` iff sub-keys are cached and persistent encrypted
/// load/save is available.
pub fn is_ready() -> bool {
    state() == KeyState::Ready
}

/// Sub-key for the `fobs` partition. `None` until [`init`] has run and
/// the device is provisioned.
pub fn fobs_key() -> Option<&'static Key> {
    if STATE.load(Ordering::Acquire) != ST_READY {
        return None;
    }
    // SAFETY: STATE == Ready means init() finished writing K_FOBS and
    // performed a Release store; this Acquire load synchronizes with it.
    // After Ready the key is immutable for the rest of the process.
    Some(unsafe { &K_FOBS })
}

/// Sub-key for the `nvs` (settings) partition.
pub fn settings_key() -> Option<&'static Key> {
    if STATE.load(Ordering::Acquire) != ST_READY {
        return None;
    }
    // SAFETY: see fobs_key.
    Some(unsafe { &K_SETTINGS })
}
