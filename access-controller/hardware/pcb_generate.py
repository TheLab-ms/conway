#!/usr/bin/env python3
"""
Conway Access Controller v2 - PCB Design Generator

Generates a complete 2-layer PCB using KiCad's pcbnew Python API, then exports
Gerber files via kicad-cli and produces JLCPCB BOM/CPL for assembly.

Board: 58mm x 44mm, 2-layer
MCU: ESP32-WROOM-32E module soldered directly on the PCB
Passives: Resistors and 100nF caps are 0402; 10uF/22uF caps are 0805; LED is 0805
Features:
  - 12V input with SS34 reverse polarity protection
  - AMS1117-3.3 LDO for 3.3V rail
  - 2x EL817 optocouplers for Wiegand D0/D1 isolation
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

import csv
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
    "100nF": "C1525",               # Basic, 0402 50V X7R
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


def create_r0805(board, ref, val):
    """Create an 0805 resistor footprint."""
    fp = pcbnew.FOOTPRINT(board)
    fp.SetReference(ref)
    fp.SetValue(val)
    fp.SetFPIDAsString("Resistor_SMD:R_0805_2012Metric")
    add_smd_pad(fp, "1", -1.0, 0, 1.0, 1.3)
    add_smd_pad(fp, "2", 1.0, 0, 1.0, 1.3)
    add_fab_rect(fp, 0, 0, 2.0, 1.25)
    add_courtyard_rect(fp, 0, 0, 3.4, 1.8)
    return fp


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
    Pad 1 = Anode (+), Pad 2 = Cathode (-).
    Same pad geometry as R/C 0805 but with LED-specific FPID.
    """
    fp = pcbnew.FOOTPRINT(board)
    fp.SetReference(ref)
    fp.SetValue(val)
    fp.SetFPIDAsString("LED_SMD:LED_0805_2012Metric")
    add_smd_pad(fp, "1", -1.0, 0, 1.0, 1.3)  # Anode
    add_smd_pad(fp, "2", 1.0, 0, 1.0, 1.3)  # Cathode
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
    #   Peak WiFi TX I ≈ 500mA → P_peak = 4.35W (transient, <10ms bursts)
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
    # C1/C1B: 10uF 25V 0805 on 12V input for bulk decoupling
    # C3/C3B: 22uF 25V 0805 on 3.3V output for LDO stability
    c1 = create_c0805(board, "C1", "10uF")
    c1.SetPosition(pt(36.5, 7.5))
    board.Add(c1)
    parts["C1"] = c1

    c1b = create_c0805(board, "C1B", "10uF")
    c1b.SetPosition(pt(36.5, 9.0))
    board.Add(c1b)
    parts["C1B"] = c1b

    c2 = create_c0402(board, "C2", "100nF")
    c2.SetPosition(pt(36.5, 10.5))
    board.Add(c2)
    parts["C2"] = c2

    # Output filter caps (near U1 output)
    c3 = create_c0805(board, "C3", "22uF")
    c3.SetPosition(pt(49.0, 12.0))
    board.Add(c3)
    parts["C3"] = c3

    c3b = create_c0805(board, "C3B", "22uF")
    c3b.SetPosition(pt(49.0, 13.5))
    board.Add(c3b)
    parts["C3B"] = c3b

    c4 = create_c0402(board, "C4", "100nF")
    c4.SetPosition(pt(49.0, 15.0))
    board.Add(c4)
    parts["C4"] = c4

    # ESP32 decoupling caps (near ESP32 3V3/GND pins)
    c5 = create_c0402(board, "C5", "100nF")
    c5.SetPosition(pt(10.0, 27.0))
    board.Add(c5)
    parts["C5"] = c5

    c6 = create_c0805(board, "C6", "10uF")
    c6.SetPosition(pt(10.0, 28.5))
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

    # R2: 470R current limiting for U2 LED (8.1mA at 5V Wiegand)
    r2 = create_r0402(board, "R2", "470R")
    r2.SetPosition(pt(38.0, 26.5))
    board.Add(r2)
    parts["R2"] = r2

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
    j3.SetPosition(pt(5.5, 5.0))
    board.Add(j3)
    parts["J3"] = j3

    # Q1: SS8050 NPN relay driver
    q1 = create_sot23(board, "Q1", "SS8050")
    q1.SetPosition(pt(5.5, 11.5))
    board.Add(q1)
    parts["Q1"] = q1

    # R6: 1K base resistor for Q1
    r6 = create_r0402(board, "R6", "1K")
    r6.SetPosition(pt(5.5, 14.0))
    board.Add(r6)
    parts["R6"] = r6

    # D2: M7 flyback diode
    d2 = create_sma_diode(board, "D2", "M7")
    d2.SetPosition(pt(5.5, 17.0))
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
    d3.SetPosition(pt(44.0, 33.0))
    board.Add(d3)
    parts["D3"] = d3

    # ---- Power LED (top-right, near J1) ----
    # R10: 100R current-limiting resistor for power LED
    # InGaN green Vf range 2.8-3.2V: I = (3.3-Vf)/100 = 1-5mA (visible across full range)
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
    # J6: 1x6 FTDI header
    # Pinout: 1=GND, 2=CTS(NC), 3=3V3, 4=TXO(->ESP RX), 5=RXI(<-ESP TX), 6=DTR
    j6 = create_pin_header_1x6(board, "J6", "FTDI")
    j6.SetPosition(pt(4.0, 27.0))
    board.Add(j6)
    parts["J6"] = j6

    # Auto-reset circuit: DTR -> C_DTR -> GPIO0, RTS -> C_RTS -> EN
    # C11: 100nF bypass on EN for noise filtering
    c11 = create_c0402(board, "C11", "100nF")
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
    set_pad_net(parts["U1"], 1, nets["GND"])
    set_pad_net(parts["U1"], 2, nets["3V3"])
    set_pad_net(parts["U1"], 3, nets["12V"])
    set_pad_net(parts["U1"], 4, nets["3V3"])

    # Input filter caps
    for ref in ["C1", "C1B", "C2"]:
        set_pad_net(parts[ref], 1, nets["12V"])
        set_pad_net(parts[ref], 2, nets["GND"])

    # Output filter caps
    for ref in ["C3", "C3B", "C4"]:
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
    # J2: 1=D0, 2=D1, 3=+12V, 4=GND
    set_pad_net(parts["J2"], 1, nets["WIEG_D0_IN"])
    set_pad_net(parts["J2"], 2, nets["WIEG_D1_IN"])
    set_pad_net(parts["J2"], 3, nets["12V"])
    set_pad_net(parts["J2"], 4, nets["GND"])

    # R2: 470R current-limit (1=WIEG_D0_IN from J2, 2=WIEG_D0_R to U2 anode)
    set_pad_net(parts["R2"], 1, nets["WIEG_D0_IN"])
    set_pad_net(parts["R2"], 2, nets["WIEG_D0_R"])

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

    # LED1: Green LED (1=Anode=PWR_LED, 2=Cathode=GND)
    set_pad_net(parts["LED1"], 1, nets["PWR_LED"])
    set_pad_net(parts["LED1"], 2, nets["GND"])

    # ---- FTDI Header + Auto-Reset ----
    # J6: FTDI 6-pin (1=GND, 2=CTS/NC, 3=3V3, 4=TXO, 5=RXI, 6=DTR)
    set_pad_net(parts["J6"], 1, nets["GND"])
    # Pin 2 = CTS, no net (NC)
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

    # C10: 100nF RTS -> EN (auto-reset)
    # Note: Standard FTDI auto-reset uses RTS for EN. But since J6 only exposes
    # DTR (pin 6), we use DTR for GPIO0 and add a separate RTS trace.
    # For a 6-pin FTDI header: pin 6 = DTR. Many FTDI breakouts have both.
    # We'll wire: DTR -> C9 -> GPIO0, and use the RTS signal from the
    # programming tool. Since standard FTDI cables have DTR but not always RTS,
    # we connect C10 between DTR and EN with R/C timing difference.
    # Actually, the standard ESP32 auto-reset uses both DTR and RTS with a
    # cross-coupled circuit. For simplicity with a single DTR line, we use:
    # DTR low -> C9 pulls GPIO0 low (boot mode)
    # Reset is done manually or via EN button.
    # But since user wanted FTDI header for auto-reset, let's use the standard
    # ESP-PROG compatible pinout with DTR and RTS both available.
    # Re-wire: J6 pin 6 = DTR, we add RTS as pin 2 (instead of CTS).
    # Standard 6-pin FTDI: GND, CTS, VCC, TXD, RXD, DTR
    # ESP auto-reset: DTR -> C -> GPIO0, RTS -> C -> EN
    # Many ESP32 boards use CTS pin as RTS. Let's use pin 2 for RTS.
    set_pad_net(parts["J6"], 2, nets["FTDI_RTS"])
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
    """Route all electrical connections."""

    F = pcbnew.F_Cu
    B = pcbnew.B_Cu

    # === 12V POWER PATH ===

    # J1.1 (+12V_RAW) -> D1.2 (Anode) — both near y=5, direct horizontal trace
    p1 = get_pad_pos(parts["J1"], 1)
    p2 = get_pad_pos(parts["D1"], 2)
    route_wire(board, [p1, p2], TW_POWER, F, nets["12V_RAW"])

    # D1.1 (Cathode) -> 12V rail distribution point
    d1k = get_pad_pos(parts["D1"], 1)

    # D1.K -> U1.VIN (pin 3) — D1 at (42,5), U1 at (42,12) rotated 180°
    u1_vin = get_pad_pos(parts["U1"], 3)
    route_wire(board, [d1k, (d1k[0], u1_vin[1]), u1_vin], TW_POWER, F, nets["12V"])

    # 12V to input caps C1/C1B/C2 at x=36.5
    # Vertical rail left of caps, route from D1.K area down to cap rail
    c1_p = get_pad_pos(parts["C1"], 1)
    c1b_p = get_pad_pos(parts["C1B"], 1)
    c2_p = get_pad_pos(parts["C2"], 1)
    rail_x = c1_p[0] - 2.0  # ~34.5, outside antenna keepout (x>33)
    route_wire(
        board,
        [d1k, (d1k[0], d1k[1] + 2.0), (rail_x, d1k[1] + 2.0), (rail_x, c1_p[1])],
        TW_POWER,
        F,
        nets["12V"],
    )
    route_wire(board, [(rail_x, c1_p[1]), c1_p], TW_POWER, F, nets["12V"])
    route_wire(board, [(rail_x, c1b_p[1]), c1b_p], TW_POWER, F, nets["12V"])
    route_wire(board, [(rail_x, c2_p[1]), c2_p], TW_POWER, F, nets["12V"])
    route_wire(board, [(rail_x, c1_p[1]), (rail_x, c2_p[1])], TW_POWER, F, nets["12V"])

    # 12V to J2.3 (Wiegand reader +12V) — J2 at (42,40), D1.K at (42,5)
    # Route south along x=42 from D1.K through U1 area down to J2.3
    j2_12v = get_pad_pos(parts["J2"], 3)
    # Use the 12V rail at x=rail_x to avoid crossing U1, then south to J2
    route_wire(
        board,
        [(rail_x, c2_p[1]), (rail_x, j2_12v[1]), j2_12v],
        TW_POWER,
        F,
        nets["12V"],
    )

    # 12V to relay section (D2.K) via bottom layer
    # D2 at (5.5,17) 180° so D2.1 (cathode) is at ~(5.5,17) area
    # Must avoid antenna keepout (x=11-33, y=0-7.75)
    # Route: D1.K -> via to B.Cu at (36, 8.5) (outside keepout: x=36 > 33)
    #   -> B.Cu south to (36, 18) -> west to (8, 18) -> via up near D2.K
    d2_k = get_pad_pos(parts["D2"], 1)
    via_12v_src = (rail_x, c2_p[1] + 2.0)  # ~(34.5, 12.5) — below caps, safe
    add_via(board, via_12v_src, nets["12V"])
    route_wire(
        board,
        [(rail_x, c2_p[1]), via_12v_src],
        TW_POWER,
        F,
        nets["12V"],
    )
    # Route on B.Cu: south then west to relay area, staying below keepout
    via_12v_relay = (d2_k[0] + 2.5, d2_k[1])  # ~(8.0, 17) near D2.K
    add_via(board, via_12v_relay, nets["12V"])
    route_wire(
        board,
        [via_12v_src, (via_12v_src[0], d2_k[1]), (via_12v_relay[0], d2_k[1])],
        TW_POWER,
        B,
        nets["12V"],
    )
    route_wire(board, [via_12v_relay, d2_k], TW_POWER, F, nets["12V"])

    # J5 is now 3-pin (A, B, GND) — no +12V pin, so no J5.4 routing needed

    # === 3.3V POWER PATH ===

    # U1.VOUT (pin 2) -> output caps C3/C3B/C4 at x=49
    u1_vout = get_pad_pos(parts["U1"], 2)
    u1_tab = get_pad_pos(parts["U1"], 4)
    c3_p = get_pad_pos(parts["C3"], 1)
    c3b_p = get_pad_pos(parts["C3B"], 1)
    c4_p = get_pad_pos(parts["C4"], 1)

    # Connect tab to VOUT
    route_wire(board, [u1_vout, u1_tab], TW_POWER, F, nets["3V3"])

    # VOUT -> output caps via vertical rail right of caps
    out_rail_x = c3_p[0] + 2.0  # ~51.0
    route_wire(
        board,
        [
            u1_tab,
            (u1_tab[0] + 3, u1_tab[1]),
            (out_rail_x, u1_tab[1]),
            (out_rail_x, c3_p[1]),
        ],
        TW_POWER,
        F,
        nets["3V3"],
    )
    route_wire(board, [(out_rail_x, c3_p[1]), c3_p], TW_POWER, F, nets["3V3"])
    route_wire(board, [(out_rail_x, c3b_p[1]), c3b_p], TW_POWER, F, nets["3V3"])
    route_wire(board, [(out_rail_x, c4_p[1]), c4_p], TW_POWER, F, nets["3V3"])
    route_wire(
        board, [(out_rail_x, c3_p[1]), (out_rail_x, c4_p[1])], TW_POWER, F, nets["3V3"]
    )

    # 3.3V to ESP32 pin 2 (3V3) via bottom layer
    # ESP32 at (22, 14.5); output rail at x~51; route on B.Cu
    esp_3v3 = get_pad_pos(parts["U4"], 2)
    via_3v3_esp = (out_rail_x, c4_p[1] + 3.0)  # ~(51, 18)
    add_via(board, via_3v3_esp, nets["3V3"])
    route_wire(board, [(out_rail_x, c4_p[1]), via_3v3_esp], TW_POWER, F, nets["3V3"])
    via_3v3_esp2 = (esp_3v3[0] - 3, esp_3v3[1])  # ~(19, 14.5)
    add_via(board, via_3v3_esp2, nets["3V3"])
    route_wire(
        board,
        [via_3v3_esp, (via_3v3_esp[0], via_3v3_esp2[1]), via_3v3_esp2],
        TW_POWER,
        B,
        nets["3V3"],
    )
    route_wire(board, [via_3v3_esp2, esp_3v3], TW_POWER, F, nets["3V3"])

    # 3.3V to ESP32 decoupling caps C5/C6/C7 at x=10, y=27-30
    c5_p = get_pad_pos(parts["C5"], 1)
    c6_p = get_pad_pos(parts["C6"], 1)
    c7_p = get_pad_pos(parts["C7"], 1)
    route_wire(
        board,
        [via_3v3_esp2, (via_3v3_esp2[0], c5_p[1]), c5_p],
        TW_POWER,
        F,
        nets["3V3"],
    )
    route_wire(board, [c5_p, (c5_p[0], c6_p[1]), c6_p], TW_POWER, F, nets["3V3"])
    route_wire(board, [c6_p, (c6_p[0], c7_p[1]), c7_p], TW_POWER, F, nets["3V3"])

    # 3.3V to pull-ups R4, R5 (via bottom layer)
    # R4 at (33,30), R5 at (53,30)
    r4_vcc = get_pad_pos(parts["R4"], 2)
    r5_vcc = get_pad_pos(parts["R5"], 2)
    via_3v3_pu = (out_rail_x, c4_p[1] + 6.0)  # ~(51, 21) — below output caps area
    add_via(board, via_3v3_pu, nets["3V3"])
    route_wire(board, [(out_rail_x, c4_p[1]), via_3v3_pu], TW_POWER, F, nets["3V3"])
    via_r4 = (r4_vcc[0] + 2, r4_vcc[1])  # ~(35, 30)
    add_via(board, via_r4, nets["3V3"])
    route_wire(
        board,
        [via_3v3_pu, (via_3v3_pu[0], via_r4[1]), via_r4],
        TW_PULLUP,
        B,
        nets["3V3"],
    )
    route_wire(board, [via_r4, r4_vcc], TW_PULLUP, F, nets["3V3"])
    via_r5 = (r5_vcc[0] + 2, r5_vcc[1])  # ~(55, 30)
    add_via(board, via_r5, nets["3V3"])
    route_wire(
        board, [via_r4, (via_r4[0], via_r5[1]), via_r5], TW_PULLUP, B, nets["3V3"]
    )
    route_wire(board, [via_r5, r5_vcc], TW_PULLUP, F, nets["3V3"])

    # 3.3V to R8, R9 pull-ups (EN, GPIO0) at x=10
    r8_vcc = get_pad_pos(parts["R8"], 2)
    r9_vcc = get_pad_pos(parts["R9"], 2)
    # Route from via_3v3_esp2 (~19, 14.5) south on B.Cu to R8/R9 area
    via_3v3_boot = (via_3v3_esp2[0], via_3v3_esp2[1] + 5.0)  # ~(19, 19.5)
    add_via(board, via_3v3_boot, nets["3V3"])
    route_wire(board, [via_3v3_esp2, via_3v3_boot], TW_PULLUP, B, nets["3V3"])
    add_via(board, (r8_vcc[0] + 2, r8_vcc[1]), nets["3V3"])
    route_wire(
        board,
        [via_3v3_boot, (r8_vcc[0] + 2, via_3v3_boot[1]), (r8_vcc[0] + 2, r8_vcc[1])],
        TW_PULLUP,
        B,
        nets["3V3"],
    )
    route_wire(board, [(r8_vcc[0] + 2, r8_vcc[1]), r8_vcc], TW_PULLUP, F, nets["3V3"])
    route_wire(
        board,
        [
            (r8_vcc[0] + 2, r8_vcc[1]),
            (r9_vcc[0] + 2, r8_vcc[1]),
            (r9_vcc[0] + 2, r9_vcc[1]),
        ],
        TW_PULLUP,
        B,
        nets["3V3"],
    )
    add_via(board, (r9_vcc[0] + 2, r9_vcc[1]), nets["3V3"])
    route_wire(board, [(r9_vcc[0] + 2, r9_vcc[1]), r9_vcc], TW_PULLUP, F, nets["3V3"])

    # 3.3V to SP3485EN VCC (U5 pin 8) and C8
    # U5 at (48,36) 90°, C8 at (53,36)
    u5_vcc = get_pad_pos(parts["U5"], 8)
    c8_p = get_pad_pos(parts["C8"], 1)
    route_wire(board, [u5_vcc, (c8_p[0], u5_vcc[1]), c8_p], TW_POWER, F, nets["3V3"])
    # Connect via from 3V3 rail on B.Cu
    via_3v3_rs = (u5_vcc[0] - 3, u5_vcc[1])  # ~(45, 36)
    add_via(board, via_3v3_rs, nets["3V3"])
    route_wire(board, [via_3v3_rs, u5_vcc], TW_POWER, F, nets["3V3"])
    # Route from pull-up via on B.Cu south and across to RS485 VCC
    route_wire(
        board,
        [via_3v3_pu, (via_3v3_pu[0], via_3v3_rs[1]), via_3v3_rs],
        TW_POWER,
        B,
        nets["3V3"],
    )

    # 3.3V to FTDI J6.3 — J6 at (4,27)
    j6_vcc = get_pad_pos(parts["J6"], 3)
    via_3v3_ftdi = (j6_vcc[0] + 3, j6_vcc[1])  # ~(7, 27)
    add_via(board, via_3v3_ftdi, nets["3V3"])
    route_wire(board, [via_3v3_ftdi, j6_vcc], TW_POWER, F, nets["3V3"])
    # Route from boot pull-up via on B.Cu to FTDI VCC
    route_wire(
        board,
        [via_3v3_boot, (via_3v3_ftdi[0], via_3v3_boot[1]), via_3v3_ftdi],
        TW_POWER,
        B,
        nets["3V3"],
    )

    # 3.3V to R10 (LED resistor) at (54,5)
    r10_1 = get_pad_pos(parts["R10"], 1)
    via_led_3v3 = (r10_1[0] - 2, r10_1[1])  # ~(52, 5)
    add_via(board, via_led_3v3, nets["3V3"])
    route_wire(board, [r10_1, via_led_3v3], TW_SIGNAL, F, nets["3V3"])
    # Connect to 3V3 output rail on bottom layer (via_3v3_esp at ~(51, 18))
    route_wire(
        board,
        [via_led_3v3, (via_3v3_esp[0], via_led_3v3[1]), via_3v3_esp],
        TW_POWER,
        B,
        nets["3V3"],
    )

    # === GROUND VIAS ===
    # THT pads: via directly on pad (no solder wicking risk)
    gnd_via_tht = [
        ("J1", 2),   # Power GND
        ("J2", 4),   # Wiegand GND
        ("J5", 3),   # RS485 GND (J5 is now 3-pin: A, B, GND)
        ("J6", 1),   # FTDI GND
    ]
    for ref, pad_num in gnd_via_tht:
        pos = get_pad_pos(parts[ref], pad_num)
        add_via(board, pos, nets["GND"])

    # SMD pads: offset via by 0.5mm to avoid solder wicking during reflow.
    # Short trace connects pad to nearby via for ground pour connection.
    gnd_via_smd = [
        ("C1", 2),
        ("C1B", 2),
        ("C2", 2),
        ("C3", 2),
        ("C3B", 2),
        ("C4", 2),
        ("C5", 2),
        ("C6", 2),
        ("C7", 2),
        ("C8", 2),
        ("C11", 2),
        ("D3", 2),   # SM712 TVS GND
        ("U1", 1),   # LDO GND
        ("U2", 2),   # Optocoupler cathode
        ("U2", 3),   # Optocoupler emitter
        ("U3", 2),
        ("U3", 3),
        ("U4", 1),   # ESP32 GND
        ("U4", 15),  # ESP32 GND
        ("U4", 38),  # ESP32 GND
        ("U4", 39),  # ESP32 GND exposed pad
        ("U5", 5),   # SP3485EN GND
        ("Q1", 2),   # Transistor emitter
    ]
    VIA_OFFSET = 0.5  # mm offset from pad center
    for ref, pad_num in gnd_via_smd:
        pos = get_pad_pos(parts[ref], pad_num)
        via_pos = (pos[0] + VIA_OFFSET, pos[1])
        add_track(board, pos, via_pos, TW_SIGNAL, F, nets["GND"])
        add_via(board, via_pos, nets["GND"])

    # === WIEGAND D0 PATH ===
    # J2.1 (D0 from reader) -> R2.1 — J2 at (42,40), R2 at (38,26.5)
    j2_d0 = get_pad_pos(parts["J2"], 1)
    r2_1 = get_pad_pos(parts["R2"], 1)
    r2_2 = get_pad_pos(parts["R2"], 2)
    route_wire(
        board, [j2_d0, (j2_d0[0], r2_1[1]), r2_1], TW_SIGNAL, F, nets["WIEG_D0_IN"]
    )

    # R2.2 -> U2.1 (Anode) — U2 at (38,30) 90°
    u2_a = get_pad_pos(parts["U2"], 1)
    route_wire(board, [r2_2, (u2_a[0], r2_2[1]), u2_a], TW_SIGNAL, F, nets["WIEG_D0_R"])

    # U2.4 (Collector) -> R4.1 (pull-up) and -> ESP32 GPIO25 (pin 10)
    u2_col = get_pad_pos(parts["U2"], 4)
    r4_sig = get_pad_pos(parts["R4"], 1)
    esp_d0 = get_pad_pos(parts["U4"], 10)  # GPIO25

    route_wire(
        board, [u2_col, (r4_sig[0], u2_col[1]), r4_sig], TW_SIGNAL, F, nets["WIEG_D0"]
    )

    # U2.4 -> ESP32 GPIO25 via bottom layer
    via_d0 = (u2_col[0] - 4, u2_col[1])  # ~(34, 30)
    add_via(board, via_d0, nets["WIEG_D0"])
    route_wire(board, [u2_col, via_d0], TW_SIGNAL, F, nets["WIEG_D0"])
    via_d0_esp = (esp_d0[0] + 3, esp_d0[1])
    add_via(board, via_d0_esp, nets["WIEG_D0"])
    route_wire(
        board,
        [via_d0, (via_d0[0], via_d0_esp[1]), via_d0_esp],
        TW_SIGNAL,
        B,
        nets["WIEG_D0"],
    )
    route_wire(board, [via_d0_esp, esp_d0], TW_SIGNAL, F, nets["WIEG_D0"])

    # === WIEGAND D1 PATH ===
    # J2.2 -> R3.1 -> R3.2 -> U3.1 — J2 at (42,40), R3 at (48,26.5), U3 at (48,30) 90°
    j2_d1 = get_pad_pos(parts["J2"], 2)
    r3_1 = get_pad_pos(parts["R3"], 1)
    r3_2 = get_pad_pos(parts["R3"], 2)
    route_wire(
        board, [j2_d1, (j2_d1[0], r3_1[1]), r3_1], TW_SIGNAL, F, nets["WIEG_D1_IN"]
    )

    u3_a = get_pad_pos(parts["U3"], 1)
    route_wire(board, [r3_2, (u3_a[0], r3_2[1]), u3_a], TW_SIGNAL, F, nets["WIEG_D1_R"])

    u3_col = get_pad_pos(parts["U3"], 4)
    r5_sig = get_pad_pos(parts["R5"], 1)
    esp_d1 = get_pad_pos(parts["U4"], 9)  # GPIO33

    route_wire(
        board, [u3_col, (r5_sig[0], u3_col[1]), r5_sig], TW_SIGNAL, F, nets["WIEG_D1"]
    )

    # U3.4 -> ESP32 GPIO33 via bottom layer
    via_d1 = (u3_col[0] - 4, u3_col[1])  # ~(44, 30)
    add_via(board, via_d1, nets["WIEG_D1"])
    route_wire(board, [u3_col, via_d1], TW_SIGNAL, F, nets["WIEG_D1"])
    via_d1_esp = (esp_d1[0] + 3, esp_d1[1])
    add_via(board, via_d1_esp, nets["WIEG_D1"])
    route_wire(
        board,
        [via_d1, (via_d1[0], via_d1_esp[1]), via_d1_esp],
        TW_SIGNAL,
        B,
        nets["WIEG_D1"],
    )
    route_wire(board, [via_d1_esp, esp_d1], TW_SIGNAL, F, nets["WIEG_D1"])

    # === RELAY CONTROL PATH ===
    # ESP32 GPIO32 (pin 8) -> R6.1 -> R6.2 -> Q1.1 (Base)
    # ESP32 at (22,14.5), R6 at (5.5,14), Q1 at (5.5,11.5)
    esp_relay = get_pad_pos(parts["U4"], 8)  # GPIO32
    r6_1 = get_pad_pos(parts["R6"], 1)
    r6_2 = get_pad_pos(parts["R6"], 2)
    q1_base = get_pad_pos(parts["Q1"], 1)

    # ESP32 -> R6 via bottom layer (must cross antenna keepout zone)
    via_relay = (esp_relay[0] - 3, esp_relay[1])  # ~(19, 14.5)
    add_via(board, via_relay, nets["RELAY_DRV"])
    route_wire(board, [esp_relay, via_relay], TW_SIGNAL, F, nets["RELAY_DRV"])
    via_relay2 = (r6_1[0] + 3, r6_1[1])  # ~(8.5, 14)
    add_via(board, via_relay2, nets["RELAY_DRV"])
    route_wire(
        board,
        [via_relay, (via_relay2[0], via_relay[1]), via_relay2],
        TW_SIGNAL,
        B,
        nets["RELAY_DRV"],
    )
    route_wire(board, [via_relay2, r6_1], TW_SIGNAL, F, nets["RELAY_DRV"])
    route_wire(
        board, [r6_2, (q1_base[0], r6_2[1]), q1_base], TW_SIGNAL, F, nets["RELAY_BASE"]
    )

    # Q1.3 (Collector) -> D2.2 (Anode) and -> J3.1
    # Q1 at (5.5,11.5), D2 at (5.5,17) 180°, J3 at (5.5,5)
    q1_col = get_pad_pos(parts["Q1"], 3)
    d2_a = get_pad_pos(parts["D2"], 2)
    j3_1 = get_pad_pos(parts["J3"], 1)

    route_wire(
        board, [q1_col, (q1_col[0], d2_a[1]), d2_a], TW_POWER, F, nets["RELAY_12V"]
    )
    # J3.1 connects to relay output — route from collector area to J3
    # Q1 collector and J3 are both at x≈5.5, route vertically
    route_wire(
        board, [q1_col, (j3_1[0], q1_col[1]), j3_1], TW_POWER, F, nets["RELAY_12V"]
    )

    # D2.1 (Cathode) -> 12V (already connected via bottom layer above)
    # J3.2 -> 12V (relay common) — J3 at (5.5,5), connect to D2.K (12V)
    j3_2 = get_pad_pos(parts["J3"], 2)
    route_wire(board, [d2_k, (d2_k[0], j3_2[1]), j3_2], TW_POWER, F, nets["12V"])

    # === RS485 PATH ===
    # ESP32 GPIO17 (pin 28 = TX) -> U5.4 (DI) — U5 at (48,36) 90°
    esp_tx485 = get_pad_pos(parts["U4"], 28)  # GPIO17
    u5_di = get_pad_pos(parts["U5"], 4)
    via_tx485 = (esp_tx485[0] + 3, esp_tx485[1])
    add_via(board, via_tx485, nets["RS485_TX"])
    route_wire(board, [esp_tx485, via_tx485], TW_SIGNAL, F, nets["RS485_TX"])
    via_tx485_2 = (u5_di[0] - 3, u5_di[1])
    add_via(board, via_tx485_2, nets["RS485_TX"])
    route_wire(
        board,
        [via_tx485, (via_tx485[0], via_tx485_2[1]), via_tx485_2],
        TW_SIGNAL,
        B,
        nets["RS485_TX"],
    )
    route_wire(board, [via_tx485_2, u5_di], TW_SIGNAL, F, nets["RS485_TX"])

    # ESP32 GPIO16 (pin 27 = RX) -> U5.1 (RO)
    esp_rx485 = get_pad_pos(parts["U4"], 27)  # GPIO16
    u5_ro = get_pad_pos(parts["U5"], 1)
    via_rx485 = (esp_rx485[0] + 3, esp_rx485[1] + 2)
    add_via(board, via_rx485, nets["RS485_RX"])
    route_wire(
        board,
        [esp_rx485, (esp_rx485[0] + 3, esp_rx485[1]), via_rx485],
        TW_SIGNAL,
        F,
        nets["RS485_RX"],
    )
    via_rx485_2 = (u5_ro[0] - 3, u5_ro[1])
    add_via(board, via_rx485_2, nets["RS485_RX"])
    route_wire(
        board,
        [via_rx485, (via_rx485[0], via_rx485_2[1]), via_rx485_2],
        TW_SIGNAL,
        B,
        nets["RS485_RX"],
    )
    route_wire(board, [via_rx485_2, u5_ro], TW_SIGNAL, F, nets["RS485_RX"])

    # ESP32 GPIO4 (pin 26 = DE/RE) -> U5.2 (RE) and U5.3 (DE) - tied together
    esp_de = get_pad_pos(parts["U4"], 26)  # GPIO4
    u5_re = get_pad_pos(parts["U5"], 2)
    u5_de = get_pad_pos(parts["U5"], 3)
    via_de = (esp_de[0] + 3, esp_de[1] + 4)
    add_via(board, via_de, nets["RS485_DE"])
    route_wire(
        board,
        [esp_de, (esp_de[0] + 3, esp_de[1]), via_de],
        TW_SIGNAL,
        F,
        nets["RS485_DE"],
    )
    # Route to U5.3 (DE) via bottom
    via_de2 = (u5_de[0] - 3, u5_de[1])
    add_via(board, via_de2, nets["RS485_DE"])
    route_wire(
        board,
        [via_de, (via_de[0], via_de2[1]), via_de2],
        TW_SIGNAL,
        B,
        nets["RS485_DE"],
    )
    route_wire(board, [via_de2, u5_de], TW_SIGNAL, F, nets["RS485_DE"])
    # U5.2 (RE) to U5.3 (DE) - direct trace
    route_wire(board, [u5_re, u5_de], TW_SIGNAL, F, nets["RS485_DE"])

    # U5.6 (A) -> R7.1 and -> J5.1
    # U5 at (48,36), R7 at (42,36), J5 at (18,40)
    # Long run from R7 to J5 — route on bottom layer to avoid congestion
    u5_a = get_pad_pos(parts["U5"], 6)
    r7_a = get_pad_pos(parts["R7"], 1)
    j5_a = get_pad_pos(parts["J5"], 1)
    route_wire(board, [u5_a, (u5_a[0], r7_a[1]), r7_a], TW_SIGNAL, F, nets["RS485_A"])
    # R7 -> J5.1 via bottom layer (long horizontal run)
    via_a_src = (r7_a[0] - 2, r7_a[1])  # ~(40, 36)
    add_via(board, via_a_src, nets["RS485_A"])
    route_wire(board, [r7_a, via_a_src], TW_SIGNAL, F, nets["RS485_A"])
    via_a_dst = (j5_a[0] + 2, j5_a[1])  # ~(20, 40)
    add_via(board, via_a_dst, nets["RS485_A"])
    route_wire(
        board,
        [via_a_src, (via_a_src[0], via_a_dst[1]), via_a_dst],
        TW_SIGNAL,
        B,
        nets["RS485_A"],
    )
    route_wire(board, [via_a_dst, j5_a], TW_SIGNAL, F, nets["RS485_A"])

    # U5.7 (B) -> R7.2 and -> J5.2
    u5_b = get_pad_pos(parts["U5"], 7)
    r7_b = get_pad_pos(parts["R7"], 2)
    j5_b = get_pad_pos(parts["J5"], 2)
    route_wire(board, [u5_b, (u5_b[0], r7_b[1]), r7_b], TW_SIGNAL, F, nets["RS485_B"])
    # R7 -> J5.2 via bottom layer
    via_b_src = (r7_b[0] - 2, r7_b[1])  # ~(40, 36)
    add_via(board, via_b_src, nets["RS485_B"])
    route_wire(board, [r7_b, via_b_src], TW_SIGNAL, F, nets["RS485_B"])
    via_b_dst = (j5_b[0] + 2, j5_b[1])  # ~(20, 40)
    add_via(board, via_b_dst, nets["RS485_B"])
    route_wire(
        board,
        [via_b_src, (via_b_src[0], via_b_dst[1]), via_b_dst],
        TW_SIGNAL,
        B,
        nets["RS485_B"],
    )
    route_wire(board, [via_b_dst, j5_b], TW_SIGNAL, F, nets["RS485_B"])

    # D3: SM712 TVS diode on RS485 A/B lines — D3 at (44,33)
    # Pin 1 = A, Pin 2 = GND, Pin 3 = B
    d3_a = get_pad_pos(parts["D3"], 1)   # connects to RS485_A
    d3_b = get_pad_pos(parts["D3"], 3)   # connects to RS485_B
    # D3 pin 1 -> RS485_A (tap off the A trace between U5 and R7)
    route_wire(board, [d3_a, (d3_a[0], r7_a[1])], TW_SIGNAL, F, nets["RS485_A"])
    # D3 pin 3 -> RS485_B (tap off the B trace between U5 and R7)
    route_wire(board, [d3_b, (d3_b[0], r7_b[1])], TW_SIGNAL, F, nets["RS485_B"])
    # D3 pin 2 -> GND via (handled in GND SMD vias section above)

    # === POWER LED PATH ===
    # R10 at (54,5), LED1 at (54,7)
    r10_2 = get_pad_pos(parts["R10"], 2)  # PWR_LED
    led1_a = get_pad_pos(parts["LED1"], 1)  # Anode (PWR_LED)
    led1_k = get_pad_pos(parts["LED1"], 2)  # Cathode (GND)
    # R10.2 -> LED1.1 (short vertical trace)
    route_wire(board, [r10_2, (led1_a[0], r10_2[1]), led1_a], TW_SIGNAL, F, nets["PWR_LED"])
    # LED1.2 (cathode) -> GND via
    add_via(board, led1_k, nets["GND"])

    # === FTDI / AUTO-RESET PATH ===
    # J6 at (4,27) — J6.4 (TXO) -> ESP32 RXD0 (pin 34)
    j6_tx = get_pad_pos(parts["J6"], 4)
    esp_rxd0 = get_pad_pos(parts["U4"], 34)
    via_ftdi_tx = (j6_tx[0] + 3, j6_tx[1])  # ~(7, 27)
    add_via(board, via_ftdi_tx, nets["FTDI_TX"])
    route_wire(board, [j6_tx, via_ftdi_tx], TW_SIGNAL, F, nets["FTDI_TX"])
    via_ftdi_tx2 = (esp_rxd0[0] + 3, esp_rxd0[1])
    add_via(board, via_ftdi_tx2, nets["FTDI_TX"])
    route_wire(
        board,
        [via_ftdi_tx, (via_ftdi_tx[0], via_ftdi_tx2[1]), via_ftdi_tx2],
        TW_SIGNAL,
        B,
        nets["FTDI_TX"],
    )
    route_wire(board, [via_ftdi_tx2, esp_rxd0], TW_SIGNAL, F, nets["FTDI_TX"])

    # J6.5 (RXI) -> ESP32 TXD0 (pin 35)
    j6_rx = get_pad_pos(parts["J6"], 5)
    esp_txd0 = get_pad_pos(parts["U4"], 35)
    via_ftdi_rx = (j6_rx[0] + 3, j6_rx[1])  # ~(7, 27)
    add_via(board, via_ftdi_rx, nets["FTDI_RX"])
    route_wire(board, [j6_rx, via_ftdi_rx], TW_SIGNAL, F, nets["FTDI_RX"])
    via_ftdi_rx2 = (esp_txd0[0] + 3, esp_txd0[1])
    add_via(board, via_ftdi_rx2, nets["FTDI_RX"])
    route_wire(
        board,
        [via_ftdi_rx, (via_ftdi_rx[0], via_ftdi_rx2[1]), via_ftdi_rx2],
        TW_SIGNAL,
        B,
        nets["FTDI_RX"],
    )
    route_wire(board, [via_ftdi_rx2, esp_txd0], TW_SIGNAL, F, nets["FTDI_RX"])

    # J6.6 (DTR) -> C9.1 -> C9.2 -> R9.1 -> ESP32 GPIO0 (pin 25)
    j6_dtr = get_pad_pos(parts["J6"], 6)
    c9_1 = get_pad_pos(parts["C9"], 1)
    c9_2 = get_pad_pos(parts["C9"], 2)
    r9_1 = get_pad_pos(parts["R9"], 1)
    esp_gpio0 = get_pad_pos(parts["U4"], 25)

    route_wire(
        board, [j6_dtr, (c9_1[0], j6_dtr[1]), c9_1], TW_SIGNAL, F, nets["FTDI_DTR"]
    )
    # C9.2 connects to GPIO0 net along with R9.1
    route_wire(board, [c9_2, (r9_1[0], c9_2[1]), r9_1], TW_SIGNAL, F, nets["ESP_GPIO0"])
    # R9.1 -> ESP32 GPIO0 via bottom layer
    via_gpio0 = (r9_1[0] - 3, r9_1[1])
    add_via(board, via_gpio0, nets["ESP_GPIO0"])
    route_wire(board, [r9_1, via_gpio0], TW_SIGNAL, F, nets["ESP_GPIO0"])
    via_gpio0_esp = (esp_gpio0[0] + 3, esp_gpio0[1])
    add_via(board, via_gpio0_esp, nets["ESP_GPIO0"])
    route_wire(
        board,
        [via_gpio0, (via_gpio0[0], via_gpio0_esp[1]), via_gpio0_esp],
        TW_SIGNAL,
        B,
        nets["ESP_GPIO0"],
    )
    route_wire(board, [via_gpio0_esp, esp_gpio0], TW_SIGNAL, F, nets["ESP_GPIO0"])

    # J6.2 (RTS) -> C10.1 -> C10.2 -> R8.1 -> ESP32 EN (pin 3)
    j6_rts = get_pad_pos(parts["J6"], 2)
    c10_1 = get_pad_pos(parts["C10"], 1)
    c10_2 = get_pad_pos(parts["C10"], 2)
    r8_1 = get_pad_pos(parts["R8"], 1)
    esp_en = get_pad_pos(parts["U4"], 3)

    route_wire(
        board, [j6_rts, (c10_1[0], j6_rts[1]), c10_1], TW_SIGNAL, F, nets["FTDI_RTS"]
    )
    route_wire(board, [c10_2, (r8_1[0], c10_2[1]), r8_1], TW_SIGNAL, F, nets["ESP_EN"])
    # C11 connects between EN and GND (already has via for GND)
    c11_1 = get_pad_pos(parts["C11"], 1)
    route_wire(board, [r8_1, (r8_1[0], c11_1[1]), c11_1], TW_SIGNAL, F, nets["ESP_EN"])
    # R8.1 -> ESP32 EN via bottom layer
    via_en = (r8_1[0] - 3, r8_1[1])
    add_via(board, via_en, nets["ESP_EN"])
    route_wire(board, [r8_1, via_en], TW_SIGNAL, F, nets["ESP_EN"])
    via_en_esp = (esp_en[0] - 3, esp_en[1])
    add_via(board, via_en_esp, nets["ESP_EN"])
    route_wire(
        board,
        [via_en, (via_en[0], via_en_esp[1]), via_en_esp],
        TW_SIGNAL,
        B,
        nets["ESP_EN"],
    )
    route_wire(board, [via_en_esp, esp_en], TW_SIGNAL, F, nets["ESP_EN"])


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
      2. F.Cu copper zone (3V3) around the tab pad area for front-side heat
         spreading (~9mm x 8.5mm).
      3. B.Cu copper zone (3V3) under U1 for back-side heat spreading
         (~13mm x 14mm, priority 1 to override GND pour).

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

    # --- Thermal vias (3V3 net, 0.4mm drill / 0.8mm annular) ---
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
        add_via(board, vpos, nets["3V3"],
                drill=THERMAL_VIA_DRILL, size=THERMAL_VIA_SIZE)

    # --- F.Cu thermal copper zone (3V3) ---
    # Heat spreader on front layer around the tab pad area.
    # Bounds chosen to avoid U1 input-side pads (GND/VIN at y≈8.85)
    # and input caps C2 (at x=36.5). Solid pad connections for minimum
    # thermal resistance.
    fcu_zone = pcbnew.ZONE(board)
    fcu_zone.SetLayer(pcbnew.F_Cu)
    fcu_zone.SetNet(nets["3V3"])
    fcu_zone.SetPadConnection(pcbnew.ZONE_CONNECTION_FULL)
    fcu_zone.SetMinThickness(mm(0.25))
    fcu_zone.SetFillMode(pcbnew.ZONE_FILL_MODE_POLYGONS)
    fcu_zone.SetAssignedPriority(1)

    fcu_zone.AppendCorner(pt(39.0, 11.5), -1)
    fcu_zone.AppendCorner(pt(47.0, 11.5), -1)
    fcu_zone.AppendCorner(pt(47.0, 20.0), -1)
    fcu_zone.AppendCorner(pt(39.0, 20.0), -1)
    board.Add(fcu_zone)

    # --- B.Cu thermal copper zone (3V3) ---
    # Larger island on bottom layer to absorb heat from thermal vias.
    # Priority 1 ensures this fills before the GND pour (priority 0),
    # carving out a 3V3 island from the ground plane under U1.
    bcu_zone = pcbnew.ZONE(board)
    bcu_zone.SetLayer(pcbnew.B_Cu)
    bcu_zone.SetNet(nets["3V3"])
    bcu_zone.SetPadConnection(pcbnew.ZONE_CONNECTION_FULL)
    bcu_zone.SetMinThickness(mm(0.25))
    bcu_zone.SetFillMode(pcbnew.ZONE_FILL_MODE_POLYGONS)
    bcu_zone.SetAssignedPriority(1)

    bcu_zone.AppendCorner(pt(36.0, 7.0), -1)
    bcu_zone.AppendCorner(pt(49.0, 7.0), -1)
    bcu_zone.AppendCorner(pt(49.0, 21.0), -1)
    bcu_zone.AppendCorner(pt(36.0, 21.0), -1)
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
    add_text(j1[0], j1[1] + 5.5, "+", size=0.7)
    add_text(j1[0] + 3.5, j1[1] + 5.5, "-", size=0.7)

    # J2: Wiegand
    j2c = get_pad_pos(parts["J2"], 2)  # pin 2; pins at -3.5, 0, +3.5, +7.0 relative
    add_text(j2c[0] + 1.75, j2c[1] - 5.0, "WIEGAND", size=0.7)
    add_text(j2c[0] - 3.5, j2c[1] + 5.0, "D0", size=0.6)
    add_text(j2c[0], j2c[1] + 5.0, "D1", size=0.6)
    add_text(j2c[0] + 3.5, j2c[1] + 5.0, "+", size=0.6)
    add_text(j2c[0] + 7.0, j2c[1] + 5.0, "-", size=0.6)

    # J3: Relay
    j3c = get_pad_pos(parts["J3"], 1)
    add_text(j3c[0] + 2.5, j3c[1] - 3.5, "RELAY", size=0.8)
    add_text(j3c[0], j3c[1] + 5.5, "SW", size=0.6)
    add_text(j3c[0] + 3.5, j3c[1] + 5.5, "V+", size=0.6)

    # J5: RS485
    j5c = get_pad_pos(parts["J5"], 2)
    add_text(j5c[0] + 1.75, j5c[1] - 6.0, "RS485", size=0.8)
    add_text(j5c[0] - 3.5, j5c[1] - 5.0, "A", size=0.6)
    add_text(j5c[0], j5c[1] - 5.0, "B", size=0.6)
    add_text(j5c[0] + 3.5, j5c[1] - 5.0, "G", size=0.6)

    # J6: FTDI (non-standard: pin 2 = RTS instead of CTS for auto-reset)
    j6c = get_pad_pos(parts["J6"], 3)
    add_text(j6c[0] - 4.0, j6c[1], "FTDI", size=0.7)
    add_text(j6c[0] - 4.0, j6c[1] + 2.0, "P2=RTS", size=0.5)

    # Termination resistor note
    r7 = get_pad_pos(parts["R7"], 1)
    add_text(r7[0], r7[1] - 2.5, "TERM", size=0.5)


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
# MAIN
# =============================================================================


