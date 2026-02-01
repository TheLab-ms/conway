#!/usr/bin/env python3
"""
ESP32 Door Access Controller PCB - SKiDL Design

Generates a KiCad-compatible netlist for PCB layout.

Circuit summary:
- 12V input with reverse polarity protection
- AMS1117-3.3 LDO for ESP32 power
- 2x PC817 optocouplers for Wiegand D0/D1 isolation (5V reader -> 3.3V ESP32)
- 2N2222 NPN transistor + flyback diode for 12V external relay control
- ESP32-WROOM-32 module on 2x19 female headers
- Screw terminals for power, Wiegand, and relay connections

Note: Optocouplers invert the Wiegand signal. Firmware must detect rising edges.
"""

from skidl import *

# Use KiCad 8 libraries
set_default_tool(KICAD8)

# =============================================================================
# PART TEMPLATES
# =============================================================================

# Passive components (0805 SMD)
resistor_template = Part(
    "Device", "R",
    dest=TEMPLATE,
    footprint="Resistor_SMD:R_0805_2012Metric"
)

capacitor_template = Part(
    "Device", "C",
    dest=TEMPLATE,
    footprint="Capacitor_SMD:C_0805_2012Metric"
)

cap_electrolytic_template = Part(
    "Device", "C_Polarized",
    dest=TEMPLATE,
    footprint="Capacitor_THT:CP_Radial_D6.3mm_P2.50mm"
)

# Semiconductors
diode_template = Part(
    "Device", "D",
    dest=TEMPLATE,
    footprint="Diode_THT:D_DO-41_SOD81_P10.16mm_Horizontal"
)

diode_do201_template = Part(
    "Device", "D",
    dest=TEMPLATE,
    footprint="Diode_THT:D_DO-201_P15.24mm_Horizontal"
)

npn_template = Part(
    "Transistor_BJT", "PN2222A",
    dest=TEMPLATE,
    footprint="Package_TO_SOT_THT:TO-92_Inline"
)

optocoupler_template = Part(
    "Isolator", "PC817",
    dest=TEMPLATE,
    footprint="Package_DIP:DIP-4_W7.62mm"
)

# Voltage regulator
ldo_template = Part(
    "Regulator_Linear", "AMS1117-3.3",
    dest=TEMPLATE,
    footprint="Package_TO_SOT_SMD:SOT-223-3_TabPin2"
)

# Connectors - use generic connectors (screw terminals have same pinout)
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

    Components:
    - D1: 1N5400 reverse polarity protection diode
    - U1: AMS1117-3.3 LDO regulator
    - C1: 100uF/25V input electrolytic
    - C2: 100nF input ceramic bypass
    - C3: 100uF/10V output electrolytic
    - C4: 100nF output ceramic bypass
    """
    # Reverse polarity protection diode
    d1 = diode_do201_template(value="1N5400")
    d1.ref = "D1"

    # Input capacitors
    c1 = cap_electrolytic_template(value="100uF/25V")
    c1.ref = "C1"
    c2 = capacitor_template(value="100nF")
    c2.ref = "C2"

    # LDO regulator
    u1 = ldo_template()
    u1.ref = "U1"

    # Output capacitors
    c3 = cap_electrolytic_template(value="100uF/10V")
    c3.ref = "C3"
    c4 = capacitor_template(value="100nF")
    c4.ref = "C4"

    # Internal net after diode
    vin_protected = Net("VIN_PROT")

    # Input side connections
    vin_net += d1["A"]  # Anode to 12V input
    d1["K"] += vin_protected  # Cathode to protected rail

    vin_protected += u1["VI"], c1[1], c2[1]
    gnd_net += c1[2], c2[2], u1["GND"]

    # Output side connections
    vout_net += u1["VO"], c3[1], c4[1]
    gnd_net += c3[2], c4[2]


@subcircuit
def wiegand_input(data_in_net, data_out_net, reader_gnd_net, esp_vcc_net, esp_gnd_net, ref_prefix):
    """
    Single Wiegand channel with PC817 optocoupler isolation.

    The optocoupler INVERTS the signal:
    - Reader idle (HIGH): LED on -> transistor on -> GPIO LOW
    - Reader pulse (LOW): LED off -> transistor off -> GPIO HIGH

    Firmware must detect RISING edges instead of falling edges.

    Components:
    - Ux: PC817 optocoupler
    - Rx_in: 390 ohm input resistor (LED current limit)
    - Rx_pu: 10K pull-up resistor on output
    """
    # Optocoupler
    opto = optocoupler_template()
    opto.ref = f"U{ref_prefix}"

    # Input current limiting resistor
    # I = (5V - 1.2V) / 390 = ~10mA
    r_in = resistor_template(value="390R")
    r_in.ref = f"R{ref_prefix}"

    # Pull-up resistor on output
    r_pu = resistor_template(value="10K")
    r_pu.ref = f"R{ref_prefix + 2}"  # R3/R4 for pull-ups

    # LED side (pins 1=Anode, 2=Cathode on PC817)
    # Reader D0/D1 -> resistor -> LED anode
    # LED cathode -> reader GND
    data_in_net += r_in[1]
    r_in[2] += opto[1]  # Anode
    reader_gnd_net += opto[2]  # Cathode

    # Phototransistor side (pins 3=Emitter, 4=Collector on PC817)
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

    Components:
    - Q1: 2N2222 NPN transistor
    - R5: 1K base resistor
    - D2: 1N4007 flyback diode (cathode to +12V)
    """
    # NPN transistor
    q1 = npn_template()
    q1.ref = "Q1"

    # Base resistor
    # I_base = (3.3V - 0.7V) / 1K = 2.6mA
    # With hFE >= 75, I_collector_max = 195mA (plenty for relay coil)
    r_base = resistor_template(value="1K")
    r_base.ref = "R5"

    # Flyback diode
    d_flyback = diode_template(value="1N4007")
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
    c5 = capacitor_template(value="100nF")
    c5.ref = "C5"
    c6 = capacitor_template(value="10uF")
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

    print("\n" + "=" * 60)
    print("PCB Design Generation Complete!")
    print("=" * 60)
    print("\nOutput files:")
    print("  - output/access_controller.net  (KiCad netlist)")
    print("  - output/access_controller.xml  (Bill of Materials)")
    print("\nNext steps:")
    print("  1. Open KiCad and create a new project")
    print("  2. Open PCB Editor (Pcbnew)")
    print("  3. File -> Import -> Netlist...")
    print("  4. Select access_controller.net and click 'Update PCB'")
    print("  5. Arrange components and route traces")
    print("  6. Run DRC, then generate Gerbers")
    print("\nNOTE: The Wiegand signals are INVERTED by the optocouplers.")
    print("      Update firmware to detect RISING edges instead of falling.")
    print("=" * 60)


if __name__ == "__main__":
    create_access_controller()
