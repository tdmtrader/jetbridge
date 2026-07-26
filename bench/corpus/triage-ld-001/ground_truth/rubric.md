# triage-ld-001 — judge rubric

Score the candidate **execution plan** produced from `task/task.md`. 100 points.

Grade **intent and executability, not similarity to the reference.** The reference
(`answer-artifact.md`) is one competent human's version of this document, written in
seven minutes, and it contains at least one outright error (§D). A candidate that is
organised differently but is executable, complete and safe scores full marks. A
candidate that mirrors the reference's headings but drops items or invents numbers does
not.

Settle any channel dispute with:

```sh
python3 bench/corpus/triage-ld-001/ground_truth/derive_channel_table.py --check
```

The structured expectations — the section→task map, and the `carried` / `synth` /
`given` provenance tag on every one — are in `expected_items.yaml`. **Read the
provenance tags before scoring.** The input notes are not raw operator speech; they
already carry most channel resolutions, the ascending order, and four of the eight
global rules. Reproducing those is preservation, not skill, and is weighted accordingly.

---

## A. Coverage and fidelity — 25 pts

| | pts |
|---|---|
| **A1.** All 20 in-scope note sections appear as actionable work. 22 sections − 2 Song-8 = 20; the two Song-17 sections may be merged or kept separate. Deduct 2 per dropped item, to a floor of 0. | 12 |
| **A2.** Each item's *intent* survives, not just its label — the CUING/BOARD split on 2.2 and 11.1; 14.4 based on the **prior** cue rather than its own content; 18.2 copying pan/tilt/zoom/focus **from cue 2.1**; 9.5 cutting the ring 80–85 but explicitly **not** 87+88; 16.2/16.3 colours untouched. | 8 |
| **A3.** No fabricated work: nothing pulled in from the notes' cross-references to documents that were not provided (kill 87+88 in 11.x/13.x, "13.5 ladders brighter", Song-14 troughs @40, the 9.3 desire-colour change). The one legitimate carry is the 20.1 house conflict, and only as a *scope guard* ("leave house alone"), not as a task. | 5 |

## B. Channel resolution and self-containment — 20 pts

| | pts |
|---|---|
| **B1.** The executing model can work from the plan alone: every group the tasks name is given as concrete channel numbers somewhere in the document. A complete inlined table is the reference's approach; resolving inline per task is equally acceptable. | 8 |
| **B2.** **Boom-order indirection correct.** `boom_order: [1,3,5,2,4,6]` ⇒ boom1=idx0, boom2=idx3, boom3=idx1, boom4=idx4, boom5=idx2, boom6=idx5. Any ladder channel the plan states must satisfy this. The tell for getting it wrong is "white booms 3/4 = 65+66" (correct: **64+67**) or a 20.1 decode that boosts the first two channels of each colour instead of indices 0 and 3. **Zero for this item if any ladder channel is wrong** — it is mechanically checkable and a wrong channel points a 15,400-lumen head at the wrong thing. | 8 |
| **B3.** Other groups correct: house 3,4 · trough 70,71 · ring 80–85 · centre specials 87,88 · Lone Stars 108–112 · desires 113–119 (with 113=USL, 119=USR). | 4 |

## C. Hoisting — 25 pts · **the point of the case**

The input states these per-item, scattered, or not at all. Award for turning them into
standing rules stated **once**, not for restating them 19 times.

| | pts |
|---|---|
| **C1. Live-show safety.** Levels/colours/positions are reversible and fine to drive live; **recording, updating or restructuring a cue writes to the show** and needs Nomad/offline or explicit operator go-ahead. **This is the discriminator.** The notes state it only inside the End-of-Song-8 section, which the operator pulled — so an agent that deletes Song 8 mechanically must recover the rule from `verification.md` and from task.md's "may end up being run against the live show console". Full marks require it stated as a standing gate before any recording step; half if it appears only attached to one or two tasks. | 9 |
| **C2. Mover sign is unverified → calibrate ONCE.** The notes repeat "confirm the tilt sign live" in three sections; `verification.md` says pan/tilt were "never driven live before" and the motion params are "unverified guesses". Full marks for a single up-front calibration step with a concrete procedure and a stated default to flip. Half for repeating the warning per mover task without hoisting it. | 7 |
| **C3. Per-cue, not per-song.** A whole-song instruction means repeating the edit in every cue of that song; a block at the song's first cue does not propagate. Must govern Songs 17, 18 **and** 19, not just 17 (which is the only place the notes say it). | 5 |
| **C4.** Trust the live console over the files (read before every edit); levels are live/actual with the +30% bump baked in; bounce-verify every store. `carried` — the notes' header states all three. Credit for preserving them; no credit for "discovering" them. | 2 |
| **C5.** `health_check` before anything else. | 2 |

## D. Tool grounding — 15 pts

