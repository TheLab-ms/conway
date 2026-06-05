# Conway Access Controller

ESP32 door access controller firmware written in Rust using Embassy async. Reads Wiegand 26/34-bit RFID credentials, authenticates against the Conway fob API (or a local fob list), and drives a door relay.

## Hardware

See [HARDWARE.md](HARDWARE.md) for pin map, power chain, and CN1 wiring.

## Requirements

- [Rust ESP toolchain](https://docs.esp-rs.org/book/installation/index.html) (`rustup +esp`). The installer also provides `xtensa-esp32-elf-gcc`, the linker referenced by `.cargo/config.toml`.
- `espflash`: `cargo install espflash`
- USB-serial driver for your board's bridge chip (CP2102 or CH340 — see HARDWARE.md).

## First flash

1. Connect the ESP32 via USB.
2. From this directory, run:

   ```bash
   cargo run --release
   ```

   Per `.cargo/config.toml` this builds, flashes, and opens the serial monitor in one step.
3. If auto-reset doesn't trigger, hold BOOT, tap RESET, release BOOT, then re-run.

After flashing, press RESET (or power-cycle) and continue with **First boot** below.

## Provision the per-device key (one-time, required)

**Before onboarding will work, the device must have its per-device root key burned into eFuse BLOCK3.** This key derives the at-rest encryption keys for settings and the local fob list; until it is set, `Save` on the config page fails (settings cannot be encrypted) and onboarding cannot complete.

This is normally a one-time factory step:

```bash
tools/provision-device-key.sh
```

The script refuses to run if BLOCK3 is already programmed, so it is safe to re-run. See [tools/README.md](tools/README.md) for details. A device whose key is unset shows a **"Device not provisioned"** banner on both the status and config pages.

## First boot

On first power-up (or after a factory reset) the device starts an open WiFi AP named `conway-XXXXXX`, where `XXXXXX` is the last 3 bytes of the MAC.

> **Prerequisite:** the per-device key must already be provisioned (see above). If it is not, the config page shows a "Device not provisioned" banner and `Save` will fail.

1. Join the `conway-XXXXXX` network from a phone or laptop.
2. Any browser request triggers a captive portal at <http://192.168.4.1/config> (all DNS is hijacked to the device).
3. Enter your real WiFi SSID and password. Optionally enter the Conway server IPv4; leave it blank to run standalone.
4. Save. The device reboots and joins your network over DHCP.

Tip: find the device's new IP from your router's DHCP lease table or from the serial monitor (look for the `IPv4` line). The status page is then at `http://<ip>/`.

## Physical controls

- **CONFIG button, short press:** sync fobs with Conway immediately.
- **CONFIG button, hold ≥ 5 s:** factory reset. Wipes WiFi credentials and the local fob list, then reboots into the onboarding AP.
- **STATUS LED:** heartbeat indicates the firmware is running.

See HARDWARE.md for the full controls and indicator table.

## Configuration

Primary configuration is the web UI at `http://<ip>/config` (also reachable via the onboarding captive portal). Settings are persisted in NVS.

`network.env` is *optional*: it lets you bake compile-time WiFi/Conway defaults into the firmware for development. Production users should leave it unset and provision via the onboarding portal above. See `network.env.example`. These values are `option_env!` fallbacks consulted only when NVS is empty (see `src/settings.rs`).

## Local fobs and standalone mode

The controller can operate without a Conway backend — useful for small installations, lab setups, or as a fallback. Local fobs added via the HTTP UI always take precedence over the Conway-synced cache; a "standalone" deployment simply has no Conway cache at all.

To run standalone:

1. Provision WiFi via the onboarding AP (or `/config`) as usual.
2. On the `/config` form, leave the Conway Host field blank.
3. Open `/fobs` and add fob IDs (with optional labels). Up to 128 entries are persisted in flash.

In standalone mode:

- Denied swipes apply the backoff schedule immediately (no remote recheck window, since there is no remote authority).
- Access events are dropped rather than buffered.
- The status page shows `(standalone)` for the Conway server.

Local fobs work the same way in either mode: a local hit grants unconditionally. A local miss falls through to the remote cache (if Conway is configured); local cannot *revoke* a remote grant.

> **Upgrading from an older build:** reflash once over USB with `cargo run --release` so espflash writes the new partition table (adds the `fobs` data partition and `ota_0`/`ota_1`/`otadata` for OTA). All subsequent updates can use OTA.

## OTA (over-the-air) firmware updates

After the first USB flash the device can be updated over the LAN — no USB cable required.

Build the firmware image, then POST it as a raw binary body:

```bash
# Produces ./firmware.bin (cargo build + espflash save-image).
./build-ota.sh

# Upload it. Replace <ip> with the device's address.
curl --data-binary @firmware.bin \
  -H 'Content-Type: application/octet-stream' \
  http://<ip>/ota
```

On success the device replies with `ok: activated ota_N (... bytes), rebooting` and reboots into the new image about 250 ms later.

The status page at `http://<ip>/` also has a file-picker that uses the same endpoint, including a progress bar, and a "Roll back to previous slot" button (which POSTs to `/ota/rollback`).

The only checks performed on upload are:

- `Content-Length` must fit inside the inactive app slot (~1.9 MiB);
- the first byte must be `0xE9` (ESP image magic);
- the received byte count must match `Content-Length`.

### Rollback

There is no automatic rollback. If a new image bricks WiFi/HTTP, the only recovery is a USB reflash. If a new image boots and is reachable but misbehaves, POST to `/ota/rollback` (or click the button on the status page) to flip `otadata` back to the previously running slot and reboot.

## Security

There is **no authentication** on any HTTP endpoint — `/config`, `/unlock`, `/fobs`, `/ota`, and `/ota/rollback` are all open. Anyone with TCP access to port 80 on the device can change settings, unlock the door, or replace the firmware. Run these devices on a trusted management VLAN/SSID only.

### At-rest encryption

Both persistent partitions (`nvs` = WiFi/Conway config, `fobs` = local fob list) are encrypted with ChaCha20-Poly1305 using per-device keys derived (HKDF-SHA256) from a 32-byte root in eFuse BLOCK3. This defends against `espflash read-flash` of a stolen unit — a flash dump yields only ciphertext.

Each device must be provisioned **exactly once** with `tools/provision-device-key.sh` (see `tools/README.md` and `HARDWARE.md`). Until provisioned, the firmware logs a loud warning and refuses to persist new state; loads return empty (settings then fall back to `option_env!` defaults from `network.env`).

**Known limits:** UART-bootloader access (`espefuse.py summary`) can still read BLOCK3 — the ESP32 classic AES peripheral has no BLOCK3 key-feeder, so the CPU must keep the bytes readable. OTA is also currently **unsigned**, so an attacker on the management VLAN can replace the firmware. If your threat model includes either, enable ESP32 native flash encryption + Secure Boot v1 and sign OTA images.

## Deterministic simulation tests

The crate's business-logic core (Wiegand frame decoders + the authorization state machine that drives `access_task`) is extracted into a small pure library that can be exercised on the host without any ESP32 hardware. Tests live in `tests/wiegand_decode.rs` and `tests/access_core.rs` and combine handwritten scenarios with `proptest`-based property tests over randomly generated event traces.

Run them with the host toolchain (NOT the `esp` toolchain pinned by `rust-toolchain.toml`):

```bash
RUSTUP_TOOLCHAIN=stable cargo test \
    --no-default-features --features sim \
    --target $(rustc -vV | sed -n 's|host: ||p')
```

The `sim` feature gates the binary out (`required-features = ["esp32"]`) and makes all hardware deps optional, so only pure code and tests are compiled. Default `cargo build --release` for the firmware is unaffected.

Properties currently proven include: no `OpenDoor` effect without a current fob-cache hit (A1/A2/A3); silent backoff window (A4); 10-second recheck deadline never grants past expiry (A5); every granted card swipe is accompanied by an `allowed:true` audit record; and Wiegand frame-parity / fob-format invariants (W1–W4).
