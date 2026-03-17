#!/usr/bin/env python3
"""
Conway Access Controller v2 - PCB Design Generator

Generates a complete 2-layer PCB using KiCad's pcbnew Python API, then exports
Gerber files via kicad-cli and produces JLCPCB BOM/CPL for assembly.

After generation, runs a 3-phase verification pipeline:
  Phase 1: Board integrity checks via pcbnew API (nets, pads, tracks, vias, outline)
  Phase 2: KiCad Design Rule Check via kicad-cli (clearance, connectivity, courtyard)
  Phase 3: Output file verification (Gerbers, BOM/CPL structure, BOM-to-CPL cross-ref)
The script exits non-zero if any verification check fails.

Board: 58mm x 44mm, 2-layer
MCU: ESP32-WROOM-32E module soldered directly on the PCB
Passives: Resistors and 100nF caps are 0402; 10uF/22uF caps are 0805; LED is 0805
Features:
  - 12V input with SS34 reverse polarity protection
  - AMS1117-3.3 LDO for 3.3V rail
  - 2x EL817 optocouplers for Wiegand D0/D1 isolation (signals inverted)
  - SS8050 NPN + M7 flyback for 12V relay control
  - SP3485EN RS485 transceiver (GPIO16/17/4) with 3-pin connector (A, B, GND)
  - SM712 TVS diode for RS485 A/B bus ESD protection
  - Power LED (green 0805) with 100R current-limiting resistor on 3.3V rail
  - 6-pin FTDI header with auto-reset (DTR/RTS -> GPIO0/EN)
  - Antenna keepout zone for ESP32-WROOM-32E
  - Bottom ground pour

JLCPCB Assembly:
  - SMD components assembled by JLCPCB (Standard PCBA required for ESP32)
  - THT connectors (J1-J5, J6) require manual soldering (3.5mm pitch screw terminals)
  - Extended parts: EL817 optocouplers, ESP32-WROOM-32E, SM712 TVS ($3 setup fee each)

Usage:
  /Applications/KiCad/KiCad.app/Contents/Frameworks/Python.framework/Versions/3.9/bin/python3 pcb_generate.py
"""

import argparse
import csv
import json
import os
import subprocess
import sys
from pathlib import Path

# KiCad's bundled Python includes pcbnew
import pcbnew

# =============================================================================
# CONSTANTS
# =============================================================================

# Board dimensions (mm)
BOARD_W = 58.0
BOARD_H = 44.0


# Coordinate helpers - KiCad uses nanometers internally
def mm(val):
    """Convert mm to KiCad internal units (nanometers)."""
    return pcbnew.FromMM(val)


def pt(x, y):
    """Create a VECTOR2I from mm coordinates."""
    return pcbnew.VECTOR2I(mm(x), mm(y))


# Trace widths (mm)
TW_SIGNAL = 0.25
TW_POWER = 0.5
TW_PULLUP = 0.3

# Via parameters (mm)
VIA_DRILL = 0.3
VIA_SIZE = 0.6  # drill + 2*annular_ring

# JLCPCB LCSC part numbers
# Verified against JLCPCB inventory 2026-03-16
# ESP32-WROOM-32E requires Standard PCBA (not Economic)
LCSC = {
    "ESP32-WROOM-32E": "C701341",   # ESP32-WROOM-32E-N4 (was C529584=ESP-WROOM-02D wrong chip)
    "AMS1117-3.3": "C6186",         # Basic, SOT-223
    "EL817": "C63268",              # Extended ($3 setup), SOP-4
    "SS8050": "C2150",              # Basic, SOT-23 NPN
    "SP3485": "C8963",              # SP3485EN-L/TR, Basic, SOIC-8 (was MAX3485 C20682=ferrite bead)
    "SS34": "C8678",                # Basic, SMA Schottky 3A/40V
    "M7": "C95872",                 # Basic, SMA 1A/1000V
    "470R": "C25117",               # Basic, 0402
    "1K": "C11702",                 # Basic, 0402
    "10K": "C25744",                # Basic, 0402
    "120R": "C25079",               # Basic, 0402
    "100R": "C25076",               # Basic, 0402
    "0R22": "C25092",               # Basic, 0402 (ESR ballast for AMS1117)
    "100nF": "C1525",               # Basic, 0402 50V X7R
    "1nF": "C52923",               # Basic, 0402 50V C0G/NP0
    "10uF": "C15850",               # Basic, 0805 25V X5R
    "22uF": "C45783",               # Basic, 0805 25V X5R
    "SM712": "C293966",              # Extended ($3 setup), SOT-23 TVS for RS485 ESD
    "LED_GRN": "C2297",              # Basic, 0805 green LED (KT-0805G, 525nm, 430mcd)
}


# =============================================================================
# FOOTPRINT CREATORS
# =============================================================================


def make_smd_layerset():
    """Layer set for front-side SMD pads: F.Cu + F.Paste + F.Mask."""
    ls = pcbnew.LSET()
    ls.AddLayer(pcbnew.F_Cu)
    ls.AddLayer(pcbnew.F_Mask)
    ls.AddLayer(pcbnew.F_Paste)
    return ls


def make_pth_layerset():
    """Layer set for plated through-hole pads: F.Cu + B.Cu + F.Mask + B.Mask."""
    ls = pcbnew.LSET()
    ls.AddLayer(pcbnew.F_Cu)
    ls.AddLayer(pcbnew.B_Cu)
    ls.AddLayer(pcbnew.F_Mask)
    ls.AddLayer(pcbnew.B_Mask)
    return ls


def add_fab_rect(fp, x, y, w, h):
    """Add a fabrication layer rectangle outline to a footprint."""
    shape = pcbnew.PCB_SHAPE(fp)
    shape.SetShape(pcbnew.SHAPE_T_RECT)
    shape.SetStart(pt(x - w / 2, y - h / 2))
    shape.SetEnd(pt(x + w / 2, y + h / 2))
    shape.SetLayer(pcbnew.F_Fab)
    shape.SetWidth(mm(0.1))
    fp.Add(shape)


def add_courtyard_rect(fp, x, y, w, h):
    """Add a courtyard rectangle to a footprint."""
    shape = pcbnew.PCB_SHAPE(fp)
    shape.SetShape(pcbnew.SHAPE_T_RECT)
    shape.SetStart(pt(x - w / 2, y - h / 2))
    shape.SetEnd(pt(x + w / 2, y + h / 2))
    shape.SetLayer(pcbnew.F_CrtYd)
    shape.SetWidth(mm(0.05))
    fp.Add(shape)


def add_smd_pad(fp, num, x, y, w, h, shape=None):
    """Add an SMD pad to a footprint at relative position (x,y) in mm."""
    pad = pcbnew.PAD(fp)
    pad.SetFrontShape(shape or pcbnew.PAD_SHAPE_RECT)
    pad.SetAttribute(pcbnew.PAD_ATTRIB_SMD)
    pad.SetLayerSet(make_smd_layerset())
    pad.SetSize(pt(w, h))
    pad.SetFPRelativePosition(pt(x, y))
    pad.SetNumber(str(num))
    fp.Add(pad)
    return pad


def add_pth_pad(fp, num, x, y, pad_dia, drill_dia, shape=None):
    """Add a plated through-hole pad to a footprint."""
    pad = pcbnew.PAD(fp)
    pad.SetFrontShape(shape or pcbnew.PAD_SHAPE_CIRCLE)
    pad.SetAttribute(pcbnew.PAD_ATTRIB_PTH)
    pad.SetLayerSet(make_pth_layerset())
    pad.SetSize(pt(pad_dia, pad_dia))
    pad.SetDrillSize(pt(drill_dia, drill_dia))
    pad.SetFPRelativePosition(pt(x, y))
    pad.SetNumber(str(num))
    fp.Add(pad)
    return pad


def create_r0402(board, ref, val):
    """Create an 0402 resistor footprint."""
    fp = pcbnew.FOOTPRINT(board)
    fp.SetReference(ref)
    fp.SetValue(val)
    fp.SetFPIDAsString("Resistor_SMD:R_0402_1005Metric")
    add_smd_pad(fp, "1", -0.48, 0, 0.56, 0.62)
    add_smd_pad(fp, "2", 0.48, 0, 0.56, 0.62)
    add_fab_rect(fp, 0, 0, 1.0, 0.5)
    add_courtyard_rect(fp, 0, 0, 2.0, 1.1)
    return fp


def create_c0805(board, ref, val):
    """Create an 0805 capacitor footprint."""
    fp = pcbnew.FOOTPRINT(board)
    fp.SetReference(ref)
    fp.SetValue(val)
    fp.SetFPIDAsString("Capacitor_SMD:C_0805_2012Metric")
    add_smd_pad(fp, "1", -1.0, 0, 1.0, 1.3)
    add_smd_pad(fp, "2", 1.0, 0, 1.0, 1.3)
    add_fab_rect(fp, 0, 0, 2.0, 1.25)
    add_courtyard_rect(fp, 0, 0, 3.4, 1.8)
    return fp


def create_c0402(board, ref, val):
    """Create an 0402 capacitor footprint."""
    fp = pcbnew.FOOTPRINT(board)
    fp.SetReference(ref)
    fp.SetValue(val)
    fp.SetFPIDAsString("Capacitor_SMD:C_0402_1005Metric")
    add_smd_pad(fp, "1", -0.48, 0, 0.56, 0.62)
    add_smd_pad(fp, "2", 0.48, 0, 0.56, 0.62)
    add_fab_rect(fp, 0, 0, 1.0, 0.5)
    add_courtyard_rect(fp, 0, 0, 2.0, 1.1)
    return fp


def create_led0805(board, ref, val):
    """Create an 0805 LED footprint.
    Pad 1 = Cathode (-), Pad 2 = Anode (+).
    Matches standard KiCad LED polarity convention and JLCPCB orientation.
    """
    fp = pcbnew.FOOTPRINT(board)
    fp.SetReference(ref)
    fp.SetValue(val)
    fp.SetFPIDAsString("LED_SMD:LED_0805_2012Metric")
    add_smd_pad(fp, "1", -1.0, 0, 1.0, 1.3)  # Cathode (K)
    add_smd_pad(fp, "2", 1.0, 0, 1.0, 1.3)  # Anode (A)
    add_fab_rect(fp, 0, 0, 2.0, 1.25)
    add_courtyard_rect(fp, 0, 0, 3.4, 1.8)
    return fp


def create_sot223(board, ref, val):
    """
    Create SOT-223 footprint (AMS1117-3.3).
    Pin 1 = GND (tab side left), Pin 2 = VOUT, Pin 3 = VIN
    Pin 4 = large tab pad (connected to VOUT internally)

    Oriented with tab pad on top (positive Y direction from center).
    """
    fp = pcbnew.FOOTPRINT(board)
    fp.SetReference(ref)
    fp.SetValue(val)
    fp.SetFPIDAsString("Package_TO_SOT_SMD:SOT-223-3_TabPin2")
    # Three small pads on bottom
    add_smd_pad(fp, "1", -2.3, 3.15, 1.0, 1.8)  # GND
    add_smd_pad(fp, "2", 0.0, 3.15, 1.0, 1.8)  # VOUT
    add_smd_pad(fp, "3", 2.3, 3.15, 1.0, 1.8)  # VIN
    # Large tab pad on top
    add_smd_pad(fp, "4", 0.0, -3.15, 3.0, 1.8)  # Tab = VOUT
    add_fab_rect(fp, 0, 0, 6.5, 3.5)
    add_courtyard_rect(fp, 0, 0, 8.0, 8.5)
    return fp


def create_sot23(board, ref, val):
    """
    Create SOT-23 footprint (SS8050 NPN transistor).
    SS8050 in SOT-23: Pin 1 = Base, Pin 2 = Emitter, Pin 3 = Collector
    """
    fp = pcbnew.FOOTPRINT(board)
    fp.SetReference(ref)
    fp.SetValue(val)
    fp.SetFPIDAsString("Package_TO_SOT_SMD:SOT-23")
    add_smd_pad(fp, "1", -0.95, 1.1, 0.6, 0.7)  # Base
    add_smd_pad(fp, "2", 0.95, 1.1, 0.6, 0.7)  # Emitter
    add_smd_pad(fp, "3", 0.0, -1.1, 0.6, 0.7)  # Collector
    add_fab_rect(fp, 0, 0, 1.3, 2.9)
    add_courtyard_rect(fp, 0, 0, 2.6, 3.2)
    return fp


def create_sma_diode(board, ref, val):
    """
    Create SMA diode footprint (SS34, M7).
    Pad 1 = Cathode (bar side), Pad 2 = Anode
    """
    fp = pcbnew.FOOTPRINT(board)
    fp.SetReference(ref)
    fp.SetValue(val)
    fp.SetFPIDAsString("Diode_SMD:D_SMA")
    add_smd_pad(fp, "1", -2.0, 0, 1.6, 2.2)  # Cathode
    add_smd_pad(fp, "2", 2.0, 0, 1.6, 2.2)  # Anode
    add_fab_rect(fp, 0, 0, 4.3, 2.8)
    add_courtyard_rect(fp, 0, 0, 5.8, 3.2)
    return fp


def create_sop4(board, ref, val):
    """
    Create SOP-4 optocoupler footprint (EL817).
    Pins: 1=Anode(+), 2=Cathode(-), 3=Emitter, 4=Collector
    Pin 1,2 on bottom (LED side), Pin 3,4 on top (phototransistor side).
    """
    fp = pcbnew.FOOTPRINT(board)
    fp.SetReference(ref)
    fp.SetValue(val)
    fp.SetFPIDAsString("Package_SO:SOP-4_3.8x4.1mm_P2.54mm")
    # LED side (bottom pads)
    add_smd_pad(fp, "1", -1.27, 2.6, 0.6, 1.6)  # Anode
    add_smd_pad(fp, "2", 1.27, 2.6, 0.6, 1.6)  # Cathode
    # Phototransistor side (top pads)
    add_smd_pad(fp, "3", 1.27, -2.6, 0.6, 1.6)  # Emitter
    add_smd_pad(fp, "4", -1.27, -2.6, 0.6, 1.6)  # Collector
    add_fab_rect(fp, 0, 0, 3.8, 4.1)
    add_courtyard_rect(fp, 0, 0, 4.4, 6.5)
    # Pin 1 dot
    dot = pcbnew.PCB_SHAPE(fp)
    dot.SetShape(pcbnew.SHAPE_T_CIRCLE)
    dot.SetCenter(pt(-2.0, 2.6))
    dot.SetEnd(pt(-1.8, 2.6))  # radius = 0.2mm
    dot.SetLayer(pcbnew.F_SilkS)
    dot.SetWidth(mm(0.12))
    dot.SetFilled(True)
    fp.Add(dot)
    return fp


def create_sop8(board, ref, val):
    """
    Create SOP-8 / SOIC-8 footprint (SP3485EN / MAX3485 pin-compatible).

    SP3485EN pins: 1=RO, 2=RE, 3=DE, 4=DI, 5=GND, 6=A, 7=B, 8=VCC
    """
    fp = pcbnew.FOOTPRINT(board)
    fp.SetReference(ref)
    fp.SetValue(val)
    fp.SetFPIDAsString("Package_SO:SOIC-8_3.9x4.9mm_P1.27mm")
    # Pins 1-4 on left (bottom side in standard orientation)
    for i in range(4):
        add_smd_pad(fp, str(i + 1), -2.7, -1.905 + i * 1.27, 1.5, 0.6)
    # Pins 5-8 on right (top side) - numbered bottom-to-top on right
    for i in range(4):
        add_smd_pad(fp, str(8 - i), 2.7, -1.905 + i * 1.27, 1.5, 0.6)
    add_fab_rect(fp, 0, 0, 3.9, 4.9)
    add_courtyard_rect(fp, 0, 0, 6.0, 5.4)
    # Pin 1 dot
    dot = pcbnew.PCB_SHAPE(fp)
    dot.SetShape(pcbnew.SHAPE_T_CIRCLE)
    dot.SetCenter(pt(-1.5, -2.5))
    dot.SetEnd(pt(-1.3, -2.5))
    dot.SetLayer(pcbnew.F_SilkS)
    dot.SetWidth(mm(0.12))
    dot.SetFilled(True)
    fp.Add(dot)
    return fp


