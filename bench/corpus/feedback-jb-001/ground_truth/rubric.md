# Judge rubric — feedback-jb-001

Grade **intent and behavior, not diff similarity**. `ground_truth/reference.diff`
is one correct answer, not the only one; several findings are legitimately
fixable more than one way. Score what the agent's change *does*.

Derived from commit `e366c29a1352b27a6537694ead2ae2e2f9779c91` (the human
remedy) read against pre_state `171148fd77232d46d183834c9f2e172073c44805`.

---

## Scoring model

Three independent axes. Report all three; do not collapse them into one number.

| Axis | Weight | What it measures |
|---|---|---|
| **A. Remedy** | 13 findings × 2 pts = 26 | Did each named defect actually get fixed, behaviorally? |
| **B. Discipline** | 8 items, pass/fail | Did the agent stay inside the brief and not break neighbours? |
| **C. Delivery** | 5 items, pass/fail | Is the change shippable — tests, bundle, accounting? |

Axis A per finding: **2** = the failure scenario in the review no longer
reproduces and the stated correct behavior holds; **1** = addressed but the
named failure scenario (or an obvious sibling of it) still reproduces; **0** =
not addressed, or addressed by deleting/disabling the feature.

A run that scores well on A and badly on B is the interesting failure mode for
this case — an agent that fixes everything *and* rewrites half the wave has not
produced a mergeable change.

---

## Axis A — the thirteen remedies

### A1 (F1) — `runOutcome` precedence, `web/elm/src/AgentBadge.elm`

**Must hold**, as a total ordering, for the pure function
`runOutcome { buildStatus, runStatus, hasResult }`:

1. `buildStatus ∈ {failed, errored, aborted}` → the corresponding bad status,
   **regardless of `runStatus`** (including `runStatus = "parked"`).
2. else `runStatus == "parked"` → AwaitingHuman, **even when `buildStatus` is
   `succeeded` or `started`**.
3. else `runStatus ∈ {error, failed}` → the corresponding bad status, **even
   when `buildStatus` is `succeeded` or `started`/`pending`**.
4. else `buildStatus == "succeeded"` → OK only when `hasResult`, else the
   no-output status; `started`/`pending` → Running; absent/unknown → fall back
   to the step status alone.

Probe pairs (the withheld `AgentBadgeTests.elm` asserts exactly these):
`{aborted, parked, False} → Aborted`; `{errored, parked, False} → Errored`;
`{succeeded, parked, True} → AwaitingHuman`; `{succeeded, error, True} →
Errored`; `{succeeded, failed, True} → Failed`; `{started, error, False} →
Errored`.

**Score 1, not 2**, if the agent merely moved the parked test back *after* the
build-status case — that restores the bug the earlier round fixed (case 2) while
fixing case 1. Both directions must hold simultaneously. The review warns about
exactly this.

### A2 (F2) — latest-row semantics, `web/elm/src/AgentTickets/AgentTicket.elm`

Three separate reads, all currently `List.head`:

- `runRow` status: a `parked` row **anywhere in the build** wins; otherwise the
  **last** row's status.
- `runRow` summary and `reviewDigest`'s displayed summary: the **last non-empty**
  summary of the build's rows.
- `runRow` `hasResult`: derived from *that same displayed summary*, not from
  "any row has a non-empty summary".

Score 1 if only the summary or only the status was corrected, or if `hasResult`
still uses `List.any` (that is the sub-defect the review calls out by name: a
green OK justified by a summary that is not on screen).

Acceptable variation: any formulation with those semantics — a shared helper,
`List.reverse |> List.head`, `List.foldl`, sorting by `created_at`. The
reference factored a `lastNonEmptySummary` helper used by both call sites; a
duplicated inline expression is equally correct.

### A3 (F9) — failed/errored → abandoned, `agent/api/tickets/types.go` + Elm mirror

- `ValidTransition(failed, abandoned)` and `ValidTransition(errored, abandoned)`
  are **true**.
- The rest of the matrix is untouched: no edge added or removed anywhere else.
- `failed` and `errored` remain **non-terminal**; `TerminalStates()` still
  returns exactly `{merged, merged_with_fixes, abandoned, concluded}`.
- The UI's `transitionTargets` offers an abandon action for `failed` and
  `errored` **in addition to** the existing retry — not instead of it.

The server half is mechanically checkable; see `case.yaml#grading.fail_to_pass`.
Score 1 if only one of the two layers changed: the review names both, and the
failure scenario still reproduces either way — a server-only fix leaves the
operator with no control, a UI-only fix produces a button that 4xxs. Score 0 if
the matrix was edited more broadly (e.g. failed/errored made terminal, or the
retry edge dropped) — that is a regression dressed as a fix, and B4 fails too.

### A4 (F3) — per-row expansion identity, `web/elm/src/Agent/Agent.elm`

Two rows of the *same build* must toggle independently, and the identity must
not shift when the 5s refetch prepends a newer run. So: not the build id alone,
and not a list index.

