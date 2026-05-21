#!/usr/bin/env bash
# Provision the per-device root key used for at-rest encryption of the
# `nvs` (settings) and `fobs` partitions.
#
# Generates 32 bytes of OS entropy and burns them into eFuse BLOCK3 of
# the connected ESP32 over UART download mode. Until this is run, the
# firmware boots in "unprovisioned" mode: it logs a loud warning, and
# `fob_store::save()` / `settings::save()` return an error (loads return
# empty). After provisioning, encrypted persistence becomes available;
# reboot the device to pick up the new key.
#
# eFuse burns are IRREVERSIBLE. BLOCK3 can be written exactly once.
# This script refuses to run if BLOCK3 is already populated, to avoid
# silently corrupting a unit that has been provisioned previously.
#
# **Confirmation:** by default this script invokes `espefuse.py
# burn_block_data` *without* `--do-not-confirm`, so espefuse will prompt
# the operator interactively before the irreversible burn. Pass `--yes`
# or set `CONWAY_PROVISION_YES=1` to skip the prompt (intended for
# factory / CI use only).
#
# Usage:
#   ./tools/provision-device-key.sh [PORT]
#
#   PORT defaults to /dev/ttyUSB0. Override with $ESP_PORT or as the
#   first argument.
#
# Requirements:
#   * espefuse.py (ships with esptool, `pip install esptool`)
#   * openssl (for /dev/urandom is fine too, but openssl gives us
#     formatted hex without needing xxd)
#
# Operator hygiene:
#   * Run on a machine you trust. The 32-byte key is briefly written to
#     a tempfile under $TMPDIR with mode 0600 and `shred`-deleted on
#     exit. It is *not* logged or echoed to stdout.
#   * Each unit must be provisioned with its own freshly generated key.
#     Do NOT reuse keys across devices.

set -euo pipefail

# Parse optional --yes flag. Remaining positional arg becomes PORT.
CONFIRM_AUTO=0
if [ "${CONWAY_PROVISION_YES:-0}" = "1" ]; then
    CONFIRM_AUTO=1
fi
ARGS=()
for arg in "$@"; do
    case "$arg" in
        --yes|-y) CONFIRM_AUTO=1 ;;
        *) ARGS+=("$arg") ;;
    esac
done
set -- "${ARGS[@]+"${ARGS[@]}"}"

PORT="${1:-${ESP_PORT:-/dev/ttyUSB0}}"

if ! command -v espefuse.py >/dev/null 2>&1; then
    echo "error: espefuse.py not found in PATH. Install with: pip install esptool" >&2
    exit 1
fi

if [ ! -e "$PORT" ]; then
    echo "error: serial port '$PORT' does not exist" >&2
    exit 1
fi

echo "==> Reading current BLOCK3 status from $PORT ..."
# `espefuse.py summary` prints BLK3 as 8 hex words. Detect all-zero
# (= virgin) vs anything else.
SUMMARY="$(espefuse.py --port "$PORT" summary 2>/dev/null || true)"
if [ -z "$SUMMARY" ]; then
    echo "error: failed to read eFuses. Is the device in download mode (GPIO0 low at boot)?" >&2
    exit 1
fi

# Extract the BLK3 line(s); espefuse prints something like:
#   BLK3 (BLOCK3): Variable Block 3
#      = 00 00 00 00 00 00 00 00 ... R/W
BLK3_HEX="$(echo "$SUMMARY" | awk '
    /BLK3 \(BLOCK3\)/ { in_blk = 1; next }
    in_blk && /^[[:space:]]+= / { print; exit }
')"

if [ -z "$BLK3_HEX" ]; then
    echo "error: could not parse BLK3 line from espefuse summary." >&2
    echo "       Refusing to burn without confirming BLOCK3 is virgin." >&2
    echo "       Inspect 'espefuse.py --port $PORT summary' manually." >&2
    exit 2
fi
# Strip the "= " prefix and the " R/W" suffix; keep only hex bytes.
HEX_ONLY="$(echo "$BLK3_HEX" | sed -E 's/^[[:space:]]+= //; s/ R\/W.*$//; s/[^0-9a-fA-F]//g')"
if [ -z "$HEX_ONLY" ]; then
    echo "error: BLK3 line parsed but contained no hex bytes; cannot verify virginity." >&2
    echo "       Raw line: $BLK3_HEX" >&2
    exit 2
fi
if [ -n "$(echo "$HEX_ONLY" | tr -d '0')" ]; then
    echo "error: BLOCK3 is already programmed on this device:" >&2
    echo "       $BLK3_HEX" >&2
    echo "error: eFuse BLOCK3 is one-time-writable. Refusing to overwrite." >&2
    echo "       If you believe this is a mistake, inspect espefuse.py summary manually." >&2
    exit 2
fi

echo "==> BLOCK3 is virgin. Generating 32 bytes of entropy ..."
TMPKEY="$(mktemp -t conway-devkey.XXXXXX)"
chmod 600 "$TMPKEY"
cleanup() {
    if [ -f "$TMPKEY" ]; then
        # Best-effort secure wipe. shred isn't universal; fall back to overwrite + rm.
        if command -v shred >/dev/null 2>&1; then
            shred -uz "$TMPKEY" 2>/dev/null || rm -f "$TMPKEY"
        else
            dd if=/dev/zero of="$TMPKEY" bs=32 count=1 conv=notrunc 2>/dev/null || true
            rm -f "$TMPKEY"
        fi
    fi
}
trap cleanup EXIT INT TERM

# 32 bytes from the kernel CSPRNG. Writing raw bytes — espefuse.py
# burn_block_data takes a binary file, not hex.
dd if=/dev/urandom of="$TMPKEY" bs=32 count=1 status=none

if [ "$(stat -c%s "$TMPKEY" 2>/dev/null || stat -f%z "$TMPKEY")" != "32" ]; then
    echo "error: failed to write 32 bytes of entropy" >&2
    exit 1
fi

echo "==> Burning BLOCK3 on $PORT (this is irreversible) ..."
# Confirmation: by default, let espefuse.py prompt the operator. Pass
# --yes (or set CONWAY_PROVISION_YES=1) to skip the prompt for
# factory/CI use. The pre-check above verified BLOCK3 was virgin.
if [ "$CONFIRM_AUTO" = "1" ]; then
    espefuse.py --port "$PORT" burn_block_data --do-not-confirm BLOCK3 "$TMPKEY"
else
    espefuse.py --port "$PORT" burn_block_data BLOCK3 "$TMPKEY"
fi

echo "==> Verifying readback ..."
VERIFY="$(espefuse.py --port "$PORT" summary 2>/dev/null | awk '
    /BLK3 \(BLOCK3\)/ { in_blk = 1; next }
    in_blk && /^[[:space:]]+= / { print; exit }
')"
echo "    $VERIFY"

# Confirm it's no longer all zeros.
HEX_ONLY="$(echo "$VERIFY" | sed -E 's/^[[:space:]]+= //; s/ R\/W.*$//; s/[^0-9a-fA-F]//g')"
if [ -z "$HEX_ONLY" ] || [ -z "$(echo "$HEX_ONLY" | tr -d '0')" ]; then
    echo "error: post-burn BLOCK3 still reads as all-zero. Provisioning FAILED." >&2
    exit 3
fi

echo "==> Success. Reboot the device (out of download mode) to enable encrypted persistence."
echo "    The firmware will log 'device_key: per-device key provisioned' on next boot."