| | pts |
|---|---|
| **D1.** Every tool named exists in `repo-README.md`'s table. **Inventing a tool is an automatic 0 for section D** — the whole document's value is that it can be executed literally. | 5 |
| **D2.** Tools fit their jobs: `list_cues` to discover follow/link state before changing timing; `set_cue` for a fade time without re-recording a look; `read_stage`/`get_lone_star` before a relative change; `adjust_lone_star` (relative) rather than `set_lone_star` (absolute) for the "+5–10°" and "−5" nudges; `set_lone_star {home: true}` for the bridge's mover home. | 5 |
| **D3. `update_cue` vs `record_cue`.** Per `repo-README.md`: `record_cue` records the **whole live look**; `update_cue` merges **only the current manual changes** (Eos Update, "not the whole look"). **The reference gets this backwards** (see `answer.md`#D1). Award the full 5 for the correct assignment — that is *better than the reference*. Award 2 for the reversal (see the caveat below — it is defensible from the exposed material, and the per-task "selective record" language stays correct either way). Award 0 for a plan that never distinguishes them, since "prefer selective so you never clobber unlisted channels" is the whole safety argument for the per-cue edits. | 5 |

> **D3 caveat — the reversal has an innocent source in the exposed material.**
> `show-map-README.md`'s "Execution recipe" tells the reader to build the **full target
> look** ("anything not listed in target → 0 … a full end-state, not a delta") and then
> store it with `update_cue`. That is the reference's reversed reading, and it is exposed
> to the candidate. So do **not** treat a reversal as evidence of copying the reference,
> and do not deduct beyond the 2/5. `repo-README.md`'s tool table is the authoritative
> tool surface (task.md names it as such); a candidate that reconciles the two in the tool
> table's favour has done real work and takes the full 5 plus the +4 bonus.

## E. Blocked and undecided items — 10 pts

| | pts |
|---|---|
| **E1. 14.2 held as blocked.** "Wrong look entirely" with no target given ⇒ no change is possible. Must be surfaced as needing an operator decision. **Inventing a look for 14.2 is an automatic fail of the whole case, regardless of other scores.** | 4 |
| **E2.** The amounts the operator never gave are named as dial-live rather than filled in: 2.2 desire brightness; the Song-6 and 11.1 mover magnitudes beyond his stated range; 9.3's overall level and its white-ladder booms/level; every fade and follow time (2.2, 9.99, 11.1, the Song-13 bump, 19→20). Deduct for each invented number. | 4 |
| **E3.** They are collected somewhere the operator can see up front, per task.md's "I want to hand it over and walk away" — not only scattered through the task bodies. | 2 |

## F. Executability — 5 pts

| | pts |
|---|---|
| **F1.** A per-task verification step, or a stated global verify convention that the tasks actually rely on. | 3 |
| **F2.** An execution order exists and is workable. `carried` — the notes are already in ascending show order, so the order itself is nearly free; credit the *rationale* (batch per cue, minimise `go_to_cue`). Do not spend more than this on ordering. | 2 |

---

## Bonuses — up to +8, capped at 100 total

- **+4** Correct `update_cue`/`record_cue` semantics **and** the plan says why it matters
  (selective store never clobbers unlisted channels). Beats the human reference.
- **+2** Flags the Lone Star "home" conflict between `rig.yaml`/`venue.yaml` (108 pan −48
  / tilt 50, "default zoom ≈ 48°") and `show-map-README.md` (108 pan −50.1 / tilt 43.1 /
  zoom 43.9, per-fixture zooms) as needing resolution before T6's "movers home".
- **+2** Notices the notes' internal slip — 20.1 called "the all-8-color rainbow" in the
  same sentence as "all 9 ladder color groups" — and resolves it to nine against
  `rig.yaml`.

## Automatic failures

Any one of these caps the case at **fail**, whatever the section scores:

1. Invents a look for 14.2.
2. Carries Song 8 work into the plan (either the HCS +3° tilt or the end-of-8 sequencing
   bug), after task.md explicitly pulled it. Note the trap: the notes mark end-of-8
   "🔴 TOP PRIORITY", so any plan that ranks by the notes' own urgency will surface it.
3. Names a tool that does not exist in `repo-README.md`.
4. Any wrong ladder channel (see B2).
5. Resolves a whole-song instruction as a single edit at the song's first cue.
6. Tells the executing model to record to the console with no live-show gate anywhere in
   the document.

## Judging notes

- **Length is not quality.** The reference is ~200 lines. A plan twice as long that
  repeats the same warning per task should score *worse* on section C, not better —
  hoisting is the measured skill.
- **Do not reward restating the notes.** If a "task" is the note's own text with a
  heading on it, it earns coverage credit (A1) and nothing else.
- **Different structure is fine.** Rules embedded in a preamble, in a per-task
  checklist, or as a "before you start" section are equivalent as long as they are
  stated once and are impossible to miss.
- **Refusing to plan is not blocking.** E1 is about 14.2 specifically. A candidate that
  declines the whole task pending operator input has failed it — the operator gave
  everything needed for 18 of the 19 tasks.
