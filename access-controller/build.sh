#!/usr/bin/env bash
#
# Build and optionally flash the access controller firmware.
#
# Usage:
#     ./build.sh [--flash] [--serial DEVICE]
#
# Network configuration is read from network.conf (JSON format).
# Command-line arguments can override values from the config file.
#
# Requirements:
#     - Rust ESP32 toolchain: rustup +esp
#     - espflash for flashing: cargo install espflash
#     - jq for parsing network.conf: brew install jq

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CONFIG_FILE="$SCRIPT_DIR/network.conf"

# Defaults
SSID=""
PASSWORD=""
CONWAY_HOST=""
CONWAY_PORT="8080"
DO_FLASH=false
SERIAL_PORT=""

# Load from network.conf if it exists
if [[ -f "$CONFIG_FILE" ]]; then
    if ! command -v jq &> /dev/null; then
        echo "Error: jq is required to parse network.conf. Install with: brew install jq" >&2
        exit 1
    fi
    SSID=$(jq -r '.ssid // empty' "$CONFIG_FILE")
    PASSWORD=$(jq -r '.password // empty' "$CONFIG_FILE")
    CONWAY_HOST=$(jq -r '.conwayHost // empty' "$CONFIG_FILE")
    CONWAY_PORT=$(jq -r '.conwayPort // "8080"' "$CONFIG_FILE")
fi

usage() {
    cat <<EOF
Usage: $0 [options]

Build the access controller firmware with embedded WiFi credentials.
Network configuration is read from network.conf by default.

Options:
  --ssid SSID          WiFi network name (overrides network.conf)
  --password PASSWORD  WiFi password (overrides network.conf)
  --host HOST          Conway server hostname or IP (overrides network.conf)
  --port PORT          Conway server port (default: 8080)
  --flash              Flash to device after building
  --serial DEVICE      Serial port for flashing (e.g., /dev/ttyUSB0)
  -h, --help           Show this help message

Examples:
  # Build using network.conf
  ./build.sh

  # Build and flash using network.conf
  ./build.sh --flash

  # Override host from network.conf
  ./build.sh --host 192.168.1.68 --flash
EOF
    exit "${1:-0}"
}

# Parse arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --ssid)
            SSID="$2"
            shift 2
            ;;
        --password)
            PASSWORD="$2"
            shift 2
            ;;
        --host)
            CONWAY_HOST="$2"
            shift 2
            ;;
        --port)
            CONWAY_PORT="$2"
            shift 2
            ;;
        --flash)
            DO_FLASH=true
            shift
            ;;
        --serial)
            SERIAL_PORT="$2"
            shift 2
            ;;
        -h|--help)
            usage 0
            ;;
        *)
            echo "Unknown option: $1" >&2
            echo "" >&2
            usage 1
            ;;
    esac
done

# Validate required arguments
MISSING=""
[[ -z "$SSID" ]] && MISSING="$MISSING ssid"
[[ -z "$PASSWORD" ]] && MISSING="$MISSING password"
[[ -z "$CONWAY_HOST" ]] && MISSING="$MISSING conwayHost"

if [[ -n "$MISSING" ]]; then
    echo "Error: Missing required values:$MISSING" >&2
    if [[ ! -f "$CONFIG_FILE" ]]; then
        echo "Create network.conf or provide command-line arguments." >&2
    else
        echo "Add missing values to network.conf or provide via command-line." >&2
    fi
    echo "" >&2
    usage 1
fi

# Display config (mask password)
echo "=== Conway Access Controller Build ==="
echo "  SSID: $SSID"
echo "  Password: $(printf '*%.0s' $(seq 1 ${#PASSWORD}))"
echo "  Conway Host: $CONWAY_HOST"
echo "  Conway Port: $CONWAY_PORT"
echo ""

# Build with environment variables
echo "Building firmware..."
CONWAY_SSID="$SSID" \
CONWAY_PASSWORD="$PASSWORD" \
CONWAY_HOST="$CONWAY_HOST" \
CONWAY_PORT="$CONWAY_PORT" \
cargo build --release

echo ""
echo "Build complete: target/xtensa-esp32-none-elf/release/access-controller"

# Flash if requested
if $DO_FLASH; then
    echo ""
    echo "Flashing to device..."

    CMD=(espflash flash)
    if [[ -n "$SERIAL_PORT" ]]; then
        CMD+=(--port "$SERIAL_PORT")
    fi
    CMD+=(--monitor target/xtensa-esp32-none-elf/release/access-controller)

    echo "Command: ${CMD[*]}"
    "${CMD[@]}"
fi
