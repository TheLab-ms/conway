# access-controller

MicroPython access controller for ESP32. Reads 34-bit Wiegand (RFID fobs + NFC tags), authenticates against a Conway server, and triggers a door relay.

## Hardware

- ESP32 (tested on ESP32-WROOM)
- Wiegand reader: D0 → GPIO14, D1 → GPIO27
- Door relay/strike: GPIO25 (active high, 200ms pulse)

## Dependencies

Requires MicroPython. Copy `aiorepl.py` to the device for REPL access over the event loop.

## Configuration

Create `network.conf` on the device:

```json
{
  "conwayHost": "192.168.1.x",
  "conwayPort": 8080,
  "ssid": "your-ssid",
  "password": "your-password"
}
```

## Deployment

```bash
# Initial connection may require a delay workaround
mpremote connect /dev/cu.usbserial-0001 sleep 2 fs cp main.py :main.py
mpremote connect /dev/cu.usbserial-0001 sleep 2 fs cp aiorepl.py :aiorepl.py
mpremote connect /dev/cu.usbserial-0001 sleep 2 fs cp network.conf :network.conf
mpremote # enter a repl
```

## Credential Encoding

The controller accepts two credential formats over 34-bit Wiegand:

- **RFID fobs (H10301)**: Facility code + card ID concatenated as decimal. E.g., facility `123`, card `45678` → stored as `12345678`.
- **NFC tags**: 4-byte UID byte-reversed, stored as decimal. E.g., UID `DE:AD:BE:EF` → `0xEFBEADDE` → `4022250974`.

Both formats are checked against the Conway server's fob list - there isn't a way to tell them apart otherwise.

## Runtime

- Polls Conway server every 10s; caches fob list to flash (`conwaystate.json`) with CRC32 validation
- HTTP server on :80 for status + manual unlock (`POST /unlock`)
- 30s hardware watchdog; exponential backoff on failed auth attempts
- `boom()` in REPL removes `main.py` for recovery/re-flash