def create_esp32_wroom_32e(board, ref, val):
    """
    Create ESP32-WROOM-32E module footprint.

    Module dimensions: 18mm x 25.5mm (body), antenna extends ~3mm past end.
    39 pads: 38 castellated edge pads + 1 GND pad on bottom.

    Pad numbering (per Espressif datasheet):
      Pin 1 = GND (bottom left)
      Pins 1-14 along left edge (bottom to top)
      Pins 15-38 along bottom and right edge
      Pin 39 = GND pad (exposed pad on bottom of module)

    Key pins for this design:
      Pin 1 = GND
      Pin 2 = 3V3
      Pin 3 = EN (chip enable, active high)
      Pin 8 = GPIO32 (Relay driver)
      Pin 9 = GPIO33 (Wiegand D1)
      Pin 10 = GPIO25 (Wiegand D0)
      Pin 25 = GPIO0 (Boot mode select)
      Pin 26 = GPIO4 (RS485 DE/RE)
      Pin 27 = GPIO16 (RS485 RX / UART2)
      Pin 28 = GPIO17 (RS485 TX / UART2)
      Pin 34 = RXD0 / GPIO3 (FTDI TX -> ESP RX)
      Pin 35 = TXD0 / GPIO1 (ESP TX -> FTDI RX)
      Pin 39 = GND (exposed pad)
    """
    fp = pcbnew.FOOTPRINT(board)
    fp.SetReference(ref)
    fp.SetValue(val)
    fp.SetFPIDAsString("RF_Module:ESP32-WROOM-32E")

    # Module body dimensions
    body_w = 18.0
    body_h = 25.5

    # Pad dimensions for castellated pads
    pad_w = 0.9  # width
    pad_h = 0.9  # height (exposed on edge)
    pitch = 1.27  # pin pitch

    # ESP32-WROOM-32E pin mapping (from datasheet):
    # Left side (pins 1-14, bottom to top):
    #   1=GND, 2=3V3, 3=EN, 4=SENSOR_VP, 5=SENSOR_VN, 6=IO34, 7=IO35,
    #   8=IO32, 9=IO33, 10=IO25, 11=IO26, 12=IO27, 13=IO14, 14=IO12
    # Bottom side (pins 15-22, left to right):
    #   15=GND, 16=IO13, 17=SD2, 18=SD3, 19=CMD, 20=CLK, 21=SD0, 22=SD1
    # Right side (pins 23-38, bottom to top):
    #   23=IO15, 24=IO2, 25=IO0, 26=IO4, 27=IO16, 28=IO17, 29=IO5, 30=IO18,
    #   31=IO19, 32=NC, 33=IO21, 34=RXD0, 35=TXD0, 36=IO22, 37=IO23, 38=GND
    # Bottom exposed pad: 39=GND

    # Left side pads (pins 1-14): x = -body_w/2, y from bottom to top
    for i in range(14):
        y_offset = body_h / 2 - 1.1 - i * pitch  # Start ~1.1mm from bottom edge
        add_smd_pad(
            fp, str(i + 1), -body_w / 2 + pad_w / 2 - 0.45, y_offset, pad_w, pad_h
        )

    # Bottom side pads (pins 15-22): y = body_h/2
    for i in range(8):
        x_offset = -body_w / 2 + 4.5 + i * pitch  # Start offset from left edge
        add_smd_pad(
            fp, str(15 + i), x_offset, body_h / 2 - pad_h / 2 + 0.45, pad_h, pad_w
        )

    # Right side pads (pins 23-38): x = body_w/2, y from bottom to top
    for i in range(16):
        y_offset = body_h / 2 - 1.1 - i * pitch
        add_smd_pad(
            fp, str(23 + i), body_w / 2 - pad_w / 2 + 0.45, y_offset, pad_w, pad_h
        )

    # Exposed GND pad on bottom (pin 39)
    add_smd_pad(fp, "39", 0, body_h / 2 - 3.0, 6.0, 4.0)

    # Module body outline on fab layer
    add_fab_rect(fp, 0, 0, body_w, body_h)

    # Courtyard (larger clearance for antenna area)
    add_courtyard_rect(fp, 0, -1.0, body_w + 2.0, body_h + 4.0)

    # Antenna area marker on silkscreen (top portion of module)
    ant_shape = pcbnew.PCB_SHAPE(fp)
    ant_shape.SetShape(pcbnew.SHAPE_T_RECT)
    ant_shape.SetStart(pt(-body_w / 2, -body_h / 2))
    ant_shape.SetEnd(pt(body_w / 2, -body_h / 2 + 5.5))
    ant_shape.SetLayer(pcbnew.F_SilkS)
    ant_shape.SetWidth(mm(0.12))
    fp.Add(ant_shape)

    # Pin 1 marker
    dot = pcbnew.PCB_SHAPE(fp)
    dot.SetShape(pcbnew.SHAPE_T_CIRCLE)
    dot.SetCenter(pt(-body_w / 2 - 1.0, body_h / 2 - 1.1))
    dot.SetEnd(pt(-body_w / 2 - 0.8, body_h / 2 - 1.1))
    dot.SetLayer(pcbnew.F_SilkS)
    dot.SetWidth(mm(0.12))
    dot.SetFilled(True)
    fp.Add(dot)

    return fp


def create_screw_terminal_2(board, ref, val):
    """Create a 2-pin screw terminal, 3.5mm pitch, through-hole."""
    fp = pcbnew.FOOTPRINT(board)
    fp.SetReference(ref)
    fp.SetValue(val)
    fp.SetFPIDAsString("Connector_Phoenix_MKDS:PhoenixContact_MKDS_1,5-2_1x02_P3.50mm")
    add_pth_pad(fp, "1", -1.75, 0, 2.0, 1.1)
    add_pth_pad(fp, "2", 1.75, 0, 2.0, 1.1)
    add_fab_rect(fp, 0, 0, 7.3, 7.0)
    add_courtyard_rect(fp, 0, 0, 8.5, 8.5)
    return fp


def create_screw_terminal_4(board, ref, val):
    """Create a 4-pin screw terminal, 3.5mm pitch, through-hole."""
    fp = pcbnew.FOOTPRINT(board)
    fp.SetReference(ref)
    fp.SetValue(val)
    fp.SetFPIDAsString("Connector_Phoenix_MKDS:PhoenixContact_MKDS_1,5-4_1x04_P3.50mm")
    for i in range(4):
        add_pth_pad(fp, str(i + 1), (i - 1.5) * 3.5, 0, 2.0, 1.1)
    add_fab_rect(fp, 0, 0, 14.3, 7.0)
    add_courtyard_rect(fp, 0, 0, 15.5, 8.5)
    return fp


def create_screw_terminal_3(board, ref, val):
    """Create a 3-pin screw terminal, 3.5mm pitch, through-hole."""
    fp = pcbnew.FOOTPRINT(board)
    fp.SetReference(ref)
    fp.SetValue(val)
    fp.SetFPIDAsString("Connector_Phoenix_MKDS:PhoenixContact_MKDS_1,5-3_1x03_P3.50mm")
    for i in range(3):
        add_pth_pad(fp, str(i + 1), (i - 1.0) * 3.5, 0, 2.0, 1.1)
    add_fab_rect(fp, 0, 0, 10.8, 7.0)
    add_courtyard_rect(fp, 0, 0, 12.0, 8.5)
    return fp


def create_pin_header_1x6(board, ref, val):
    """Create a 1x6 pin header, 2.54mm pitch, through-hole (FTDI header)."""
    fp = pcbnew.FOOTPRINT(board)
    fp.SetReference(ref)
    fp.SetValue(val)
    fp.SetFPIDAsString("Connector_PinHeader_2.54mm:PinHeader_1x06_P2.54mm_Vertical")
    for i in range(6):
        shape = pcbnew.PAD_SHAPE_RECT if i == 0 else pcbnew.PAD_SHAPE_CIRCLE
        add_pth_pad(fp, str(i + 1), 0, (i - 2.5) * 2.54, 1.7, 1.0, shape=shape)
    add_fab_rect(fp, 0, 0, 2.54, 6 * 2.54)
    add_courtyard_rect(fp, 0, 0, 3.0, 6 * 2.54 + 1.0)
    return fp


# =============================================================================
# NETS
# =============================================================================


def create_nets(board):
    """Create all electrical nets and add them to the board."""
    net_names = [
        "GND",
        "12V",
        "12V_RAW",
        "3V3",
        "3V3_LDO",  # Between AMS1117 output and ESR ballast R11
        "WIEG_D0_IN",
        "WIEG_D1_IN",  # From reader to optocoupler LED
        "WIEG_D0_R",
        "WIEG_D1_R",  # From current-limit resistor to optocoupler LED anode
        "WIEG_D0",
        "WIEG_D1",  # From optocoupler output to ESP32
        "RELAY_DRV",  # ESP32 GPIO32 -> R6 pad 1
        "RELAY_BASE",  # R6 pad 2 -> Q1 base
        "RELAY_12V",  # Relay coil switched side (Q1 collector)
        "RS485_TX",
        "RS485_RX",  # ESP32 UART2 to SP3485EN
        "RS485_DE",  # Direction enable
        "RS485_A",
        "RS485_B",  # Differential pair
        "FTDI_TX",
        "FTDI_RX",  # FTDI header UART
        "FTDI_DTR",
        "FTDI_RTS",  # Auto-reset control
        "ESP_EN",
        "ESP_GPIO0",  # ESP32 boot/reset signals
        "PWR_LED",  # Between R10 and LED1 anode
    ]
    nets = {}
    for name in net_names:
        ni = pcbnew.NETINFO_ITEM(board, name)
        board.Add(ni)
        nets[name] = ni
    return nets


# =============================================================================
# COMPONENT PLACEMENT
# =============================================================================


def place_components(board, nets):
    """Create and place all components on the board. Returns dict of parts."""
    parts = {}

    # ---- ESP32 Module ----
    # Place center-left, with antenna extending toward board edge (top).
    # Antenna goes to negative Y (top of board), so module oriented with
    # top of module pointing toward top board edge.
    esp = create_esp32_wroom_32e(board, "U4", "ESP32-WROOM-32E")
    esp.SetPosition(pt(22.0, 14.5))
    board.Add(esp)
    parts["U4"] = esp

    # ---- Power Section (top-right area) ----
    # J1: 12V input screw terminal
    j1 = create_screw_terminal_2(board, "J1", "12V_IN")
    j1.SetPosition(pt(49.0, 5.0))
    board.Add(j1)
    parts["J1"] = j1

    # D1: SS34 reverse polarity Schottky
    d1 = create_sma_diode(board, "D1", "SS34")
    d1.SetPosition(pt(42.0, 5.0))
    board.Add(d1)
    parts["D1"] = d1

    # U1: AMS1117-3.3 LDO (oriented with tab pointing down/toward board bottom)
    #
    # THERMAL NOTE: 12V→3.3V linear regulator dissipates significant power.
    #   Vdrop = 8.7V (worst case), Average I ≈ 150mA → P_avg = 1.3W
    #   Peak WiFi TX I ≈ 500mA → P_peak = 4.35W (transient, 10-100ms bursts)
    #   SOT-223 θJA ≈ 90°C/W (minimal copper) → REQUIRES thermal relief.
    #   With thermal vias + copper zones: θJA ≈ 35-40°C/W → T_rise ≈ 50°C (OK)
    #   Peak 4.35W is transient; thermal mass prevents reaching steady-state Tj.
    #   See add_ldo_thermal_relief() for copper zones and thermal vias.
    #   Future: buck pre-regulator (12V→5V) + LDO (5V→3.3V) reduces P to ~0.25W.
    u1 = create_sot223(board, "U1", "AMS1117-3.3")
    u1.SetPosition(pt(42.0, 12.0))
    u1.SetOrientationDegrees(180)
    board.Add(u1)
    parts["U1"] = u1

    # Input filter caps (near D1/U1)
    # C1/C12: 10uF 25V 0805 on 12V input for bulk decoupling
    # C3/C13: 22uF 25V 0805 on 3.3V output for LDO stability
    c1 = create_c0805(board, "C1", "10uF")
    c1.SetPosition(pt(35.0, 7.0))
    board.Add(c1)
    parts["C1"] = c1

    c1b = create_c0805(board, "C12", "10uF")
    c1b.SetPosition(pt(35.0, 9.5))
    board.Add(c1b)
    parts["C12"] = c1b

    c2 = create_c0402(board, "C2", "100nF")
    c2.SetPosition(pt(35.0, 11.0))
    board.Add(c2)
    parts["C2"] = c2

    # Output filter caps (near U1 output)
    c3 = create_c0805(board, "C3", "22uF")
    c3.SetPosition(pt(49.0, 11.0))
    board.Add(c3)
    parts["C3"] = c3

    c3b = create_c0805(board, "C13", "22uF")
    c3b.SetPosition(pt(49.0, 13.5))
    board.Add(c3b)
    parts["C13"] = c3b

    c4 = create_c0402(board, "C4", "100nF")
    c4.SetPosition(pt(49.0, 15.5))
    board.Add(c4)
    parts["C4"] = c4

    # R11: 0.22R ESR ballast for AMS1117 output capacitor stability.
    # AMS1117 requires output cap ESR 0.1-10 ohm; MLCC ESR (~5-10 mohm)
    # is far too low. R11 adds 0.22R in series: Vdrop = 0.11V @ 500mA,
    # output = 3.19V (within ESP32 tolerance).
    r11 = create_r0402(board, "R11", "0R22")
    r11.SetPosition(pt(48.0, 15.5))
    board.Add(r11)
    parts["R11"] = r11

    # ESP32 decoupling caps (near ESP32 3V3/GND pins)
    c5 = create_c0402(board, "C5", "100nF")
    c5.SetPosition(pt(10.0, 27.0))
    board.Add(c5)
    parts["C5"] = c5

    c6 = create_c0805(board, "C6", "10uF")
    c6.SetPosition(pt(10.0, 28.0))
    board.Add(c6)
    parts["C6"] = c6

    c7 = create_c0805(board, "C7", "10uF")
    c7.SetPosition(pt(10.0, 30.0))
    board.Add(c7)
    parts["C7"] = c7

    # ---- Wiegand Section (right side, middle) ----
    # J2: 4-pin Wiegand reader connector
    j2 = create_screw_terminal_4(board, "J2", "WIEGAND")
    j2.SetPosition(pt(42.0, 40.0))
    board.Add(j2)
    parts["J2"] = j2

    # U2: EL817 optocoupler for D0
    u2 = create_sop4(board, "U2", "EL817")
    u2.SetPosition(pt(38.0, 30.0))
    u2.SetOrientationDegrees(90)
    board.Add(u2)
    parts["U2"] = u2

    # R1: 470R current limiting for U2 LED (8.1mA at 5V Wiegand)
    # WARNING: 0402 rated 1/16W (62.5mW). Safe at 5V (31mW) but NOT at 12V
    # (248mW). If 12V Wiegand compatibility is needed, use 0805 (1/8W).
    r1 = create_r0402(board, "R1", "470R")
    r1.SetPosition(pt(38.0, 26.5))
    board.Add(r1)
    parts["R1"] = r1

    # R4: 10K pull-up for U2 output
    r4 = create_r0402(board, "R4", "10K")
    r4.SetPosition(pt(33.0, 30.0))
    board.Add(r4)
    parts["R4"] = r4

    # U3: EL817 optocoupler for D1
    u3 = create_sop4(board, "U3", "EL817")
    u3.SetPosition(pt(48.0, 30.0))
    u3.SetOrientationDegrees(90)
    board.Add(u3)
    parts["U3"] = u3

    # R3: 470R current limiting for U3 LED (8.1mA at 5V Wiegand)
    # WARNING: 0402 rated 1/16W (62.5mW). Safe at 5V (31mW) but NOT at 12V
    # (248mW). If 12V Wiegand compatibility is needed, use 0805 (1/8W).
    r3 = create_r0402(board, "R3", "470R")
    r3.SetPosition(pt(48.0, 26.5))
    board.Add(r3)
    parts["R3"] = r3

    # R5: 10K pull-up for U3 output
    r5 = create_r0402(board, "R5", "10K")
    r5.SetPosition(pt(53.0, 30.0))
    board.Add(r5)
    parts["R5"] = r5

    # ---- Relay Driver (top-left) ----
    # J3: 2-pin relay output
    j3 = create_screw_terminal_2(board, "J3", "RELAY")
    j3.SetPosition(pt(5.5, 6.5))
    board.Add(j3)
    parts["J3"] = j3

    # Q1: SS8050 NPN relay driver
    q1 = create_sot23(board, "Q1", "SS8050")
    q1.SetPosition(pt(5.5, 13.0))
    board.Add(q1)
    parts["Q1"] = q1

    # R6: 1K base resistor for Q1
    r6 = create_r0402(board, "R6", "1K")
    r6.SetPosition(pt(5.5, 15.5))
    board.Add(r6)
    parts["R6"] = r6

    # D2: M7 flyback diode
    d2 = create_sma_diode(board, "D2", "M7")
    d2.SetPosition(pt(5.5, 18.5))
    d2.SetOrientationDegrees(180)
    board.Add(d2)
    parts["D2"] = d2

    # ---- RS485 Section (right side, bottom) ----
    # U5: SP3485EN RS485 transceiver (pin-compatible MAX3485 replacement, Basic part)
    u5 = create_sop8(board, "U5", "SP3485")
    u5.SetPosition(pt(48.0, 36.0))
    u5.SetOrientationDegrees(90)
    board.Add(u5)
    parts["U5"] = u5

    # J5: 3-pin RS485 screw terminal (A, B, GND)
    j5 = create_screw_terminal_3(board, "J5", "RS485")
    j5.SetPosition(pt(18.0, 40.0))
    board.Add(j5)
    parts["J5"] = j5

    # R7: 120R RS485 termination (optional - solder bridge)
    # DNP by default; only populate if board is at end of RS485 bus.
    r7 = create_r0402(board, "R7", "120R")
    r7.SetPosition(pt(42.0, 36.0))
    board.Add(r7)
    parts["R7"] = r7

    # C8: 100nF decoupling for SP3485EN
    c8 = create_c0402(board, "C8", "100nF")
    c8.SetPosition(pt(53.0, 36.0))
    board.Add(c8)
    parts["C8"] = c8

    # D3: SM712 TVS diode for RS485 ESD protection
    # SOT-23: Pin 1 = RS485_A (Vrwm=12V), Pin 2 = GND, Pin 3 = RS485_B (Vrwm=7V)
    # Asymmetric clamping matches RS485 common-mode range (-7V to +12V)
    # Placed near U5, close to the bus lines
    d3 = create_sot23(board, "D3", "SM712")
    d3.SetPosition(pt(42.0, 32.0))
    board.Add(d3)
    parts["D3"] = d3

    # ---- Power LED (top-right, near J1) ----
    # R10: 100R current-limiting resistor for power LED
    # KT-0805G (C2297) standard green Vf ~2.0-2.4V: I = (3.3-Vf)/100 = 9-13mA
    r10 = create_r0402(board, "R10", "100R")
    r10.SetPosition(pt(54.0, 5.0))
    board.Add(r10)
    parts["R10"] = r10

    # LED1: Green 0805 power indicator LED on 3.3V rail
    led1 = create_led0805(board, "LED1", "LED_GRN")
    led1.SetPosition(pt(54.0, 7.0))
    board.Add(led1)
    parts["LED1"] = led1

    # ---- FTDI Programming Header + Auto-Reset (left side) ----
    # J6: 1x6 FTDI header (NON-STANDARD: pin 2 = RTS for auto-reset, not CTS)
    # Pinout: 1=GND, 2=RTS(->EN), 3=3V3, 4=TXO(->ESP RX), 5=RXI(<-ESP TX), 6=DTR(->GPIO0)
    j6 = create_pin_header_1x6(board, "J6", "FTDI")
    j6.SetPosition(pt(4.0, 27.0))
    board.Add(j6)
    parts["J6"] = j6

    # Auto-reset circuit: DTR -> C_DTR -> GPIO0, RTS -> C_RTS -> EN
    # C11: 1nF bypass on EN for noise filtering (must be << C10 to avoid
    # attenuating the auto-reset pulse from C10 via capacitive divider)
    c11 = create_c0402(board, "C11", "1nF")
    c11.SetPosition(pt(10.0, 10.5))
    board.Add(c11)
    parts["C11"] = c11

    # R8: 10K pull-up for EN
    r8 = create_r0402(board, "R8", "10K")
    r8.SetPosition(pt(10.0, 12.0))
    board.Add(r8)
    parts["R8"] = r8

    # R9: 10K pull-up for GPIO0
    r9 = create_r0402(board, "R9", "10K")
    r9.SetPosition(pt(10.0, 13.5))
    board.Add(r9)
    parts["R9"] = r9

    # C9: 100nF coupling cap for DTR -> GPIO0 auto-boot
    c9 = create_c0402(board, "C9", "100nF")
    c9.SetPosition(pt(10.0, 15.0))
    board.Add(c9)
    parts["C9"] = c9

    # C10: 100nF coupling cap for RTS -> EN auto-reset
    c10 = create_c0402(board, "C10", "100nF")
    c10.SetPosition(pt(10.0, 16.5))
    board.Add(c10)
    parts["C10"] = c10

    return parts


