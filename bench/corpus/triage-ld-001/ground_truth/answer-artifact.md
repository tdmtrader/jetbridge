# Cue Fix — Execution Plan (2026-06-26)

**For:** a model with the **lighting MCP** (`mcp__lighting__*`) connected to the *DareToDream2026*
show on the Eos console. Self-contained — all channels resolved inline. Source of truth for intent:
[cue-notes-20260626.md](cue-notes-20260626.md).

> **Scope note:** Song 8 work is intentionally **excluded** from this plan (per operator, 2026-06-26).

---

## 0. READ THIS FIRST — global rules

1. **Trust the live console, not the files.** The captured/audit YAMLs are stale. For EVERY cue you
   touch: `go_to_cue <n>` → `read_stage` to get the ACTUAL current state, THEN apply the delta.
2. **Levels are live/actual numbers.** A +30% bump is baked into stored values; ignore the YAML's
   pre-bump numbers. When a note says "@10%", set 10 live.
3. **Storing a cue:** command-line editing (`Cue X Chan Y At Z`) does NOT persist on this console.
   Use the typed tools: **`update_cue`** (captures the whole live stage — only safe if you set just
   the intended channels) or **`record_cue`** (selective merge). Prefer **selective** so you never
   clobber unlisted channels.
4. **Bounce-verify every store:** after recording, `go_to_cue` to a neighbor and back (or re-fire),
   then `read_stage` and confirm the change held. A store that doesn't read back did not happen.
5. **Per-cue, not per-song.** Tracking/bleed is baked into each cue individually — "block at song
   start" does NOT propagate. A "whole song" instruction = repeat the edit in every listed cue.
6. **LIVE-SHOW SAFETY.** Levels / colors / positions are reversible and fine to drive live.
   **Recording, updating, or restructuring cues writes to the show** — only do it on **Nomad /
   offline / a throwaway show**, OR with **explicit operator go-ahead** for the live console.
   Never delete cues. If you are unsure which environment you're on, STOP and ask.
7. **Mover sign is not verified.** Pan/tilt/zoom direction has never been hardware-confirmed. Do the
   **one-time calibration in §1** before any mover move, then apply the confirmed sign everywhere.
8. **`health_check` first** to confirm the MCP is talking to the console.

---

## 1. One-time mover calibration (do before any tilt/zoom task)

Tasks 6-spots, 11.1, 18.2 move Lone Stars. Confirm direction ONCE:
1. Pick an idle Lone Star (e.g. 108). Note its current tilt via `read_stage`/`get_lone_star`.
2. `adjust_lone_star {channels:"108", tilt:+5}` (relative). Watch the beam.
   - Beam goes **UP** → "tilt up" = **+tilt**. Beam goes down → "tilt up" = **−tilt**. Record which.
3. Same for zoom: nudge zoom wider, confirm whether "zoom out / wider beam" = **+zoom** or **−zoom**
   (default zoom ≈ 48°; larger angle is usually wider — confirm).
4. Put 108 back. Use the confirmed signs for all mover tasks below.

> Notes below assume **tilt up = +tilt** and **wider = +zoom**; FLIP if calibration says otherwise.

---

## 2. Channel reference (resolved)

**Ladders** — 6 booms, channels in boom order `[1,3,5,2,4,6]`. For a color `[a,b,c,d,e,f]`:
`boom1=a · boom2=d · boom3=b · boom4=e · boom5=c · boom6=f`.

| Color | b1 | b2 | b3 | b4 | b5 | b6 | (raw range) |
|---|----|----|----|----|----|----|---|
| blue    | 7  | 10 | 8  | 11 | 9  | 12 | 7–12 |
| yellow  | 14 | 17 | 15 | 18 | 16 | 19 | 14–19 |
| green   | 21 | 24 | 22 | 25 | 23 | 26 | 21–26 |
| lt_blue | 28 | 31 | 29 | 32 | 30 | 33 | 28–33 |
| orange  | 35 | 38 | 36 | 39 | 37 | 40 | 35–40 |
| pink    | 42 | 45 | 43 | 46 | 44 | 47 | 42–47 |
| red     | 49 | 52 | 50 | 53 | 51 | 54 | 49–54 |
| dk_blue | 56 | 59 | 57 | 60 | 58 | 61 | 56–61 |
| white   | 63 | 66 | 64 | 67 | 65 | 68 | 63–68 |

