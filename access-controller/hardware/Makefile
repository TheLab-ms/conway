KICAD_PYTHON := /Applications/KiCad/KiCad.app/Contents/Frameworks/Python.framework/Versions/3.9/bin/python3
OUTPUT_DIR   := output
GERBER_DIR   := $(OUTPUT_DIR)/gerbers
GERBER_ZIP   := $(OUTPUT_DIR)/access_controller_v2_gerbers.zip

.PHONY: all generate zip clean

all: generate zip

generate:
	$(KICAD_PYTHON) pcb_generate.py

zip: generate
	cd $(GERBER_DIR) && zip -FS ../access_controller_v2_gerbers.zip *

clean:
	rm -rf $(OUTPUT_DIR)
