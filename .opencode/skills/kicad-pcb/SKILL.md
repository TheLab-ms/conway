---
name: kicad-pcb
description: Design KiCad PCBs programmatically - import components from LCSC/EasyEDA via easyeda2kicad, author connectivity with SKiDL/atopile/kicad-skip, GPU-autoroute dense digital boards with OrthoRoute, and validate + emit JLCPCB full-assembly artifacts headlessly via a per-board Makefile + kicad-cli. Use when the user wants to create, modify, route, or generate fab outputs for a KiCad project.
---

## Scope & limits

Agents are good at: component sourcing, netlists, schematic connectivity, ERC/DRC loops, fab-file generation, and bulk autorouting of dense digital nets via OrthoRoute (below). Agents are **bad at**: initial part placement, routing critical/analog/RF/high-speed signals, pretty schematic layout. Default: stop at schematic + netlist + placement and hand off to a human. Only autoroute when the user explicitly asks, and always leave critical signals for manual routing.

## Tools

- `easyeda2kicad` (`pip install easyeda2kicad`) — pulls LCSC parts (`C#####`) as KiCad symbol + footprint + 3D.
- `kicad-cli` — headless ERC, DRC, gerbers, BOM, pos, STEP.
- `make` + the per-board `Makefile` (template below) — single source of truth for every fab artifact.
- Python libs (pick one): `kiutils` (typed), `kicad-skip` (REPL-style s-expr edits), `skidl` (netlist-as-code), or `atopile` (declarative HDL, greenfield).

## Opinionated workflow (one board = one subdirectory)

**Every PCB design lives in its own self-contained subdirectory.** No shared KiCad projects, no cross-board library sharing outside `libs/`, no fab artifacts checked into git. The directory is the unit of build, review, fab order, and version control.

```
<repo>/
└── <proj>/                       # one board, one folder
    ├── .gitignore                # MUST contain `build/`
    ├── Makefile                  # `make jlcpcb` → ./build/<proj>-jlcpcb.zip
    ├── README.md                 # board purpose, rev, assembly notes
    ├── <proj>.kicad_pro
    ├── <proj>.kicad_sch          # (+ any hierarchical sheets)
    ├── <proj>.kicad_pcb
    ├── sym-lib-table             # project-local, portable paths
    ├── fp-lib-table
    ├── libs/                     # COMMITTED — easyeda2kicad output is not reproducible
    │   ├── <proj>.kicad_sym
    │   ├── <proj>.pretty/
    │   └── <proj>.3dshapes/
    └── build/                    # GITIGNORED — every generated artifact lives here
        ├── erc.rpt
        ├── drc.json
        ├── gerbers/              # raw gerber + drill set
        ├── <proj>-gerbers.zip    # what you upload to JLCPCB "Add gerber file"
        ├── <proj>-bom.csv        # JLCPCB-formatted BOM
        ├── <proj>-cpl.csv        # JLCPCB-formatted pick-and-place
        ├── <proj>.step           # 3D model for mechanical review
        └── <proj>-jlcpcb.zip     # convenience bundle: gerbers + bom + cpl
```

When creating a new board, **always** scaffold the full layout above — never drop loose `.kicad_*` files at the repo root, never mix two designs in one directory.

### `.gitignore` (required, in the board subdirectory)

```
build/
*-backups/
*.kicad_prl
fp-info-cache
~*
```

### Library table portability

Register project-local libs with `${KIPRJMOD}` so the project relocates cleanly:

```
(sym_lib_table (lib (name "<proj>")(type "KiCad")(uri "${KIPRJMOD}/libs/<proj>.kicad_sym")(options "")(descr "")))
(fp_lib_table  (lib (name "<proj>")(type "KiCad")(uri "${KIPRJMOD}/libs/<proj>.pretty")(options "")(descr "")))
```

### `Makefile` template (drop into every board, edit only `PROJ`)

