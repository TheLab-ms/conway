---
name: kicad-review
description: Review a KiCad PCB design for substantive defects. Catches pinout/footprint mismatches vs. datasheet, missing/undervalued decoupling, clearance/trace-width violations for voltage and current, schematic-PCB parity breaks, thermal/crystal/RF/differential-pair layout mistakes, stackup and return-path issues, and mechanical problems. Use when the user asks to review, audit, sanity-check, or sign off on a `.kicad_sch`/`.kicad_pcb`. Emits a prioritized findings list with concrete fixes for the invoking agent to apply.
---

## Non-negotiables

1. **Never trust the schematic symbol or footprint.** Generated parts (esp. easyeda2kicad, SnapEDA) are wrong often enough that every IC, connector, and mechanically-critical passive must be cross-checked against the **manufacturer datasheet** — not a distributor page, not a cached description. Fetch the PDF. Verify pin name + pin number + bank/function, pad pitch, pad size, thermal pad, courtyard, polarity marker, and package variant suffix (e.g. `-Q1`, `-R`, `TR` reel vs. tube).
2. **Run the tools, don't eyeball.** ERC, DRC with `--schematic-parity`, and refilled zones must all pass clean or every remaining violation must be explicitly justified in the report.
3. **Be specific.** Every finding = `severity | location (refdes/net/coords) | rule violated | quoted datasheet/standard | concrete fix`. No vague "consider reviewing..." — the invoking agent needs actionable diffs.
4. **Don't invent requirements.** If the user didn't specify a spec (impedance target, class, standard), ask or flag as "assumed". Don't downgrade a design by imposing rules it was never meant to meet.

## Inputs to gather first

```bash
kicad-cli version
# Parse structure — don't regex s-expr by hand
python -c "import kiutils" 2>/dev/null || pip install kiutils kicad-skip
```

Reviews assume the standard one-board-per-subdirectory layout from the `kicad-pcb` skill: a self-contained `<proj>/` with `Makefile` and a gitignored `./build/`. **Always `cd` into the board directory and run `make check` first** — it produces `build/erc.rpt` and `build/drc.json` with the correct flags. If a board lacks the standard layout or Makefile, flag that as a MAJOR finding (reproducibility/handoff risk) and scaffold it before continuing the review.

- `.kicad_pro` → netclasses, design rules, constraints, stackup (`board.layers`, `board.design_settings`).
- `.kicad_sch` (all sheets) → symbols, nets, hierarchical labels, power flags, DNPs.
- `.kicad_pcb` → footprints, tracks, zones, vias, layers, board outline.
- `Makefile` + `.gitignore` → confirm `build/` is ignored, `make jlcpcb` recipe is the ones documented in `kicad-pcb`, and no fab artifacts are committed alongside source.
- Project `libs/` + `sym-lib-table`/`fp-lib-table` → which symbols/footprints are project-local vs. stock (project-local = higher suspicion).
- `build/<proj>-bom.csv` (produced by `make bom`) drives the part inventory; if missing, run `make bom` rather than hand-rolling a BOM export.

Build a part inventory: `{refdes → (MPN, value, footprint, datasheet_url, symbol_libid, LCSC)}`. This table drives everything below. Every fitted part must have a non-empty `LCSC` field — JLCPCB full assembly will reject the order otherwise.

## Automated checks (run first, fail fast)

