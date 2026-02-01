#!/usr/bin/env python3
"""
ESP32 Door Access Controller PCB - SKiDL Design

Generates a KiCad-compatible netlist for PCB layout.
Optimized for JLCPCB assembly with LCSC part numbers.

Circuit summary:
- 12V input with reverse polarity protection (SS34 Schottky)
- AMS1117-3.3 LDO for ESP32 power (44uF MLCC filtering)
- 2x EL817 SMD optocouplers for Wiegand D0/D1 isolation (5V reader -> 3.3V ESP32)
- SS8050 NPN transistor + M7 flyback diode for 12V external relay control
- ESP32-WROOM-32 module on 2x19 female headers (manual assembly)
- Screw terminals for power, Wiegand, and relay connections (manual assembly)

Note: Optocouplers invert the Wiegand signal. Firmware must detect rising edges.

JLCPCB Assembly:
- SMD components: Fully assembled by JLCPCB
- THT connectors (J1-J5): Require manual soldering after delivery
- Extended parts: Only optocouplers (U2, U3) - $6 total setup fee
- All other parts are JLCPCB basic parts (no setup fee)
"""

import csv
from skidl import *

# Use KiCad 8 libraries
set_default_tool(KICAD8)

# =============================================================================
# JLCPCB-COMPATIBLE PART TEMPLATES (with LCSC part numbers)
# =============================================================================
# All SMD components have LCSC part numbers for JLCPCB assembly.
# THT connectors are for manual assembly after SMT delivery.
#
# Part types:
#   Basic    - No setup fee, lower cost
#   Extended - $3 setup fee per unique part type

# --- SMD Resistors (0805) - JLCPCB Basic Parts ---
resistor_390r = Part(
    "Device", "R",
    dest=TEMPLATE,
    footprint="Resistor_SMD:R_0805_2012Metric",
    value="390R",
    LCSC="C17630"
)

resistor_1k = Part(
    "Device", "R",
    dest=TEMPLATE,
    footprint="Resistor_SMD:R_0805_2012Metric",
    value="1K",
    LCSC="C17513"
)

resistor_10k = Part(
    "Device", "R",
    dest=TEMPLATE,
    footprint="Resistor_SMD:R_0805_2012Metric",
    value="10K",
    LCSC="C17414"
)

# --- SMD Capacitors (0805 MLCC) - JLCPCB Basic Parts ---
capacitor_100nf = Part(
    "Device", "C",
    dest=TEMPLATE,
    footprint="Capacitor_SMD:C_0805_2012Metric",
    value="100nF",
    LCSC="C49678"
)

capacitor_10uf = Part(
    "Device", "C",
    dest=TEMPLATE,
    footprint="Capacitor_SMD:C_0805_2012Metric",
    value="10uF",
    LCSC="C15850"
)

# --- SMD MLCC Capacitors for Power Supply - JLCPCB Basic Parts ---
# Using 22uF MLCC instead of 100uF electrolytic to avoid extended part fees
# Two 22uF in parallel provide adequate filtering for LDO input/output

# Input cap: 22uF/25V MLCC for 12V input filtering (0805)
capacitor_22uf_25v = Part(
    "Device", "C",
    dest=TEMPLATE,
    footprint="Capacitor_SMD:C_0805_2012Metric",
    value="22uF/25V",
    LCSC="C45783"
)

# Output cap: 22uF/25V MLCC for 3.3V output filtering (0805)
# Same part as input - 25V rating is fine for 3.3V rail
capacitor_22uf_output = Part(
    "Device", "C",
    dest=TEMPLATE,
    footprint="Capacitor_SMD:C_0805_2012Metric",
    value="22uF/25V",
    LCSC="C45783"
)

# --- Voltage Regulator - JLCPCB Basic Part ---
ldo_ams1117_33 = Part(
    "Regulator_Linear", "AMS1117-3.3",
    dest=TEMPLATE,
    footprint="Package_TO_SOT_SMD:SOT-223-3_TabPin2",
    LCSC="C6186"
)

# --- SMD Optocoupler - JLCPCB Extended Part ---
# EL817S is SMD equivalent of PC817, same pinout
# Pin 1=Anode, 2=Cathode, 3=Emitter, 4=Collector
# Using PC817 symbol (electrically identical) with SMD footprint
optocoupler_el817 = Part(
    "Isolator", "PC817",
    dest=TEMPLATE,
    footprint="Package_SO:SOP-4_4.4x2.6mm_P1.27mm",
    LCSC="C63268"
)