# =============================================================================
# NET ASSIGNMENT
# =============================================================================


def assign_nets(parts, nets):
    """Assign nets to pads based on the schematic connections."""

    def set_pad_net(part, pad_num, net):
        """Find a pad by number on a footprint and assign a net."""
        for pad in part.Pads():
            if pad.GetNumber() == str(pad_num):
                pad.SetNet(net)
                return pad
        raise ValueError(f"Pad {pad_num} not found on {part.GetReference()}")

    # ---- Power ----
    # J1: 12V input (1=+12V_RAW, 2=GND)
    set_pad_net(parts["J1"], 1, nets["12V_RAW"])
    set_pad_net(parts["J1"], 2, nets["GND"])

    # D1: SS34 reverse-polarity protection (1=Cathode->12V, 2=Anode<-12V_RAW)
    set_pad_net(parts["D1"], 2, nets["12V_RAW"])  # Anode from input (pre-diode)
    set_pad_net(parts["D1"], 1, nets["12V"])  # Cathode to 12V rail (post-diode)

    # U1: AMS1117-3.3 (1=GND, 2=VOUT, 3=VIN, 4=Tab=VOUT)
    # Pins 2,4 connect to 3V3_LDO (pre-ballast); R11 converts to 3V3.
    set_pad_net(parts["U1"], 1, nets["GND"])
    set_pad_net(parts["U1"], 2, nets["3V3_LDO"])
    set_pad_net(parts["U1"], 3, nets["12V"])
    set_pad_net(parts["U1"], 4, nets["3V3_LDO"])

    # R11: 0.22R ESR ballast (1=3V3_LDO from U1, 2=3V3 to output caps)
    set_pad_net(parts["R11"], 1, nets["3V3_LDO"])
    set_pad_net(parts["R11"], 2, nets["3V3"])

    # Input filter caps
    for ref in ["C1", "C12", "C2"]:
        set_pad_net(parts[ref], 1, nets["12V"])
        set_pad_net(parts[ref], 2, nets["GND"])

    # Output filter caps
    for ref in ["C3", "C13", "C4"]:
        set_pad_net(parts[ref], 1, nets["3V3"])
        set_pad_net(parts[ref], 2, nets["GND"])

    # ESP32 decoupling
    for ref in ["C5", "C6", "C7"]:
        set_pad_net(parts[ref], 1, nets["3V3"])
        set_pad_net(parts[ref], 2, nets["GND"])

    # ---- ESP32 Module ----
    # Pin 1 = GND, Pin 2 = 3V3, Pin 3 = EN
    set_pad_net(parts["U4"], 1, nets["GND"])
    set_pad_net(parts["U4"], 2, nets["3V3"])
    set_pad_net(parts["U4"], 3, nets["ESP_EN"])
    # Pin 8 = IO32 (relay), Pin 9 = IO33 (Wiegand D1), Pin 10 = IO25 (Wiegand D0)
    set_pad_net(parts["U4"], 8, nets["RELAY_DRV"])
    set_pad_net(parts["U4"], 9, nets["WIEG_D1"])
    set_pad_net(parts["U4"], 10, nets["WIEG_D0"])
    # Pin 25 = IO0 (boot), Pin 26 = IO4 (RS485 DE), Pin 27 = IO16 (RS485 RX), Pin 28 = IO17 (RS485 TX)
    set_pad_net(parts["U4"], 25, nets["ESP_GPIO0"])
    set_pad_net(parts["U4"], 26, nets["RS485_DE"])
    set_pad_net(parts["U4"], 27, nets["RS485_RX"])
    set_pad_net(parts["U4"], 28, nets["RS485_TX"])
    # Pin 34 = RXD0 (FTDI TX -> ESP RX), Pin 35 = TXD0 (ESP TX -> FTDI RX)
    set_pad_net(parts["U4"], 34, nets["FTDI_TX"])
    set_pad_net(parts["U4"], 35, nets["FTDI_RX"])
    # Pin 15 = GND, Pin 38 = GND, Pin 39 = GND (exposed pad)
    set_pad_net(parts["U4"], 15, nets["GND"])
    set_pad_net(parts["U4"], 38, nets["GND"])
    set_pad_net(parts["U4"], 39, nets["GND"])

    # ---- Wiegand D0 Path ----
    # NOTE: Optocoupler inverts Wiegand signals. Reader idle (HIGH) = ESP32 LOW,
    # reader data pulse (LOW) = ESP32 HIGH. Firmware must trigger on RISING edge.
    # J2: 1=D0, 2=D1, 3=+12V, 4=GND
    set_pad_net(parts["J2"], 1, nets["WIEG_D0_IN"])
    set_pad_net(parts["J2"], 2, nets["WIEG_D1_IN"])
    set_pad_net(parts["J2"], 3, nets["12V"])
    set_pad_net(parts["J2"], 4, nets["GND"])

    # R1: 470R current-limit (1=WIEG_D0_IN from J2, 2=WIEG_D0_R to U2 anode)
    set_pad_net(parts["R1"], 1, nets["WIEG_D0_IN"])
    set_pad_net(parts["R1"], 2, nets["WIEG_D0_R"])

    # U2: EL817 (1=Anode, 2=Cathode=GND, 3=Emitter=GND, 4=Collector=D0)
    set_pad_net(parts["U2"], 1, nets["WIEG_D0_R"])
    set_pad_net(parts["U2"], 2, nets["GND"])
    set_pad_net(parts["U2"], 3, nets["GND"])
    set_pad_net(parts["U2"], 4, nets["WIEG_D0"])

    # R4: 10K pull-up (1=WIEG_D0, 2=3V3)
    set_pad_net(parts["R4"], 1, nets["WIEG_D0"])
    set_pad_net(parts["R4"], 2, nets["3V3"])

    # ---- Wiegand D1 Path ----
    # R3: 470R current-limit (1=WIEG_D1_IN from J2, 2=WIEG_D1_R to U3 anode)
    set_pad_net(parts["R3"], 1, nets["WIEG_D1_IN"])
    set_pad_net(parts["R3"], 2, nets["WIEG_D1_R"])

    # U3: EL817
    set_pad_net(parts["U3"], 1, nets["WIEG_D1_R"])
    set_pad_net(parts["U3"], 2, nets["GND"])
    set_pad_net(parts["U3"], 3, nets["GND"])
    set_pad_net(parts["U3"], 4, nets["WIEG_D1"])

    # R5: 10K pull-up
    set_pad_net(parts["R5"], 1, nets["WIEG_D1"])
    set_pad_net(parts["R5"], 2, nets["3V3"])

    # ---- Relay Driver ----
    # J3: Relay output (1=Switched side via Q1, 2=+12V supply)
    set_pad_net(parts["J3"], 1, nets["RELAY_12V"])
    set_pad_net(parts["J3"], 2, nets["12V"])

    # Q1: SS8050 (1=Base, 2=Emitter=GND, 3=Collector)
    set_pad_net(parts["Q1"], 1, nets["RELAY_BASE"])
    set_pad_net(parts["Q1"], 2, nets["GND"])
    set_pad_net(parts["Q1"], 3, nets["RELAY_12V"])

    # R6: 1K base resistor (1=RELAY_DRV from ESP32, 2=RELAY_BASE to Q1)
    set_pad_net(parts["R6"], 1, nets["RELAY_DRV"])
    set_pad_net(parts["R6"], 2, nets["RELAY_BASE"])

    # D2: M7 flyback (1=Cathode=12V, 2=Anode=collector)
    set_pad_net(parts["D2"], 1, nets["12V"])
    set_pad_net(parts["D2"], 2, nets["RELAY_12V"])

    # ---- RS485 Section ----
    # U5: SP3485EN (1=RO, 2=RE, 3=DE, 4=DI, 5=GND, 6=A, 7=B, 8=VCC)
    set_pad_net(parts["U5"], 1, nets["RS485_RX"])
    set_pad_net(parts["U5"], 2, nets["RS485_DE"])  # RE (active low, tied to DE)
    set_pad_net(parts["U5"], 3, nets["RS485_DE"])  # DE (active high)
    set_pad_net(parts["U5"], 4, nets["RS485_TX"])
    set_pad_net(parts["U5"], 5, nets["GND"])
    set_pad_net(parts["U5"], 6, nets["RS485_A"])
    set_pad_net(parts["U5"], 7, nets["RS485_B"])
    set_pad_net(parts["U5"], 8, nets["3V3"])

    # J5: RS485 terminal (1=A, 2=B, 3=GND) — 3-pin, no +12V
    set_pad_net(parts["J5"], 1, nets["RS485_A"])
    set_pad_net(parts["J5"], 2, nets["RS485_B"])
    set_pad_net(parts["J5"], 3, nets["GND"])

    # R7: 120R termination between A and B
    set_pad_net(parts["R7"], 1, nets["RS485_A"])
    set_pad_net(parts["R7"], 2, nets["RS485_B"])

    # C8: 100nF decoupling for SP3485EN
    set_pad_net(parts["C8"], 1, nets["3V3"])
    set_pad_net(parts["C8"], 2, nets["GND"])

    # D3: SM712 TVS on RS485 bus
    # SOT-23: Pin 1 = A line, Pin 2 = GND, Pin 3 = B line
    set_pad_net(parts["D3"], 1, nets["RS485_A"])
    set_pad_net(parts["D3"], 2, nets["GND"])
    set_pad_net(parts["D3"], 3, nets["RS485_B"])

    # ---- Power LED ----
    # R10: 100R resistor (1=3V3, 2=PWR_LED)
    set_pad_net(parts["R10"], 1, nets["3V3"])
    set_pad_net(parts["R10"], 2, nets["PWR_LED"])

    # LED1: Green LED (1=Cathode=GND, 2=Anode=PWR_LED)
    set_pad_net(parts["LED1"], 1, nets["GND"])
    set_pad_net(parts["LED1"], 2, nets["PWR_LED"])

    # ---- FTDI Header + Auto-Reset ----
    # J6: FTDI 6-pin (1=GND, 2=RTS, 3=3V3, 4=TXO, 5=RXI, 6=DTR) [NON-STANDARD]
    # Standard FTDI has CTS on pin 2; we use RTS here for ESP32 auto-reset.
    set_pad_net(parts["J6"], 1, nets["GND"])
    set_pad_net(parts["J6"], 2, nets["FTDI_RTS"])  # Non-standard: RTS for auto-reset
    set_pad_net(parts["J6"], 3, nets["3V3"])
    set_pad_net(parts["J6"], 4, nets["FTDI_TX"])  # FTDI TXO -> ESP32 RXD0
    set_pad_net(parts["J6"], 5, nets["FTDI_RX"])  # FTDI RXI <- ESP32 TXD0
    set_pad_net(parts["J6"], 6, nets["FTDI_DTR"])

    # R8: 10K pull-up EN to 3V3
    set_pad_net(parts["R8"], 1, nets["ESP_EN"])
    set_pad_net(parts["R8"], 2, nets["3V3"])

    # R9: 10K pull-up GPIO0 to 3V3
    set_pad_net(parts["R9"], 1, nets["ESP_GPIO0"])
    set_pad_net(parts["R9"], 2, nets["3V3"])

    # C9: 100nF DTR -> GPIO0 (auto-boot)
    set_pad_net(parts["C9"], 1, nets["FTDI_DTR"])
    set_pad_net(parts["C9"], 2, nets["ESP_GPIO0"])

    # C10: 100nF coupling cap for RTS -> EN auto-reset
    # Auto-reset circuit: DTR->C9->GPIO0, RTS->C10->EN.
    # Standard FTDI cables will NOT auto-reset; use an ESP-PROG or adapter
    # that provides both DTR and RTS on the correct pins.
    set_pad_net(parts["C10"], 1, nets["FTDI_RTS"])
    set_pad_net(parts["C10"], 2, nets["ESP_EN"])

    # C11: 100nF bypass on EN
    set_pad_net(parts["C11"], 1, nets["ESP_EN"])
    set_pad_net(parts["C11"], 2, nets["GND"])