**Other groups:** house `3,4` · trough `70(sl),71(sr)` · circle ring `80–85` · center specials
`87,88` · Lone Stars `108(dsl) 109(dsr) 110(hsl) 111(hcs) 112(hsr)` · desires `113(usl) 114(sl)
115(dsl) 116(dcs) 117(dsr) 118(sr) 119(usr)`.

---

## 3. Execution order

Walk the show once, ascending:

2.2 → Song 6 → 6.4 → 9.3 → 9.5 → end-9 → 11.1 → end-13 → 14.1 → 14.2(blocked) → 14.4 →
16.2/16.3 → Song 17 → Song 18 → 18.2 → 19.1 → Song 19 → 19→20 → 20.1.

Grouping by song minimizes `go_to_cue` navigation; batch all edits for a cue while you're in it.

---

## 4. Tasks

### T1 — 2.2  · CUING + BOARD
- **Cuing:** 2.2 fires too late musically. If 2.2 **auto-follows** 2.1 → shorten that follow time
  (`list_cues` to check; `set_cue`). If **manual GO** → operator takes it earlier (no console edit).
- **Board:** desires brighter. `read_stage` 2.2; raise the desire (113–119) levels, keeping their
  relative balance (the high one stays highest). Amount not given → dial to taste. Colors unchanged.
- **Verify:** read back; desires up, balance preserved.

### T2 — Song 6: Lone Stars tilt up  · BOARD
- For **each 6.x cue with Lone Stars up** (read each; past reads showed 6.1/6.2/6.3 — verify):
  `adjust_lone_star {channels:"108 Thru 112", tilt:+5..+10}` (relative, calibrated sign). Per-cue record.
- **Verify:** beams visibly higher; read back per cue.

### T3 — 6.4: add white side ladders  · BOARD
- `read_stage` 6.4. Set **white booms 3+4 = ch 64 + 67 @ 10%** (additive). Selective record `64+67`.
- **Verify:** 64,67 @10, nothing else changed.

### T4 — 9.3: brighter + white side ladders  · BOARD
- `read_stage` 9.3. (a) Raise overall intensity to taste (dial live, operator watching).
  (b) Add **white ladders** (63–68) as side light — booms/level not fixed; default booms 3/4
  (64+67) like 6.4 unless operator wants wider/brighter. Record.
- **Verify:** read back.

### T5 — 9.5: cut Lone Stars + circle ring  · BOARD
- `read_stage` 9.5. Set **108–112 → 0** (`set_channel_level "108 Thru 112" 0`). Set **circle ring
  80–85 → 35** (`set_channel_level "80 Thru 85" 35`). Leave 87+88 untouched. Record.
- **Verify:** movers out, ring @35.

### T6 — End of Song 9: fade to neutral bridge  · CUING + BOARD
- Target neutral wash = standard `.99` bridge: **stair(2) 100 · circle 80–85 @30 · desires 113–119
  @20 amber hue 35/sat 90 · Lone Stars 108–112 @0 + home · everything else OUT.**
- **9.99 already exists** ("Bridge"). `read_stage` 9.6 and 9.99. Make 9.99 = the neutral look above;
  give the move INTO it a fade (≈3–5s — confirm). Decide whether the fade lives on 9.6→9.99 (cue
  time on 9.99) or a longer down on 9.6. `set_lone_star {channels:"108 Thru 112", home:true}` for home.
- **Verify:** fire 9.6→9.99; confirm smooth fade to neutral, no Song-9 residue.

### T7 — 11.1  · CUING + BOARD
- **Cuing:** lengthen 11.1 cue time (slow). Amount → dial live. `set_cue` / `update_cue` time.
- **Board:** `read_stage` 11.1. Lone Stars (108–112): **zoom wider** (calibrated sign, amount live)
  and **tilt −5** (`adjust_lone_star {channels:"108 Thru 112", zoom:+X, tilt:-5}`). Record.
- **Verify:** read back; slower fade, wider/lower beams.

### T8 — End of Song 13: bump earlier  · CUING
- `list_cues` Song 13; ID the end bump cue (likely last 13.x). If it auto-follows → shorten follow
  time a touch. If manual GO → operator takes it earlier. Small nudge, dial live.
- **Verify:** bump lands earlier against the music.

