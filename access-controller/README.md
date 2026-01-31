# Conway Building Access Controller

Bare metal Rust implementation of the Conway building access controller for ESP32.

## Architecture

- **Core 0 (PRO_CPU)**: Real-time Wiegand reader + door relay. Never blocks on network.
- **Core 1 (APP_CPU)**: WiFi, HTTP client (Conway sync), HTTP server, flash storage.

**Inter-core communication** uses lock-free atomics:
- Fob list: Core 1 writes, Core 0 reads
- Access events: Core 0 writes, Core 1 reads
- Unlock requests: Core 1 writes, Core 0 reads

## Hardware

- ESP32-WROOM
- Wiegand reader: D0 → GPIO14, D1 → GPIO27
- Door relay: GPIO25 (active high, 200ms pulse)

## Prerequisites

1. **Rust** (stable): https://rustup.rs
2. **espup** (ESP32 Rust toolchain manager):
   ```bash
   cargo install espup
   espup install
   ```
3. **espflash** (flashing tool):
   ```bash
   cargo install espflash
   ```

## Building

WiFi and server credentials are embedded at build time. Use `build.sh`:

```bash
# Source the ESP toolchain (add to .bashrc/.zshrc for persistence)
. $HOME/export-esp.sh

# Build with credentials
./build.sh --ssid "YourWiFi" --password "secret" --host 192.168.1.68
```

Options:
- `--ssid` (required) - WiFi network name
- `--password` (required) - WiFi password
- `--host` (required) - Conway server hostname or IP
- `--port` - Conway server port (default: 8080)
- `--flash` - Flash to device after building
- `--serial` - Serial port for flashing (e.g., `/dev/ttyUSB0`)

The binary is at `target/xtensa-esp32-none-elf/release/access-controller`.

## Flashing

Connect the ESP32 via USB, then either:

```bash
# Build and flash in one step
./build.sh --ssid "YourWiFi" --password "secret" --host 192.168.1.68 --flash

# Or flash manually
espflash flash target/xtensa-esp32-none-elf/release/access-controller --monitor
```

Hold BOOT button while flashing if it fails to connect.

