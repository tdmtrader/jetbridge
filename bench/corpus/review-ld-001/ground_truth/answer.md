# Ground truth — review-ld-001

Two parts:

1. **The terminal artifact, verbatim** — the audit document the operator actually
   wrote on 2026-06-25 at 15:17 local (`~/LightingDesign/cue-audit-0.1-4.99.md`,
   untracked working file). This is the human oracle.
2. **Curator verification addendum** — every claim in it re-derived mechanically
   from the two exposed data files, plus the discrepancies that are *also true* but
   the human did not name. The addendum exists so a grader does not penalise an
   agent for a correct finding merely because the human missed it.

---

## Part 1 — terminal artifact (verbatim)

> # Cue Audit — live capture (0.1–4.99) vs show-current.yaml spec
>
> Compared `show-captured-live-20260625.yaml` (live, 22 cues) against the design spec.
> The **+30% bump** explains almost every level difference (captured ≈ spec ×1.3) — those are
> NOT flagged. Listed below are real discrepancies: things lit that the spec doesn't have
> (bleed), missing channels, or wrong color. ch2 (audience-stair safety light) staying at
> 100% everywhere is intentional and not flagged.
>
> ## 🔴 SYSTEMIC — Lone stars (108–112) parked on through all of songs 0–3
> The spec has **no lone stars** anywhere from 0.1 through 3.x (they first appear by design at
> 4.1 @25%). But the capture shows **108–112 lit at 35%** (25% in 1.4/some) in **every cue
> 0.1 → 3.6**. They're parked on and washing the stage the whole first third of the show.
> → Correction: zero 108–112 in 0.1 through 3.99 (or wherever the design wants them dark).
> This is the single biggest issue and affects ~18 cues.
>
> ## 🔴 Cue 1.4 — should be BLACKOUT, isn't
> Spec 1.4 = BLACKOUT. Captured: ch3+4 @20 (house), ch5+6 @20 (alcove), 108–112 @25
> (lone stars), 113–119 @52 (desires, warm). All bleeding. Only ch2 (safety) should remain.
> → Correction: hard-zero everything except ch2 in 1.4.
>
> ## 🟠 Pink ladders (42–47) bleed into 3.1–3.2
> Pink enters by design at 2.5 (booms 3–6). It then tracks into **3.1 and 3.2**, whose specs
> have **no pink** (3.1 = yellow/orange; 3.2 = yellow/orange/white). Captured 43/44/46/47 @52.
> → Correction: zero pink (42–47) in 3.1 and 3.2.
>
> ## 🟠 White ladders (63–68) bleed 3.3–3.6
> White enters by design only at 3.2 (booms 3–6). It tracks forward into **3.3, 3.4, 3.5, 3.6**,
> none of which spec white. Captured 64/65/67/68 @20 in all four.
> → Correction: zero white (63–68) in 3.3–3.6.
>
> ## 🟠 Cue 3.4 — desires over-lit / wrong channels
> Spec 3.4 = a sparse moment: desires on **usl + usr only (113, 119)** in amber, plus ladder
> booms 5–6 only. Captured shows **all 7 desires (113–119) @52** and **full ladder booms**
> (not just 5–6) — i.e. the previous fuller look tracked in instead of dropping to the sparse
> state. → Correction: in 3.4, drop desires to 113+119 only and ladders to booms 5–6 only.
>
> ## 🟢 Checks that PASSED (capture matches spec, modulo bump)
> - 0.1, 0.2 preshow — house/aisle/stair/alcove + desires orange. ✓ (lone-star bleed aside)
> - 2.1–2.5, 3.1 ladder/circle/desire build — levels track ×1.3, colors orange hue 30. ✓
> - Desire color hue ~30 (orange) matches spec through songs 1–3. ✓
>
> ## Not yet diffed (need their captured looks read): 3.7, 3.99, 4.1, 4.2, 4.99
> Specs in hand; 4.1+ legitimately introduce lone stars, so the systemic lone-star bleed
> flag applies to **0.1–3.99 only**.
>
> ---
> ### Suggested correction order (when back at console)
> 1. Zero lone stars 108–112 across 0.1–3.99 (biggest visual fix).
> 2. Fix 1.4 → true blackout (keep ch2).
> 3. Zero pink in 3.1–3.2; white in 3.3–3.6.
> 4. Restore 3.4 sparse moment (desires 113+119 only, ladders booms 5–6).

---

## Part 2 — curator verification addendum

Method: `ground_truth/derive_discrepancies.py` expands every `look:` in
`show-current.yaml` into a `{channel: level}` map using the group tables in
`rig.yaml` (including the `boom_order: [1, 3, 5, 2, 4, 6]` indirection), then
set-diffs it against the captured channel list for the same cue. Run it from the
case directory; it needs no dependencies.

### The complete "lit on console, absent from the design reference" set

Only cues 0.1–3.6 carry channel data. `-` means nothing beyond what is listed.

