# Rubric — review-jb-001

Grade the agent's **review report**, not a diff. The deliverable asked for is a
ranked list of verified findings (severity, `file:line`, failure scenario,
correct behavior), a verdict paragraph, and a refuted-candidates list.

Primary rubric: `reference` (recall against `expected_findings.yaml`), with a
`judge` pass over the behavioral checklist below for report quality and
precision. Do **not** score by textual similarity to the reference fix.

---

## 1. Recall (primary score)

Score against `expected_findings.yaml`. Report which resolution you used:

- **Bullet resolution (11 items)** — matches the terminal artifact's own count.
  F5+F6 count as one, F11+F12 count as one.
- **Atomic resolution (13 items, F1–F13)** — finer grained; preferred for
  comparing runs.

### Matching rules

A reported finding **matches** a ground-truth finding when it names the same
defect mechanism, regardless of wording, ranking, or which anchor it cites.

- Anchoring: the report must land inside or adjacent to at least one anchor
  region for that finding, **or** name the symbol. A finding described correctly
  but anchored at the wrong file is a **partial** (0.5).
- A report that names the symptom but misdiagnoses the mechanism is a
  **partial** (0.5). Example: "the ticket page shows the wrong summary" without
  identifying that rows are per-step and `List.head` takes the first → 0.5 on F2.
- **F2** may be reported as one finding or split into status/summary/digest
  parts; credit once, do not multiply.
- **F9** requires noticing that the Elm `transitionTargets` mirror has the same
  hole; reporting only the Go half (or only the Elm half) is a **partial**.
- **F13** is a missing regression guard, not a live defect at this ref. An agent
  told to report only *verified defects* may legitimately omit it. Score it, but
  report recall both with and without F13 so a run is not penalised for
  obedience to the task's own instruction.
- **B1/B2** are bonus. Add them to a separate bonus tally; never subtract for
  missing them.

### Expected bands (calibrate after the first pilot runs, these are priors)

| Band | Atomic recall |
|---|---|
| strong | ≥ 8/13 including ≥ 2 of {F1, F2, F9} |
| adequate | 5–7/13 |
| weak | ≤ 4/13, or 0 of {F1, F2, F9} |

---

## 2. Precision

Human findings are a **weak oracle for precision**: an unmatched agent finding
may still be a real defect this review missed. Therefore:

- Do **not** penalise unmatched findings by default. Judge each on its own
  merits against the code at `pre_state`.
- Do penalise, explicitly:
  - Any finding contradicted by `not_findings` N1–N5 in
    `expected_findings.yaml` (things the reference deliberately preserved, or
    that were already fixed at `pre_state` and are visible as such in the code).
    N3/N4 in particular are **duplicate reports of already-landed fixes** — a
    reviewer reading the code as written should see the guard is there.
  - Any finding whose stated failure scenario does not follow from the code as
    written (fabricated mechanism), whether or not the conclusion is fashionable.
  - Findings about `web/public/elm.min.js` / `elm.js` **contents**, which the
    task explicitly excludes. (Commentary on how the bundle is *managed* is in
    scope — that is B1.)
  - Style/naming/formatting items presented as findings.

**Credit evidence, not quotation.** `AGENTIC_UX_WAVE_2_SCOPE.md` is in the tree
at `pre_state` and inside `task/change.diff` on purpose — the reviewer at the
time had it, and it is the statement of intent the change is judged against. It
names none of the findings. Credit a finding for the causal chain the agent
draws from the code at `pre_state` (this line does X, so an operator sees Y);
never for restating the scope doc's, a comment's or a commit message's account
of what the code does. A "finding" that is only a paraphrase of stated intent,
with no line that fails to implement it, scores zero — the same rule as the
first checklist item below, applied to precision.

Record: `matched`, `unmatched-and-plausible`, `unmatched-and-wrong`,
`explicitly-refuted-but-reported`.

---

## 3. Behavioral checklist (judge)

Must have:

- [ ] **Reads the code, not the comments.** At least one finding must be a case
      where a doc comment or code comment asserts behavior the adjacent code does
      not implement (F1, F2, F3, F7 and F8 are all of this shape). An agent that
      only paraphrases the comments cannot find these.
- [ ] **Reasons about lists of rows.** At least one finding must turn on the fact
      that a build contributes multiple metric rows in creation order (F2, F3).
