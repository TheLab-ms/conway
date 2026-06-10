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
//! Per-device key lives in eFuse BLOCK3 (32 bytes, 256 bits). The firmware
//! **self-provisions on first boot**: [`auto_provision`] reads BLOCK3, and
//! if it is still blank (virgin silicon) it generates 256 bits from the
//! hardware TRNG and burns them into BLOCK3 via the eFuse programming
//! registers. No external script or host tooling is required — power on a
//! fresh unit and it provisions itself, then [`init`] derives the sub-keys
//! on that same boot.
//!
//! eFuse BLOCK3 is one-time-writable (OTP), so [`auto_provision`] only ever
//! burns when the block reads back all-zero; on an already-provisioned unit
//! it is a no-op. The burn is guarded further by:
//!   * a coding-scheme check (raw 32-byte writes are only valid under
//!     coding scheme NONE; 3/4 and repeat encodings are refused), and
//!   * a post-burn readback verify (the derived key is only trusted if the
//!     bytes read back equal what we burned).
//!
//! The legacy operator script `tools/provision-device-key.sh` (host-side
//! `espefuse.py burn_block_data`) still works and remains as a fallback for
//! the rare case where self-provisioning is refused (e.g. unsupported
//! coding scheme); see `tools/README.md`.
//!
//! If a burn is ever refused or fails verification, [`state()`] returns
//! [`KeyState::Unprovisioned`] and both stores degrade safely: loads return
//! empty, saves return an error that is surfaced in the HTTP UI and logs.
//!
//! ### Threat note on self-generated entropy
//!
//! The root key is produced by [`esp_hal::rng::Trng`], which mixes SAR-ADC
//! noise (via [`esp_hal::rng::TrngSource`]) on top of the RF-derived noise
//! already present once `esp_radio::init()` has run. [`auto_provision`]
//! MUST therefore be called after radio init so the RNG is a true TRNG and
//! not a boot-deterministic PRNG.
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
    /// BLOCK3 is all zero — the device is not provisioned with a root
    /// key. Normally unreachable, because [`auto_provision`] burns a key
    /// into blank BLOCK3 on first boot; this state is only reached if that
    /// self-provisioning was refused (unsupported eFuse coding scheme) or
    /// its post-burn readback failed. Persistent stores will refuse to
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

/// eFuse coding scheme stored in BLK0. `0` == NONE (full 256-bit BLOCK3
/// usable, raw word writes valid). `1` == 3/4 encoding, `2` == repeat —
/// both shrink the usable block and require encoded writes, so we refuse
/// to auto-burn under them.
const CODING_SCHEME_NONE: u8 = 0;

/// eFuse controller op-codes (ESP32 TRM / esp-idf `efuse_ll.h`).
const EFUSE_WRITE_OP_CODE: u16 = 0x5A5A;
const EFUSE_READ_OP_CODE: u16 = 0x5AA5;

/// Outcome of a first-boot [`auto_provision`] attempt.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ProvisionOutcome {
    /// BLOCK3 already held a key; nothing was burned (the common case on
    /// every boot after the first).
    AlreadyProvisioned,
    /// BLOCK3 was blank and we burned a fresh random root key into it.
    Provisioned,
    /// BLOCK3 was blank but we deliberately did **not** end up with a
    /// usable key: the eFuse coding scheme is unsupported, the TRNG
    /// returned an unusable value, or the post-burn readback did not match.
    /// The reason is logged at `error` level. The device continues to boot
    /// unprovisioned (encryption disabled).
    Skipped,
}

/// Read the BLK0 coding scheme field (`RD_CODING_SCHEME`, 2 bits).
fn coding_scheme() -> u8 {
    esp_hal::peripherals::EFUSE::regs()
        .blk0_rdata6()
        .read()
        .rd_coding_scheme()
        .bits()
}