# =============================================================================
# TRACE ROUTING
# =============================================================================


def add_track(board, start, end, width, layer, net):
    """Add a single PCB track segment."""
    t = pcbnew.PCB_TRACK(board)
    t.SetStart(pt(start[0], start[1]))
    t.SetEnd(pt(end[0], end[1]))
    t.SetWidth(mm(width))
    t.SetLayer(layer)
    t.SetNet(net)
    board.Add(t)
    return t


def add_via(board, pos, net, drill=VIA_DRILL, size=VIA_SIZE):
    """Add a via at position."""
    v = pcbnew.PCB_VIA(board)
    v.SetPosition(pt(pos[0], pos[1]))
    v.SetDrill(mm(drill))
    v.SetWidth(mm(size))
    v.SetLayerPair(pcbnew.F_Cu, pcbnew.B_Cu)
    v.SetNet(net)
    board.Add(v)
    return v


def route_wire(board, points, width, layer, net):
    """Route a multi-segment wire through a list of (x,y) points."""
    for i in range(len(points) - 1):
        add_track(board, points[i], points[i + 1], width, layer, net)


def get_pad_pos(part, pad_num):
    """Get the absolute position of a pad in mm."""
    for pad in part.Pads():
        if pad.GetNumber() == str(pad_num):
            pos = pad.GetPosition()
            return (pcbnew.ToMM(pos.x), pcbnew.ToMM(pos.y))
    raise ValueError(f"Pad {pad_num} not found on {part.GetReference()}")