### T9 — 14.1: cue time = 6s  · CUING
- `set_cue 14.1 time 6` (or `update_cue` time). **Verify** via `list_cues`/read.

### T10 — 14.2: wrong look  · BOARD · ⛔ BLOCKED
- **Do not touch.** Intended look not specified. Hold for operator target ("match cue X" / "like
  14.1 but …" / explicit look). Skip until provided.

### T11 — 14.4: brighter + warmer  · BOARD
- Identify the **prior cue** (read `list_cues`; likely 14.3). `go_to_cue` prior, `read_stage` to
  capture its levels as the base. Then add **pink ladders @ 25%** (42–47, all booms 1–6 unless
  operator limits booms). Record into 14.4 (base levels + pink).
- **Verify:** 14.4 reads as prior-cue levels + pink fill.

### T12 — 16.2 / 16.3: desire levels  · BOARD
- 16.2: `set_channel_level "113" 85` (USL). Selective record.
- 16.3: `set_channel_level "113" 60` and `set_channel_level "119" 85` (USL, USR). Selective record.
- Colors unchanged in both. **Verify** read back.

### T13 — Song 17: cut center specials + trough  · BOARD
- In **every 17.x cue** (17.1–17.8, + 17.99 if it carries them): set **87+88 → 0** AND **70+71 → 0**.
  `set_channel_level "70 + 71 + 87 + 88" 0`; selective record per cue. Leave ring 80–85 alone.
- **Verify:** per cue, those four out; spec colors intact.

### T14 — Song 18: cut trough  · BOARD
- In every 18.x cue that has them: **70+71 → 0**; selective record per cue.
- **Verify:** trough out across 18.x.

### T15 — 18.2: Lone Star front light, aim from 2.1  · BOARD
- `go_to_cue 2.1`, `read_stage`/`get_lone_star "108 Thru 112"` → capture each mover's **pan / tilt /
  zoom / focus(edge)**. (If 2.1 has NO usable mover position, STOP and confirm the right reference cue.)
- `go_to_cue 18.2`, `read_stage`. Apply the captured pan/tilt/zoom/focus to 108–112 and **raise their
  level** for front light (amount → dial live). `set_lone_star` per fixture. Record.
- **Verify:** movers aimed as 2.1, up as front fill.

### T16 — 19.1: cue time = 5s  · CUING
- `set_cue 19.1 time 5`. **Verify.**

### T17 — Song 19: cut center specials + trough  · BOARD
- In every 19.x cue (19.1 + 19.99 if present): **87+88 → 0** AND **70+71 → 0**; selective record per
  cue. Ring 80–85 untouched. **Verify.**

### T18 — Song 19 → 20: link transition  · CUING
- `list_cues`; ID last 19.x cue. Set an **auto-follow → 20.1** (or link/group). Follow time → dial
  live (tight for finale). Writes to cue list → go-ahead/offline only. **Verify** by running 19→20.

### T19 — 20.1: focus ladders to front booms  · BOARD
- `read_stage` 20.1. For **each active ladder color** (expect all 9 in the rainbow):
  - **Boost booms 1/2 by +50% relative** (b1=`a`, b2=`d` per §2 table): read current, set ×1.5,
    OR `adjust_channel_level` relative +50% if supported.
  - **Cut booms 3/4/5/6 → 0** (b3=`b`, b4=`e`, b5=`c`, b6=`f`).
  - Example yellow [14,15,16,17,18,19]: 14,17 ×1.5; 15,16,18,19 → 0.
- Record. ⚠️ Scope guard: this note is ladders only — **leave house (3,4) and everything else in
  20.1 untouched.**
- **Verify:** per color, booms 1/2 up, 3–6 out.

---

## 5. Blocked / needs an operator decision (do NOT guess)
- **T10 / 14.2** — intended look unknown. Blocked.
- **Amounts to dial live (operator present):** 2.2 desire brightness · 6/11.1 mover amounts ·
  9.3 overall + white-ladder booms/level · all fade/follow times (2.2, 9.99, 11.1, 13 bump, 19→20).
- **Mover sign** — confirm via §1 calibration before any tilt/zoom.

## 6. Done-criteria
Each task: change read back correctly after a bounce, only intended channels moved, no new bleed
introduced. Cuing tasks: the sequence/timing behaves as described when run live.
