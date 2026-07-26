# triage-ld-001 — the answer

**Terminal artifact:** `~/LightingDesign/cue-fix-execution-plan-20260626.md`, sealed
verbatim as [`answer-artifact.md`](answer-artifact.md)
(sha256 `4aef718486a925f94f5d2caf10303ca58a8b79a022e85f541f987e56edd45bc3`,
mtime 2026-06-26T11:14:10-0700). Written seven minutes after the input notes, by
the same person, in the same session. It was the document actually carried into the
next console session.

This file is the curator's reading of it: what it does, what it adds over its input,
and where it is wrong. The machine-checkable form is
[`expected_items.yaml`](expected_items.yaml).

## What the reference produces

Six sections, in this order:

0. **Global rules** — eight standing instructions, applied to everything below.
1. **One-time mover calibration** — a prerequisite step, before any task that moves a head.
2. **Channel reference** — the resolved 9-colour × 6-boom ladder table plus the other
   named groups, inlined so the executing model never opens `rig.yaml`.
3. **Execution order** — one ascending walk through the show, batching per cue.
4. **Tasks T1–T19** — each with the concrete tool calls and a Verify line.
5. **Blocked / needs an operator decision** and **6. Done-criteria**.

## The transformation, counted

The input has **22 note sections**. The output has **19 tasks**:

```
22  note sections in cue-notes-20260626.md
 −2  Song 8 (the HCS +3° tilt, and the TOP-PRIORITY end-of-8 sequencing bug)
 −1  the two Song-17 sections merged into one per-cue pass (T13)
 ————
 19  tasks
```

The full section → task mapping is in `expected_items.yaml#required_tasks`. Nothing
else is dropped, renumbered or invented.

## What is genuinely added, and what is only carried

This is the part a grader has to get right, because the input document is not raw
operator speech — it is already a worked-up capture with most channel resolutions in
it. Scoring the resolutions as discoveries would flatter every answer.

**Carried forward from the input (low signal — check for preservation, not credit):**

- Every channel decode the plan uses except the completed table: `white booms 3/4 =
  64+67`, `pink = 42–47`, Lone Stars `108–112`, desires `113/119`, centre specials
  `87+88`, trough `70+71`, ring `80–85`, and the boom-order decode with its yellow
  worked example. All of it is in the notes already, and `rig.yaml`'s own header
  comment repeats the indirection with a pink example.
- Four of the eight global rules (trust the live console; the +30% bump; bounce-verify;
  storing via typed tools) — the notes' header block states all four.
- The ascending execution order. The notes' body is *already* in show order; the plan's
  order is the same list with Song 8 removed and Song 17 merged. Only the stated
  rationale ("minimises `go_to_cue`", "batch all edits for a cue while you're in it")
  is new.
- The `.99` neutral-bridge template used in T6, quoted from the notes.
- 14.2's blocked status, and the "spots = Lone Stars, not the LED spots 98/99"
  disambiguation — the notes resolve both.

**Synthesised by the plan (this is the case):**

- **The complete channel table.** Nine colours × six booms, correct under the
  `boom_order: [1,3,5,2,4,6]` indirection. Verified end-to-end by
  `derive_channel_table.py --check`. The input gives the rule and two examples; the
  plan applies it nine times and inlines the result, which is what makes the document
  self-contained.
- **The one-time calibration.** The notes warn "confirm the tilt sign live" in three
  separate sections. The plan turns three warnings into one prerequisite step with a
  concrete procedure and a stated default to flip.
- **Live-show safety as a standing rule.** The notes state it *only* inside the
  End-of-Song-8 section — the one the operator pulled. An agent that deletes Song 8
  mechanically loses the rule unless it also reads `verification.md`. This is the
  single sharpest discriminator in the case.
- **"Per-cue, not per-song."** Stated per-item for Song 17 in the notes; promoted to a
  rule that also governs Songs 18 and 19.
- **`health_check` first.**
- **Tool-call resolution.** Twelve real tools named across the tasks, all of which
  exist in `repo-README.md`'s table.
- **A per-task Verify line and a done-criteria section.** Absent from the notes.
- **The collected blocked/decide-live section** — 14.2, six dial-live amounts, and
  the calibration, gathered in one place at the end.
- **The 20.1 scope guard.** The notes flag an unreconciled house conflict for 20.1;
  the plan converts it into "this note is ladders only — leave house (3,4) and
  everything else untouched", which is a scoping decision rather than a copy.

## Where the reference is wrong

**D1 — `update_cue` and `record_cue` are described backwards.** Global rule 3 reads:

> **`update_cue`** (captures the whole live stage — only safe if you set just the
> intended channels) or **`record_cue`** (selective merge). Prefer **selective**…

`repo-README.md` says the opposite: `record_cue` = "Record the live look into a cue";
`update_cue` = "Merge the current manual changes into a cue — the 'tweak one thing and
save it' move (Eos Update; **not the whole look**)". So `update_cue` is the selective
one. The plan's per-task text ("selective record") survives either reading, so the
error is confined to that one parenthetical — but it is a real defect in a document
whose entire purpose is to be executed literally.

**A candidate answer that assigns these two tools correctly is better than the
reference and must be scored up, not marked as a deviation.** Reproducing the reversal
is weak evidence of having copied the reference's framing rather than read the tool
table.

**D2** — the notes call 20.1 "the all-8-color rainbow" and then say "all 9 ladder color
groups" in the same sentence. There are nine. The plan silently corrects to nine.

**D3** — `rig.yaml`/`venue.yaml` and `show-map-README.md` disagree about Lone Star home
(pan −48/tilt 50 vs −50.1/43.1 for ch108, and the latter carries per-fixture zooms
against the former's "default zoom ≈ 48°"). The plan uses the `rig.yaml` figure without
noting the conflict. Flagging it is a bonus; either source is defensible.

## The outcome

`ground_truth.outcome: merged` — meaning **the plan was accepted as the deliverable of
this triage step**. That is the outcome this case grades. Be precise about what is and
is not attested:

- The plan is the last document written in the 2026-06-26 working session and the form
  the work was handed off in. Nothing supersedes or revises it.
- **Per-task execution is not attested.** The next dated working file,
  `cue-notes-20260716-session.md` (withheld, three weeks post-cut), is a different
  sweep — fade times on songs 5/10 and a whole-show Lone Star position audit. It does
  not reference the plan or its T-numbering, and it does work on Song 8, which the plan
  excluded. So do not grade against "did the tasks get done".

One piece of downstream corroboration is worth recording, because it independently
validates the plan's most-synthesised element. The 2026-07-16 session ran the sign test
the plan's §1 asks for and found:

> increasing tilt = aim UP (not down as the tool description states) … verified live by
> the operator via a deliberate ±20 swing test

So the calibration step was necessary (the tool's own documentation had the convention
backwards for this rig), and the plan's stated working assumption — "assume tilt up =
+tilt; FLIP if calibration says otherwise" — was correct. An agent that hoists this into
a one-time calibration is not being pedantic; it is catching the thing that actually
went wrong later. This paragraph is post-cut evidence and must never reach the agent.