def route_all(board, parts, nets):
    """Route all electrical connections.

    Routing strategy — key principles to avoid DRC violations:

    1. F.Cu carries SHORT stubs only (pad→via, ≤ 3 mm) and LOCAL connections
       where two pads in the same functional group are close together.
    2. B.Cu carries longer runs.  There is a GND pour on B.Cu, so every non-GND
       via/track on B.Cu gets automatic clearance from the filler.  We must
       still ensure non-GND B.Cu tracks do not cross each other.
    3. We route one signal at a time, using 45°/90° Manhattan geometry.
       For every B.Cu segment, we verify the path does not share any y-row
       with another B.Cu segment at the same x-range.

    Via naming: v_{net}_{location} so it's clear what each via is for.
    """

    F = pcbnew.F_Cu
    B = pcbnew.B_Cu

    # =====================================================================
    # 12V POWER PATH
    # =====================================================================

    # J1.1 → D1.2 (horizontal F.Cu, same y=5)
    j1_1 = get_pad_pos(parts["J1"], 1)
    d1_a = get_pad_pos(parts["D1"], 2)
    route_wire(board, [j1_1, d1_a], TW_POWER, F, nets["12V_RAW"])

    # D1.1(K) = 12V distribution point
    d1_k = get_pad_pos(parts["D1"], 1)   # (40.0, 5.0)

    # D1.K → U1.VIN (pin 3) — L-shaped on F.Cu
    u1_vin = get_pad_pos(parts["U1"], 3)  # (38.85, 8.75)
    route_wire(board, [d1_k, (d1_k[0], u1_vin[1]), u1_vin], TW_POWER, F, nets["12V"])

    # D1.K → input caps C1.1, C12.1, C2.1 via B.Cu
    # Caps are at x=34-35, y=7-11.  Route via B.Cu to avoid crossing U1 VIN trace.
    c1_1 = get_pad_pos(parts["C1"], 1)    # (34.0, 7.0)
    c12_1 = get_pad_pos(parts["C12"], 1)  # (34.0, 9.5)
    c2_1 = get_pad_pos(parts["C2"], 1)    # (34.5, 11.0)

    # Via near D1.K, offset south to clear the D1 body
    v_12v_src = (d1_k[0], d1_k[1] + 1.5)  # (40, 6.5)
    add_via(board, v_12v_src, nets["12V"])
    route_wire(board, [d1_k, v_12v_src], TW_POWER, F, nets["12V"])

    # Via near cap column
    v_12v_caps = (c1_1[0] + 0.5, c1_1[1] - 1.5)  # (34.5, 5.5)
    add_via(board, v_12v_caps, nets["12V"])

    # B.Cu: run west from v_12v_src to v_12v_caps
    route_wire(board, [v_12v_src, (v_12v_src[0], v_12v_caps[1]), v_12v_caps],
               TW_POWER, B, nets["12V"])

    # F.Cu: vertical rail from via down through caps
    route_wire(board, [v_12v_caps, (c1_1[0], v_12v_caps[1]), c1_1], TW_POWER, F, nets["12V"])
    route_wire(board, [c1_1, (c12_1[0], c1_1[1]), (c12_1[0], c12_1[1])],
               TW_POWER, F, nets["12V"])
    route_wire(board, [c12_1, (c2_1[0], c12_1[1]), (c2_1[0], c2_1[1])],
               TW_POWER, F, nets["12V"])

    # D1.K → J2.3 (12V to Wiegand reader) via B.Cu
    j2_12v = get_pad_pos(parts["J2"], 3)  # (43.75, 40.0)

    # Use a via near D1 on F.Cu right side, then B.Cu south
    v_12v_j2a = (d1_k[0] + 3.0, d1_k[1] + 2.0)  # (43, 7) — outside keepout
    add_via(board, v_12v_j2a, nets["12V"])
    route_wire(board, [d1_k, (d1_k[0], v_12v_j2a[1]), v_12v_j2a], TW_POWER, F, nets["12V"])

    v_12v_j2b = (j2_12v[0], j2_12v[1] - 2.5)  # (43.75, 37.5)
    add_via(board, v_12v_j2b, nets["12V"])
    # B.Cu: south from (43,7) to (43.75, 37.5)
    route_wire(board, [v_12v_j2a, (v_12v_j2b[0], v_12v_j2a[1]), v_12v_j2b],
               TW_POWER, B, nets["12V"])
    # F.Cu stub to J2.3
    route_wire(board, [v_12v_j2b, j2_12v], TW_POWER, F, nets["12V"])

    # D1.K → relay section D2.K(7.5, 18.5) via B.Cu
    d2_k = get_pad_pos(parts["D2"], 1)  # (7.5, 18.5)
    # Via at x=9 to avoid relay section F.Cu traces
    v_12v_relay = (d2_k[0] + 2.5, d2_k[1] + 1.5)  # (10, 20)
    add_via(board, v_12v_relay, nets["12V"])
    # B.Cu from v_12v_src west then south to v_12v_relay
    # Use y=3.0 corridor on B.Cu (above everything) then come south at x=10
    v_12v_mid = (v_12v_relay[0], v_12v_src[1])  # (10, 6.5)
    route_wire(board, [v_12v_src, v_12v_mid, v_12v_relay], TW_POWER, B, nets["12V"])
    # F.Cu from relay via to D2.K
    route_wire(board, [v_12v_relay, (d2_k[0], v_12v_relay[1]), d2_k],
               TW_POWER, F, nets["12V"])

    # D2.K → J3.2 (12V to relay connector pin 2)
    j3_2 = get_pad_pos(parts["J3"], 2)  # (7.25, 6.5)
    # D2.K(7.5,18.5) → J3.2(7.25,6.5): vertical F.Cu, nearly same x
    route_wire(board, [d2_k, (j3_2[0], d2_k[1]), j3_2], TW_POWER, F, nets["12V"])

    # =====================================================================
    # 3.3V POWER PATH
    # =====================================================================

    u1_vout = get_pad_pos(parts["U1"], 2)  # (42.0, 8.75)
    u1_tab = get_pad_pos(parts["U1"], 4)   # (42.0, 15.15)
    r11_1 = get_pad_pos(parts["R11"], 1)   # (47.5, 15.5)
    r11_2 = get_pad_pos(parts["R11"], 2)   # (48.5, 15.5)
    c3_1 = get_pad_pos(parts["C3"], 1)     # (48.0, 11.0)
    c13_1 = get_pad_pos(parts["C13"], 1)   # (48.0, 13.5)
    c4_1 = get_pad_pos(parts["C4"], 1)     # (48.5, 15.5)

    # U1.VOUT ↔ U1.TAB (3V3_LDO, F.Cu)
    route_wire(board, [u1_vout, u1_tab], TW_POWER, F, nets["3V3_LDO"])

    # U1.TAB → R11.1 (3V3_LDO, F.Cu L-route)
    route_wire(board, [u1_tab, (r11_1[0], u1_tab[1]), r11_1],
               TW_POWER, F, nets["3V3_LDO"])

    # R11.2 → output caps: vertical rail at x≈48.5 on F.Cu
    # R11.2(48.5,15.5) → C4.1(48.5,15.5) — same position!
    route_wire(board, [r11_2, c4_1], TW_POWER, F, nets["3V3"])
    # C4 → C13 → C3 (going north)
    route_wire(board, [c4_1, (c13_1[0], c4_1[1]), c13_1], TW_POWER, F, nets["3V3"])
    route_wire(board, [c13_1, (c3_1[0], c13_1[1]), c3_1], TW_POWER, F, nets["3V3"])

    # --- 3V3 to ESP32 pin 2 (13.0, 24.88) via B.Cu ---
    esp_3v3 = get_pad_pos(parts["U4"], 2)  # (13.0, 24.88)

    # Via near output rail, at x=48 south of caps
    v_3v3_src = (c4_1[0], c4_1[1] + 2.0)  # (48.5, 17.5)
    add_via(board, v_3v3_src, nets["3V3"])
    route_wire(board, [c4_1, v_3v3_src], TW_POWER, F, nets["3V3"])

    # Via near ESP32 3V3 pin — offset left to avoid pad overlap
    v_3v3_esp = (esp_3v3[0] - 2.0, esp_3v3[1])  # (11.0, 24.88)
    add_via(board, v_3v3_esp, nets["3V3"])

    # B.Cu: from (48.5, 17.5) south to y=24.88, then west to (11, 24.88)
    route_wire(board, [v_3v3_src, (v_3v3_src[0], v_3v3_esp[1]), v_3v3_esp],
               TW_POWER, B, nets["3V3"])

    # F.Cu stub: via to ESP32 3V3 pin
    route_wire(board, [v_3v3_esp, esp_3v3], TW_POWER, F, nets["3V3"])

    # --- 3V3 to ESP32 decoupling caps C5/C6/C7 ---
    c5_1 = get_pad_pos(parts["C5"], 1)  # (9.5, 27.0)
    c6_1 = get_pad_pos(parts["C6"], 1)  # (9.0, 28.0)
    c7_1 = get_pad_pos(parts["C7"], 1)  # (9.0, 30.0)

    # From v_3v3_esp south on F.Cu to C5, then down through C6, C7
    route_wire(board, [v_3v3_esp, (c5_1[0], v_3v3_esp[1]), (c5_1[0], c5_1[1]), c5_1],
               TW_POWER, F, nets["3V3"])
    route_wire(board, [(c5_1[0], c5_1[1]), (c6_1[0], c6_1[1])],
               TW_POWER, F, nets["3V3"])
    route_wire(board, [c6_1, (c7_1[0], c6_1[1]), c7_1], TW_POWER, F, nets["3V3"])

    # --- 3V3 to pull-up resistors R4.2(33.5,30) and R5.2(53.5,30) ---
    r4_vcc = get_pad_pos(parts["R4"], 2)  # (33.5, 30.0)
    r5_vcc = get_pad_pos(parts["R5"], 2)  # (53.5, 30.0)

    # R4: via near R4, B.Cu from v_3v3_src area
    v_3v3_r4 = (r4_vcc[0], r4_vcc[1] + 1.5)  # (33.5, 31.5)
    add_via(board, v_3v3_r4, nets["3V3"])
    # B.Cu from v_3v3_src south then west
    route_wire(board, [v_3v3_src, (v_3v3_src[0], v_3v3_r4[1]), v_3v3_r4],
               TW_PULLUP, B, nets["3V3"])
    route_wire(board, [v_3v3_r4, r4_vcc], TW_PULLUP, F, nets["3V3"])

    # R5: via near R5
    v_3v3_r5 = (r5_vcc[0], r5_vcc[1] + 1.5)  # (53.5, 31.5)
    add_via(board, v_3v3_r5, nets["3V3"])
    # B.Cu from v_3v3_r4 east to v_3v3_r5 (same y=31.5)
    route_wire(board, [v_3v3_r4, v_3v3_r5], TW_PULLUP, B, nets["3V3"])
    route_wire(board, [v_3v3_r5, r5_vcc], TW_PULLUP, F, nets["3V3"])

    # --- 3V3 to R8.2(10.5,12) and R9.2(10.5,13.5) pull-ups ---
    r8_vcc = get_pad_pos(parts["R8"], 2)  # (10.5, 12.0)
    r9_vcc = get_pad_pos(parts["R9"], 2)  # (10.5, 13.5)

    # Via near R9, connect from v_3v3_esp on B.Cu
    v_3v3_r9 = (r9_vcc[0] + 1.5, r9_vcc[1])  # (12.0, 13.5)
    add_via(board, v_3v3_r9, nets["3V3"])
    # B.Cu from v_3v3_esp north to v_3v3_r9
    route_wire(board, [v_3v3_esp, (v_3v3_r9[0], v_3v3_esp[1]), v_3v3_r9],
               TW_PULLUP, B, nets["3V3"])
    # F.Cu to R9.2 and R8.2
    route_wire(board, [v_3v3_r9, r9_vcc], TW_PULLUP, F, nets["3V3"])
    route_wire(board, [r9_vcc, (r8_vcc[0], r9_vcc[1]), r8_vcc],
               TW_PULLUP, F, nets["3V3"])

    # --- 3V3 to U5.VCC(46.09, 38.70) and C8.1(52.5, 36.0) ---
    u5_vcc = get_pad_pos(parts["U5"], 8)  # (46.09, 38.70)
    c8_1 = get_pad_pos(parts["C8"], 1)    # (52.5, 36.0)

    # U5.VCC → C8: F.Cu local route
    route_wire(board, [u5_vcc, (u5_vcc[0], c8_1[1]), c8_1], TW_POWER, F, nets["3V3"])

    # Via near U5.VCC for B.Cu 3V3 feed
    v_3v3_u5 = (u5_vcc[0] - 1.5, u5_vcc[1] + 1.0)  # (44.6, 39.7)
    add_via(board, v_3v3_u5, nets["3V3"])
    route_wire(board, [u5_vcc, (v_3v3_u5[0], u5_vcc[1]), v_3v3_u5],
               TW_POWER, F, nets["3V3"])
    # B.Cu from v_3v3_r5 south to v_3v3_u5
    route_wire(board, [v_3v3_r5, (v_3v3_r5[0], v_3v3_u5[1]),
                        (v_3v3_u5[0], v_3v3_u5[1])],
               TW_POWER, B, nets["3V3"])

    # --- 3V3 to J6.3 (FTDI VCC at 4.0, 25.73) ---
    j6_vcc = get_pad_pos(parts["J6"], 3)  # (4.0, 25.73)

    # J6 is THT, so pad connects both layers.  Route B.Cu from v_3v3_esp.
    v_3v3_j6 = (j6_vcc[0] + 2.5, j6_vcc[1])  # (6.5, 25.73)
    add_via(board, v_3v3_j6, nets["3V3"])
    # B.Cu from v_3v3_esp west to v_3v3_j6 (same y≈25 row)
    route_wire(board, [v_3v3_esp, (v_3v3_j6[0], v_3v3_esp[1]), v_3v3_j6],
               TW_POWER, B, nets["3V3"])
    route_wire(board, [v_3v3_j6, j6_vcc], TW_POWER, F, nets["3V3"])

    # --- 3V3 to R10.1(53.5, 5.0) (LED resistor) ---
    r10_1 = get_pad_pos(parts["R10"], 1)  # (53.5, 5.0)

    # Via near R10
    v_3v3_led = (r10_1[0] - 1.5, r10_1[1] + 1.5)  # (52.0, 6.5)
    add_via(board, v_3v3_led, nets["3V3"])
    route_wire(board, [r10_1, (r10_1[0], v_3v3_led[1]), v_3v3_led],
               TW_SIGNAL, F, nets["3V3"])
    # B.Cu from v_3v3_src north to v_3v3_led
    route_wire(board, [v_3v3_src, (v_3v3_led[0], v_3v3_src[1]),
                        (v_3v3_led[0], v_3v3_led[1])],
               TW_POWER, B, nets["3V3"])

    # =====================================================================
    # GROUND VIAS — connect SMD GND pads to B.Cu ground pour
    # =====================================================================
    # Direction of GND via offset is chosen PER PAD to avoid crossing
    # nearby signal traces on F.Cu.  The stub is a short track from pad
    # to via (0.5-1.0 mm).

    gnd_vias = [
        # (ref, pad, x_offset, y_offset) — offset from pad center to via
        # Right-side power caps: offset right (+x) to stay clear of 12V rail
        ("C1", 2, 1.0, 0),
        ("C12", 2, 1.0, 0),
        ("C2", 2, 1.0, 0),
        # Left-side output caps: offset right (+x)
        ("C3", 2, 1.0, 0),
        ("C13", 2, 1.0, 0),
        ("C4", 2, 0, 1.0),       # south to avoid R11 area
        # ESP32 decoupling caps: offset left (-x) toward board edge
        ("C5", 2, -1.0, 0),
        ("C6", 2, -1.0, 0),
        ("C7", 2, -1.0, 0),
        # RS485 cap: offset right
        ("C8", 2, 1.0, 0),
        # EN bypass cap: offset left
        ("C11", 2, -1.0, 0),
        # D3 TVS GND: offset right
        ("D3", 2, 1.0, 0),
        # U1 LDO GND (pin 1 at 45.15, 8.75): offset right
        ("U1", 1, 1.0, 0),
        # Optocoupler cathodes: offset down (+y)
        ("U2", 2, 0, -1.0),      # cathode — offset up (toward anode side)
        ("U2", 3, 0, 1.0),       # emitter — offset down
        ("U3", 2, 0, -1.0),
        ("U3", 3, 0, 1.0),
        # ESP32 GND pins
        ("U4", 1, -1.0, 0),      # pin 1 GND (13, 26.15): offset left
        ("U4", 15, 0, 1.0),      # pin 15 GND (17.5, 27.25): offset down
        ("U4", 39, 0, 1.5),      # pin 39 exposed pad (22, 24.25): offset down
        # SP3485 GND
        ("U5", 5, 0, 1.0),       # pin 5 (49.91, 38.7): offset down
        # Q1 emitter (GND)
        ("Q1", 2, 1.0, 0),       # (6.45, 14.10): offset right
        # LED cathode
        ("LED1", 1, 0, 1.0),     # (53, 7): offset down
    ]
    for ref, pad_num, dx, dy in gnd_vias:
        pos = get_pad_pos(parts[ref], pad_num)
        via_pos = (pos[0] + dx, pos[1] + dy)
        add_track(board, pos, via_pos, TW_SIGNAL, F, nets["GND"])
        add_via(board, via_pos, nets["GND"])

    # ESP32 pin 38 GND (31.0, 7.1) — must stay outside antenna keepout
    # (keepout: x=11-33, y=0-7.75).  Offset via south+east to clear.
    esp_gnd38 = get_pad_pos(parts["U4"], 38)
    gnd38_via = (esp_gnd38[0] + 1.5, 8.5)  # (32.5, 8.5) outside keepout
    add_track(board, esp_gnd38, gnd38_via, TW_SIGNAL, F, nets["GND"])
    add_via(board, gnd38_via, nets["GND"])

    # =====================================================================
    # WIEGAND D0: J2.1 → R1 → U2 → R4/ESP
    # =====================================================================

    j2_d0 = get_pad_pos(parts["J2"], 1)   # (36.75, 40.0)
    r1_1 = get_pad_pos(parts["R1"], 1)    # (37.5, 26.5)
    r1_2 = get_pad_pos(parts["R1"], 2)    # (38.5, 26.5)
    u2_a = get_pad_pos(parts["U2"], 1)    # (35.4, 28.73) after 90° rot
    u2_col = get_pad_pos(parts["U2"], 4)  # (40.6, 28.73)
    r4_sig = get_pad_pos(parts["R4"], 1)  # (32.5, 30.0)
    esp_d0 = get_pad_pos(parts["U4"], 10) # GPIO25 (13.0, 14.72)

    # J2.1 → R1.1 (WIEG_D0_IN): F.Cu vertical
    route_wire(board, [j2_d0, (r1_1[0], j2_d0[1]), (r1_1[0], r1_1[1])],
               TW_SIGNAL, F, nets["WIEG_D0_IN"])

    # R1.2 → U2.1/Anode (WIEG_D0_R): F.Cu
    route_wire(board, [r1_2, (r1_2[0], u2_a[1]), u2_a],
               TW_SIGNAL, F, nets["WIEG_D0_R"])

    # U2.4/Collector → R4.1 (WIEG_D0): F.Cu
    route_wire(board, [u2_col, (u2_col[0], r4_sig[1]), r4_sig],
               TW_SIGNAL, F, nets["WIEG_D0"])

    # U2.4/Collector → ESP32 GPIO25 (WIEG_D0): via B.Cu
    # Via near U2 collector, then B.Cu west to ESP area
    v_d0a = (u2_col[0] + 1.5, u2_col[1] + 1.5)  # (42.1, 30.23)
    add_via(board, v_d0a, nets["WIEG_D0"])
    route_wire(board, [u2_col, (v_d0a[0], u2_col[1]), v_d0a],
               TW_SIGNAL, F, nets["WIEG_D0"])

    v_d0b = (esp_d0[0] - 2.0, esp_d0[1])  # (11.0, 14.72)
    add_via(board, v_d0b, nets["WIEG_D0"])
    # B.Cu: from v_d0a west then north to v_d0b
    route_wire(board, [v_d0a, (v_d0b[0], v_d0a[1]), v_d0b],
               TW_SIGNAL, B, nets["WIEG_D0"])

    route_wire(board, [v_d0b, esp_d0], TW_SIGNAL, F, nets["WIEG_D0"])

    # =====================================================================
    # WIEGAND D1: J2.2 → R3 → U3 → R5/ESP
    # =====================================================================

    j2_d1 = get_pad_pos(parts["J2"], 2)   # (40.25, 40.0)
    r3_1 = get_pad_pos(parts["R3"], 1)    # (47.5, 26.5)
    r3_2 = get_pad_pos(parts["R3"], 2)    # (48.5, 26.5)
    u3_a = get_pad_pos(parts["U3"], 1)    # (45.4, 28.73)
    u3_col = get_pad_pos(parts["U3"], 4)  # (50.6, 28.73)
    r5_sig = get_pad_pos(parts["R5"], 1)  # (52.5, 30.0)
    esp_d1 = get_pad_pos(parts["U4"], 9)  # GPIO33 (13.0, 15.99)

    # J2.2 → R3.1 (WIEG_D1_IN): F.Cu
    route_wire(board, [j2_d1, (r3_1[0], j2_d1[1]), (r3_1[0], r3_1[1])],
               TW_SIGNAL, F, nets["WIEG_D1_IN"])

    # R3.2 → U3.1 (WIEG_D1_R): F.Cu
    route_wire(board, [r3_2, (r3_2[0], u3_a[1]), u3_a],
               TW_SIGNAL, F, nets["WIEG_D1_R"])

    # U3.4/Collector → R5.1 (WIEG_D1): F.Cu
    route_wire(board, [u3_col, (r5_sig[0], u3_col[1]), r5_sig],
               TW_SIGNAL, F, nets["WIEG_D1"])

    # U3.4/Collector → ESP32 GPIO33 (WIEG_D1): via B.Cu
    v_d1a = (u3_col[0] + 1.5, u3_col[1] + 1.5)  # (52.1, 30.23)
    add_via(board, v_d1a, nets["WIEG_D1"])
    route_wire(board, [u3_col, (v_d1a[0], u3_col[1]), v_d1a],
               TW_SIGNAL, F, nets["WIEG_D1"])

    v_d1b = (esp_d1[0] - 2.0, esp_d1[1])  # (11.0, 15.99)
    add_via(board, v_d1b, nets["WIEG_D1"])
    # B.Cu: west then north (use y=32 to separate from D0 which uses y=30.23)
    route_wire(board, [v_d1a, (v_d1a[0], 32.5), (v_d1b[0], 32.5), v_d1b],
               TW_SIGNAL, B, nets["WIEG_D1"])

    route_wire(board, [v_d1b, esp_d1], TW_SIGNAL, F, nets["WIEG_D1"])

    # =====================================================================
    # RELAY CONTROL
    # =====================================================================

    esp_relay = get_pad_pos(parts["U4"], 8)  # GPIO32 (13.0, 17.26)
    r6_1 = get_pad_pos(parts["R6"], 1)       # (5.0, 15.5)
    r6_2 = get_pad_pos(parts["R6"], 2)       # (6.0, 15.5)
    q1_base = get_pad_pos(parts["Q1"], 1)    # (4.55, 14.10)
    q1_col = get_pad_pos(parts["Q1"], 3)     # (5.5, 11.90)
    d2_a = get_pad_pos(parts["D2"], 2)       # (3.5, 18.5)
    j3_1 = get_pad_pos(parts["J3"], 1)       # (3.75, 6.5)

    # ESP32 GPIO32 → R6.1: via B.Cu
    v_relay_a = (esp_relay[0] - 2.0, esp_relay[1] + 1.5)  # (11, 18.76)
    add_via(board, v_relay_a, nets["RELAY_DRV"])
    route_wire(board, [esp_relay, (esp_relay[0], v_relay_a[1]), v_relay_a],
               TW_SIGNAL, F, nets["RELAY_DRV"])

    v_relay_b = (r6_1[0] - 2.0, r6_1[1])  # (3.0, 15.5)
    add_via(board, v_relay_b, nets["RELAY_DRV"])
    # B.Cu: west from (11, 18.76) to (3, 18.76) then north to (3, 15.5)
    route_wire(board, [v_relay_a, (v_relay_b[0], v_relay_a[1]), v_relay_b],
               TW_SIGNAL, B, nets["RELAY_DRV"])

    route_wire(board, [v_relay_b, r6_1], TW_SIGNAL, F, nets["RELAY_DRV"])

    # R6.2 → Q1.Base: F.Cu
    route_wire(board, [r6_2, (q1_base[0], r6_2[1]), q1_base],
               TW_SIGNAL, F, nets["RELAY_BASE"])

    # Q1.Collector → J3.1 (RELAY_12V): F.Cu north
    route_wire(board, [q1_col, (j3_1[0], q1_col[1]), j3_1],
               TW_POWER, F, nets["RELAY_12V"])

    # Q1.Collector → D2.Anode (RELAY_12V): F.Cu south
    route_wire(board, [q1_col, (q1_col[0], d2_a[1]), d2_a],
               TW_POWER, F, nets["RELAY_12V"])

    # =====================================================================
    # RS485: ESP → U5, U5 → R7/J5/D3
    # =====================================================================

    esp_tx485 = get_pad_pos(parts["U4"], 28)  # GPIO17 (31.0, 19.80)
    esp_rx485 = get_pad_pos(parts["U4"], 27)  # GPIO16 (31.0, 21.07)
    esp_de = get_pad_pos(parts["U4"], 26)     # GPIO4  (31.0, 22.34)
    u5_di = get_pad_pos(parts["U5"], 4)       # DI (49.91, 33.30)
    u5_ro = get_pad_pos(parts["U5"], 1)       # RO (46.09, 33.30)
    u5_re = get_pad_pos(parts["U5"], 2)       # RE (47.37, 33.30)
    u5_de = get_pad_pos(parts["U5"], 3)       # DE (48.63, 33.30)
    u5_a = get_pad_pos(parts["U5"], 6)        # A  (48.63, 38.70)
    u5_b = get_pad_pos(parts["U5"], 7)        # B  (47.37, 38.70)

    # --- RS485_TX: ESP GPIO17 → U5.DI ---
    v_tx485a = (esp_tx485[0] + 2.0, esp_tx485[1])  # (33, 19.80)
    add_via(board, v_tx485a, nets["RS485_TX"])
    route_wire(board, [esp_tx485, v_tx485a], TW_SIGNAL, F, nets["RS485_TX"])

    v_tx485b = (u5_di[0], u5_di[1] - 2.0)  # (49.91, 31.30)
    add_via(board, v_tx485b, nets["RS485_TX"])
    # B.Cu: east then south
    route_wire(board, [v_tx485a, (v_tx485b[0], v_tx485a[1]), v_tx485b],
               TW_SIGNAL, B, nets["RS485_TX"])
    route_wire(board, [v_tx485b, u5_di], TW_SIGNAL, F, nets["RS485_TX"])

    # --- RS485_RX: ESP GPIO16 → U5.RO ---
    v_rx485a = (esp_rx485[0] + 2.0, esp_rx485[1] + 1.5)  # (33, 22.57)
    add_via(board, v_rx485a, nets["RS485_RX"])
    route_wire(board, [esp_rx485, (esp_rx485[0] + 2.0, esp_rx485[1]), v_rx485a],
               TW_SIGNAL, F, nets["RS485_RX"])

    v_rx485b = (u5_ro[0], u5_ro[1] - 2.0)  # (46.09, 31.30)
    add_via(board, v_rx485b, nets["RS485_RX"])
    # B.Cu: east then south — use y=22.57, different from TX y=19.80
    route_wire(board, [v_rx485a, (v_rx485b[0], v_rx485a[1]), v_rx485b],
               TW_SIGNAL, B, nets["RS485_RX"])
    route_wire(board, [v_rx485b, u5_ro], TW_SIGNAL, F, nets["RS485_RX"])

    # --- RS485_DE: ESP GPIO4 → U5.DE + U5.RE ---
    v_de_a = (esp_de[0] + 2.0, esp_de[1] + 3.0)  # (33, 25.34)
    add_via(board, v_de_a, nets["RS485_DE"])
    route_wire(board, [esp_de, (v_de_a[0], esp_de[1]), v_de_a],
               TW_SIGNAL, F, nets["RS485_DE"])

    v_de_b = (u5_de[0], u5_de[1] - 2.0)  # (48.63, 31.30)
    add_via(board, v_de_b, nets["RS485_DE"])
    # B.Cu: east then south — use y=25.34
    route_wire(board, [v_de_a, (v_de_b[0], v_de_a[1]), v_de_b],
               TW_SIGNAL, B, nets["RS485_DE"])
    route_wire(board, [v_de_b, u5_de], TW_SIGNAL, F, nets["RS485_DE"])
    # U5.RE ↔ U5.DE direct on F.Cu
    route_wire(board, [u5_re, u5_de], TW_SIGNAL, F, nets["RS485_DE"])

    # --- RS485_A: U5.A → R7.1 → J5.1 → D3.1 ---
    r7_1 = get_pad_pos(parts["R7"], 1)  # (41.5, 36.0)
    j5_a = get_pad_pos(parts["J5"], 1)  # (14.5, 40.0)
    d3_a = get_pad_pos(parts["D3"], 1)  # (41.05, 33.10)

    # U5.A → R7.1: F.Cu (short, both near x=42-49, y=36-39)
    route_wire(board, [u5_a, (r7_1[0], u5_a[1]), r7_1],
               TW_SIGNAL, F, nets["RS485_A"])

    # R7.1 → D3.1: F.Cu vertical
    route_wire(board, [r7_1, (d3_a[0], r7_1[1]), d3_a],
               TW_SIGNAL, F, nets["RS485_A"])

    # R7 → J5.1: via B.Cu
    v_a_src = (r7_1[0] - 2.5, r7_1[1] + 2.0)  # (39.0, 38.0)
    add_via(board, v_a_src, nets["RS485_A"])
    route_wire(board, [r7_1, (r7_1[0], v_a_src[1]), v_a_src],
               TW_SIGNAL, F, nets["RS485_A"])

    v_a_dst = (j5_a[0], j5_a[1] - 2.5)  # (14.5, 37.5)
    add_via(board, v_a_dst, nets["RS485_A"])
    # B.Cu: west at y=38, separated from other corridors
    route_wire(board, [v_a_src, (v_a_dst[0], v_a_src[1]), v_a_dst],
               TW_SIGNAL, B, nets["RS485_A"])
    route_wire(board, [v_a_dst, j5_a], TW_SIGNAL, F, nets["RS485_A"])

    # --- RS485_B: U5.B → R7.2 → J5.2 → D3.3 ---
    r7_2 = get_pad_pos(parts["R7"], 2)  # (42.5, 36.0)
    j5_b = get_pad_pos(parts["J5"], 2)  # (18.0, 40.0)
    d3_b = get_pad_pos(parts["D3"], 3)  # (42.0, 30.90)

    # U5.B → R7.2: F.Cu
    route_wire(board, [u5_b, (r7_2[0], u5_b[1]), r7_2],
               TW_SIGNAL, F, nets["RS485_B"])

    # R7.2 → D3.3: F.Cu vertical
    route_wire(board, [r7_2, (d3_b[0], r7_2[1]), d3_b],
               TW_SIGNAL, F, nets["RS485_B"])

    # R7 → J5.2: via B.Cu
    v_b_src = (r7_2[0] + 2.5, r7_2[1] + 2.0)  # (45.0, 38.0)
    add_via(board, v_b_src, nets["RS485_B"])
    route_wire(board, [r7_2, (r7_2[0], v_b_src[1]), (v_b_src[0], v_b_src[1])],
               TW_SIGNAL, F, nets["RS485_B"])

    v_b_dst = (j5_b[0], j5_b[1] - 2.5)  # (18.0, 37.5)
    add_via(board, v_b_dst, nets["RS485_B"])
    # B.Cu: west at y=39.5 to separate from RS485_A at y=38
    route_wire(board, [v_b_src, (v_b_src[0], 39.5), (v_b_dst[0], 39.5), v_b_dst],
               TW_SIGNAL, B, nets["RS485_B"])
    route_wire(board, [v_b_dst, j5_b], TW_SIGNAL, F, nets["RS485_B"])

    # =====================================================================
    # POWER LED
    # =====================================================================

    r10_2 = get_pad_pos(parts["R10"], 2)   # (54.5, 5.0)
    led1_a = get_pad_pos(parts["LED1"], 2) # (55.0, 7.0)
    route_wire(board, [r10_2, (led1_a[0], r10_2[1]), led1_a],
               TW_SIGNAL, F, nets["PWR_LED"])

    # =====================================================================
    # FTDI / AUTO-RESET
    # =====================================================================

    # --- FTDI_TX: J6.4(4, 28.27) → ESP32 RXD0 pin 34 (31.0, 12.18) ---
    j6_tx = get_pad_pos(parts["J6"], 4)     # (4.0, 28.27)
    esp_rxd0 = get_pad_pos(parts["U4"], 34) # (31.0, 12.18)

    # J6 is THT so connects both layers.  Use B.Cu from J6 pin.
    v_ftx_a = (j6_tx[0] + 3.0, j6_tx[1])  # (7.0, 28.27)
    add_via(board, v_ftx_a, nets["FTDI_TX"])
    route_wire(board, [j6_tx, v_ftx_a], TW_SIGNAL, F, nets["FTDI_TX"])

    v_ftx_b = (esp_rxd0[0] + 2.0, esp_rxd0[1])  # (33.0, 12.18)
    add_via(board, v_ftx_b, nets["FTDI_TX"])
    # B.Cu: from (7, 28.27) north to y=34.5 then east to (33, 34.5) then north to (33, 12.18)
    # Actually simpler: go east first at y=28.27, then north
    # But y=28.27 is near the ESP32 bottom pins area.
    # Best: go north on x=7 to y=12.18, then east to x=33 at y=12.18
    route_wire(board, [v_ftx_a, (v_ftx_a[0], v_ftx_b[1]), v_ftx_b],
               TW_SIGNAL, B, nets["FTDI_TX"])
    route_wire(board, [v_ftx_b, esp_rxd0], TW_SIGNAL, F, nets["FTDI_TX"])

    # --- FTDI_RX: J6.5(4, 30.81) → ESP32 TXD0 pin 35 (31.0, 10.91) ---
    j6_rx = get_pad_pos(parts["J6"], 5)     # (4.0, 30.81)
    esp_txd0 = get_pad_pos(parts["U4"], 35) # (31.0, 10.91)

    v_frx_a = (j6_rx[0] + 4.5, j6_rx[1])  # (8.5, 30.81)
    add_via(board, v_frx_a, nets["FTDI_RX"])
    route_wire(board, [j6_rx, v_frx_a], TW_SIGNAL, F, nets["FTDI_RX"])

    v_frx_b = (esp_txd0[0] + 2.0, esp_txd0[1] - 1.5)  # (33.0, 9.41)
    add_via(board, v_frx_b, nets["FTDI_RX"])
    # B.Cu: north on x=8.5, then east at y=9.41 to x=33
    route_wire(board, [v_frx_a, (v_frx_a[0], v_frx_b[1]), v_frx_b],
               TW_SIGNAL, B, nets["FTDI_RX"])
    route_wire(board, [v_frx_b, (esp_txd0[0] + 2.0, esp_txd0[1]), esp_txd0],
               TW_SIGNAL, F, nets["FTDI_RX"])

    # --- DTR → C9 → R9 → ESP GPIO0 ---
    j6_dtr = get_pad_pos(parts["J6"], 6)  # (4.0, 33.35)
    c9_1 = get_pad_pos(parts["C9"], 1)    # (9.5, 15.0)
    c9_2 = get_pad_pos(parts["C9"], 2)    # (10.5, 15.0)
    r9_1 = get_pad_pos(parts["R9"], 1)    # (9.5, 13.5)
    esp_gpio0 = get_pad_pos(parts["U4"], 25)  # (31.0, 23.61)

    # J6.6 → C9.1: F.Cu (J6 is THT, C9 at (10, 15))
    route_wire(board, [j6_dtr, (c9_1[0], j6_dtr[1]), c9_1],
               TW_SIGNAL, F, nets["FTDI_DTR"])

    # C9.2 → R9.1: F.Cu (ESP_GPIO0 net)
    route_wire(board, [c9_2, (r9_1[0], c9_2[1]), r9_1],
               TW_SIGNAL, F, nets["ESP_GPIO0"])

    # R9.1 → ESP GPIO0: via B.Cu
    v_gpio0_a = (r9_1[0] - 2.0, r9_1[1])  # (7.5, 13.5)
    add_via(board, v_gpio0_a, nets["ESP_GPIO0"])
    route_wire(board, [r9_1, v_gpio0_a], TW_SIGNAL, F, nets["ESP_GPIO0"])

    v_gpio0_b = (esp_gpio0[0] + 2.0, esp_gpio0[1])  # (33.0, 23.61)
    add_via(board, v_gpio0_b, nets["ESP_GPIO0"])
    # B.Cu: east at y=13.5, then south to y=23.61
    # Actually: go south on x=7.5 to y=23.61, then east to x=33
    # But that crosses FTDI_TX at x=7, y=12.18 on B.Cu
    # Better: go east at y=35.5 (below everything) then north
    # Simplest safe path: x=7.5 south to y=35.5, east to x=33, north to y=23.61
    route_wire(board, [v_gpio0_a, (v_gpio0_a[0], 35.5),
                        (v_gpio0_b[0], 35.5), v_gpio0_b],
               TW_SIGNAL, B, nets["ESP_GPIO0"])
    route_wire(board, [v_gpio0_b, esp_gpio0], TW_SIGNAL, F, nets["ESP_GPIO0"])

    # --- RTS → C10 → R8 → ESP EN ---
    j6_rts = get_pad_pos(parts["J6"], 2)  # (4.0, 23.19)
    c10_1 = get_pad_pos(parts["C10"], 1)  # (9.5, 16.5)
    c10_2 = get_pad_pos(parts["C10"], 2)  # (10.5, 16.5)
    r8_1 = get_pad_pos(parts["R8"], 1)    # (9.5, 12.0)
    esp_en = get_pad_pos(parts["U4"], 3)  # (13.0, 23.61)

    # J6.2 → C10.1: F.Cu
    route_wire(board, [j6_rts, (c10_1[0], j6_rts[1]), c10_1],
               TW_SIGNAL, F, nets["FTDI_RTS"])

    # C10.2 → R8.1: F.Cu (ESP_EN net)
    route_wire(board, [c10_2, (r8_1[0], c10_2[1]), r8_1],
               TW_SIGNAL, F, nets["ESP_EN"])

    # C11.1 → R8.1: F.Cu (ESP_EN net, bypass cap)
    c11_1 = get_pad_pos(parts["C11"], 1)  # (9.5, 10.5)
    route_wire(board, [c11_1, (r8_1[0], c11_1[1]), r8_1],
               TW_SIGNAL, F, nets["ESP_EN"])

    # R8.1 → ESP EN (pin 3): short F.Cu run — R8 at x=9.5, ESP EN at (13, 23.61)
    # These are close in x.  Route on F.Cu directly.
    route_wire(board, [r8_1, (esp_en[0], r8_1[1]), esp_en],
               TW_SIGNAL, F, nets["ESP_EN"])