```make
# ---- per-board Makefile: builds every artifact JLCPCB needs for full assembly ----
PROJ    := <proj>
SCH     := $(PROJ).kicad_sch
PCB     := $(PROJ).kicad_pcb
BUILD   := build
GERBERS := $(BUILD)/gerbers
KICAD   ?= kicad-cli

.PHONY: all jlcpcb erc drc gerbers drill bom pos step bundle clean check

all: jlcpcb                  ## default: produce the JLCPCB upload bundle
jlcpcb: check bundle         ## DRC/ERC clean + ./build/$(PROJ)-jlcpcb.zip ready to upload

$(BUILD) $(GERBERS):
	mkdir -p $@

# --- validation (fail the build on any violation) ---
check: erc drc

erc: | $(BUILD)
	$(KICAD) sch erc $(SCH) -o $(BUILD)/erc.rpt \
	    --severity-error --exit-code-violations

drc: | $(BUILD)
	$(KICAD) pcb drc $(PCB) -o $(BUILD)/drc.json --format json \
	    --schematic-parity --all-track-errors --refill-zones \
	    --severity-error --exit-code-violations

# --- fab artifacts ---
gerbers: | $(GERBERS)
	$(KICAD) pcb export gerbers $(PCB) -o $(GERBERS)/

drill: | $(GERBERS)
	$(KICAD) pcb export drill $(PCB) -o $(GERBERS)/ \
	    --generate-map --map-format gerberx2 \
	    --excellon-separate-th --excellon-units mm

$(BUILD)/$(PROJ)-gerbers.zip: gerbers drill
	cd $(GERBERS) && zip -qr ../$(PROJ)-gerbers.zip .

# JLCPCB BOM: Comment, Designator, Footprint, LCSC Part # (LCSC field must exist on every fitted part)
bom: | $(BUILD)
	$(KICAD) sch export bom $(SCH) -o $(BUILD)/$(PROJ)-bom.csv \
	    --preset "JLCPCB" \
	    --fields "Value,Reference,Footprint,LCSC" \
	    --labels "Comment,Designator,Footprint,LCSC Part #" \
	    --group-by "Value,Footprint,LCSC" \
	    --exclude-dnp

# JLCPCB CPL (pick-and-place): use drill origin so coords match the gerber zip
pos: | $(BUILD)
	$(KICAD) pcb export pos $(PCB) -o $(BUILD)/$(PROJ)-cpl.csv \
	    --format csv --units mm --use-drill-file-origin \
	    --side both --exclude-dnp

step: | $(BUILD)
	$(KICAD) pcb export step $(PCB) -o $(BUILD)/$(PROJ).step --subst-models

# --- final upload bundle ---
bundle: $(BUILD)/$(PROJ)-gerbers.zip bom pos step
	cd $(BUILD) && zip -q $(PROJ)-jlcpcb.zip \
	    $(PROJ)-gerbers.zip $(PROJ)-bom.csv $(PROJ)-cpl.csv

clean:
	rm -rf $(BUILD)
```

`make jlcpcb` is the **only** command an agent or human should ever need to produce fab artifacts. If a step needs a tweak (e.g. `--exclude-dnp` toggled, extra BOM field), edit the Makefile and commit — never run `kicad-cli` ad-hoc and upload the result.

### Uploading to JLCPCB (full assembly, SMT)

1. `make jlcpcb` — confirm exit 0; inspect `build/erc.rpt` and `build/drc.json` if not.
2. JLCPCB order page → "Add gerber file" → upload `build/<proj>-gerbers.zip`.
3. Enable "PCB Assembly" → upload `build/<proj>-bom.csv` as BOM and `build/<proj>-cpl.csv` as CPL.
4. JLCPCB's column-mapping UI may prompt you; the headers from the Makefile (`Comment, Designator, Footprint, LCSC Part #` for BOM; `Designator, Mid X, Mid Y, Layer, Rotation` aliasing for CPL) match their defaults — accept and continue.
5. Verify rotations and pin-1 markers in JLCPCB's preview before placing the order. **Rotation mismatches between KiCad and JLCPCB libraries are the #1 cause of misassembled boards** — see https://jlcpcb.com/help/article/how-to-correct-rotational-offsets-of-the-pick-and-place-file

## Importing components

Find the LCSC part (`Cxxxxxx`) on lcsc.com or jlcpcb.com/parts, then:

```bash
# Pull symbol + footprint + 3D into project libs, portable paths, idempotent.
easyeda2kicad --full --lcsc_id C2040 C14877 \
  --output <proj>/libs/<proj> --project-relative --overwrite
```

Flags that matter: `--full` | `--symbol` | `--footprint` | `--3d`, `--lcsc_id` (space-separated), `--output <base>` (no extension; creates `.kicad_sym` + `.pretty/` + `.3dshapes/`), `--project-relative` (required for repo portability), `--overwrite` (without it, existing parts are silently skipped), `--use-cache`, `--debug`.

