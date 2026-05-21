# Conway Access Controller — Hardware (as built 2026-05-03)

This document describes the physical board fabricated on **2026-05-03** under
the name *TheLab Door Access Controller*. The firmware in this crate targets
this board exactly.

## Mechanical overview

The PCB is a carrier board for an **ESP32-DevKitC-V4** (a.k.a. NodeMCU-32S
38-pin DevKit) module. Only the **left** rail of the DevKit is socketed
into a 1×19 female header; the right rail is unused.

Header pin numbering matches the DevKit-V4 left side:

| Header pin | DevKit label | Notes                          |
|-----------:|--------------|--------------------------------|
| 1          | 3V3          | DevKit on-board AMS1117 output |
| 14         | GND          |                                |
| 19         | VIN / 5V     | Fed from board's K7805 buck    |

The board provides:

- One 2.1mm barrel jack / screw input for **12V DC**.
- One 10-position 5.08mm screw terminal (`CN1`) for the reader, relay, and
  feedback wiring.
- One `CONFIG` tact switch and one `STATUS` LED for local I/O.

## Power chain

```
12V IN → SMAJ5.0CA TVS  →  M7 reverse-polarity diode  →  K7805-1000 buck (12V → 5V, 1A)
                                                              │
                                                              ▼
                                                        DevKit VIN (header pin 19)
                                                              │
                                                              ▼
                                                  DevKit AMS1117 (5V → 3V3)
                                                              │
                                                              ▼
                                                  3V3 rail (header pin 1)
```

The 5V rail also powers:

- The reader's `+5V` side (via `CN1`, but the supplied reader is 12V-powered;
  +5V is only used as the pull-up rail for D0/D1, see below).
- The Wiegand pull-up network.

## Wiegand front-end

Wiegand `D0`/`D1` from the reader enter through `CN1`, then:

1. **TVS clamp**: `SMAJ5.0CA` to GND (D1, D5).
2. **Series resistor**: `1 kΩ` (R5, R6) for current limit into the clamp /
   buffer.
3. **Pull-up**: `10 kΩ` to **+5V** (R2, R3). Reader is open-drain, idle-HIGH.
4. **Buffer**: `SN74LVC2G17` dual non-inverting Schmitt-trigger buffer (U2),
   Vcc = 3V3, with **5V-tolerant inputs**. Output is a clean 3V3 logic swing
   into the ESP32, with the reader's native polarity preserved (idle HIGH,
   data pulse LOW).

The firmware therefore triggers on **falling edges** on D0/D1, and uses
`Pull::None` on the ESP32 input pins (the buffer drives them actively).

## Output drivers

All three outputs use **SS8050 NPN** transistors as low-side switches:

| Reference | Load                      | Sourced from `CN1`             |
|-----------|---------------------------|--------------------------------|
| Q1        | Door relay coil           | +12V → coil → Q1 → GND         |
| Q2        | Reader LED                | +12V → reader → Q2 → GND       |
| Q3        | Reader beeper             | +12V → reader → Q3 → GND       |

Because the loads sit between +12V and the collector, **GPIO HIGH on the
base-driver = load energized**.

> ⚠️ **No on-board flyback diode** is fitted across the relay coil. Use an
> external relay module that integrates its own flyback diode, or fit a
> `1N4007` across the relay coil at the wiring side. The on-board `M7` (D2)
> is only the input reverse-polarity protector and does **not** protect Q1.

## GPIO map (ESP32 ↔ board)

| Header pin | DevKit GPIO | Net          | Direction | Function                                                |
|-----------:|:-----------:|--------------|-----------|---------------------------------------------------------|
| 6          | GPIO35      | `CONFIG_BTN` | input     | Tact switch (ext 10kΩ pull-up to 3V3, 100nF debounce)   |
| 7          | GPIO12      | `DOOR`       | output    | Drives Q1 (door relay), active-HIGH                     |
| 8          | GPIO33      | `WIEG_D1`    | input     | Wiegand D1 via SN74LVC2G17, falling edge = '1' bit      |
| 9          | GPIO25      | `WIEG_D0`    | input     | Wiegand D0 via SN74LVC2G17, falling edge = '0' bit      |
| 10         | GPIO26      | `READER_LED` | output    | Drives Q2 (reader's LED line), active-HIGH              |
| 11         | GPIO27      | `READER_BEEP`| output    | Drives Q3 (reader's beeper line), active-HIGH           |
| 12         | GPIO14      | `STATUS_LED` | output    | On-board status LED via 330Ω, active-HIGH               |

`GPIO35` is input-only on the ESP32, which is why the CONFIG button relies on
the **external** pull-up + debounce cap rather than an internal pull.

## CN1 — 10-position screw terminal

| Pos | Net         | Notes                                       |
|----:|-------------|---------------------------------------------|
| 1   | +12V        | Same node as 12V input (post reverse-prot.) |
| 2   | GND         |                                             |
| 3   | RELAY +12V  | To one side of relay coil                   |
| 4   | RELAY -     | To other side of coil → Q1 collector        |
| 5   | READER +12V | Reader Vcc                                  |
| 6   | READER GND  | Reader 0V                                   |
| 7   | READER BEEP | Reader beeper line → Q3 collector           |
| 8   | READER LED  | Reader LED line → Q2 collector              |
| 9   | WIEG D0     | Reader D0                                   |
| 10  | WIEG D1     | Reader D1                                   |

## Local controls

- **STATUS LED** (GPIO14, 330Ω series, active-HIGH):
  - 1 Hz heartbeat once the network stack has an IPv4 lease.
  - 5 Hz fast blink while there is no IP / WiFi is not ready.
  - Fast 5× flash immediately before a CONFIG-button factory-reset reboot.
- **CONFIG button** (GPIO35, active-LOW):
  - **Short press** (≥50 ms, <5 s): requests an on-demand Conway sync.
  - **Long hold** (≥5 s): Holding CONFIG for ≥5 seconds wipes both the `nvs`
    partition (WiFi credentials + Conway host) and the `fobs` partition (local
    fob list), then reboots into onboarding AP mode.

## Firmware caveats / things to remember

- The Wiegand pull-ups are tied to **+5V**, not 3V3. This is only safe
  because the SN74LVC2G17 has 5V-tolerant inputs. Do not bypass the buffer.
- All three driver transistors are low-side switches with the load returning
  to +12V; the firmware must drive GPIO **HIGH** to assert each output.
- There is no on-board flyback diode for the relay (see above).
- The DevKit's on-board USB-UART (CP2102 / CH340 depending on revision)
  handles flashing; the carrier board exposes no USB of its own.