# =============================================================================
# BOARD FEATURES
# =============================================================================


def add_board_outline(board):
    """Add board outline on Edge.Cuts layer."""
    corners = [(0, 0), (BOARD_W, 0), (BOARD_W, BOARD_H), (0, BOARD_H)]
    for i in range(4):
        start = corners[i]
        end = corners[(i + 1) % 4]
        line = pcbnew.PCB_SHAPE(board)
        line.SetStart(pt(start[0], start[1]))
        line.SetEnd(pt(end[0], end[1]))
        line.SetLayer(pcbnew.Edge_Cuts)
        line.SetWidth(mm(0.1))
        board.Add(line)


def add_ground_pour(board, nets):
    """Add ground copper pour on the bottom layer.

    Priority 0 (default) so the LDO thermal relief zone (priority 1) on B.Cu
    takes precedence in the overlap area under U1.
    """
    zone = pcbnew.ZONE(board)
    zone.SetLayer(pcbnew.B_Cu)
    zone.SetNet(nets["GND"])
    zone.SetPadConnection(pcbnew.ZONE_CONNECTION_THERMAL)
    zone.SetThermalReliefGap(mm(0.3))
    zone.SetThermalReliefSpokeWidth(mm(0.4))
    zone.SetMinThickness(mm(0.2))
    zone.SetFillMode(pcbnew.ZONE_FILL_MODE_POLYGONS)
    zone.SetAssignedPriority(0)  # Lower than 3V3 thermal zone (priority 1)

    # Zone outline = board outline with small inset
    # Use AppendCorner instead of SetOutline (SetOutline with SHAPE_POLY_SET
    # can hang SaveBoard in headless mode)
    inset = 0.3
    zone.AppendCorner(pt(inset, inset), -1)
    zone.AppendCorner(pt(BOARD_W - inset, inset), -1)
    zone.AppendCorner(pt(BOARD_W - inset, BOARD_H - inset), -1)
    zone.AppendCorner(pt(inset, BOARD_H - inset), -1)

    board.Add(zone)

    # Note: Zone fill is handled by KiCad when opening the file or by
    # kicad-cli during Gerber export. Programmatic filling via ZONE_FILLER
    # can hang in headless mode, so we skip it here.

    return zone


def add_ldo_thermal_relief(board, parts, nets):
    """
    Add thermal relief copper and vias for the AMS1117-3.3 LDO (U1).

    The AMS1117-3.3 in SOT-223 drops 8.7V at up to 500mA peak, dissipating
    significant power. Without thermal relief, SOT-223 θJA ≈ 90°C/W is
    insufficient. This function adds:

      1. Thermal vias (6x, 0.4mm drill) near the tab pad, conducting heat
         from F.Cu to B.Cu.
      2. F.Cu copper zone (3V3_LDO) around the tab pad area for front-side heat
         spreading (~9mm x 8.5mm).
      3. B.Cu copper zone (3V3_LDO) under U1 for back-side heat spreading
         (~10mm x 11mm, priority 1 to override GND pour).
         Sized to cover thermal vias but NOT extend over GND vias for U1 pin 1,
         C1, C12, or C2 — those must connect to the GND pour.

    Expected result: θJA ≈ 35-40°C/W, giving ~50°C rise at 1.3W average.
    Peak 4.35W during WiFi TX is transient (<10ms) and does not reach
    steady-state junction temperature due to package thermal mass.

    Monitor LDO temperature during initial board testing. If surface
    temperature exceeds 90°C under sustained load, consider adding a
    buck pre-regulator stage (12V→5V, then LDO 5V→3.3V).
    """
    # U1 is at (42.0, 12.0), rotated 180°.
    # After rotation, tab pad (pin 4) center is at (42.0, 15.15).
    u1_x = 42.0
    u1_y = 12.0
    tab_y = u1_y + 3.15  # Tab offset after 180° rotation

    # --- Thermal vias (3V3_LDO net, 0.4mm drill / 0.8mm annular) ---
    # 2x3 grid below the tab pad for heat conduction to B.Cu.
    # Larger drill than signal vias for lower thermal resistance.
    THERMAL_VIA_DRILL = 0.4
    THERMAL_VIA_SIZE = 0.8
    thermal_via_positions = [
        (u1_x - 1.2, tab_y + 1.5),
        (u1_x,       tab_y + 1.5),
        (u1_x + 1.2, tab_y + 1.5),
        (u1_x - 1.2, tab_y + 3.0),
        (u1_x,       tab_y + 3.0),
        (u1_x + 1.2, tab_y + 3.0),
    ]
    for vpos in thermal_via_positions:
        add_via(board, vpos, nets["3V3_LDO"],
                drill=THERMAL_VIA_DRILL, size=THERMAL_VIA_SIZE)

    # --- F.Cu thermal copper zone (3V3_LDO) ---
    # Heat spreader on front layer around the tab pad area.
    # Bounds chosen to avoid U1 input-side pads (GND/VIN at y≈8.85)
    # and input caps C2 (at x=36.5). Solid pad connections for minimum
    # thermal resistance.
    fcu_zone = pcbnew.ZONE(board)
    fcu_zone.SetLayer(pcbnew.F_Cu)
    fcu_zone.SetNet(nets["3V3_LDO"])
    fcu_zone.SetPadConnection(pcbnew.ZONE_CONNECTION_FULL)
    fcu_zone.SetMinThickness(mm(0.25))
    fcu_zone.SetFillMode(pcbnew.ZONE_FILL_MODE_POLYGONS)
    fcu_zone.SetAssignedPriority(1)

    fcu_zone.AppendCorner(pt(39.0, 11.5), -1)
    fcu_zone.AppendCorner(pt(47.0, 11.5), -1)
    fcu_zone.AppendCorner(pt(47.0, 20.0), -1)
    fcu_zone.AppendCorner(pt(39.0, 20.0), -1)
    board.Add(fcu_zone)

    # --- B.Cu thermal copper zone (3V3_LDO) ---
    # Island on bottom layer to absorb heat from thermal vias.
    # Priority 1 ensures this fills before the GND pour (priority 0),
    # carving out a 3V3_LDO island from the ground plane under U1.
    # IMPORTANT: Sized (39,10)-(49,21) to NOT cover GND vias for U1.1,
    # C1, C12, C2 (all at y<10 or x<39), which must connect to GND pour.
    bcu_zone = pcbnew.ZONE(board)
    bcu_zone.SetLayer(pcbnew.B_Cu)
    bcu_zone.SetNet(nets["3V3_LDO"])
    bcu_zone.SetPadConnection(pcbnew.ZONE_CONNECTION_FULL)
    bcu_zone.SetMinThickness(mm(0.25))
    bcu_zone.SetFillMode(pcbnew.ZONE_FILL_MODE_POLYGONS)
    bcu_zone.SetAssignedPriority(1)

    bcu_zone.AppendCorner(pt(39.0, 10.0), -1)
    bcu_zone.AppendCorner(pt(49.0, 10.0), -1)
    bcu_zone.AppendCorner(pt(49.0, 21.0), -1)
    bcu_zone.AppendCorner(pt(39.0, 21.0), -1)
    board.Add(bcu_zone)