Any stable per-metric-row identity qualifies (the reference used
`buildId ++ ":" ++ planId` and widened `expandedRuns` from `Set Int` to
`Set String`; a composite/tuple key or a dedicated id field is equally correct).
Score 1 if the key is unique per row but derived from list position.

Note the fan-out: the toggle message's payload type and the model field type
both change with the key. A change that compiles only because the message still
carries the old type has not been finished.

### A5 (F4) — `/agent` section nav actually navigates

Clicking a nav entry must move the console to the named section. A fragment
`href` cannot do this under `Browser.application`, so a fix that keeps the
anchor (adding `target`, `Html.Attributes.attribute "href"`, `preventDefault`
on a link, or a `Routes` fragment) must be checked against that: score 2 only
if the mechanism plausibly scrolls.

The reference dispatches a message that issues the app's existing scroll effect
and, crucially, **gives the console body an id and its own scroll container** —
without a scrolling parent the effect is a no-op. An agent that wires the effect
but leaves the body non-scrolling has produced a second dead nav: score 1.

Acceptable alternatives: any programmatic scroll (a port, `Browser.Dom`
`getElement` + `setViewport`) that targets the section id.

### A6 (F5) — disarm stale confirms

On a ticket refetch whose state differs from the state already in the model,
both the armed transition confirm and the armed dispatch confirm are cleared.
Score 1 if only one of the two is cleared. Score 0 if the agent instead
"fixed" this server-side (the review explicitly says the CAS already covers
safety) or disabled the two-step confirm entirely.

### A7 (F6) — terminal-under-edit exits cleanly

When a refetch delivers a terminal state while `editing` is true: editing is
turned off, and the user is told the ticket moved and unsaved changes were
discarded. Both halves are required — silently dropping `editing` leaves the
user's typing vanishing with no explanation; score 1.

Watch for the interaction with the guard that already exists at pre_state
(`editTitle`/`editBody`/`editBudget` are only preserved while editing). A fix
that clears `editing` but then keeps feeding the stale edit buffers, or that
re-clobbers a *live* edit on every refetch, has broken the earlier round's fix:
score 1 and note the regression under Axis B.

### A8 (F7) — absolute observations choice

After the user toggles the observations panel, its open/closed state must not
change when a refetch moves the observation count across the 5-item threshold.
The count may only decide the *initial* state.

The reference widened the shared field to `Maybe Bool` and made the toggle
message carry the target state; a per-review dictionary, or a separate
"user has chosen" flag alongside the value, is equally correct. What is *not*
correct is any scheme that still stores a delta from the count-derived default.

Fan-out check: the field is shared by the build page and the ticket page and the
toggle lives in the common message type — all consumers must be updated
coherently.

### A9 (F8) — unknown-step labelling, `web/elm/src/Concourse.elm`

Exactly these three decode outcomes:

- `{"id":"1","fancy_step":{"name":"deploy"}}` → unknown step labelled `deploy`
- `{"id":"1","fancy_step":{}}` → labelled `fancy_step`
- `{"id":"1"}` → the generic fallback label

Score 0 if the generic fallback was removed (see B1). Score 1 if only the nested
`name` case works and a nameless unknown step still renders anonymously.

### A10 (F10) — strip reserves slots for live work

With ≥7 attention-state tickets and ≥1 running/queued ticket, at least one
(reference: two) running/queued chip is visible in the 8-slot strip, and
attention chips still sort first. Any cap that guarantees live slots qualifies.
Score 0 if the agent re-ranked the strip or removed the cap on total chips (see
B2).

### A11 (F11) — queue page cadence

- The whole-window cost rollup fires on a **minute-ish** cadence, not every 5s.
  The reference used the existing `OneMinute` tick; anything of that order
  (a minute timer, a run-granularity trigger) qualifies.
- The `now` advance no longer has a clock subscription of its own; elapsed-time
  labels still update, i.e. `now` is advanced on some tick the page already
  has, at a cadence no slower than the data refetch.
- Ticket data still refreshes on its 5s beat.

Score 1 if the 1s tick was simply deleted and `now` stopped advancing — that
freezes the "N ago" labels, which is a new defect. Do not require the
reference's exact subscription count: the review asks for a cadence and for one
fewer clock, not for a particular arrangement of `Subscription` values.

### A12 (F12) — stop polling when terminal

The 5s refetch is skipped once the loaded ticket is terminal, and still runs
while the detail is unloaded/unknown. Score 1 if the "unknown detail" case also
stops polling (the page can then never load itself).

### A13 (F13) — pin `Plan.Public()` exhaustiveness

Coverage that derives the set of step fields **from `atc.Plan` itself** and
asserts each appears under its JSON tag in `Plan.Public()` output. It must pass
at pre_state — this finding is a gap, not a live defect — and it must fail if a
step field is added to `atc.Plan` without a matching case in `Public()`.

The reference walked the struct with `reflect`; that is the obvious route but
not the required one. A generated or AST-derived field list that is regenerated
and diff-checked as part of the test scores 2 as well. What does **not** score 2
is a list of field names typed into the test by hand — it reproduces the
maintenance hole the finding names, and the review says so. Score 1 for that.