# --- SMD NPN Transistor - JLCPCB Basic Part ---
# SS8050: NPN, 1.5A, 25V, hFE 200-350, SOT-23
# Pinout (SOT-23): Pin 1=Base, 2=Emitter, 3=Collector
# Using generic NPN symbol with SOT-23 footprint
npn_ss8050 = Part(
    "Transistor_BJT", "PN2222A",
    dest=TEMPLATE,
    footprint="Package_TO_SOT_SMD:SOT-23",
    LCSC="C2150"
)

# --- SMD Diodes ---
# SS34: 3A 40V Schottky for reverse polarity protection (SMA package)
# Using generic diode symbol (schematic symbol is the same)
diode_ss34 = Part(
    "Device", "D",
    dest=TEMPLATE,
    footprint="Diode_SMD:D_SMA",
    value="SS34",
    LCSC="C8678"
)

# M7: 1A 1000V rectifier for flyback protection (SMA package, 1N4007 equivalent)
diode_m7 = Part(
    "Device", "D",
    dest=TEMPLATE,
    footprint="Diode_SMD:D_SMA",
    value="M7",
    LCSC="C95872"
)

# --- THT Connectors (Manual Assembly - No LCSC) ---
# These components are not assembled by JLCPCB and require manual soldering.
screw_terminal_2_template = Part(
    "Connector", "Conn_01x02_Pin",
    dest=TEMPLATE,
    footprint="TerminalBlock_Phoenix:TerminalBlock_Phoenix_MKDS-1,5-2_1x02_P5.00mm_Horizontal"
)

screw_terminal_4_template = Part(
    "Connector", "Conn_01x04_Pin",
    dest=TEMPLATE,
    footprint="TerminalBlock_Phoenix:TerminalBlock_Phoenix_MKDS-1,5-4_1x04_P5.00mm_Horizontal"
)

header_1x19_template = Part(
    "Connector", "Conn_01x19_Socket",
    dest=TEMPLATE,
    footprint="Connector_PinSocket_2.54mm:PinSocket_1x19_P2.54mm_Vertical"
)

# =============================================================================
# GLOBAL NETS
# =============================================================================

gnd = Net("GND")
vcc_12v = Net("+12V")
vcc_3v3 = Net("+3V3")

# =============================================================================
# SUBCIRCUITS
# =============================================================================

@subcircuit
def power_supply(vin_net, vout_net, gnd_net):
    """
    12V to 3.3V power supply with reverse polarity protection.

    Components (all SMD JLCPCB Basic parts):
    - D1: SS34 Schottky reverse polarity protection (SMA)
    - U1: AMS1117-3.3 LDO regulator (SOT-223)
    - C1, C1B: 2x 22uF/25V MLCC input (0805) - 44uF total
    - C2: 100nF ceramic input bypass (0805)
    - C3, C3B: 2x 22uF/25V MLCC output (0805) - 44uF total
    - C4: 100nF ceramic output bypass (0805)

    Note: Using parallel 22uF MLCCs instead of 100uF electrolytics
    to use only JLCPCB basic parts (no extended part setup fees).
    """
    # Reverse polarity protection diode (SS34 Schottky)
    d1 = diode_ss34()
    d1.ref = "D1"

    # Input capacitors - 2x 22uF in parallel = 44uF
    c1 = capacitor_22uf_25v()
    c1.ref = "C1"
    c1b = capacitor_22uf_25v()
    c1b.ref = "C1B"
    c2 = capacitor_100nf()
    c2.ref = "C2"

    # LDO regulator
    u1 = ldo_ams1117_33()
    u1.ref = "U1"

    # Output capacitors - 2x 22uF in parallel = 44uF
    c3 = capacitor_22uf_output()
    c3.ref = "C3"
    c3b = capacitor_22uf_output()
    c3b.ref = "C3B"
    c4 = capacitor_100nf()
    c4.ref = "C4"

    # Internal net after diode
    vin_protected = Net("VIN_PROT")

    # Input side connections
    vin_net += d1["A"]  # Anode to 12V input
    d1["K"] += vin_protected  # Cathode to protected rail

    vin_protected += u1["VI"], c1[1], c1b[1], c2[1]
    gnd_net += c1[2], c1b[2], c2[2], u1["GND"]

    # Output side connections
    vout_net += u1["VO"], c3[1], c3b[1], c4[1]
    gnd_net += c3[2], c3b[2], c4[2]