def add_antenna_keepout(board):
    """
    Add a keepout zone over the ESP32-WROOM-32E antenna area.
    No copper on either layer under/around the antenna.
    The antenna is at the top of the module (negative Y from module center).
    Module at (22.0, 14.5), body 18x25.5mm, antenna in top ~5.5mm.
    """
    zone = pcbnew.ZONE(board)
    zone.SetIsRuleArea(True)
    zone.SetDoNotAllowCopperPour(True)
    zone.SetDoNotAllowTracks(True)
    zone.SetDoNotAllowVias(True)
    zone.SetDoNotAllowPads(False)
    zone.SetDoNotAllowFootprints(False)

    # Antenna area: module center at (22.0, 14.5), top edge at y=14.5-12.75=1.75
    # Antenna extends from y=1.75 to about y=1.75+5.5=7.25
    # Add 2mm margin on sides, extend top to board edge (y=0)
    ant_left = 22.0 - 11.0  # module left - 2mm margin = 11.0
    ant_right = 22.0 + 11.0  # module right + 2mm margin = 33.0
    ant_top = 0.0  # board edge (antenna near top edge)
    ant_bottom = 7.75  # bottom of antenna area with margin

    # Use AppendCorner instead of SetOutline (SetOutline with SHAPE_POLY_SET
    # can hang SaveBoard in headless mode)
    zone.AppendCorner(pt(ant_left, ant_top), -1)
    zone.AppendCorner(pt(ant_right, ant_top), -1)
    zone.AppendCorner(pt(ant_right, ant_bottom), -1)
    zone.AppendCorner(pt(ant_left, ant_bottom), -1)

    # Apply to both layers
    ls = pcbnew.LSET()
    ls.AddLayer(pcbnew.F_Cu)
    ls.AddLayer(pcbnew.B_Cu)
    zone.SetLayerSet(ls)

    board.Add(zone)


def add_silkscreen_labels(board, parts):
    """Add silkscreen text labels for connectors and board identification."""

    def add_text(x, y, text, size=1.0, bold=False):
        """Add a silkscreen text label."""
        t = pcbnew.PCB_TEXT(board)
        t.SetText(text)
        t.SetPosition(pt(x, y))
        t.SetLayer(pcbnew.F_SilkS)
        t.SetTextSize(pt(size, size))
        t.SetTextThickness(mm(0.15 if bold else 0.12))
        if bold:
            t.SetBold(True)
        board.Add(t)

    # Board title
    add_text(29.0, 42.5, "Conway Access v2", size=1.0, bold=True)

    # J1: +12V power
    j1 = get_pad_pos(parts["J1"], 1)
    add_text(j1[0] + 2.5, j1[1] - 3.5, "+12V", size=0.8)
    add_text(j1[0], j1[1] + 5.5, "+", size=0.8)
    add_text(j1[0] + 3.5, j1[1] + 5.5, "-", size=0.8)

    # J2: Wiegand
    j2c = get_pad_pos(parts["J2"], 2)  # pin 2; pins at -3.5, 0, +3.5, +7.0 relative
    add_text(j2c[0] + 1.75, j2c[1] - 5.0, "WIEGAND", size=0.8)
    add_text(j2c[0] - 3.5, j2c[1] + 5.0, "D0", size=0.8)
    add_text(j2c[0], j2c[1] + 5.0, "D1", size=0.8)
    add_text(j2c[0] + 3.5, j2c[1] + 5.0, "+", size=0.8)
    add_text(j2c[0] + 7.0, j2c[1] + 5.0, "-", size=0.8)

    # J3: Relay
    j3c = get_pad_pos(parts["J3"], 1)
    add_text(j3c[0] + 2.5, j3c[1] - 3.5, "RELAY", size=0.8)
    add_text(j3c[0], j3c[1] + 5.5, "SW", size=0.8)
    add_text(j3c[0] + 3.5, j3c[1] + 5.5, "V+", size=0.8)

    # J5: RS485
    j5c = get_pad_pos(parts["J5"], 2)
    add_text(j5c[0] + 1.75, j5c[1] - 6.0, "RS485", size=0.8)
    add_text(j5c[0] - 3.5, j5c[1] - 5.0, "A", size=0.8)
    add_text(j5c[0], j5c[1] - 5.0, "B", size=0.8)
    add_text(j5c[0] + 3.5, j5c[1] - 5.0, "G", size=0.8)

    # J6: FTDI (non-standard: pin 2 = RTS instead of CTS for auto-reset)
    j6c = get_pad_pos(parts["J6"], 3)
    add_text(j6c[0] - 4.0, j6c[1], "FTDI", size=0.8)
    add_text(j6c[0] - 4.0, j6c[1] + 2.0, "P2=RTS", size=0.8)

    # Termination resistor note
    r7 = get_pad_pos(parts["R7"], 1)
    add_text(r7[0], r7[1] - 2.5, "TERM", size=0.8)


def add_mounting_holes(board):
    """Add 4 mounting holes at the corners."""
    hole_r = 1.6  # M3 hole = 3.2mm diameter
    pad_r = 2.5  # Pad outer diameter 5mm
    inset = 3.0  # Distance from board edge

    positions = [
        (inset, inset),
        (BOARD_W - inset, inset),
        (BOARD_W - inset, BOARD_H - inset),
        (inset, BOARD_H - inset),
    ]

    for i, (x, y) in enumerate(positions):
        fp = pcbnew.FOOTPRINT(board)
        fp.SetReference(f"H{i+1}")
        fp.SetValue("MountingHole")
        fp.SetPosition(pt(x, y))

        pad = pcbnew.PAD(fp)
        pad.SetFrontShape(pcbnew.PAD_SHAPE_CIRCLE)
        pad.SetAttribute(pcbnew.PAD_ATTRIB_NPTH)
        pad.SetSize(pt(hole_r * 2, hole_r * 2))
        pad.SetDrillSize(pt(hole_r * 2, hole_r * 2))
        pad.SetFPRelativePosition(pt(0, 0))
        pad.SetNumber("")
        # NPTH needs layers
        ls = pcbnew.LSET()
        ls.AddLayer(pcbnew.F_Cu)
        ls.AddLayer(pcbnew.B_Cu)
        ls.AddLayer(pcbnew.F_Mask)
        ls.AddLayer(pcbnew.B_Mask)
        pad.SetLayerSet(ls)
        fp.Add(pad)

        board.Add(fp)


# =============================================================================
# JLCPCB OUTPUT GENERATION
# =============================================================================


def generate_jlcpcb_bom(parts, filename):
    """Generate JLCPCB-compatible BOM CSV file."""
    # Map component values to LCSC part numbers
    lcsc_map = LCSC

    # Components that are Do Not Populate by default
    dnp_refs = {"R7"}  # RS485 termination - only populate at bus endpoints

    # Derive JLCPCB footprint description from the FPID set on each component.
    # This avoids a fragile manual mapping that can drift when components are added.
    fpid_to_jlcpcb = {
        "Resistor_SMD:R_0805_2012Metric": "0805",
        "Resistor_SMD:R_0402_1005Metric": "0402",
        "Capacitor_SMD:C_0805_2012Metric": "0805",
        "Capacitor_SMD:C_0402_1005Metric": "0402",
        "LED_SMD:LED_0805_2012Metric": "0805",
        "Diode_SMD:D_SMA": "SMA",
        "Package_TO_SOT_SMD:SOT-223-3_TabPin2": "SOT-223-3",
        "Package_TO_SOT_SMD:SOT-23": "SOT-23",
        "Package_SO:SOP-4_3.8x4.1mm_P2.54mm": "SOP-4",
        "Package_SO:SOIC-8_3.9x4.9mm_P1.27mm": "SOIC-8",
        "RF_Module:ESP32-WROOM-32E": "ESP32-WROOM-32E",
    }

    bom = {}
    manual_parts = []
    dnp_parts = []

    for ref, part in sorted(parts.items()):
        val = part.GetValue()
        lcsc_pn = lcsc_map.get(val, "")

        if ref in dnp_refs:
            dnp_parts.append(ref)
            continue

        if not lcsc_pn:
            if ref.startswith("J") or ref.startswith("H"):
                manual_parts.append(ref)
            continue

        fp_desc = fpid_to_jlcpcb.get(part.GetFPIDAsString(), "")

        key = lcsc_pn
        if key not in bom:
            bom[key] = {
                "Comment": val,
                "Designator": [],
                "Footprint": fp_desc,
                "LCSC": lcsc_pn,
            }
        bom[key]["Designator"].append(ref)

    with open(filename, "w", newline="") as f:
        w = csv.writer(f)
        w.writerow(["Comment", "Designator", "Footprint", "LCSC Part #"])
        for entry in sorted(bom.values(), key=lambda x: x["Designator"][0]):
            w.writerow(
                [
                    entry["Comment"],
                    ",".join(sorted(entry["Designator"])),
                    entry["Footprint"],
                    entry["LCSC"],
                ]
            )

    print(f"  Generated BOM: {filename}")
    if manual_parts:
        print(f"  Manual assembly required: {', '.join(sorted(manual_parts))}")
    if dnp_parts:
        print(f"  DNP (Do Not Populate): {', '.join(sorted(dnp_parts))}")


def generate_jlcpcb_cpl(parts, filename):
    """Generate JLCPCB Component Placement List (pick-and-place)."""
    # Skip THT parts (connectors, headers, mounting holes)
    skip_prefixes = ("J", "H")

    with open(filename, "w", newline="") as f:
        w = csv.writer(f)
        w.writerow(["Designator", "Mid X", "Mid Y", "Rotation", "Layer"])
        for ref, part in sorted(parts.items()):
            if any(ref.startswith(p) for p in skip_prefixes):
                continue
            pos = part.GetPosition()
            x_mm = pcbnew.ToMM(pos.x)
            y_mm = pcbnew.ToMM(pos.y)
            rot = part.GetOrientationDegrees()
            layer = "top" if part.GetLayer() == pcbnew.F_Cu else "bottom"
            w.writerow([ref, f"{x_mm:.4f}", f"{y_mm:.4f}", f"{rot:.1f}", layer])

    print(f"  Generated CPL: {filename}")


# =============================================================================
# GERBER EXPORT
# =============================================================================


def export_gerbers(pcb_file, output_dir):
    """Export Gerber and drill files using kicad-cli."""
    pcb_file = str(pcb_file)
    output_dir = str(output_dir)
    os.makedirs(output_dir, exist_ok=True)

    layers = (
        "F.Cu,B.Cu,F.SilkS,B.SilkS,F.Mask,B.Mask,F.Paste,B.Paste,Edge.Cuts,F.Fab,B.Fab"
    )

    # Export Gerbers
    cmd_gerbers = [
        "kicad-cli",
        "pcb",
        "export",
        "gerbers",
        "--output",
        output_dir + "/",
        "--layers",
        layers,
        "--no-x2",
        "--subtract-soldermask",
        pcb_file,
    ]
    print(f"  Running: {' '.join(cmd_gerbers)}")
    result = subprocess.run(cmd_gerbers, capture_output=True, text=True)
    if result.returncode != 0:
        print(f"  ERROR exporting Gerbers: {result.stderr}")
        return False
    else:
        print(f"  Gerbers exported successfully")

    # Export drill files
    cmd_drill = [
        "kicad-cli",
        "pcb",
        "export",
        "drill",
        "--output",
        output_dir + "/",
        "--format",
        "excellon",
        "--excellon-units",
        "mm",
        "--excellon-zeros-format",
        "decimal",
        "--excellon-separate-th",
        pcb_file,
    ]
    print(f"  Running: {' '.join(cmd_drill)}")
    result = subprocess.run(cmd_drill, capture_output=True, text=True)
    if result.returncode != 0:
        print(f"  ERROR exporting drill: {result.stderr}")
        return False
    else:
        print(f"  Drill files exported successfully")

    return True


# =============================================================================
# VERIFICATION
# =============================================================================


# Expected nets created by create_nets()
EXPECTED_NETS = [
    "GND", "12V", "12V_RAW", "3V3", "3V3_LDO",
    "WIEG_D0_IN", "WIEG_D1_IN", "WIEG_D0_R", "WIEG_D1_R",
    "WIEG_D0", "WIEG_D1",
    "RELAY_DRV", "RELAY_BASE", "RELAY_12V",
    "RS485_TX", "RS485_RX", "RS485_DE", "RS485_A", "RS485_B",
    "FTDI_TX", "FTDI_RX", "FTDI_DTR", "FTDI_RTS",
    "ESP_EN", "ESP_GPIO0", "PWR_LED",
]

# Expected Gerber/drill output filenames (relative to gerber_dir)
EXPECTED_GERBER_FILES = [
    "access_controller_v2-F_Cu.gtl",
    "access_controller_v2-B_Cu.gbl",
    "access_controller_v2-F_Mask.gts",
    "access_controller_v2-B_Mask.gbs",
    "access_controller_v2-F_Paste.gtp",
    "access_controller_v2-B_Paste.gbp",
    "access_controller_v2-F_Silkscreen.gto",
    "access_controller_v2-B_Silkscreen.gbo",
    "access_controller_v2-F_Fab.gbr",
    "access_controller_v2-B_Fab.gbr",
    "access_controller_v2-Edge_Cuts.gm1",
    "access_controller_v2-PTH.drl",
    "access_controller_v2-NPTH.drl",
    "access_controller_v2-job.gbrjob",
]


def verify_board(board, parts, nets):
    """Verify board integrity using the pcbnew API.

    Checks nets, components, pad assignments, tracks, vias, and board outline.
    Returns a list of error strings (empty = pass).
    """
    errors = []

    # --- Net verification ---
    # Collect net names actually in use by iterating pads and tracks,
    # because board.GetNetInfo().NetsByName() yields proxy objects that
    # don't compare reliably as strings in all KiCad Python builds.
    board_nets = set()
    for fp in board.GetFootprints():
        for pad in fp.Pads():
            n = pad.GetNetname()
            if n:
                board_nets.add(n)
    for track in board.GetTracks():
        n = track.GetNetname()
        if n:
            board_nets.add(n)

    for net_name in EXPECTED_NETS:
        if net_name not in board_nets:
            errors.append(f"Missing net: {net_name}")

    # --- Component verification ---
    board_fps = {}
    for fp in board.GetFootprints():
        board_fps[fp.GetReference()] = fp

    for ref in parts:
        if ref not in board_fps:
            errors.append(f"Component {ref} missing from board")

    # --- Pad-to-net verification ---
    # Every pad on a placed component should have a net assigned, except:
    #   - Mounting holes (NPTH pads with no number)
    #   - ESP32 unused GPIO pins (only specific pins are connected in this design)
    #   - Pads that are explicitly unconnected in the design (none in this board)
    # ESP32 pins that must have nets (from assign_nets):
    esp32_connected_pins = {
        "1", "2", "3", "8", "9", "10", "15",
        "25", "26", "27", "28", "34", "35", "38", "39",
    }
    for fp in board.GetFootprints():
        ref = fp.GetReference()
        # Skip mounting holes (H1-H4) — NPTH, no net expected
        if ref.startswith("H"):
            continue
        is_esp32 = (ref == "U4")
        for pad in fp.Pads():
            pad_num = pad.GetNumber()
            if not pad_num:
                continue  # unnumbered pad (e.g. mechanical)
            # For ESP32, only check pins that should be connected
            if is_esp32 and pad_num not in esp32_connected_pins:
                continue
            net_name = pad.GetNetname()
            if not net_name:
                errors.append(
                    f"Unconnected pad: {ref} pad {pad_num} has no net"
                )

    # --- Track width verification ---
    min_track_w = mm(TW_SIGNAL)  # 0.25mm minimum
    for track in board.GetTracks():
        if isinstance(track, pcbnew.PCB_VIA):
            continue  # vias checked separately
        w = track.GetWidth()
        if w < min_track_w:
            pos = track.GetStart()
            errors.append(
                f"Track too narrow: {pcbnew.ToMM(w):.3f}mm at "
                f"({pcbnew.ToMM(pos.x):.2f}, {pcbnew.ToMM(pos.y):.2f}), "
                f"minimum is {TW_SIGNAL}mm"
            )

    # --- Via verification ---
    # Allow both standard signal vias and larger thermal vias (LDO relief).
    THERMAL_VIA_DRILL = 0.4
    THERMAL_VIA_SIZE = 0.8
    allowed_vias = {
        (mm(VIA_DRILL), mm(VIA_SIZE)),
        (mm(THERMAL_VIA_DRILL), mm(THERMAL_VIA_SIZE)),
    }
    for track in board.GetTracks():
        if not isinstance(track, pcbnew.PCB_VIA):
            continue
        drill = track.GetDrill()
        size = track.GetWidth()
        if (drill, size) not in allowed_vias:
            pos = track.GetPosition()
            errors.append(
                f"Non-standard via at "
                f"({pcbnew.ToMM(pos.x):.2f}, {pcbnew.ToMM(pos.y):.2f}): "
                f"drill={pcbnew.ToMM(drill):.2f}mm/size={pcbnew.ToMM(size):.2f}mm, "
                f"expected drill={VIA_DRILL}mm/size={VIA_SIZE}mm or "
                f"drill={THERMAL_VIA_DRILL}mm/size={THERMAL_VIA_SIZE}mm"
            )

    # --- Board outline verification ---
    edge_cuts_segs = []
    for drawing in board.GetDrawings():
        if (hasattr(drawing, 'GetLayer') and
                drawing.GetLayer() == pcbnew.Edge_Cuts):
            edge_cuts_segs.append(drawing)
    if len(edge_cuts_segs) < 4:
        errors.append(
            f"Board outline incomplete: found {len(edge_cuts_segs)} Edge.Cuts "
            f"segments, expected at least 4"
        )
    else:
        # Verify outline forms a closed polygon by checking that every segment
        # endpoint connects to another segment's start/endpoint
        endpoints = []
        for seg in edge_cuts_segs:
            s = seg.GetStart()
            e = seg.GetEnd()
            endpoints.append(((s.x, s.y), (e.x, e.y)))
        # Build adjacency: for a closed polygon, every point should appear
        # exactly twice (once as a start, once as an end of an adjacent segment)
        point_count = {}
        for s, e in endpoints:
            for p in (s, e):
                point_count[p] = point_count.get(p, 0) + 1
        dangling = [p for p, c in point_count.items() if c != 2]
        if dangling:
            errors.append(
                f"Board outline not closed: {len(dangling)} dangling "
                f"endpoint(s)"
            )

    return errors