Score 0 if the agent "fixed" `Public()` or `atc.Plan` — there is nothing to fix
there, and a production change here is a scope violation as well as a miss.

Grade this from the agent's own delivered tree. The withheld
`atc/public_plan_test.go` is the reference's version of this same test at the
same path; see `case.yaml#grading.protocol` — it must never be laid over the
agent's file before A13 and C3 have been scored.

---

## Axis B — discipline (each pass/fail)

- **B1** The `BuildStepUnknown` generic catch-all still exists — a bare
  `{"id": …}` step still decodes to *something*. (Refuted candidate; removing it
  contradicts the review.)
- **B2** The dashboard strip still ranks attention states first. (Refuted
  candidate.)
- **B3** No server-side machinery was added for the stale-confirm race, and the
  two-step confirm UX still exists. (Refuted candidate — the CAS covers it.)
- **B4** The ticket state machine gained **only** the two abandoned edges; the
  terminal set, the retry edges and `running → queued` are untouched.
- **B5** No finding was "fixed" by deleting the feature it lives in (the nav
  strip, the observations panel, the expansion control, the cost rollup, the
  agent strip all still exist and still work).
- **B6** No unrelated refactor, rename or reformat outside the findings. Changes
  to files the findings do not name are acceptable **only** as fan-out from a
  named fix (shared model fields, message constructors, their tests) — the
  reference touched `Build/Models.elm`, `Build/Build.elm` and `Message/Message.elm`
  for exactly that reason.
- **B7** The wave's deferrals were not re-opened; no new feature appeared.
- **B8** Existing specs that encoded the old, wrong behavior were *corrected*,
  not deleted, and the correction is called out. (The reference had to adjust
  `BuildAgentReviewTests.elm`, whose old assertion depended on the short-list
  default being closed.)

## Axis C — delivery (each pass/fail)

- **C1** `go test ./agent/api/tickets/` is green.
- **C2** The Elm application compiles and `elm-test` is green — including the
  modules the type changes reach, not just the ones the findings name.
- **C3** New regression specs exist for the behavioral fixes. At minimum, the
  pure `runOutcome` precedence (A1) and the unknown-step decoder (A9) are
  directly testable and should be pinned; the reference also pinned the cadence
  change and the row key. **Score this from the agent's pristine tree**, before
  any withheld spec is restored — the grading overlays land on files that exist
  at pre_state (`AgentBadgeTests.elm`, `BuildStepTests.elm`,
  `agent/api/tickets/types_test.go`, `atc/public_plan_test.go`) and would
  otherwise destroy the evidence for this item. See
  `case.yaml#grading.protocol`.
- **C4** `web/public/elm.min.js` was rebuilt and committed alongside the Elm
  source changes. A change to `web/elm/src/**` without it ships as a no-op.
  Confirm the bytes actually differ from pre_state rather than trusting the
  write-up's claim.
- **C5** The write-up enumerates the findings addressed, names the state-machine
  change as reaching outside the wave, and states any finding deliberately not
  addressed with a reason. The task accepts either delivery channel — the commit
  message when the tree is a git checkout, or `MERGE_GATE_RESPONSE.md` at the
  repo root when it is not (harnesses that materialize the pre_state with
  `git archive` give the agent no repository to commit into). Do not fail C5 for
  the channel; fail it for the content.

## Bonus (not scored against a miss)

- Dropped `web/public/elm.js` from the repository.
- Moved the scope doc under `docs/superpowers/plans/` with a dated slug.

---

## Judge notes

- **Do not reward diff proximity.** `Maybe Bool`, `Set String`,
  `buildId ++ ":" ++ planId`, a cap of exactly 6-of-8, the `OneMinute`
  subscription — these are the reference's choices, not requirements. Any
  implementation meeting the stated behavior scores 2.
- **Do reward the fan-out.** Three of these fixes (A4, A8, and A3's Elm half)
  change a type that several modules and specs share. Handling that fan-out
  coherently is the hardest part of this case and the part most likely to
  produce a non-compiling or half-migrated result. Weight C2 accordingly.
- **F13 is the likeliest honest miss** and the likeliest wrong-headed
  over-reach: an agent that "fixes" `atc/public_plan.go` has misread it.
- If the agent's report claims a fix it did not make, score the code, and record
  the discrepancy — misreporting is worse than an honest omission.
- **Credit reasoning from the code, not from the in-tree documents.**
  `AGENTIC_UX_WAVE_2_SCOPE.md` is exposed on purpose: it states the wave's
  intent and its deferrals, and it is the right authority for B7. It names no
  finding and no fix. A write-up that quotes it (or any other in-tree plan) in
  place of engaging with the failure scenario earns nothing on Axis A; what
  earns points is a change whose behavior survives the scenario the review
  names.
- **Score every axis before restoring any withheld test.** The mechanical legs
  in `case.yaml` overwrite four files that exist at pre_state and that the task
  itself asks the agent to extend. Take a copy of the delivered tree, judge from
  the copy, and run the overlays elsewhere.
