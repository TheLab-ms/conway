#!/usr/bin/env python3
"""
ESP32 Door Access Controller PCB - pcbflow Design

Generates complete PCB layout with Gerber output for JLCPCB.
This replaces the previous SKiDL-based design that only generated netlists.

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
from pathlib import Path
from pcbflow import *


# =============================================================================
# JLCPCB PART NUMBERS
# =============================================================================

LCSC_PARTS = {
    "R_390R_0805": "C17630",
    "R_1K_0805": "C17513",
    "R_10K_0805": "C17414",
    "C_100nF_0805": "C49678",
    "C_10uF_0805": "C15850",
    "C_22uF_25V_0805": "C45783",
    "U_AMS1117-3.3": "C6186",
    "U_EL817": "C63268",
    "Q_SS8050": "C2150",
    "D_SS34": "C8678",
    "D_M7": "C95872",
}


# =============================================================================
# CUSTOM FOOTPRINTS
# =============================================================================

class D_SMA(PCBPart):
    """
    SMA diode package for SS34 and M7 diodes.
    Pad ordering: [0]=Cathode (K), [1]=Anode (A)
    """
    def __init__(self, *args, **kwargs):
        self.family = "D"
        self.footprint = "D_SMA"
        super().__init__(*args, **kwargs)

    def place(self, dc):
        self.chamfered(dc, 4.3, 2.8)
        dc.push()
        dc.goxy(-2.3, 0)
        dc.rect(1.6, 2.2)
        self.smd_pad(dc)
        dc.pop()
        dc.push()
        dc.goxy(2.3, 0)
        dc.rect(1.6, 2.2)
        self.smd_pad(dc)
        dc.pop()


class SOP4(PCBPart):
    """
    SOP-4 optocoupler package (EL817 compatible).
    Pad ordering: [0]=Pin1/Anode, [1]=Pin2/Cathode, [2]=Pin3/Emitter, [3]=Pin4/Collector
    """
    def __init__(self, *args, **kwargs):
        self.family = "U"
        self.footprint = "SOP-4"
        super().__init__(*args, **kwargs)

    def place(self, dc):
        self.chamfered(dc, 4.4, 2.6)
        for pos in [(-0.635, -2.0), (0.635, -2.0), (0.635, 2.0), (-0.635, 2.0)]:
            dc.push()
            dc.goxy(*pos)
            dc.rect(0.6, 1.7)
            self.smd_pad(dc)
            dc.pop()


class ScrewTerminal2(PCBPart):
    """2-pin screw terminal, 5mm pitch. Pads: [0]=Pin1, [1]=Pin2"""
    def __init__(self, *args, **kwargs):
        self.family = "J"
        self.footprint = "ScrewTerminal_1x02_P5.00mm"
        super().__init__(*args, **kwargs)

    def place(self, dc):
        dc.push()
        dc.rect(11, 8)
        dc.silk(side=self.side)
        dc.pop()
        for i in range(2):
            dc.push()
            dc.goxy((i - 0.5) * 5.0, 0)
            dc.board.add_drill(dc.xy, 1.3)
            p = dc.copy()
            p.n_agon(2.0, 8)
            p.pin_pad()
            self.pads.append(p)
            dc.pop()


class ScrewTerminal4(PCBPart):
    """4-pin screw terminal, 5mm pitch. Pads: [0]-[3] = Pin1-Pin4"""
    def __init__(self, *args, **kwargs):
        self.family = "J"
        self.footprint = "ScrewTerminal_1x04_P5.00mm"
        super().__init__(*args, **kwargs)

    def place(self, dc):
        dc.push()
        dc.rect(21, 8)
        dc.silk(side=self.side)
        dc.pop()
        for i in range(4):
            dc.push()
            dc.goxy((i - 1.5) * 5.0, 0)
            dc.board.add_drill(dc.xy, 1.3)
            p = dc.copy()
            p.n_agon(2.0, 8)
            p.pin_pad()
            self.pads.append(p)
            dc.pop()


class PinHeader_1x19(PCBPart):
    """1x19 pin header, 2.54mm pitch. Pads: [0]-[18] = Pin1-Pin19"""
    def __init__(self, *args, **kwargs):
        self.family = "J"
        self.footprint = "PinSocket_1x19_P2.54mm"
        super().__init__(*args, **kwargs)

    def place(self, dc):
        total = 18 * 2.54
        dc.push()
        dc.rect(2.54, total + 2.54)
        dc.silk(side=self.side)
        dc.pop()
        for i in range(19):
            dc.push()
            dc.goxy(0, (i - 9) * 2.54)
            dc.board.add_drill(dc.xy, 1.0)
            p = dc.copy()
            p.n_agon(1.7, 8)
            p.pin_pad()
            self.pads.append(p)
            dc.pop()


# =============================================================================
# BOARD CREATION
# =============================================================================

def create_board():
    """Create and configure the PCB board."""
    brd = Board((65, 50))
    brd.drc.trace_width = 0.2
    brd.drc.clearance = 0.2
    brd.drc.via_drill = 0.3
    brd.drc.via_annular_ring = 0.15
    brd.drc.hole_clearance = 0.25
    brd.drc.outline_clearance = 0.3
    brd.drc.soldermask_margin = 0.05
    return brd


# =============================================================================
# COMPONENT PLACEMENT
# =============================================================================

def place_components(brd):
    """Place all components on the board."""
    parts = {}

    # Power section
    parts["J1"] = ScrewTerminal2(brd.DC((8, 45)), val="12V_IN", side="top")
    parts["D1"] = D_SMA(brd.DC((18, 45)), val="SS34", side="top")
    parts["U1"] = SOT223(brd.DC((28, 43)).right(90), val="AMS1117-3.3", side="top")
    parts["C1"] = C0805(brd.DC((20, 38)), val="22uF", side="top")
    parts["C1B"] = C0805(brd.DC((24, 38)), val="22uF", side="top")
    parts["C2"] = C0805(brd.DC((28, 38)), val="100nF", side="top")
    parts["C3"] = C0805(brd.DC((34, 43)), val="22uF", side="top")
    parts["C3B"] = C0805(brd.DC((38, 43)), val="22uF", side="top")
    parts["C4"] = C0805(brd.DC((36, 38)), val="100nF", side="top")

    # Wiegand section
    parts["J2"] = ScrewTerminal4(brd.DC((50, 47)), val="WIEGAND", side="top")
    parts["U2"] = SOP4(brd.DC((42, 38)), val="EL817", side="top")
    parts["R2"] = R0805(brd.DC((42, 32)).right(90), val="390R", side="top")
    parts["R4"] = R0805(brd.DC((46, 32)).right(90), val="10K", side="top")
    parts["U3"] = SOP4(brd.DC((54, 38)), val="EL817", side="top")
    parts["R3"] = R0805(brd.DC((54, 32)).right(90), val="390R", side="top")
    parts["R5"] = R0805(brd.DC((58, 32)).right(90), val="10K", side="top")

    # ESP32 socket
    parts["J4"] = PinHeader_1x19(brd.DC((9, 25)), val="ESP32_L", side="top")
    parts["J5"] = PinHeader_1x19(brd.DC((56, 25)), val="ESP32_R", side="top")
    parts["C5"] = C0805(brd.DC((14, 4)), val="100nF", side="top")
    parts["C6"] = C0805(brd.DC((18, 4)), val="10uF", side="top")

    # Relay driver
    parts["J3"] = ScrewTerminal2(brd.DC((8, 5)), val="RELAY", side="top")
    parts["Q1"] = SOT23(brd.DC((18, 8)), val="SS8050", side="top")
    parts["R6"] = R0805(brd.DC((24, 8)), val="1K", side="top")
    parts["D2"] = D_SMA(brd.DC((18, 14)).right(180), val="M7", side="top")

    return parts


# =============================================================================
# ROUTING
# =============================================================================

def wire(brd, points, width=0.25, layer="GTL"):
    """Create a wire through a list of points."""
    if len(points) < 2:
        return
    dc = brd.DC(points[0])
    dc.newpath()
    for p in points[1:]:
        dc.goxy(p[0] - dc.xy[0], p[1] - dc.xy[1])
    dc.wire(layer=layer, width=width)


def via(brd, xy):
    """Add a via at location."""
    dc = brd.DC(xy)
    dc.via(connect="GND")


def route_all(brd, parts):
    """Route all connections."""

    # === 12V Power Path ===
    # J1.1 -> D1.A (anode)
    j1_1 = parts["J1"].pads[0].xy
    d1_a = parts["D1"].pads[1].xy
    wire(brd, [j1_1, (j1_1[0]+3, j1_1[1]), (d1_a[0], j1_1[1]), d1_a], width=0.5)

    # D1.K -> U1.VIN (SOT223: pads[3]=VIN)
    d1_k = parts["D1"].pads[0].xy
    u1_vin = parts["U1"].pads[3].xy
    wire(brd, [d1_k, (d1_k[0]-3, d1_k[1]), (d1_k[0]-3, u1_vin[1]), u1_vin], width=0.5)

    # 12V rail to input caps
    wire(brd, [parts["C1"].pads[0].xy, (parts["C1"].pads[0].xy[0], 41), (d1_k[0]-3, 41)], width=0.5)
    wire(brd, [parts["C1B"].pads[0].xy, (parts["C1B"].pads[0].xy[0], 41)], width=0.5)
    wire(brd, [parts["C2"].pads[0].xy, (parts["C2"].pads[0].xy[0], 41)], width=0.5)

    # 12V to J2.3 (Wiegand reader power)
    j2_3 = parts["J2"].pads[2].xy
    wire(brd, [d1_k, (d1_k[0], 49), (j2_3[0], 49), j2_3], width=0.5)

    # 12V to D2.K and J3.2 (via bottom layer)
    d2_k = parts["D2"].pads[0].xy
    j3_2 = parts["J3"].pads[1].xy
    via(brd, (d1_k[0]-6, d1_k[1]-3))
    wire(brd, [(d1_k[0]-6, d1_k[1]-3), (d1_k[0]-6, 16), (d2_k[0], 16), d2_k], width=0.5, layer="GBL")
    wire(brd, [d2_k, (d2_k[0], j3_2[1]), j3_2], width=0.5)

    # === 3.3V Power Path ===
    # U1.VOUT (pads[2]) to output caps
    u1_vout = parts["U1"].pads[2].xy
    wire(brd, [u1_vout, (u1_vout[0], 41)], width=0.5)
    wire(brd, [parts["C3"].pads[0].xy, (parts["C3"].pads[0].xy[0], 41)], width=0.5)
    wire(brd, [parts["C3B"].pads[0].xy, (parts["C3B"].pads[0].xy[0], 41)], width=0.5)
    wire(brd, [parts["C4"].pads[0].xy, (parts["C4"].pads[0].xy[0], 41)], width=0.5)

    # 3.3V to ESP32 (J4.1 = pads[0]) via bottom layer
    j4_1 = parts["J4"].pads[0].xy
    via(brd, (u1_vout[0]+5, 41))
    wire(brd, [(u1_vout[0]+5, 41), (u1_vout[0]+5, j4_1[1]-5), (j4_1[0]+3, j4_1[1]-5)], width=0.5, layer="GBL")
    via(brd, (j4_1[0]+3, j4_1[1]-5))
    wire(brd, [(j4_1[0]+3, j4_1[1]-5), (j4_1[0]+3, j4_1[1]), j4_1], width=0.5)

    # 3.3V to decoupling caps
    c5_1 = parts["C5"].pads[0].xy
    c6_1 = parts["C6"].pads[0].xy
    wire(brd, [(j4_1[0]+3, j4_1[1]-5), (c5_1[0], j4_1[1]-5), c5_1], width=0.5)
    wire(brd, [c5_1, (c6_1[0], c5_1[1]), c6_1], width=0.5)

    # 3.3V to pullups R4, R5 (via bottom layer)
    r4_vcc = parts["R4"].pads[1].xy
    r5_vcc = parts["R5"].pads[1].xy
    wire(brd, [(u1_vout[0]+5, 41), (r4_vcc[0], 41), (r4_vcc[0], r4_vcc[1]-1)], width=0.3, layer="GBL")
    via(brd, (r4_vcc[0], r4_vcc[1]-1))
    wire(brd, [(r4_vcc[0], r4_vcc[1]-1), r4_vcc], width=0.3)
    wire(brd, [(r4_vcc[0], 41), (r5_vcc[0], 41), (r5_vcc[0], r5_vcc[1]-1)], width=0.3, layer="GBL")
    via(brd, (r5_vcc[0], r5_vcc[1]-1))
    wire(brd, [(r5_vcc[0], r5_vcc[1]-1), r5_vcc], width=0.3)

    # === Ground Connections (vias to bottom pour) ===
    for ref in ["C1", "C1B", "C2", "C3", "C3B", "C4", "C5", "C6"]:
        via(brd, parts[ref].pads[1].xy)
    via(brd, parts["U1"].pads[1].xy)  # U1 GND
    via(brd, parts["J1"].pads[1].xy)  # J1 GND
    via(brd, parts["J2"].pads[3].xy)  # J2 GND
    via(brd, parts["U2"].pads[1].xy)  # U2 cathode
    via(brd, parts["U2"].pads[2].xy)  # U2 emitter
    via(brd, parts["U3"].pads[1].xy)  # U3 cathode
    via(brd, parts["U3"].pads[2].xy)  # U3 emitter
    via(brd, parts["J4"].pads[13].xy)  # ESP32 GND (pin 14)
    via(brd, parts["J5"].pads[0].xy)   # ESP32 GND (pin 1)
    via(brd, parts["Q1"].pads[1].xy)   # Q1 emitter

    # === Wiegand D0 Path ===
    # J2.1 -> R2 -> U2.1 (anode)
    j2_d0 = parts["J2"].pads[0].xy
    r2_in = parts["R2"].pads[0].xy
    r2_out = parts["R2"].pads[1].xy
    u2_anode = parts["U2"].pads[0].xy
    wire(brd, [j2_d0, (j2_d0[0], r2_in[1]-5), (r2_in[0], r2_in[1]-5), r2_in], width=0.25)
    wire(brd, [r2_out, (r2_out[0], u2_anode[1]), u2_anode], width=0.25)

    # U2.4 (collector) -> R4.1 (pullup) -> J4.9 (GPIO25)
    u2_col = parts["U2"].pads[3].xy
    r4_sig = parts["R4"].pads[0].xy
    j4_gpio25 = parts["J4"].pads[8].xy
    wire(brd, [u2_col, (u2_col[0]+2, u2_col[1]), (r4_sig[0], u2_col[1]), r4_sig], width=0.25)
    via(brd, (u2_col[0]-5, u2_col[1]))
    wire(brd, [(u2_col[0]-5, u2_col[1]), (u2_col[0]-5, j4_gpio25[1]), (j4_gpio25[0]+3, j4_gpio25[1])], width=0.25, layer="GBL")
    via(brd, (j4_gpio25[0]+3, j4_gpio25[1]))
    wire(brd, [(j4_gpio25[0]+3, j4_gpio25[1]), j4_gpio25], width=0.25)

    # === Wiegand D1 Path ===
    # J2.2 -> R3 -> U3.1 (anode)
    j2_d1 = parts["J2"].pads[1].xy
    r3_in = parts["R3"].pads[0].xy
    r3_out = parts["R3"].pads[1].xy
    u3_anode = parts["U3"].pads[0].xy
    wire(brd, [j2_d1, (j2_d1[0], r3_in[1]-3), (r3_in[0], r3_in[1]-3), r3_in], width=0.25)
    wire(brd, [r3_out, (r3_out[0], u3_anode[1]), u3_anode], width=0.25)

    # U3.4 (collector) -> R5.1 (pullup) -> J4.8 (GPIO33)
    u3_col = parts["U3"].pads[3].xy
    r5_sig = parts["R5"].pads[0].xy
    j4_gpio33 = parts["J4"].pads[7].xy
    wire(brd, [u3_col, (u3_col[0]+2, u3_col[1]), (r5_sig[0], u3_col[1]), r5_sig], width=0.25)
    via(brd, (u3_col[0]-3, u3_col[1]+2))
    wire(brd, [(u3_col[0]-3, u3_col[1]+2), (u3_col[0]-3, j4_gpio33[1]+2), (j4_gpio33[0]+5, j4_gpio33[1]+2)], width=0.25, layer="GBL")
    via(brd, (j4_gpio33[0]+5, j4_gpio33[1]+2))
    wire(brd, [(j4_gpio33[0]+5, j4_gpio33[1]+2), (j4_gpio33[0]+5, j4_gpio33[1]), j4_gpio33], width=0.25)

    # === Relay Control Path ===
    # J4.7 (GPIO32) -> R6 -> Q1.B -> Q1.C -> D2.A -> J3.1
    j4_gpio32 = parts["J4"].pads[6].xy
    r6_in = parts["R6"].pads[0].xy
    r6_out = parts["R6"].pads[1].xy
    q1_base = parts["Q1"].pads[0].xy
    q1_col = parts["Q1"].pads[2].xy
    d2_a = parts["D2"].pads[1].xy
    j3_1 = parts["J3"].pads[0].xy

    via(brd, (j4_gpio32[0]+5, j4_gpio32[1]))
    wire(brd, [(j4_gpio32[0]+5, j4_gpio32[1]), (r6_in[0], j4_gpio32[1]), (r6_in[0], r6_in[1])], width=0.25, layer="GBL")
    via(brd, (r6_in[0], r6_in[1]))
    wire(brd, [r6_out, (q1_base[0], r6_out[1]), q1_base], width=0.25)
    wire(brd, [q1_col, (q1_col[0], d2_a[1]), d2_a], width=0.5)
    wire(brd, [q1_col, (q1_col[0], j3_1[1]), j3_1], width=0.5)


# =============================================================================
# JLCPCB OUTPUT GENERATION
# =============================================================================

def generate_jlcpcb_bom(parts, filename):
    """Generate JLCPCB-compatible BOM CSV file."""
    bom = {}
    manual = []
    lcsc = {"390R": "C17630", "1K": "C17513", "10K": "C17414",
            "100nF": "C49678", "10uF": "C15850", "22uF": "C45783",
            "SS34": "C8678", "M7": "C95872", "AMS1117-3.3": "C6186",
            "EL817": "C63268", "SS8050": "C2150"}

    for ref, part in parts.items():
        val = part.val if hasattr(part, 'val') else ""
        pn = lcsc.get(val, "")
        if not pn:
            manual.append(ref)
            continue
        fp = part.footprint if hasattr(part, 'footprint') else ""
        if pn not in bom:
            bom[pn] = {"Comment": val, "Designator": [], "Footprint": fp, "LCSC": pn}
        bom[pn]["Designator"].append(ref)

    with open(filename, 'w', newline='') as f:
        w = csv.writer(f)
        w.writerow(["Comment", "Designator", "Footprint", "LCSC Part #"])
        for e in sorted(bom.values(), key=lambda x: x["Designator"][0]):
            w.writerow([e["Comment"], ",".join(sorted(e["Designator"])), e["Footprint"], e["LCSC"]])

    print(f"Generated JLCPCB BOM: {filename}")
    if manual:
        print(f"  Manual assembly: {', '.join(sorted(manual))}")


def generate_jlcpcb_cpl(parts, filename):
    """Generate JLCPCB Component Placement List."""
    with open(filename, 'w', newline='') as f:
        w = csv.writer(f)
        w.writerow(["Designator", "Mid X", "Mid Y", "Rotation", "Layer"])
        for ref, part in sorted(parts.items()):
            if ref.startswith("J"):
                continue
            x, y = part.center.xy
            w.writerow([ref, f"{x:.3f}", f"{y:.3f}", f"{part.center.dir:.1f}",
                       "top" if part.side == "top" else "bottom"])
    print(f"Generated JLCPCB CPL: {filename}")


# =============================================================================
# MAIN
# =============================================================================

def main():
    """Generate complete PCB design with Gerber output."""
    import os
    script_dir = Path(__file__).parent.resolve()
    os.chdir(script_dir)
    output_dir = Path("output")
    output_dir.mkdir(exist_ok=True)

    print("=" * 70)
    print("ESP32 Door Access Controller PCB Design")
    print("Using pcbflow for layout and Gerber generation")
    print("=" * 70)

    print("\n[1/5] Creating board (65mm x 50mm)...")
    brd = create_board()

    print("[2/5] Placing components...")
    parts = place_components(brd)
    print(f"       Placed {len(parts)} components")

    print("[3/5] Routing traces...")
    route_all(brd, parts)

    print("[4/5] Adding board outline...")
    brd.add_outline()

    print("[5/5] Generating output files...")
    brd.save("output/access_controller_pcb", in_subdir=False, gerber=True, pdf=True, bom=False)
    generate_jlcpcb_bom(parts, "output/access_controller_jlcpcb_bom.csv")
    generate_jlcpcb_cpl(parts, "output/access_controller_jlcpcb_cpl.csv")

    print("\n" + "=" * 70)
    print("PCB Design Generation Complete!")
    print("=" * 70)
    print("\nOutput files in output/:")
    print("  - access_controller_pcb_*.GBR  (Gerber files)")
    print("  - access_controller_pcb_*.DRL  (Drill files)")
    print("  - access_controller_pcb.pdf    (Preview)")
    print("  - access_controller_jlcpcb_bom.csv")
    print("  - access_controller_jlcpcb_cpl.csv")
    print("\nJLCPCB Upload Instructions:")
    print("  1. ZIP all *_pcb_* files and upload to jlcpcb.com")
    print("  2. Select 'SMT Assembly' and upload BOM and CPL files")
    print("  3. Review component placement and confirm order")
    print("\nManual Assembly Required (after delivery):")
    print("  - J1, J2, J3: Screw terminals")
    print("  - J4, J5: ESP32 socket headers")
    print("=" * 70)


if __name__ == "__main__":
    main()