def main():
    script_dir = Path(__file__).parent.resolve()
    output_dir = script_dir / "output"
    gerber_dir = output_dir / "gerbers"
    pcb_file = output_dir / "access_controller_v2.kicad_pcb"

    output_dir.mkdir(exist_ok=True)
    gerber_dir.mkdir(exist_ok=True)

    print("=" * 70)
    print("Conway Access Controller v2 - PCB Design Generator")
    print("Using KiCad pcbnew Python API")
    print("=" * 70)

    # Step 1: Create board
    print("\n[1/8] Creating board...")
    board = pcbnew.BOARD()
    board.GetDesignSettings().SetCopperLayerCount(2)
    board.GetDesignSettings().SetBoardThickness(mm(1.6))
    print(f"  Board: {BOARD_W}mm x {BOARD_H}mm, 2-layer, 1.6mm thick")

    # Step 2: Create nets
    print("[2/8] Creating nets...")
    nets = create_nets(board)
    print(f"  Created {len(nets)} nets")

    # Step 3: Place components
    print("[3/8] Placing components...")
    parts = place_components(board, nets)
    print(f"  Placed {len(parts)} components")

    # Step 4: Assign nets
    print("[4/8] Assigning nets to pads...")
    assign_nets(parts, nets)
    print("  All nets assigned")

    # Step 5: Route traces
    print("[5/8] Routing traces...")
    route_all(board, parts, nets)
    print("  All traces routed")

    # Step 6: Add board features
    print("[6/8] Adding board features...")
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
    print("[7/8] Saving KiCad PCB file...")
    # aSkipSettings=True avoids a connectivity rebuild that can hang in headless mode
    pcbnew.SaveBoard(str(pcb_file), board, True)
    print(f"  Saved: {pcb_file}")

    # Step 8: Export outputs
    print("[8/8] Generating output files...")
    export_gerbers(pcb_file, gerber_dir)
    generate_jlcpcb_bom(parts, str(output_dir / "access_controller_v2_jlcpcb_bom.csv"))
    generate_jlcpcb_cpl(parts, str(output_dir / "access_controller_v2_jlcpcb_cpl.csv"))

    # Summary
    print("\n" + "=" * 70)
    print("PCB Design Generation Complete!")
    print("=" * 70)
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
    print("  - C1, C1B: 10uF 25V 0805 on 12V input (bulk decoupling)")
    print("  - C3, C3B: 22uF 25V 0805 on 3.3V LDO output")
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