Always **verify** the output after import — generated symbols/footprints frequently have wrong pin numbering, pad sizes, or courtyards. Grep the `.kicad_sym` for the expected symbol name and open the footprint in KiCad (or `kicad-cli fp export svg`) to eyeball it.

## Authoring connectivity

- **Modify existing schematic:** use `kicad-skip` — dynamic s-expr wrapper, safe (preserves unknown nodes). Clone symbols, set `lib_id` → `<proj>:PartName`, set `Reference`/`Value`/`Footprint` properties, wire nets with `new()` wires/labels.
- **From scratch:** use `skidl` to emit a netlist importable into pcbnew, or `atopile` for a modern code-first flow.
- **Never** try to hand-write a pretty schematic; emit netlist + let human place, or generate a minimal grid layout only if asked.

Every schematic symbol instance must have a `lib_id` that exists in both `lib_symbols` in the `.kicad_sch` *and* the project `sym-lib-table`. The `Footprint` property must name a footprint present in a registered `.pretty`.

## Validation loop

Prefer `make check` (runs ERC + DRC with the right flags into `./build/`). If you need to invoke `kicad-cli` directly during iteration, still write outputs under `./build/`:

```bash
mkdir -p build
kicad-cli sch erc <proj>.kicad_sch -o build/erc.rpt \
  --severity-error --exit-code-violations
kicad-cli pcb drc <proj>.kicad_pcb -o build/drc.json --format json \
  --schematic-parity --severity-error --exit-code-violations
```

Non-zero exit → parse report, fix, retry. `--schematic-parity` catches footprint/symbol mismatches that agents commonly introduce.

## Autorouting with OrthoRoute (optional, dense digital only)

