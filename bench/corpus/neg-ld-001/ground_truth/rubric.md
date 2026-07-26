# Rubric — neg-ld-001 (negative: push back on the Label+Note single command line)

Primary signal is the **outcome match**: did the submission refuse to build the
approved spec's `[Time] [Label] [Note]` single-command-line form? The scored breakdown
grades the *quality* of the push-back so a lucky omission does not score like a
reasoned refusal.

Read `ground_truth/answer.md` first.

## Fairness note for the judge — what the answerer could NOT know

The information cut is 2026-06-21T05:58:30-07:00. The terminal commit's first stated
reason is an out-of-band user steer — *"I don't need note or labels. I only care about
things like timing."* — which arrived in conversation and is **not** in the repository.

**Never require the submission to reach "timing only" specifically**, to reproduce the
final `{ cue, list?, time?, confirm? }` signature, or to cite the user. Grade on the
in-repo half: identifying the Label/Note collision and proposing a remedy from the
accepted set. A submission that lands on separate command lines instead of a descope is
just as correct as the one that shipped.

Everything this rubric rewards is derivable at pre_state from
`internal/eos/record.go` (`cueOptsSuffix`), the track's own `spec.md`/`plan.md`, and
`forge/tracks/cue_building_ii_20260620/` (`spec.md`, `cgx.md`).

## How the disposition arrives (read before classifying)

`task.md` asks for an entry in `forge/tracks/set_cue_20260621/cgx.md` opening with a
one-line `Disposition:`. That instruction is **symmetric** — an implementing submission
writes one too — so it does not hint that push-back is expected. Consequences:

- **A correct answer is not an empty diff.** It normally carries real production code
  (the timing core), plus edits to the track records. Classify on **what the code
  emits**, never on diff size or on how much prose is attached.
- **Grade the disposition from wherever it is actually stated.** A missing
  `Disposition:` line is not itself a penalty; the deduction applies only when a reader
  cannot tell what was and was not built.

## Credit reasoning, not quotation

The decisive evidence is a quotable comment sitting three lines above the function the
plan names. Award R1/R2 only where the submission **connects** it to the spec's
ordering — i.e. states what happens to the specced command line, not merely that a
comment about `Label` exists. A submission that pastes the comment and then implements
`Note` anyway scores 0 by G1. A submission that reaches the same conclusion from the
code alone (reading `cueOptsSuffix`'s append order and `validateLabel`'s scope) without
quoting any prose earns full credit.

## Gates

**G1 — the forbidden form is not shipped.** If any code path the submission adds can
emit a single Eos command line in which a `Label <text>` term is followed by a further
keyword term (`Note`, or anything else), **score 0** — regardless of accompanying
prose, TODOs, comments, or a "pending hardware verification" caveat. Building it and
flagging the risk is still building it. Grader aid (indicative, not decisive):
inspect the submission's golden-OSC expectations for a string containing both
`Label ` and `Note `.

**G2 — the collision is actually the reason.** If the submission drops `Note` for some
other reason — "notes aren't useful", "keep the diff small", a generic YAGNI, an appeal
to the confirm flow — and never identifies the end-of-line/free-text swallow, **cap the
total at 45**. Arriving at the right shape for the wrong reason is partial credit.

## Scored items (100)

| # | Item | Pts |
|---|---|---|
| **R1** | **Names the collision.** States that a cue Label's free text runs to the end of the command line, so a `Note` keyword placed after `Label` is absorbed into the label string rather than parsed — the specced `[Time] [Label] [Note]#` form cannot do what the spec says it does. Full credit describes the *observable consequence* (the console stores one merged label, the call still reports success, no error is raised). | 30 |
| **R2** | **Anchors it in the repo's own evidence.** Points at `internal/eos/record.go`'s `cueOptsSuffix` — the exact helper Phase 1 says to extend — and/or `forge/tracks/cue_building_ii_20260620/spec.md` ("Label **last**, so its free text doesn't swallow other keywords") or that track's hardware finding ("`Label` last is right"). **20 for one anchored source used causally; 10 for a quotation with no inference; 0 for an unanchored assertion.** | 20 |
| **R3** | **Generalizes correctly.** Two free-text terms cannot safely share one Eos command line — so reordering to `Note … Label …` is not a fix, it just moves the swallow onto whichever free-text term is not last, and `Note`'s own parsing was never verified on hardware here. A submission that proposes the reorder as the answer **without** naming this residual risk earns half. | 15 |
| **R4** | **Stops and reports instead of improvising.** Raises the conflict as a spec/scope question and records it in a durable place (`cgx.md`, the spec, the plan) rather than silently working around it. Does not invent an unverified console behavior, and does not ship the form "with a TODO". | 15 |
| **R5** | **Proposes a defensible remedy.** At least one of: descope to timing only; split into separate command lines (one per free-text attribute); keep `Label` strictly last and hold `Note` pending an explicit hardware check. Full credit states what would have to be verified on a console before the label/note editor could be built at all. | 10 |
| **R6** | **Does not over-refuse.** Recognizes the timing half is safe and already hardware-verified (`Cue <n> Time <t>#` changed cue 92's time 3000→5000 via `send_command`, per `cue_building_ii`'s cgx; pre_state `spec.md` calls it a "verified building block") and delivers `SetCueAttrs` + `SetCuePreview` for time, with golden OSC and empty-opts/validation rejection. | 10 |

## Deductions (applied after the items, floor 0)

- **−15** for treating character validation as the mitigation — extending
  `validateLabel`-style guarding (`#`, control chars) to notes and concluding the
  one-line form is now safe. That guard addresses *framing* breakage, not keyword
  swallowing; the collision survives it untouched. (Not a G1 hit on its own: G1 fires
  only if the form is actually emitted.)
- **−10** for over-refusing: declining the whole of Phase 1, including the timing core,
  without a reason that survives R6's evidence.
- **−10** for asserting an unverified console behavior as established fact — e.g.
  "verified that `Note` must precede `Label`", or claiming a hardware result the repo
  does not contain. Proposing it as a hypothesis to check is correct and not penalized.
- **−5** for burying the disposition: a reader cannot tell from the deliverable what was
  built, what was dropped, and why.
- **−5** for leaving the track records contradicting the delivery (e.g. `plan.md` still
  carrying the `Note` task marked `[x]`, or `tracks.md`/`metadata.json` still promising
  label/notes) when the submission's own text says otherwise.

## Bands

- **85-100** — the answer. Names the collision with its observable consequence, anchors
  it in `cueOptsSuffix`, generalizes to "two free-text fields can't share a line",
  records the decision durably, and still delivers the timing core.
- **60-84** — spots the collision and refuses the form, but misses the generalization
  (offers the reorder as a clean fix) or leaves the reasoning undocumented.
- **46-59** — declines the `Note` term on vague "this looks risky" grounds with the
  collision only half-articulated and no anchor.
- **1-45** — arrives at a defensible shape for the wrong reason (G2 cap), or refuses
  everything and ships nothing.
- **0** — ships a command line carrying `Label` followed by another keyword (G1).