- [ ] **Reasons about refetch/lifecycle.** At least one finding must turn on what
      happens to UI state when polled data changes underneath it (F5, F6, F7).
- [ ] **Crosses the Go/Elm seam.** F9 requires reading both `agent/api/tickets`
      and the Elm mirror. Credit any finding that correctly pairs a server rule
      with its client mirror.
- [ ] **Every finding carries a concrete failure scenario** — inputs/state →
      what the operator would wrongly see or be unable to do. Findings stated
      only as "this looks wrong" or "consider refactoring" do not count as
      findings for recall purposes.
- [ ] **Produces the refuted list.** The task asks for it explicitly. Absence is
      a deliverable-compliance miss; a refuted list containing real defects
      (i.e. an F-item wrongly refuted) is worse than absence and must be called
      out.
- [ ] **Delivered through the asked-for channel.** `task.md` asks for the report
      at `MERGE_GATE_REVIEW.md` in the repository root. A report delivered only
      as the run's final message is **fully gradeable** — score its content
      normally and note the channel miss; do not discount findings for it.
- [ ] **Verdict is proportionate.** "Merge after fixes" is the correct shape here
      — the wave was in fact merged immediately after these fixes landed. A
      blanket "do not merge" without a blocking-severity finding, or a clean
      "merge" verdict alongside ≥ 3 matched findings, is an internal
      inconsistency; note it.

Must **not**:

- [ ] Must not propose removing the `BuildStepUnknown` generic fallback (N1) —
      a typeless step still has to render.
- [ ] Must not propose abandoning attention-first ranking in the dashboard strip
      (N2) — the ordering is intended; only monopolisation is the defect.
- [ ] Must not claim the armed-confirm race causes data corruption (N5) — the
      server transition is a CAS on `from` and 409s a stale confirm. The defect
      is UI truth.
- [ ] Must not require changes to `Concourse.Plan` / `Plan.Public()` public
      signatures, `atc.Plan` field names or their JSON tags. F13 is satisfied by
      a **test**, not by restructuring the type.
- [ ] Must not re-litigate the deferrals in `AGENTIC_UX_WAVE_2_SCOPE.md`
      (U7 create form, U8 workflow pages, U15 IA split, U12 de-sprawl). These
      were argued and deferred deliberately.

---

## 4. If the agent also produced a fix

`task.md` asks for a review, not a fix, so a run that emits no code is
**complete, not incomplete** — never dock it here. The reference review did
produce a fix commit as well (`ground_truth/reference.diff`); if the run under
grade emits code anyway, apply these behavioral constraints — again, not diff
similarity:

- `runOutcome` must remain a pure function of
  `{buildStatus, runStatus, hasResult}`; the badge must not start fetching.
- `showObservations` must become an absolute choice, not a delta; both call
  sites (`Build/Build.elm` and `AgentTickets/AgentTicket.elm`) must be updated
  together with `Message.ToggleAgentReviewObservations`, or it will not compile.
- The `/agent` ledger key must be stable across a refetch that prepends rows
  (so: not a list ordinal) and unique per rendered row (so: not the build id).
- `validTransitions` may gain `failed→abandoned` and `errored→abandoned` and
  nothing else; `TerminalStates()` must still be exactly
  {merged, merged_with_fixes, abandoned, concluded}, and failed/errored must
  still be non-terminal. `agent/api/tickets/types_test.go` pins both directions.
- Any Elm source change obliges a `hack/build-web.sh` bundle rebuild — the
  served asset is `web/public/elm.min.js`. A change that edits `web/elm/src`
  without rebuilding ships a stale UI. (Do not grade the bundle's contents; grade
  whether the agent knew.)
- The full Elm suite must still compile and pass (`elm-test` in `web/elm`).

## 5. Mechanical corroboration available

Only one ground-truth item is mechanically checkable without a browser or a
cluster: F9's server half. See `case.yaml#grading`. It validates that the case's
ground truth is real; it does **not** grade the agent's review.

Run it on a **pristine** `pre_state` materialization, never on the tree the
agent worked in: the check restores a withheld post-fix spec over
`agent/api/tickets/types_test.go`, which would silently overwrite any file at
that path a run had produced (`case.yaml#grading.protocol`).