/// Clear every eFuse program (WDATA) register to zero so a subsequent
/// program cycle burns *only* the bits we explicitly stage. Burning a
/// zero bit is a no-op, so zeroing the other blocks' WDATA leaves them
/// untouched. Mirrors esp-idf's `efuse_hal_clear_program_registers`.
///
/// The WDATA registers are written through raw register pointers
/// (`as_ptr`) rather than the typed field writers: this PAC models several BLOCK3 words (e.g. WDATA3/WDATA4)
/// as named ADC-calibration sub-fields instead of a single 32-bit value,
/// and WDATA4 has no writer at all. Raw 32-bit stores sidestep that and are
/// exactly what an eFuse program cycle consumes.
fn clear_program_registers() {
    let efuse = esp_hal::peripherals::EFUSE::regs();
    let zero = |p: *mut u32| unsafe { p.write_volatile(0) };
    zero(efuse.blk0_wdata0().as_ptr());
    zero(efuse.blk0_wdata1().as_ptr());
    zero(efuse.blk0_wdata2().as_ptr());
    zero(efuse.blk0_wdata3().as_ptr());
    zero(efuse.blk0_wdata4().as_ptr());
    zero(efuse.blk0_wdata5().as_ptr());
    zero(efuse.blk0_wdata6().as_ptr());
    zero(efuse.blk1_wdata0().as_ptr());
    zero(efuse.blk1_wdata1().as_ptr());
    zero(efuse.blk1_wdata2().as_ptr());
    zero(efuse.blk1_wdata3().as_ptr());
    zero(efuse.blk1_wdata4().as_ptr());
    zero(efuse.blk1_wdata5().as_ptr());
    zero(efuse.blk1_wdata6().as_ptr());
    zero(efuse.blk1_wdata7().as_ptr());
    zero(efuse.blk2_wdata0().as_ptr());
    zero(efuse.blk2_wdata1().as_ptr());
    zero(efuse.blk2_wdata2().as_ptr());
    zero(efuse.blk2_wdata3().as_ptr());
    zero(efuse.blk2_wdata4().as_ptr());
    zero(efuse.blk2_wdata5().as_ptr());
    zero(efuse.blk2_wdata6().as_ptr());
    zero(efuse.blk2_wdata7().as_ptr());
    zero(efuse.blk3_wdata0().as_ptr());
    zero(efuse.blk3_wdata1().as_ptr());
    zero(efuse.blk3_wdata2().as_ptr());
    zero(efuse.blk3_wdata3().as_ptr());
    zero(efuse.blk3_wdata4().as_ptr());
    zero(efuse.blk3_wdata5().as_ptr());
    zero(efuse.blk3_wdata6().as_ptr());
    zero(efuse.blk3_wdata7().as_ptr());
}

/// Program the eFuse controller timing for the current APB clock, then run
/// one program cycle that burns `key` into BLOCK3, followed by a read cycle
/// that refreshes the readback registers.
///
/// The firmware always runs at `CpuClock::max()` (APB = 80 MHz), so we use
/// the 80 MHz timing row from esp-idf's `efuse_hal_set_timing`
/// (clk_sel0=80, clk_sel1=128, dac_clk_div=100). Lower APB rows are not
/// applicable here.
///
/// We never set any `RD_DIS` / read-protect bit: the CPU must keep read
/// access to BLOCK3 to derive the AEAD sub-keys at every boot (the ESP32
/// classic has no BLOCK3 key-feeder peripheral — see the module threat
/// model).
fn burn_block3(key: &Key) {
    let efuse = esp_hal::peripherals::EFUSE::regs();

    // eFuse programming timing for APB = 80 MHz.
    efuse
        .clk()
        .modify(|_, w| unsafe { w.sel0().bits(80).sel1().bits(128) });
    efuse
        .dac_conf()
        .modify(|_, w| unsafe { w.dac_clk_div().bits(100) });

    // Stage exactly the 32 key bytes into BLOCK3's WDATA, everything else 0.
    // Raw 32-bit stores (see `clear_program_registers`) because the typed
    // field writers don't expose a plain 32-bit value on every BLOCK3 word.
    clear_program_registers();
    let word = |i: usize| u32::from_le_bytes([key[i], key[i + 1], key[i + 2], key[i + 3]]);
    let wdata = [
        efuse.blk3_wdata0().as_ptr(),
        efuse.blk3_wdata1().as_ptr(),
        efuse.blk3_wdata2().as_ptr(),
        efuse.blk3_wdata3().as_ptr(),
        efuse.blk3_wdata4().as_ptr(),
        efuse.blk3_wdata5().as_ptr(),
        efuse.blk3_wdata6().as_ptr(),
        efuse.blk3_wdata7().as_ptr(),
    ];
    for (i, p) in wdata.iter().enumerate() {
        unsafe { p.write_volatile(word(i * 4)) };
    }

    // Program cycle. `modify` preserves CONF bit16 (force_no_wr_rd_dis,
    // part of the reset value) while setting the write op-code.
    efuse
        .conf()
        .modify(|_, w| unsafe { w.op_code().bits(EFUSE_WRITE_OP_CODE) });
    efuse.cmd().write(|w| w.pgm_cmd().set_bit());
    while efuse.cmd().read().bits() != 0 {}

    // Read cycle: reload the readback registers from the eFuse array so a
    // subsequent `read_block3()` observes the freshly burned bits.
    efuse
        .conf()
        .modify(|_, w| unsafe { w.op_code().bits(EFUSE_READ_OP_CODE) });
    efuse.cmd().write(|w| w.read_cmd().set_bit());
    while efuse.cmd().read().bits() != 0 {}
}