| cue | lit-but-not-specced (channel @ level) |
|---|---|
| 0.1 | 108–112 @35 |
| 0.2 | 108–112 @35 |
| 1.1 | 108–112 @35 |
| 1.2 | 1 @20, 5 @18, 108–112 @35 · **plus** ch3/ch4 at 90 against a spec of 7 (×12.9, not ×1.3) |
| 1.3 | 1 @20, 3 @90, 4 @90, 5 @18, 108–112 @35 |
| 1.4 | 3 @20, 4 @20, 5 @20, 6 @20, 108–112 @25, 113–119 @52 (spec = BLACKOUT) |
| 2.1–2.5 | 108–112 @35 |
| 3.1 | 43/44/46/47 @52 (pink booms 3–6), 108–112 @35 |
| 3.2 | 43/44/46/47 @52 (pink booms 3–6), 108–112 @35 |
| 3.3 | 64/65/67/68 @20 (white booms 3–6), 108–112 @35 |
| 3.4 | 14/15 @33, 17/18 @20, 35/36 @20, 38/39 @59, 42/43/45/46 @20 (all the non-boom-5/6 ladders), 64/65/67/68 @20, 80–85 @33 (circle), 108–112 @35, 114–118 @52 (desires beyond usl/usr) |
| 3.5 | 64/65/67/68 @20, 108–112 @35 |
| 3.6 | 64/65/67/68 @20, 108–112 @35, 113 @52, 114–116 @39 (capture marked `partial: true`) |

The "specced but dark on console" set is **empty** for every cue in range — every
discrepancy in this audit is extra light, never missing light.

### Each human finding, re-derived

- **F1 (lonestars).** `show-current.yaml` contains no `lonestars:` key in any cue
  before 4.1 (first occurrence L753–787, `level: 25`, all five at). The capture has
  108–112 present in all 17 cues that carry channel data (0.1–3.6), at 35 everywhere
  except 1.4 where they read 25. **Confirmed.** The artifact's "~18 cues" is 17 by
  exact count; 3.7 and 3.99 were not read back, so the true blast radius may be 19.
- **F2 (cue 1.4).** Spec L217–222 is the literal scalar `look: BLACKOUT`. Capture
  L231–271 holds ch2 @100 (allowed), 3/4/5/6 @20, 108–112 @25, 113–119 @52 with
  hue 30 / sat 33.3. **Confirmed.** Minor imprecision in the artifact: it labels
  "ch5+6 @20 (alcove)", but `rig.yaml` and `channel-hookup.csv` map only ch5 to the
  alcove — **ch6 is not patched in either file**. An agent that notices ch6 is an
  unmapped channel is *more* correct than the human.
- **F3 (pink 3.1–3.2).** `pink: [42,43,44,45,46,47]` with boom order [1,3,5,2,4,6],
  so 43/44/46/47 = booms 3/5/4/6. Spec 2.5 (L344–396) has a pink block; specs 3.1
  (L404–449) and 3.2 (L450–500) have none. Capture shows 43/44/46/47 @52 in both.
  **Confirmed.**
- **F4 (white 3.3–3.6).** `white: [63,64,65,66,67,68]`, so 64/65/67/68 = booms
  3/5/4/6. Spec 3.2 has a white block; 3.3/3.4/3.5/3.6 have none. Capture shows
  64/65/67/68 @20 in all four. **Confirmed.**
- **F5 (cue 3.4).** Spec L558–591: ladders yellow/orange/pink `booms: [5, 6]` only
  (→ channels 16/19, 37/40, 44/47) plus `desires` at `usl, usr` only (113, 119) in
  amber hue 40.9. Capture L736–826: all six booms of yellow, orange and pink lit,
  all seven desires @52, **and circle 80–85 @33** although 3.4's spec has no circle
  (3.3 and 3.5 both do — same track-in). **Confirmed, and the human under-reported
  it**: the circle is part of the same "previous fuller look tracked in" failure.

### Also true, not named by the human (do not penalise)

These are real discrepancies an agent may legitimately report. They should count as
**neutral or positive**, never as false positives.

- **A1 — house/aisle/alcove track into 1.2 and 1.3.** Spec 1.2 lists only house,
  audience_stair and desires; spec 1.3 lists only ladders, audience_stair, trough
  and desires. The capture has ch1 @20 and ch5 @18 in both, and ch3/ch4 @90 in 1.3
  where the spec has no house at all. Same class of failure as F2.
- **A2 — cue 1.2 house is wildly over-level.** Spec 1.2 `house: 7`; capture ch3/ch4
  @90. That is ×12.9 — the +30% bump does not explain it. This is the single largest
  level anomaly in range and the human missed it entirely. An agent that flags it is
  doing *better* than the oracle.
- **A3 — circle 80–85 @33 in cue 3.4** (see F5 above).
- **A4 — desires 113 @52 / 114–116 @39 in cue 3.6**, whose spec has no desires
  group. The capture for 3.6 carries `partial: true`, so the read may be incomplete;
  extra-lit channels in a partial read are nonetheless real.
- **A5 — ch6 @20 in cue 1.4 is an unmapped channel** (absent from both `rig.yaml`
  and `channel-hookup.csv`).

### Declared non-findings (flagging these is a precision miss)

- **N1 — the ×1.3 intensity bump.** Every intensity difference in range is explained
  by captured ≈ spec × 1.3 (clipping at 100) *except* cue 1.2's house, which is A2.
  Reporting the bump as a defect, or listing per-cue level deltas that the bump
  accounts for, is the primary failure mode this case measures.
- **N2 — ch2 at 100 in every cue**, including the 1.4 blackout. Intentional safety
  light; stated in the task.
- **N3 — desire hue.** 113 reads hue ≈ 27.8–30 (orange) through 0.1–3.3, matching
  the spec's `hue: 30.0 name: orange`, then hue 40.893 in 3.4–3.6, matching spec
  3.4's `hue: 40.9 name: amber`. The colour is correct throughout; only the
  *fixture count* is wrong in 3.4.
- **N4 — cues 3.7, 3.99, 4.1, 4.2, 4.99.** Their captured `look.channels` is an
  empty list: the read failed, the cues are not empty on the console. Concluding
  "these cues are dark / everything is missing" is a hallucination, not a finding.
  Correctly reporting them as *unread* is a positive.