@subcircuit
def wiegand_input(data_in_net, data_out_net, reader_gnd_net, esp_vcc_net, esp_gnd_net, ref_prefix):
    """
    Single Wiegand channel with EL817 SMD optocoupler isolation.

    The optocoupler INVERTS the signal:
    - Reader idle (HIGH): LED on -> transistor on -> GPIO LOW
    - Reader pulse (LOW): LED off -> transistor off -> GPIO HIGH

    Firmware must detect RISING edges instead of falling edges.

    Components (all SMD for JLCPCB assembly):
    - Ux: EL817 optocoupler (SOP-4)
    - Rx_in: 390 ohm input resistor (0805)
    - Rx_pu: 10K pull-up resistor (0805)
    """
    # Optocoupler (EL817 SMD - same pinout as PC817)
    opto = optocoupler_el817()
    opto.ref = f"U{ref_prefix}"

    # Input current limiting resistor
    # I = (5V - 1.2V) / 390 = ~10mA
    r_in = resistor_390r()
    r_in.ref = f"R{ref_prefix}"

    # Pull-up resistor on output
    r_pu = resistor_10k()
    r_pu.ref = f"R{ref_prefix + 2}"  # R4/R5 for pull-ups

    # LED side (pins 1=Anode, 2=Cathode on EL817)
    # Reader D0/D1 -> resistor -> LED anode
    # LED cathode -> reader GND
    data_in_net += r_in[1]
    r_in[2] += opto[1]  # Anode
    reader_gnd_net += opto[2]  # Cathode

    # Phototransistor side (pins 3=Emitter, 4=Collector on EL817)
    # Collector with pull-up to 3.3V -> GPIO output
    # Emitter to ESP32 GND
    esp_gnd_net += opto[3]  # Emitter
    data_out_net += opto[4], r_pu[1]  # Collector and pull-up
    esp_vcc_net += r_pu[2]  # Pull-up to 3.3V


@subcircuit
def relay_driver(gpio_net, coil_minus_net, coil_plus_net, gnd_net):
    """
    NPN low-side switch for external 12V relay coil.

    The relay coil connects between +12V (coil+) and the transistor collector (coil-).
    GPIO HIGH -> transistor on -> coil- pulled to GND -> relay energized.

    Components (all SMD for JLCPCB assembly):
    - Q1: SS8050 NPN transistor (SOT-23)
    - R6: 1K base resistor (0805)
    - D2: M7 flyback diode (SMA, cathode to +12V)
    """
    # NPN transistor (SS8050 SMD)
    q1 = npn_ss8050()
    q1.ref = "Q1"

    # Base resistor
    # I_base = (3.3V - 0.7V) / 1K = 2.6mA
    # SS8050 hFE >= 200, I_collector_max = 520mA (plenty for relay coil)
    r_base = resistor_1k()
    r_base.ref = "R6"

    # Flyback diode (M7 SMD - equivalent to 1N4007)
    d_flyback = diode_m7()
    d_flyback.ref = "D2"

    # Transistor connections
    gpio_net += r_base[1]
    r_base[2] += q1["B"]  # Base
    gnd_net += q1["E"]  # Emitter to ground

    # Collector connects to relay coil- terminal
    # Flyback diode: anode to collector, cathode to +12V
    coil_minus_net += q1["C"], d_flyback["A"]
    coil_plus_net += d_flyback["K"]


# =============================================================================
# ESP32 HEADER PIN MAPPING
# =============================================================================

# ESP32-WROOM-32 DevKit 38-pin pinout
# These are the physical header positions (1-19 for each side)
# Mapping based on common ESP32 DevKit V1 layout

ESP32_LEFT_PINOUT = {
    1: "3V3",
    2: "EN",
    3: "VP",       # GPIO36
    4: "VN",       # GPIO39
    5: "IO34",
    6: "IO35",
    7: "IO32",     # GPIO32 - Relay control
    8: "IO33",     # GPIO33 - Wiegand D1
    9: "IO25",     # GPIO25 - Wiegand D0
    10: "IO26",
    11: "IO27",
    12: "IO14",
    13: "IO12",
    14: "GND",
    15: "IO13",
    16: "D2",
    17: "D3",
    18: "CMD",
    19: "5V",
}

ESP32_RIGHT_PINOUT = {
    1: "GND",
    2: "IO23",
    3: "IO22",
    4: "TX",
    5: "RX",
    6: "IO21",
    7: "NC",
    8: "IO19",
    9: "IO18",
    10: "IO5",
    11: "IO17",
    12: "IO16",
    13: "IO4",
    14: "IO0",
    15: "IO2",
    16: "IO15",
    17: "D1",
    18: "D0",
    19: "CLK",
}


