KICAD_PYTHON := /Applications/KiCad/KiCad.app/Contents/Frameworks/Python.framework/Versions/3.9/bin/python3
OUTPUT_DIR   := output
GERBER_DIR   := $(OUTPUT_DIR)/gerbers
GERBER_ZIP   := $(OUTPUT_DIR)/access_controller_v2_gerbers.zip
PCB_FILE     := $(OUTPUT_DIR)/access_controller_v2.kicad_pcb
ORTHOROUTE   := OrthoRoute

.PHONY: all generate zip clean placement autoroute autoroute-gui orthoroute-setup

all: generate zip

# Generate the full board (with hand-coded routing)
generate:
	$(KICAD_PYTHON) pcb_generate.py

zip: generate
	cd $(GERBER_DIR) && zip -FS ../access_controller_v2_gerbers.zip *

clean:
	rm -rf $(OUTPUT_DIR)

# --- OrthoRoute Autorouting Workflow ---

# Install OrthoRoute Python dependencies
orthoroute-setup:
	pip3 install -r $(ORTHOROUTE)/requirements.txt

# Generate board with placement only (no traces) for autorouting
placement:
	$(KICAD_PYTHON) pcb_generate.py --no-route
	@echo ""
	@echo "Unrouted board ready: $(PCB_FILE)"
	@echo "Next: open in KiCad and run 'make autoroute-gui', or use 'make autoroute'"

# Run OrthoRoute CLI autorouter on the unrouted board (CPU-only, no KiCad needed)
autoroute: placement
	python3 $(ORTHOROUTE)/main.py cli $(PCB_FILE) -o $(OUTPUT_DIR)

# Run OrthoRoute GUI with KiCad IPC API (requires KiCad running with board open)
autoroute-gui:
	@echo "=== OrthoRoute GUI Autorouting ==="
	@echo ""
	@echo "Prerequisites:"
	@echo "  1. Run 'make placement' to generate the unrouted board"
	@echo "  2. Open $(PCB_FILE) in KiCad 9.0+"
	@echo "  3. Enable KiCad API: Preferences -> Plugins -> Enable KiCad API"
	@echo "  4. Make sure the Select tool (arrow) is active, nothing selected"
	@echo ""
	@echo "Starting OrthoRoute plugin..."
	python3 $(ORTHOROUTE)/main.py plugin