[OrthoRoute](https://github.com/bbenchoff/OrthoRoute) is a GPU-accelerated KiCad autorouter using a Manhattan lattice (layer N horizontal, layer N+1 vertical) + PathFinder (negotiation-based, CUDA SSSP). It shines on backplanes and BGA escape routing with thousands of mundane nets; **don't** use it for analog, RF, differential pairs (not yet supported), or anything where trace geometry matters. Requires KiCad 9.0+, Python 3.12+, NVIDIA GPU + CUDA/CuPy.

**Install** (KiCad plugin manager currently broken per KiCad issue #19465 — run from source):

```bash
git clone https://github.com/bbenchoff/OrthoRoute
cd OrthoRoute && pip install -r requirements.txt
```

Enable `Preferences → Plugins → Enable KiCad API` in KiCad. The IPC API only responds when the selection arrow is active with nothing selected.

**VRAM sizing** (fails hard on OOM):

```
nodes = (W_mm / pitch_mm) * (H_mm / pitch_mm) * layers    # pitch usually 0.4mm
VRAM_GB ≈ nodes / 200_000
```

Rough reference: 100×100mm / 6L → ~10 GB; 200×200mm / 32L → ~40 GB (A100). VRAM depends on board geometry not net count; net count only affects runtime. Use `--cpu-only` if it won't fit (slow).

**Interactive flow** (agent drives a human at the KiCad GUI):

1. Open `.kicad_pcb` in KiCad with components placed.
2. `python main.py` (from the OrthoRoute checkout) — GUI launches, reads board via IPC.
3. Run Manhattan routing from the GUI; import result back to KiCad from the plugin.

**Headless / cloud flow** (agent-friendly, runs anywhere with a GPU):

```bash
# 1. In KiCad GUI with OrthoRoute plugin: File → Export PCB... → save <board>.ORP
# 2. Upload ORP to GPU host (e.g. vast.ai, runpod), then:
python main.py headless <board>.ORP                # writes <board>.ORS
python main.py headless <board>.ORP -o sol.ORS --max-iterations 200 --use-gpu
python main.py headless <board>.ORP --cpu-only     # fallback, slow
# 3. Back in KiCad: OrthoRoute plugin → File → Import Solution... (Ctrl+I) → Apply to KiCad
```

`.ORP` = board export, `.ORS` = routing solution (format docs: `docs/ORP_ORS_file_formats.md` in the repo).

**Expect DRC cleanup afterward.** PathFinder output is "good not great" — some pad-escape geometry fails strict DRC, and a few overlaps are normal. Always run `kicad-cli pcb drc --schematic-parity --exit-code-violations` after import and budget cleanup time. Manually re-route critical nets before autorouting bulk nets (OrthoRoute will respect existing tracks).

## Fab outputs

**Always go through the Makefile.** `make jlcpcb` runs ERC + DRC, emits gerbers/drill/BOM/CPL/STEP into `./build/`, and produces `build/<proj>-jlcpcb.zip` for upload. Do **not** invoke `kicad-cli pcb export ...` by hand for anything that ends up shipped — if the recipe is wrong, fix the Makefile and re-run, so the next agent and the next human get the same artifacts.

If you genuinely need a one-off (e.g. exploratory STEP for a mechanical check), still write it under `./build/` so it's gitignored:

```bash
mkdir -p build && kicad-cli pcb export step <proj>.kicad_pcb -o build/scratch.step --subst-models
```

## Gotchas

- Commit `libs/` to git; EasyEDA API output isn't reproducible. Never commit `build/`.
- One board = one subdirectory = one Makefile. Don't share a `build/` between boards.
- `kicad-cli` ERC/DRC returns 0 on violations unless `--exit-code-violations` is passed (the Makefile already does this — don't drop the flag).
- JLCPCB CPL coordinates must use `--use-drill-file-origin` to align with the gerber zip; the default auxiliary-axis origin will silently shift every part.
- JLCPCB BOM requires an `LCSC` field on every fitted part; `easyeda2kicad`-imported symbols already populate it, hand-placed parts often don't — audit before `make jlcpcb`.
- Rotation offsets between KiCad's symbol orientation and JLCPCB's library orientation are the most common assembly defect; review JLCPCB's preview, and patch persistent offenders with a `JLCPCB Rotation Offset` field consumed by a post-process step in the Makefile if the same parts keep flipping.
- `kicad-cli fp upgrade <lib>.pretty` can migrate Altium/Eagle/EasyEDA legacy footprints into KiCad format.
- macOS path: `/Applications/KiCad/KiCad.app/Contents/MacOS/kicad-cli` — set `KICAD=...` when invoking `make` to override.
- Don't autoroute without explicit user consent. When autorouting, use OrthoRoute only for dense digital nets; pre-route critical signals manually; DRC-clean after.

## Workflow tips (learned the hard way)

- **easyeda2kicad rate-limits silently.** EasyEDA's API returns HTTP 403 after ~4 fast requests. Worse: imports often **partially succeed** — the `.kicad_sym` may be written even when stderr says "ERROR". Always `grep` the symbol file (or `kicad-cli sym list`) after each import to confirm presence, and `sleep 5+` between requests when batching.
- **`--project-relative` has a path-resolution bug** (at least up through easyeda2kicad 0.8.x): it can write outputs to the wrong directory. Pass an **absolute** `--output /abs/path/<proj>/libs/<proj>` instead and let `${KIPRJMOD}` in the lib tables handle portability.
- **Prefer KiCad stdlib footprints for commodity parts** (0402/0603/0805 R/C, headers, screw terminals, tact switches, common diodes/MOSFETs in SOT-23/SOD-323). The stdlib pads are IPC-correct, the courtyards are sane, and you avoid the EasyEDA throttling tax. Reserve `easyeda2kicad --footprint` for ICs and oddball connectors that aren't in the stdlib.
- **Imported symbol pins are typed `unspecified`,** which makes ERC emit a flood of `pin_to_pin` warnings on every wired net. Either retype critical pins to `passive`/`power_in`/`power_out`, or run ERC with `--severity-error` (not `--severity-all`) so the report stays signal-bearing. Document the choice — don't silently downgrade.
- **Schematic-only is a legitimate deliverable.** When the user wants to lay out the PCB themselves, stop at an ERC-clean `.kicad_sch` + `libs/` + `Makefile` and **delete** any stub `.kicad_pcb` to avoid `--schematic-parity` confusion. Don't generate a placement just to "be helpful".
- **Generate schematics with `kiutils`, not by hand-emitting s-exprs.** Even for tiny boards, a Python `gen.py` that builds the sheet, instantiates symbols from `lib_symbols`, and wires nets via labels is faster, regenerable, and reviewable. Commit `gen.py` alongside the `.kicad_sch`.
- **Verify imports via the symbol file, not the PDF.** Schematic PDFs exported by `kicad-cli sch export pdf` are images and effectively unreviewable by an agent. Parse the `.kicad_sch` directly (kiutils / sexpdata / kicad-skip) when verifying that nets, refdes, and `Footprint` properties are correct.
- **`pip` is PEP-668 blocked on most modern Linux.** Always create a per-board `.venv/` (gitignored) for `easyeda2kicad`, `kiutils`, `kicad-skip`. Don't `pip install --break-system-packages`.