def create_esp32_socket():
    """
    Create 2x 1x19 female headers for ESP32 module.
    Returns the header parts and a dict of important GPIO nets.
    """
    global gnd, vcc_3v3

    # Create headers
    hdr_left = header_1x19_template()
    hdr_left.ref = "J4"

    hdr_right = header_1x19_template()
    hdr_right.ref = "J5"

    # Create nets for the GPIOs we need
    gpio_nets = {
        "GPIO25": Net("GPIO25"),  # Wiegand D0
        "GPIO32": Net("GPIO32"),  # Relay control
        "GPIO33": Net("GPIO33"),  # Wiegand D1
    }

    # Connect power pins on left header
    vcc_3v3 += hdr_left[1]   # Pin 1: 3V3
    gnd += hdr_left[14]      # Pin 14: GND
    # Note: Pin 19 is 5V input, we don't connect it (using 3.3V regulated)

    # Connect power pins on right header
    gnd += hdr_right[1]      # Pin 1: GND

    # Connect our GPIO pins (based on ESP32_LEFT_PINOUT)
    gpio_nets["GPIO32"] += hdr_left[7]   # Pin 7: IO32
    gpio_nets["GPIO33"] += hdr_left[8]   # Pin 8: IO33
    gpio_nets["GPIO25"] += hdr_left[9]   # Pin 9: IO25

    return hdr_left, hdr_right, gpio_nets


# =============================================================================
# MAIN CIRCUIT ASSEMBLY
# =============================================================================

def create_access_controller():
    """Assemble the complete access controller circuit."""

    # Declare global nets
    global gnd, vcc_12v, vcc_3v3

    # -------------------------------------------------------------------------
    # Connectors
    # -------------------------------------------------------------------------

    # J1: Power input (12V, GND)
    j1_power = screw_terminal_2_template()
    j1_power.ref = "J1"

    # J2: Wiegand reader (D0, D1, +12V, GND)
    j2_wiegand = screw_terminal_4_template()
    j2_wiegand.ref = "J2"

    # J3: Relay output (COIL-, COIL+/12V)
    j3_relay = screw_terminal_2_template()
    j3_relay.ref = "J3"

    # -------------------------------------------------------------------------
    # Power input connections
    # -------------------------------------------------------------------------

    vcc_12v += j1_power[1]  # Pin 1: +12V
    gnd += j1_power[2]       # Pin 2: GND

    # -------------------------------------------------------------------------
    # Power supply
    # -------------------------------------------------------------------------

    power_supply(vcc_12v, vcc_3v3, gnd)

    # -------------------------------------------------------------------------
    # ESP32 module socket
    # -------------------------------------------------------------------------

    hdr_left, hdr_right, gpio_nets = create_esp32_socket()

    # Decoupling capacitors near ESP32 power pins
    c5 = capacitor_100nf()
    c5.ref = "C5"
    c6 = capacitor_10uf()
    c6.ref = "C6"

    vcc_3v3 += c5[1], c6[1]
    gnd += c5[2], c6[2]

    # -------------------------------------------------------------------------
    # Wiegand interface
    # -------------------------------------------------------------------------

    # Internal nets for Wiegand signals
    wiegand_d0_reader = Net("WIEG_D0_IN")  # From reader
    wiegand_d1_reader = Net("WIEG_D1_IN")  # From reader
    reader_gnd = Net("READER_GND")

    # Wiegand connector assignments
    # J2 pin 1: D0 from reader
    # J2 pin 2: D1 from reader
    # J2 pin 3: +12V to reader
    # J2 pin 4: GND to reader
    wiegand_d0_reader += j2_wiegand[1]
    wiegand_d1_reader += j2_wiegand[2]
    vcc_12v += j2_wiegand[3]  # 12V pass-through for reader
    reader_gnd += j2_wiegand[4]

    # Connect reader ground to main ground
    # (Optocouplers provide isolation, but grounds are common)
    gnd += reader_gnd

    # Wiegand D0 optocoupler (U2, R1, R3)
    wiegand_input(
        data_in_net=wiegand_d0_reader,
        data_out_net=gpio_nets["GPIO25"],
        reader_gnd_net=reader_gnd,
        esp_vcc_net=vcc_3v3,
        esp_gnd_net=gnd,
        ref_prefix=2  # U2, R1, R3
    )

    # Wiegand D1 optocoupler (U3, R2, R4)
    wiegand_input(
        data_in_net=wiegand_d1_reader,
        data_out_net=gpio_nets["GPIO33"],
        reader_gnd_net=reader_gnd,
        esp_vcc_net=vcc_3v3,
        esp_gnd_net=gnd,
        ref_prefix=3  # U3, R2, R4
    )

    # -------------------------------------------------------------------------
    # Relay driver
    # -------------------------------------------------------------------------

    relay_coil_minus = Net("RELAY_COIL_NEG")

    relay_driver(
        gpio_net=gpio_nets["GPIO32"],
        coil_minus_net=relay_coil_minus,
        coil_plus_net=vcc_12v,
        gnd_net=gnd
    )

    # Relay connector
    # J3 pin 1: Coil- (switched by transistor)
    # J3 pin 2: Coil+ (+12V)
    relay_coil_minus += j3_relay[1]
    vcc_12v += j3_relay[2]

    # -------------------------------------------------------------------------
    # Run checks and generate output
    # -------------------------------------------------------------------------

    # Electrical Rules Check
    ERC()

    # Generate netlist for KiCad
    generate_netlist(file_="output/access_controller.net")

    # Generate BOM as XML
    generate_xml(file_="output/access_controller.xml")

    # Generate JLCPCB-compatible BOM CSV
    generate_jlcpcb_bom("output/access_controller_bom.csv")

    print("\n" + "=" * 70)
    print("PCB Design Generation Complete!")
    print("=" * 70)
    print("\nOutput files:")
    print("  - output/access_controller.net      (KiCad netlist)")
    print("  - output/access_controller.xml      (Bill of Materials - XML)")
    print("  - output/access_controller_bom.csv  (JLCPCB BOM)")
    print("\nJLCPCB Assembly Workflow:")
    print("  1. Open KiCad and create a new project")
    print("  2. Open PCB Editor (Pcbnew)")
    print("  3. File -> Import -> Netlist... -> Select access_controller.net")
    print("  4. Arrange components and route traces")
    print("  5. Run DRC, then generate Gerbers (File -> Fabrication Outputs)")
    print("  6. Generate CPL file (File -> Fabrication Outputs -> Component Placement)")
    print("  7. Upload to JLCPCB:")
    print("     - Gerbers as ZIP")
    print("     - access_controller_bom.csv for BOM")
    print("     - CPL file for pick-and-place positions")
    print("\nManual Assembly Required:")
    print("  - J1, J2, J3: Screw terminals (solder after SMT delivery)")
    print("  - J4, J5: ESP32 socket headers (solder after SMT delivery)")
    print("\nNOTE: The Wiegand signals are INVERTED by the optocouplers.")
    print("      Firmware must detect RISING edges instead of falling.")
    print("=" * 70)