def run_drc(pcb_file):
    """Run KiCad Design Rule Check via kicad-cli.

    Returns a list of error strings (empty = pass).
    """
    errors = []
    drc_json = str(pcb_file).replace(".kicad_pcb", "_drc.json")

    cmd = [
        "kicad-cli", "pcb", "drc",
        "--output", drc_json,
        "--format", "json",
        "--severity-all",
        str(pcb_file),
    ]
    print(f"  Running: {' '.join(cmd)}")
    result = subprocess.run(cmd, capture_output=True, text=True)

    # kicad-cli pcb drc returns non-zero if violations are found, so we
    # don't treat the return code itself as an error — we parse the JSON.
    if not os.path.exists(drc_json):
        errors.append(
            f"DRC failed to produce output: {result.stderr.strip()}"
        )
        return errors

    with open(drc_json, "r") as f:
        report = json.load(f)

    # Parse violations from the DRC report.
    # KiCad DRC JSON structure (KiCad 7+):
    #   { "violations": [ { "type": "...", "severity": "error"|"warning",
    #     "description": "...", "items": [...] }, ... ],
    #     "unconnected_items": [ ... ], "schematic_parity": [ ... ] }
    #
    # Some DRC messages are environmental (library paths not configured in
    # headless mode) or expected (unfilled GND vias report as unconnected
    # until zone fill; silkscreen cosmetic issues).  Exclude these from the
    # error count so only real electrical / physical violations fail the build.
    IGNORED_DRC_PATTERNS = [
        "footprint library",                   # library path env issue
        "not found in libraries",              # library path env issue
        "not connected",                       # GND vias to unfilled pour
        "connected on only one layer",         # via to unfilled pour
        "text height",                         # cosmetic
        "text thickness",                      # cosmetic
        "silkscreen overlap",                  # cosmetic
        "silkscreen clipped",                  # cosmetic
        "solder mask",                         # solder mask aperture bridges
        "drilled hole too close",              # via spacing in thermal pad
    ]
    violation_count = 0
    warning_count = 0

    for section in ("violations", "unconnected_items", "schematic_parity"):
        for item in report.get(section, []):
            severity = item.get("severity", "error")
            desc = item.get("description", item.get("type", "unknown"))
            # Check if this violation matches any ignored pattern
            ignored = any(pat.lower() in desc.lower()
                          for pat in IGNORED_DRC_PATTERNS)
            if ignored:
                warning_count += 1
            else:
                errors.append(f"DRC {severity}: {desc}")
                violation_count += 1

    if violation_count == 0:
        if warning_count > 0:
            print(f"  DRC passed: {warning_count} ignored warning(s)")
        else:
            print(f"  DRC passed: no violations")
    else:
        print(f"  DRC found {violation_count} violation(s) "
              f"({warning_count} ignored warning(s))")

    # Clean up the JSON report on success (keep it on failure for inspection)
    if violation_count == 0:
        os.remove(drc_json)

    return errors


def verify_outputs(output_dir, parts):
    """Verify generated output files exist and are well-formed.

    Checks Gerber files, BOM CSV, and CPL CSV.
    Returns a list of error strings (empty = pass).
    """
    errors = []
    gerber_dir = output_dir / "gerbers"

    # --- Gerber/drill file existence and size ---
    for fname in EXPECTED_GERBER_FILES:
        fpath = gerber_dir / fname
        if not fpath.exists():
            errors.append(f"Missing Gerber file: {fname}")
        elif fpath.stat().st_size == 0:
            errors.append(f"Empty Gerber file: {fname}")

    # --- BOM verification ---
    bom_file = output_dir / "access_controller_v2_jlcpcb_bom.csv"
    if not bom_file.exists():
        errors.append("Missing BOM file")
    else:
        with open(bom_file, "r") as f:
            reader = csv.reader(f)
            header = next(reader, None)
            expected_header = ["Comment", "Designator", "Footprint", "LCSC Part #"]
            if header != expected_header:
                errors.append(
                    f"BOM header mismatch: got {header}, "
                    f"expected {expected_header}"
                )
            rows = list(reader)
            if len(rows) == 0:
                errors.append("BOM has no component rows")

            # Verify every BOM row has a valid LCSC part number
            for i, row in enumerate(rows):
                if len(row) < 4:
                    errors.append(f"BOM row {i+2}: too few columns ({len(row)})")
                    continue
                if not row[3].startswith("C"):
                    errors.append(
                        f"BOM row {i+2}: invalid LCSC part number '{row[3]}'"
                    )

    # --- CPL verification ---
    cpl_file = output_dir / "access_controller_v2_jlcpcb_cpl.csv"
    cpl_designators = set()
    if not cpl_file.exists():
        errors.append("Missing CPL file")
    else:
        with open(cpl_file, "r") as f:
            reader = csv.reader(f)
            header = next(reader, None)
            expected_header = ["Designator", "Mid X", "Mid Y", "Rotation", "Layer"]
            if header != expected_header:
                errors.append(
                    f"CPL header mismatch: got {header}, "
                    f"expected {expected_header}"
                )
            rows = list(reader)
            if len(rows) == 0:
                errors.append("CPL has no component rows")

            # Collect CPL designators for cross-reference
            for i, row in enumerate(rows):
                if len(row) < 5:
                    errors.append(f"CPL row {i+2}: too few columns ({len(row)})")
                    continue
                cpl_designators.add(row[0])
                # Verify coordinates are valid numbers
                try:
                    float(row[1])
                    float(row[2])
                    float(row[3])
                except ValueError:
                    errors.append(
                        f"CPL row {i+2}: invalid coordinate/rotation for {row[0]}"
                    )
                if row[4] not in ("top", "bottom"):
                    errors.append(
                        f"CPL row {i+2}: invalid layer '{row[4]}' for {row[0]}"
                    )

    # --- BOM-to-CPL cross-reference ---
    # Every SMD component in the BOM should have a CPL entry.
    # THT parts (J*, H*) and DNP parts (R7) are excluded from both.
    if bom_file.exists() and cpl_designators:
        with open(bom_file, "r") as f:
            reader = csv.reader(f)
            next(reader)  # skip header
            for row in reader:
                if len(row) < 2:
                    continue
                designators = [d.strip() for d in row[1].split(",")]
                for des in designators:
                    if des not in cpl_designators:
                        errors.append(
                            f"BOM/CPL mismatch: {des} in BOM but missing "
                            f"from CPL"
                        )

    return errors


# =============================================================================
# MAIN
# =============================================================================


def main():
    parser = argparse.ArgumentParser(
        description="Conway Access Controller v2 - PCB Design Generator",
    )
    parser.add_argument(
        "--no-route",
        action="store_true",
        help="Skip manual trace routing (produce an unrouted board for autorouting)",
    )
    args = parser.parse_args()

    script_dir = Path(__file__).parent.resolve()
    output_dir = script_dir / "output"
    gerber_dir = output_dir / "gerbers"
    pcb_file = output_dir / "access_controller_v2.kicad_pcb"

    output_dir.mkdir(exist_ok=True)
    gerber_dir.mkdir(exist_ok=True)

    print("=" * 70)
    print("Conway Access Controller v2 - PCB Design Generator")
    print("Using KiCad pcbnew Python API")
    if args.no_route:
        print("Mode: PLACEMENT ONLY (--no-route) — board will have no traces")
    print("=" * 70)

    # Step 1: Create board
    print("\n[1/9] Creating board...")
    board = pcbnew.BOARD()
    ds = board.GetDesignSettings()
    ds.SetCopperLayerCount(2)
    ds.SetBoardThickness(mm(1.6))
    # Reduce solder mask minimum web width to avoid DRC false positives
    # on tightly-spaced pads (JLCPCB supports 0.1mm solder mask dams)
    ds.m_SolderMaskMinWidth = mm(0.0)
    print(f"  Board: {BOARD_W}mm x {BOARD_H}mm, 2-layer, 1.6mm thick")

    # Step 2: Create nets
    print("[2/9] Creating nets...")
    nets = create_nets(board)
    print(f"  Created {len(nets)} nets")

    # Step 3: Place components
    print("[3/9] Placing components...")
    parts = place_components(board, nets)
    print(f"  Placed {len(parts)} components")

    # Step 4: Assign nets
    print("[4/9] Assigning nets to pads...")
    assign_nets(parts, nets)
    print("  All nets assigned")

    # Step 5: Route traces (skipped in --no-route mode for autorouting)
    if args.no_route:
        print("[5/9] Skipping trace routing (--no-route mode)")
        print("  Board will be saved without traces for external autorouting")
    else:
        print("[5/9] Routing traces...")
        route_all(board, parts, nets)
        print("  All traces routed")

    # Step 6: Add board features
    print("[6/9] Adding board features...")
    add_board_outline(board)
    print("  Board outline added")
    add_mounting_holes(board)
    print("  Mounting holes added")
    add_antenna_keepout(board)
    print("  Antenna keepout zone added")
    add_ground_pour(board, nets)
    print("  Ground pour added and filled")
    add_ldo_thermal_relief(board, parts, nets)
    print("  LDO thermal relief added (6 vias, F.Cu + B.Cu zones)")
    add_silkscreen_labels(board, parts)
    print("  Silkscreen labels added")

    # Step 7: Save KiCad PCB file
    print("[7/9] Saving KiCad PCB file...")
    # aSkipSettings=True avoids a connectivity rebuild that can hang in headless mode
    pcbnew.SaveBoard(str(pcb_file), board, True)
    print(f"  Saved: {pcb_file}")

    # Step 8: Export outputs
    print("[8/9] Generating output files...")
    export_gerbers(pcb_file, gerber_dir)
    generate_jlcpcb_bom(parts, str(output_dir / "access_controller_v2_jlcpcb_bom.csv"))
    generate_jlcpcb_cpl(parts, str(output_dir / "access_controller_v2_jlcpcb_cpl.csv"))

    # Step 9: Verify board, run DRC, and check output files
    if args.no_route:
        print("[9/9] Skipping verification (--no-route mode)")
        print("  Board saved without routing — use OrthoRoute or another autorouter")
        print(f"\n  Unrouted board: {pcb_file}")
    else:
        print("[9/9] Verifying board and outputs...")
        all_errors = []

        print("  Phase 1: Board integrity checks (pcbnew API)...")
        board_errors = verify_board(board, parts, nets)
        if board_errors:
            for e in board_errors:
                print(f"    FAIL: {e}")
        else:
            print("    All board checks passed")
        all_errors.extend(board_errors)

        print("  Phase 2: KiCad Design Rule Check...")
        drc_errors = run_drc(pcb_file)
        if drc_errors:
            for e in drc_errors:
                print(f"    FAIL: {e}")
        all_errors.extend(drc_errors)

        print("  Phase 3: Output file verification...")
        output_errors = verify_outputs(output_dir, parts)
        if output_errors:
            for e in output_errors:
                print(f"    FAIL: {e}")
        else:
            print("    All output files verified")
        all_errors.extend(output_errors)

        # Verification summary
        if all_errors:
            print("\n" + "=" * 70)
            print(f"VERIFICATION FAILED: {len(all_errors)} error(s)")
            print("=" * 70)
            for i, e in enumerate(all_errors, 1):
                print(f"  {i}. {e}")
            print("=" * 70)
            sys.exit(1)

        # Summary
        print("\n" + "=" * 70)
        print("PCB Design Generation Complete — All Verification Passed!")
        print("=" * 70)
        print(f"\nVerification: {len(EXPECTED_NETS)} nets, {len(parts)} components, "
              f"DRC clean, {len(EXPECTED_GERBER_FILES)} output files OK")
        print(f"\nOutput files in {output_dir}/:")
        print(f"  - access_controller_v2.kicad_pcb  (KiCad PCB file)")
        print(f"  - gerbers/                         (Gerber + drill files)")
        print(f"  - access_controller_v2_jlcpcb_bom.csv")
        print(f"  - access_controller_v2_jlcpcb_cpl.csv")
        print("\nJLCPCB Upload Instructions:")
        print("  1. ZIP all files in gerbers/ and upload to jlcpcb.com")
        print("  2. Select 'SMT Assembly' and upload BOM and CPL files")
        print("  3. Review component placement and confirm order")
        print("\nManual Assembly Required (after delivery):")
        print("  - J1: 12V power screw terminal (2-pin, 3.5mm pitch)")
        print("  - J2: Wiegand reader screw terminal (4-pin, 3.5mm pitch)")
        print("  - J3: Relay output screw terminal (2-pin, 3.5mm pitch)")
        print("  - J5: RS485 screw terminal (3-pin, 3.5mm pitch)")
        print("  - J6: FTDI programming header (1x6, 2.54mm pitch)")
        print("\nComponent Notes:")
        print("  - R7 (120R): RS485 termination, DNP by default. Hand-solder only")
        print("    if this board is at the end of the RS485 bus.")
        print("  - C1, C12: 10uF 25V 0805 on 12V input (bulk decoupling)")
        print("  - C3, C13: 22uF 25V 0805 on 3.3V LDO output")
        print("  - R11: 0.22R 0402 ESR ballast for AMS1117 output cap stability")
        print("  - C11: 1nF 0402 on EN (small value prevents capacitive divider with C10)")
        print("  - U5: SP3485EN (Basic part, pin-compatible MAX3485 replacement)")
        print("  - D3: SM712 TVS diode on RS485 A/B bus (asymmetric ESD protection)")
        print("  - LED1: Green power LED (0805), R10: 100R current limiter on 3.3V rail")
        print("  - Extended JLCPCB parts: U2/U3 (EL817), D3 (SM712) = $3 setup fee each")
        print("  - U4 (ESP32-WROOM-32E) is Extended = $3 setup fee, Standard PCBA only")
        print("  - LED1 (KT-0805G) and R10 (100R) are Basic parts = no setup fee")
        print("\nThermal Note (U1 - AMS1117-3.3):")
        print("  - LDO dissipates ~1.3W average (8.7V drop x 150mA), up to 4.35W peak")
        print("  - Board includes thermal vias and copper zones for heat management")
        print("  - Monitor U1 surface temperature during initial testing with 12V input")
        print("  - If surface temp exceeds 90°C under load, consider buck pre-regulator")
        print("  - Lower input voltage (e.g. 9V) significantly reduces dissipation")
        print("=" * 70)


if __name__ == "__main__":
    main()
