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
source network.env && cargo build --release

# Flash (auto-detects serial port)
espflash flash --monitor target/xtensa-esp32-none-elf/release/access-controller
```

## Flashing an ESP32

1. Connect ESP32 via USB
2. Hold BOOT button, press RESET, release BOOT (if auto-reset doesn't work)
3. Run the flash command above
4. After flashing, press RESET or power cycle

The device will connect to WiFi, sync fobs from Conway, and begin accepting card scans.