/// First-boot bootstrap: if eFuse BLOCK3 is blank, generate a fresh
/// 256-bit random root key from the hardware TRNG and burn it in, so the
/// device provisions itself with no external tooling or operator step.
///
/// Idempotent and safe to call on every boot: it is a no-op once BLOCK3
/// holds a key (BLOCK3 is one-time-writable, so re-burning is both
/// impossible and never attempted — the function gates hard on a blank
/// readback).
///
/// MUST be called:
///   * **after** `esp_radio::init()` — so the hardware RNG is a true TRNG
///     (RF noise mixed in) and not a boot-deterministic PRNG; and
///   * **before** [`init`] — which reads BLOCK3 and derives the sub-keys.
///
/// `rng` and `adc1` are consumed for the duration of key generation to
/// stand up an esp-hal [`TrngSource`] (adds SAR-ADC entropy); both are
/// released before the function returns.
pub fn auto_provision(
    rng: esp_hal::peripherals::RNG<'_>,
    adc1: esp_hal::peripherals::ADC1<'_>,
) -> ProvisionOutcome {
    use esp_hal::rng::{Trng, TrngSource};

    // Only a virgin unit should ever burn. Re-burning OTP is impossible and
    // a buggy double-burn could corrupt the block, so gate hard on a blank
    // readback.
    if read_block3().iter().any(|&b| b != 0) {
        return ProvisionOutcome::AlreadyProvisioned;
    }

    // Raw 32-byte BLOCK3 writes are only valid under coding scheme NONE.
    let scheme = coding_scheme();
    if scheme != CODING_SCHEME_NONE {
        log::error!(
            "device_key: eFuse coding scheme {} is not NONE — refusing to \
             auto-burn BLOCK3 (3/4 and repeat encodings need encoded writes \
             and would corrupt the key). Use tools/provision-device-key.sh.",
            scheme
        );
        return ProvisionOutcome::Skipped;
    }

    // Generate the root key from the true RNG. `TrngSource` enables the
    // SAR-ADC noise source; `Trng::try_new` then cannot fail.
    let source = TrngSource::new(rng, adc1);
    let mut key = Zeroizing::new([0u8; 32]);
    {
        let trng =
            Trng::try_new().expect("TrngSource is alive, so Trng::try_new must succeed");
        trng.read(&mut *key);
    }

    // An all-zero key is indistinguishable from "unprovisioned" and must
    // never be burned. A true RNG returning 32 zero bytes is astronomically
    // unlikely; treat it as an RNG fault and abort (next boot retries).
    if key.iter().all(|&b| b == 0) {
        log::error!("device_key: TRNG produced an all-zero key — aborting BLOCK3 burn");
        drop(source);
        return ProvisionOutcome::Skipped;
    }

    log::info!(
        "device_key: BLOCK3 is blank — burning a fresh per-device root key \
         (one-time, irreversible)"
    );
    burn_block3(&key);
    drop(source); // release RNG + ADC1

    // Verify the burn actually took. An undervolted or otherwise failed OTP
    // write can leave bits unset; only trust the key if it reads back
    // byte-for-byte.
    let readback = read_block3();
    if *readback != *key {
        log::error!(
            "device_key: BLOCK3 readback does not match the burned key — \
             provisioning FAILED. Encryption stays disabled this boot."
        );
        return ProvisionOutcome::Skipped;
    }

    log::info!("device_key: auto-provisioned a per-device root key into eFuse BLOCK3");
    ProvisionOutcome::Provisioned
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
             Encrypted at-rest storage is DISABLED. First-boot \
             auto-provisioning was refused or failed; check earlier logs. \
             tools/provision-device-key.sh can provision this unit manually."
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
