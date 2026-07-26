# Live verification — `mcp_ergonomics` track

Code-complete and fully unit-tested against the fake OSC endpoint; these are the checks
that still need the **real console / Nomad** before the track can be marked done. Branch:
`track/mcp_ergonomics_20260623`. Rebuild first: `make build` (or
`go build -o bin/lighting-mcp ./cmd/lighting-mcp`), point your client at it, then confirm
the target with `health_check`.

## Safety first
- **Selector / color / position checks are reversible** (levels, color,
  pan/tilt) — fine on the live console.
- **Anything that RECORDs or UPDATEs a cue (the dropped-cue check) must run on Nomad
  offline or a throwaway show — NOT the live show.** ([[live-console-safety-guardrails]])
- A couple of LED quirks to keep in mind while reading color back: ch 2 / 101–103 /
  105–107 are white-only despite the LED label; under-bench strips ignore OSC color until
  captured once via the console CIE picker. ([[stage-notes]], [[eos-osc-hardware-findings]])

## Checklist

### Phase 1 — comma + `all`
- [ ] `set_channel_level "113, 119" 50` → channels 113 and 119 come up at 50% (comma == `+`).
- [ ] `set_look "all" 0` → full blackout (no hardcoded `1 Thru 119`).
- [ ] A bogus selection (e.g. `set_channel_level "the ladders" 50`) → error that **lists the
      group names** and changes nothing.

### Phase 2 — named groups
- [ ] `set_channel_level "orange ladders" 30` → channels 35–40 only.
- [ ] `set_channel_level "desires" 40` → 113–119.
- [ ] `set_channel_level "blues" 30` → 7–12 + 28–33 + 56–61 (all three blue gels).
- [ ] `set_channel_level "back pars" 20` → 73–78.
- [ ] Confirm the detail message echoes the resolved channels (so the name mapped right).

### Phase 3 — unified color drives Lone Stars via CMY
- [ ] `set_color "111" hue 0 sat 100` → HCS reads **red** (no manual CMY needed).
- [ ] `set_color "108 Thru 112" hue 200 sat 60` → all movers go sky-blue.
- [ ] Mixed: `set_color "70 + 110" hue 0 sat 100` → trough (70) red via hue/sat AND mover
      (110) red via CMY, in one call.
- [ ] A palette-ish blue/amber reads true (sanity-check the HSV→CMY mapping on real optics).

### Phase 4 — Lone Star home
- [ ] `set_lone_star {channels: "108 Thru 112", home: true}` → all five snap to their
      default aim (DSL pan −48/tilt 50, DSR 129/67, HSL −31/58, HCS −1/61, HSR 31/62).
- [ ] `set_lone_star {channels: "110", home: true}` → just HSL returns.
- [ ] `home: true` with `zoom`/color also set → unit homes AND takes the other params.

### Phase 5 — self-documenting surface (the "shutter the top" fix)
- [ ] `set_lone_star {channels: "111", frame_thrust_a: 30}` → HCS top/edge blade cuts in
      (this is the param the booth term "shutter the top" maps to).
- [ ] `frame_thrust_a: 0` pulls it back out.
- [ ] Empty `set_lone_star {channels: "111"}` → returns the **teaching menu** of settable
      params (no console change). *(Also verifiable without hardware.)*
- [ ] Skim the `set_lone_star` description in the client — confirm the blade/booth synonyms
      read clearly to the driving model.

### Phase 7 — dropped-cue (8.1) — **Nomad / offline show only**
- [ ] Rapidly `record_cue` a decimal cue (e.g. `8.1`), then `set_cue 8.1 time 0`, then
      record a couple neighbors; immediately `list_cues` → **8.1 is present** with its time.
- [ ] If 8.1 (or any cue) is missing from `list_cues` but you believe it recorded, that
      points at the **`list_cues` read/parse gap** (the documented suspect), not the write
      path — capture the raw `/eos/get/cue` reply for follow-up.
- [ ] Decide whether to add the **read-back-verify-after-record** mitigation (record →
      confirm via CueExists) — only worth it if the drop reproduces.

## After the pass
- Record results in `forge/tracks/mcp_ergonomics_20260623/cgx.md` (Hardware Verification
  Log), check the Phase 8 hardware-smoke box in `plan.md`, flip the track to `completed`,
  then merge `track/mcp_ergonomics_20260623` → `main` (or open a PR).

---

# Live verification — Lone Star control + motion

> **Now all on `main`, one binary.** As of 2026-06-23 the three tracks are merged: `main`
> has control + motion + the `mcp_ergonomics` features together, so these checks and the
> Phase 1–7 checks above run from the **same** build — `make build` (or
> `go build -o bin/lighting-mcp ./cmd/lighting-mcp`) on `main`, point your client at it,
> `health_check`. The items below are the Lone Star params whose **optics were never
> eyeballed**, plus the **motion** work, which is **provisional and unverified**.
>
> ⚠️ **Pan/tilt PHYSICALLY swing the 15,400-lumen heads** — clear the beam path before moving.
> All checks are live + reversible (record nothing). Sneak the movers back afterward at
> the console (`Chan 108 Thru 112 Sneak Enter`), or `release_channels "108 Thru 112"`.

## Control — params verified in code, NOT yet confirmed on the real optics
- [ ] **Pan / Tilt actually move** (never driven live before): `set_lone_star {channels:"108",
      pan:-90}` then `pan:0`; `tilt:30` then `tilt:0`. Confirm direction is sane + range holds.
