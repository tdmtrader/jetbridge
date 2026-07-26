# Show Map — DareToDream2026 (cues 0.1–20.1)

Two parallel files with an **identical per-cue schema** so the diff between them is literal:

| File | `look:` holds | Who writes it |
|------|---------------|---------------|
| `show-current.yaml` | the **captured** state on the console | the agent (read_stage) |
| `show-target.yaml`  | the **desired** end-state | **you**, by hand |

`show-target.yaml` is initialized to a copy of the current look. **Edit only the cues you want
to change.** Then `diff show-current.yaml show-target.yaml` shows exactly your intended changes,
and tomorrow the agent applies *only* those cues.

Regenerate current-state anytime (e.g. after capturing the PENDING cues live):
`python3 tools/build_show_map.py` (no dependencies). Add new raw reads to `RAW` in that script.

## Scope & status
- Cues **0.1–20.1**, 93 entries. Songs present: 1,2,3,4,5,6,8,9,11,13,14,15,16,17,18,19,20 (17 of 22).
- **Cut in body (no cues): 7, 10, 12** — the gaps validate the song↔number mapping.
- Current looks captured for **0.1–9.5**; **9.6–20.1 = `PENDING_LIVE_READ`** (capture live next session, then re-run the builder).
- Lighting **major cue number = song number** (e.g. 8.x = song 8 = *Surface Pressure*).

## Rig-group vocabulary (use these names in `look:`)
Color-capable groups take `color:`; **gelled** groups are intensity-only (a color word just selects channels).

| Group | Members (semantic → channel) | Color? |
|-------|------------------------------|--------|
| `entrance_aisle` | 1 | gel |
| `audience_stair` | 2 (safety light — normally on) | **LED** |
| `house` | ch3, ch4 | dimmer |
| `alcove` | 5 | gel |
| `ladders.<color>` | by **boom 1–6**; colors: blue yellow green lt_blue orange pink red dk_blue white | gel (intensity only) |
| `trough` | sl=70, sr=71 | **LED** |
| `pars` | dsl73 dsc74 dsr75 usl76 usc77 usr78 | gel |
| `circle` | o'clock 1=80 3=81 5=82 7=83 9=84 11=85 | gel |
| `specials` | center_a87 center_b88 dsc89 dsr90 msr91 usr92 msl95 | gel |
| `spots` | hr=98 hl=99 | LED (tunable white) |
| `under_bench` | sr101 cs102 sl103 | LED (white-only in practice) |
| `strip` | sr105 cs106 sl107 | LED (white-only in practice) |
| `lonestars` | dsl108 dsr109 hsl110 hcs111 hsr112 | **LED + movement** |
| `desires` | usl113 sl114 dsl115 dcs116 dsr117 sr118 usr119 | **LED** |

## How to express a `look:` (target authoring)
```yaml
look:
  audience_stair: {level: 100}
  ladders:
    pink:   {booms: [1,2,3,4,5,6], level: 50}     # uniform level
    yellow: {boom_levels: {1: 30, 2: 20}}          # per-boom
  circle:  {level: 25, at: [1,3,5,7,9,11]}
  desires: {level: 25, at: [usl,sl,dsl,dcs,dsr,sr,usr], color: {hue: 35, sat: 90, name: amber}}
  lonestars:
    levels: {dsl: 0, dsr: 0, hsl: 100, hcs: 100, hsr: 100}   # per-fixture intensity
    position: home          # optional: home | scene | {dsl: {pan: -50.1, tilt: 43.1, zoom: 43.9}, ...}
```
- `level` + `at:` = uniform level across the listed members. `levels:` = per-member map. `BLACKOUT` = empty stage.
- Color only on LED groups. `name` is a hint; `hue`/`sat` are authoritative.
- Omit a group entirely to mean "this group is OUT (0)" in a full target look.

### Color palette (name → hue/sat)
blue 240/100 · orange 30/100 · amber 35/90 · pink 320/100 · red 0/100 · green 120/100 ·
lavender 270/60 · pale_blue ~199/45 · magenta ~310/82 · teal ~161/100 · white 0/0

### Lonestar home positions (`position: home`)
| ch | pan | tilt | zoom |
|----|-----|------|------|
| 108 DSL | -50.1 | 43.1 | 43.9 |
| 109 DSR | 125.4 | 51.1 | 44.3 |
| 110 HSL | -26.5 | 58.0 | 36.6 |
| 111 HCS | -0.1 | 49.6 | 44.3 |
| 112 HSR | 34.9 | 56.0 | 37.5 |
All home: edge 97.2, no gobo/frost/blades, white.

## Execution recipe (next console session)
For each cue where `target.look != current.look`:
1. `go_to_cue <n>` (loads current as the base / reads it live if PENDING).
2. Build the **full target look**: set every listed group's levels + LED colors; set lonestar
   positions/levels; anything not listed in target → 0 (this is a full end-state, not a delta).
3. **Overwrite-confirm**: it's an existing cue → `update_cue <n>` with `confirm:true`.
4. Read back to verify.
Cues with no diff are skipped. PENDING cues: read live first, regenerate current, then diff.

## ⚠ Anomalies to resolve (carried in each cue's `notes:`)
- **1.1** — content changed THIS session via the "save in 1.1 without lights" update; likely
  overwrote the original (red ladders + trough + warm desires). **Verify vs a backup before reprogramming.**
- **5.2 = 6.1 = 6.2** byte-identical — confirm intentional hold.
- **ch2 (safety stair) absent** in 6.5, 6.6, 8.8, 9.1–9.5 — confirm intended.
- **Song 5** is present in lighting (5.1/5.2) but a memory note lists audio "05" as cut — reconcile against QLab.
- `has_color` flag on ch2/ch71 is unreliable (LED capture quirk), not a programming error.