Preferred: `make check` (runs the same commands below with the project's pinned flags). Equivalent direct invocation, with all outputs under the gitignored `./build/`:

```bash
mkdir -p build
kicad-cli sch erc  <proj>.kicad_sch -o build/erc.json  --format json --severity-all --exit-code-violations
kicad-cli pcb drc  <proj>.kicad_pcb -o build/drc.json  --format json --schematic-parity \
                   --all-track-errors --refill-zones --severity-all --exit-code-violations
kicad-cli pcb export step <proj>.kicad_pcb -o build/board.step --subst-models  # catches 3D collisions / missing models
```

Then dry-run the full fab pipeline — a review is not signed off until `make jlcpcb` exits 0 and the resulting `build/<proj>-jlcpcb.zip` is inspectable:

```bash
make jlcpcb
unzip -l build/<proj>-jlcpcb.zip   # confirm gerbers + bom + cpl all present
```

Classify each violation: **true positive → fix**, **false positive → document exclusion in `.kicad_dru`/project rules**, **agent-introduced → refactor**. Never silence a violation by loosening a rule without written justification.

## Per-component datasheet audit (the bulk of the work)

For every IC, connector, crystal, inductor, MOSFET, LDO, ADC/DAC, sensor, and any passive with a tight tolerance/voltage/current spec:

1. **Resolve MPN → datasheet PDF.** Prefer manufacturer domain. For LCSC-sourced parts, follow LCSC → manufacturer link; JLCPCB "Basic Parts" library data is frequently wrong on pin 1 marker orientation.
2. **Pinout:** enumerate every pin on the symbol; compare pin number ↔ pin name ↔ function to the datasheet pin table. Flag swapped power/ground, NC pins that are actually DNC/thermal, exposed-pad net connection, multi-function pins wired to the wrong default.
3. **Footprint/land pattern:** pitch, pad size, pad shape, solder-mask expansion, paste reduction for thermal pads, courtyard vs. package max dimensions, pin-1 marker placement. Compare against the datasheet's "Recommended Land Pattern" (or IPC-7351 Nominal density if absent).
4. **Absolute maximum ratings:** Vin, Iin, Vgs, Vds, temperature. Confirm schematic voltages/currents are within spec *at worst case*, not nominal.
5. **Required support circuitry:** decoupling caps (count, value, placement distance ≤ recommendation — datasheet usually says "within 2 mm of pin"), bulk cap, feedback dividers, compensation, bootstrap cap, EN/RST pull-up/down, unused input handling.
6. **Package suffix:** verify BOM MPN suffix matches the footprint (e.g. `QFN-24-EP` vs. `QFN-24` without exposed pad; `-Q100` automotive vs. commercial; reel vs. tube doesn't matter electrically but breaks JLC assembly orders).

## Layout review (after the per-part audit)

- **Decoupling placement:** every VCC pin has its cap on the same layer within the datasheet-recommended distance; vias to power/ground ≤ 2 where required; cap-first topology (current enters cap before IC pin) on sensitive supplies.
- **Return paths:** high-speed nets have a continuous reference plane directly beneath; no splits, no via fences crossed without stitching caps.
- **Clearance (IPC-2221B):** trace-to-trace and pad-to-pad clearance ≥ table B-1 for the peak working voltage (with 1.5× derating for safety, more for B1/B2 conformal-coat classes). Check mains/battery rails specifically.
- **Current capacity (IPC-2152):** trace width × copper-weight must carry I_max with ≤ ΔT spec'd for the board (typ. 10 °C rise). Check VIN rails and power MOSFET drains; flag 0.2 mm traces carrying >1 A.
- **Via current:** a 0.3 mm drilled via carries ~1–1.5 A before derating; parallel vias for each ampere beyond.
- **Thermals:** thermal pad connected with sufficient vias (datasheet typically 4–9); copper pour area sized for θ_JA target at worst-case P_diss.
- **Crystal/oscillator:** XTAL inside guard ring, GND pour under it, load caps sized from `CL_spec = 2·C_L − C_stray` (C_stray ≈ 3–5 pF), traces short and symmetric, no signals crossing beneath.
- **Differential pairs (USB, Ethernet, HDMI, LVDS, MIPI):** intra-pair length match within spec (USB2 FS: <3 mm; USB3: <0.15 mm; HDMI: <0.13 mm), impedance-controlled netclass with correct target (USB 90 Ω, HDMI 100 Ω), no unbalanced vias, reference plane continuity.
- **RF:** 50 Ω CPWG/microstrip geometry validated against stackup (use `kicad-cli pcb export ipc2581` for impedance fields; otherwise flag as "uncalculated"); antenna keepout zones per module datasheet.
- **ESD/protection:** TVS on every external-facing connector pin, reverse-polarity protection on power input, fuse/PTC rating ≥ normal draw and ≤ bus/connector ampacity.
- **Soldermask/silkscreen:** no silk on pads, no soldermask slivers <0.1 mm, pin-1 markers on silk + fab layers.
- **Mechanical:** mounting-hole sizes match hardware (M3 → 3.2 mm clearance / 5.5–6 mm keepout), connector strain reliefs, board-edge clearance ≥ 0.3 mm for copper, exposed-edge components inside courtyard.

## Schematic-specific review

- All power nets driven by a source (no floating `VCC`/`3V3`). `power_flag` on every rail.
- Every IC power pin has a net, not NC; every NC pin either tied per datasheet or explicitly labelled NC in schematic.
- No mixed ground nets without intentional star/ferrite connection; AGND/DGND/PGND handling documented.
- Pull-up/pull-down presence and value vs. datasheet requirement (I²C: 2.2–10 kΩ typical, check bus capacitance; SPI CS idle high; boot-strap pins on MCUs).
- DNP parts: reason in a field or sheet note; verify BOM variant configuration.

## Output format (emit to invoking agent)

Markdown, grouped by severity, each finding self-contained:

```
### [BLOCKER] U3 pin 14 (RESET) has no pull-up
- Location: sheet `power.kicad_sch`, symbol U3 (STM32G474RET6), net `/RESET`
- Rule: datasheet RM0440 §7.3.2 — NRST is an input; internal pull-up is weak (~40 kΩ) and insufficient for noise immunity on external reset line exposed via J5.
- Fix: add 10 kΩ pull-up to 3V3 at U3.14, plus 100 nF to GND; place within 10 mm of MCU.
- Evidence: STM32G474 datasheet DS12288 Rev 5, Table 19; reference schematic RM0440 figure 15.
```

Severity ladder:
- **BLOCKER** — will not function, will release smoke, or fails safety/regulatory (wrong pinout, ampacity, clearance for mains).
- **MAJOR** — intermittent/marginal operation, EMI failure, yield problem (missing decoupling, bad crystal layout, length mismatch).
- **MINOR** — best-practice, cosmetic, assembly-friendliness (silk on pad, missing fiducial, inconsistent refdes).
- **QUESTION** — cannot verify without user input (missing impedance spec, unclear intended operating voltage, ambiguous DNP).

End with: (a) summary counts per severity, (b) an ordered fix list the invoking agent can apply via `kicad-skip`/`kiutils`, (c) a `build/drc.json`/`build/erc.json` diff showing which existing violations are legitimate exclusions vs. must-fix, and (d) confirmation that `make jlcpcb` exits 0 and produces a complete `build/<proj>-jlcpcb.zip` (or the list of missing artifacts blocking fab).

## Anti-patterns to refuse

- Approving a board because "DRC passes" without per-part datasheet verification — DRC only enforces rules already in the project file; it cannot catch a wrong footprint or swapped pin.
- Accepting "this symbol came from the official KiCad library so it's fine" — stock libraries have errors too; confirm anyway, just more quickly.
- Rewriting the layout. This skill **reviews and recommends**; it does not silently edit the board. Emit a fix list; let the invoking agent (or the `kicad-pcb` skill) apply it.

## Workflow tips (learned the hard way)

- **Parse `.kicad_sch` / `.kicad_pcb` directly, never review from a PDF export.** PDFs from `kicad-cli sch export pdf` are rasterized images — you cannot grep nets, refdes, or `Footprint` properties. Use `kiutils` (typed) or `kicad-skip` (s-expr REPL) and walk the actual project file.
- **Calibrate ERC severity to the part-source mix.** Boards with `easyeda2kicad`-imported symbols emit hundreds of `pin_to_pin` warnings because imported pins are typed `unspecified`. These are false positives, not findings. Run ERC with `--severity-error` for go/no-go and only escalate to `--severity-all` when hunting a specific class of issue. State explicitly in the report which severity floor was used.
- **A schematic-only review is valid.** If the user has scoped out the PCB, skip the layout/DRC/fab sections entirely — don't fabricate findings about a `.kicad_pcb` that doesn't exist or is a placeholder. Confirm absence intentional, then deliver schematic findings + LCSC/BOM audit only.
- **Always cross-check `Footprint` property strings against the registered libraries.** A symbol can name `MyLib:R_0603_1608Metric` that doesn't exist in any `.pretty` registered in `fp-lib-table` — ERC won't catch it but `--schematic-parity` will, and `make bom` will emit blanks. Diff the set of referenced footprints against `fp-lib-table` URIs before signing off.
- **Distrust easyeda2kicad imports as a category, not just individually.** When auditing, sort the part inventory by symbol source and put every easyeda-imported IC at the top of the datasheet-verification queue: pin numbering and pin-1 markers are the most common defect class.
- **Two QUESTIONs are better than one wrong assumption.** When the spec is silent on a default jumper position, pull-up choice, or NC-vs-DNC pin, file a `[QUESTION]` finding rather than guessing — the user resolves it in seconds and the design ships correct.