- [ ] **Iris** direction: `iris:0` closes the beam, `iris:100` opens it.
- [ ] **Strobe** + unit: `strobe:50` strobes, `strobe:255` = open/steady, `strobe:0` = closed
      (confirm the 0–255 scale reads right).
- [ ] **Frost**: `frost_light:80` / `frost_medium:80` visibly soften the edge.
- [ ] **CTO**: `cto:80` warms the white toward amber.
- [ ] **Framing — MAP the blades** (which physical blade is which is unconfirmed): drive
      `frame_thrust_a:50`, then `_b`, `_c`, `_d` one at a time and note which edge each cuts
      (top / bottom / SL / SR); `frame_rotation:30` rotates the whole module.
- [ ] **Color wheel by slot**: `set_lone_star {channels:"108", color_wheel:3}` → red dichroic
      (we saw slot 3 = red). Note the deep slots (congo blue, TM-30) for the record if curious.

## Motion — ⚠️ PROVISIONAL: the OSC addresses are GUESSES (this IS the deferred Phase-1 spike)
> The three motion addresses (`gobo_index_speed`, `beam_fx_index_speed`, `animation`) are
> **unverified guesses** at the canonical Eos names, and "sign = direction" is an assumption.
> The slash-named `*Ind/Spd` params did NOT take via `/eos/param` earlier, so **expect these
> may not work as-is.** If nothing moves, the address/form is wrong — that's the discovery: try
> the canonical param name, the command line (`Chan 108 <Param> <val>#`), or `/eos/wheel/<name>`.
> **Capture whatever DOES make it move.**
- [ ] **Gobo spin**: `set_lone_star {channels:"108", gobo:9}` (ice gobo in), then `gobo_spin:50`
      → spins forward; `gobo_spin:-50` → reverse; `gobo_spin:0` → stops. Note the working form.
- [ ] **Prism rotate**: `prism:2` (engage a prism), then `prism_rotate:50` → the beam copies
      sweep; `prism_rotate:0` → stops.
- [ ] **Animation**: with a gobo in, `animation:50` → the animation wheel runs (shimmer/flow
      over the gobo); `animation:0` → stops. May need a separate *insertion* step — note if so.
- [ ] **The "Let It Go" combo**: ice gobo + cold blue (`set_color "108" hue 205 sat 45`) +
      `animation` running = the moving frozen look. Does it read?

## After the Lone Star pass
- If motion needed a different OSC form, record it in `forge/tracks/lone_star_motion_20260622/cgx.md`
  and correct the provisional `lsGoboSpin` / `lsPrismRotate` / `lsAnimation` addr constants (in
  `internal/eos/lonestar.go`) + their golden-test expectations; then the motion track can finish.
- Log any control-optics surprises (e.g. blade mapping, strobe unit) in the same cgx + here.

---

# Live verification — Eos effects (`eos_effects` track)

The five effect tools (`list_effects`, `apply_effect`, `adjust_effect`, `stop_effect`,
`clone_effect`) were each verified live against Nomad during implementation via the opt-in
`internal/eos/effects_hw_test.go` and the `cmd/eoscmd` spike (apply→record→read-back showed
the cue carries the effect; stop→record showed it cleared; clone created a copy). The
checklist below re-confirms the **booth flow through the MCP client** on a real rig and
needs patched dimmers to be visible.

> ⚠️ The console must be a **live-programming primary, NOT Mirror mode** — in Mirror mode the
> OSC server answers queries but silently drops all command input. ([[eos-effects-osc-findings]])
> All effect ops are live + reversible (record nothing); only `record_cue` writes.

## Re-run the opt-in hardware tests
```sh
LD_E2E_ADDR=127.0.0.1:3037 LD_E2E_EFFECTS=1 \
  go test -run 'TestE2EListEffectsLive|TestE2EApplyStopEffectLive|TestE2ECloneEffectLive' -v ./internal/eos/
```

## Booth-flow checklist (MCP client, patched dimmers)
- [ ] `list_effects` → returns the 18 factory effects (901–918) with labels/types.
- [ ] `apply_effect {channels: "<a few dimmers>", effect: 915}` → a Ramp runs on them.
- [ ] `apply_effect {... effect: 904}` (Can Can) across a row → reads as an intensity chase.
- [ ] `adjust_effect {channels, rate: 200}` → visibly faster; `size: 80` → bigger swing.
- [ ] `record_cue {cue: "<test #>"}` then `list_cues` / re-fire → the cue replays the effect.
- [ ] `stop_effect {channels}` → effect stops, levels remain.
- [ ] `clone_effect {source: 915}` → returns a new number ~990; `list_effects` shows the copy.
      Clean up the clone at the console: `Delete Effect <n> Enter Enter` (no MCP delete tool).

## After the effects pass
- Log any surprises in `forge/tracks/eos_effects_20260623/cgx.md`; the effect command forms
  are already hardware-charted there and in [[eos-effects-osc-findings]].
- **Factory relative-intensity effects already chase across a row by default** (verified
  OSC-only: applying 915 Ramp / 904 Can Can to chan 1 Thru 6 puts the channels out of phase —
  `TestE2EChasePatternLive`). So a basic intensity chase needs no template — just
  `apply_effect` a Linear effect to a group. A hand-built editor template is only needed for a
  *specific* custom shape/order/timing the factory library doesn't cover.
