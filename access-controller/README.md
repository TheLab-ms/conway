# Access Controller v2

ESP32 door access controller firmware written in Rust using Embassy async. Reads Wiegand 26/34-bit RFID credentials, authenticates against the Conway fob API, and controls a door relay.

## Hardware

- ESP32 (4MB flash)
- Wiegand RFID reader on GPIO14 (D0) and GPIO27 (D1)
- Door relay on GPIO25

## Requirements

- [Rust ESP toolchain](https://docs.esp-rs.org/book/installation/index.html): `rustup +esp`
- espflash: `cargo install espflash`

## Configuration

Copy `network.env.example` to `network.env` and edit with your values:

## Build and Flash

```bash
# Build
source network.env && cargo run --release
```

## Deterministic simulation tests

The crate's business-logic core (Wiegand frame decoders + the
authorization state machine that drives `access_task`) is extracted into
a small pure library that can be exercised on the host without any
ESP32 hardware. Tests live in `tests/wiegand_decode.rs` and
`tests/access_core.rs` and combine handwritten scenarios with
`proptest`-based property tests over randomly generated event traces.

Run them with the host toolchain (NOT the `esp` toolchain pinned by
`rust-toolchain.toml`):

```bash
RUSTUP_TOOLCHAIN=stable cargo test \
    --no-default-features --features sim \
    --target x86_64-unknown-linux-gnu
```

The `sim` feature gates the binary out (`required-features = ["esp32"]`)
and makes all hardware deps optional, so only pure code and tests are
compiled. Default `cargo build --release` for the firmware is
unaffected.

Properties currently proven include: no `OpenDoor` effect without a
current fob-cache hit (A1/A2/A3); silent backoff window (A4); 10-second
recheck deadline never grants past expiry (A5); every granted card swipe
is accompanied by an `allowed:true` audit record; and Wiegand
frame-parity / fob-format invariants (W1–W4).

## Flashing an ESP32

1. Connect ESP32 via USB
2. Hold BOOT button, press RESET, release BOOT (if auto-reset doesn't work)
3. Run the flash command above
4. After flashing, press RESET or power cycle

The device will connect to WiFi, sync fobs from Conway, and begin accepting card scans.

## OTA (over-the-air) firmware updates

After the first USB flash the device can be updated over the LAN — no
USB cable required.

### One-time migration (required before first OTA)

OTA needs a custom partition table (`partitions.csv` in this directory)
and otadata slot that a stock factory build does not have. The first
time you upgrade a device from a pre-OTA build you MUST reflash over
USB so espflash writes the new partition table:

```bash
source network.env && cargo run --release
```

`espflash.toml` pins espflash to `partitions.csv` so this Just Works.
After this one USB flash, the device permanently has two app slots
(`ota_0`, `ota_1`) and an `otadata` partition; all subsequent updates
can go over the network.

### Updating over the network

Build the firmware image, then POST it as a raw binary body:

```bash
# Produces ./firmware.bin (cargo build + espflash save-image).
./build-ota.sh

# Upload it. Replace <ip> with the device's address.
curl --data-binary @firmware.bin \
  -H 'Content-Type: application/octet-stream' \
  http://<ip>/ota
```

On success the device replies with `ok: activated ota_N (... bytes),
rebooting` and reboots into the new image about 250 ms later.

The status page at `http://<ip>/` also has a file-picker that uses the
same endpoint, including a progress bar, and a "Roll back to previous
slot" button (which POSTs to `/ota/rollback`).

### Security model

There is **no authentication** on the OTA endpoint. Anyone with TCP
access to port 80 on the device can replace the firmware. Run these
devices on a trusted management VLAN/SSID only.

The only checks performed on upload are:
- `Content-Length` must fit inside the inactive app slot (~1.9 MiB);
- the first byte must be `0xE9` (ESP image magic);
- the received byte count must match `Content-Length`.

### Rollback

There is no automatic rollback. If a new image bricks WiFi/HTTP, the
only recovery is a USB reflash. If a new image boots and is reachable
but misbehaves, POST to `/ota/rollback` (or click the button on the
status page) to flip `otadata` back to the previously running slot and
reboot.


