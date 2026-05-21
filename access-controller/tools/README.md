# access-controller tools

## `provision-device-key.sh`

One-time provisioning of the per-device root key used for at-rest
encryption of the `nvs` (settings) and `fobs` partitions.

### Why this exists

The firmware encrypts both partitions with ChaCha20-Poly1305 keys
derived from 32 bytes stored in eFuse BLOCK3. Burning eFuse can
brick a device if done wrong, so we keep that operation **out of the
firmware itself** — a buggy build that mis-burns BLOCK3 would
permanently corrupt every unit it touched. Instead, BLOCK3 is burned
externally over UART download mode using `espefuse.py`, exactly once,
per device.

### Usage

```sh
# 1. Put the device in download mode (hold GPIO0 low while resetting).
# 2. Connect over USB-serial.
# 3. Run:
./tools/provision-device-key.sh /dev/ttyUSB0
# 4. Reboot the device into normal mode.
```

The script:

1. Reads `espefuse.py summary` and refuses to proceed if BLOCK3 is
   already populated (BLOCK3 is one-time-writable; overwrite would
   corrupt the existing key without erasing the old one).
2. Generates 32 bytes from `/dev/urandom` into a `mktemp` file with
   mode `0600`, registers a `shred` cleanup trap.
3. Calls `espefuse.py burn_block_data BLOCK3 ...`.
4. Verifies the burn by re-reading BLOCK3.

On the next boot the firmware logs:

```
device_key: per-device key provisioned, sub-keys derived
```

If unprovisioned, the firmware instead logs (loudly) and runs with
encrypted persistence disabled — `fob_store::save()` /
`settings::save()` return errors, loads return empty.

### Requirements

* `espefuse.py` — `pip install esptool`
* `openssl` is not actually required; just `dd` from `/dev/urandom`
* `shred` (recommended) for tempfile cleanup

### Operator hygiene

* Each unit must be provisioned with its own freshly generated key.
  **Do not reuse keys across devices** — that defeats the purpose of
  per-device encryption.
* Run on a trusted machine; the script never logs or echoes the key
  to stdout, but the tempfile briefly exists on disk under `$TMPDIR`.
* This is normally a factory step. End users should never need to
  re-run it.

### Threat model

See `src/device_key.rs` doc comments. TL;DR:

* **Defends against:** `espflash read-flash` of a stolen unit
  (offline flash dumps yield only ciphertext).
* **Does NOT defend against:** an attacker who can also run
  `espefuse.py summary` over UART download mode (they recover BLOCK3
  in cleartext, because BLOCK3 is *not* read-protected — the ESP32
  classic AES peripheral has no BLOCK3 key-feeder so the CPU must
  remain able to read it). For that threat model, enable native ESP32
  flash encryption + Secure Boot v1 instead.
* **OTA is currently unsigned** — that is the next weakest link after
  flash encryption. Tracked separately.