def generate_jlcpcb_bom(filename):
    """
    Generate JLCPCB-compatible BOM CSV file.

    JLCPCB requires a BOM with columns: Comment, Designator, Footprint, LCSC Part #
    Components without LCSC numbers are listed as requiring manual assembly.
    """
    bom_entries = {}
    manual_assembly = []

    for part in default_circuit.parts:
        # LCSC is stored as a direct attribute by SKiDL, not in fields dict
        lcsc = getattr(part, 'LCSC', None) or part.fields.get("LCSC", "")
        if not lcsc:
            manual_assembly.append(part.ref)
            continue

        if lcsc not in bom_entries:
            # Extract just the footprint name without library prefix
            footprint_name = part.footprint
            if ":" in footprint_name:
                footprint_name = footprint_name.split(":")[-1]

            bom_entries[lcsc] = {
                "Comment": part.value if part.value else part.name,
                "Designator": [],
                "Footprint": footprint_name,
                "LCSC Part #": lcsc
            }
        bom_entries[lcsc]["Designator"].append(part.ref)

    # Write CSV
    with open(filename, 'w', newline='') as f:
        writer = csv.writer(f)
        writer.writerow(["Comment", "Designator", "Footprint", "LCSC Part #"])

        for entry in sorted(bom_entries.values(), key=lambda x: x["Designator"][0]):
            writer.writerow([
                entry["Comment"],
                ",".join(sorted(entry["Designator"])),
                entry["Footprint"],
                entry["LCSC Part #"]
            ])

    print(f"Generated JLCPCB BOM: {filename}")
    if manual_assembly:
        print(f"  Manual assembly: {', '.join(sorted(manual_assembly))}")


if __name__ == "__main__":
    create_access_controller()
