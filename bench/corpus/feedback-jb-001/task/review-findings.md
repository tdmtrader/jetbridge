# Merge-gate review — `agentic-ux-wave-2`

**Reviewer:** merge-gate adversarial review (8 angles)
**Target branch:** `agentic-ux-wave-2` → `jetbridge`
**Reviewed tree:** branch tip after the merge-in of `jetbridge`
**Date:** 2026-07-19

## Verdict

**Merge after fixes.** The wave does what its scope doc says and the surfaces it
adds are a real improvement over the pre-wave console. But thirteen defects are
verified in the code as written, and three of them (F1, F2, F9) make the
agentic UI *lie to the operator* about money-spending runs — a green OK over a
dead run, a stale "Waiting on you" on a run that can never continue, and a class
of ticket that can only be cleared by paying for another dispatch. Those are not
acceptable on a mainline that the self-build pipeline deploys within minutes of
merge. The remaining ten are ordinary correctness and cost defects. None
requires re-opening the wave's design; all are addressable in place.

Findings are ranked by what an operator would be misled into believing, or be
unable to do, on the live UI. Line numbers are at the reviewed tree.

---

## F1 — HIGH — `runOutcome` precedence is not worst-truth-wins

`web/elm/src/AgentBadge.elm:275-309` (`runOutcome`)

`runOutcome` tests `runStatus == "parked"` before it looks at `buildStatus` at
all, and once a `buildStatus` is present it never consults a step-reported
`"error"`/`"failed"`. Two distinct wrong renders follow.

(a) A run that was **aborted** (or failed, or errored) while parked at a HITL
checkpoint leaves the parked metric row behind forever. The badge pulses
"Waiting on you" for a run that is dead — the operator is asked for input that
can never be consumed.

(b) A step that reported `error`/`failed` inside a build that still
**succeeded** (an `attempts:`/`try` wrapping the agent step) renders a green OK.
The same step inside a build that is still `started` renders Running
indefinitely — the metric row lands at step end, so a wedged build shows a dead
run as live for as long as the build stays open.

*Failure scenario.*
`runOutcome { buildStatus = "aborted", runStatus = "parked", hasResult = False }`
→ `AwaitingHuman`, should be `Aborted`.
`runOutcome { buildStatus = "succeeded", runStatus = "error", hasResult = True }`
→ `Succeeded`, should be `Errored`.

*Correct behavior.* Worst truth wins: a terminally-bad build
(failed / errored / aborted) is final; otherwise parked wins; otherwise a
step-reported error/failed is never masked by a succeeded or still-open build;
only then does the build status speak (and `succeeded` is a green OK only with a
result in hand).

*Note.* The parked-first ordering was introduced by the earlier review round on
this branch as a fix for the opposite bug. Do not simply revert it — both
orderings are wrong; the precedence needs to be stated as a whole.

---

## F2 — HIGH — the ticket run ledger and review digest read the EARLIEST metric row of a build

`web/elm/src/AgentTickets/AgentTicket.elm:1112` (`runRow` → `runStatus`)
`web/elm/src/AgentTickets/AgentTicket.elm:1118-1132` (`runRow` → `hasResult`, `summary`)
`web/elm/src/AgentTickets/AgentTicket.elm:784-798` (`reviewDigest` → `latestSummary`)

A build produces **one `agent_run_metrics` row per step** — agent, then harvest,
then judge — and the API returns them `created_at ASC`. Both call sites take
`List.head` of the build's rows, i.e. the **first** step.

So the run's displayed status is the first step's status, and the displayed
summary is the first step's non-empty summary — the agent's own self-report.
The review digest and the ledger row therefore never show the harvest/judge
verdict that actually decided the run. The status read has a second failure: a
mid-build HITL park recorded on a later step hides behind an earlier step's
`"ok"` and renders as merely Running.

*Failure scenario.* Build 42 has rows
`[agent(status="ok", summary="I implemented X"), harvest(status="failed", summary="gates failed: build")]`.
The ledger row renders status "ok" with the agent's self-congratulation, and the
digest shows the same, while the run actually failed its gates.

*Correct behavior.* Status: a parked row anywhere in the build wins; otherwise
the **latest** step's status. Summary: the **last** non-empty summary of the
build's rows. And `hasResult` must be derived from the summary that is actually
displayed — deriving it from "any row has a summary" lets a green OK be claimed
on the strength of a summary that is not the one on screen.

---

## F9 — HIGH — failed/errored tickets have no exit except a paid re-dispatch

`agent/api/tickets/types.go:41-49` (`validTransitions`)
`web/elm/src/AgentTickets/AgentTicket.elm:692-716` (`transitionTargets`, failed/errored arms)

`validTransitions` gives `StateFailed` and `StateErrored` exactly one outgoing
edge: `queued`. The only way to clear a dead ticket is therefore to spend money
re-running it. Since failed and errored are also in the "active" set that this
wave just surfaced on the dashboard strip and on the queue page, undispositioned
dead tickets accumulate in every active listing with no write-off available —
and they then crowd out live work (see F10).

The Elm copy of the transition table has the same hole, so even if the server
allowed the write-off the operator would have no control to trigger it.

*Failure scenario.* A ticket errors on a bad prompt. The operator does not want
to spend money re-running it and has no other action; the ticket sits in the
dashboard agent strip and the queue indefinitely.

*Correct behavior.* Failed and errored must each gain a human-disposition edge
to `abandoned`, in the server state machine **and** in the UI's mirrored copy,
offered alongside the existing retry. Note explicitly: this is a change to a
frozen state machine that lives outside the wave's own files. Do not disturb the
rest of the matrix — in particular, failed and errored must remain **non**-terminal,
and the terminal set must stay exactly what it is today.

---

## F3 — MEDIUM — `/agent` ledger row expansion is keyed by build id alone

`web/elm/src/Agent/Agent.elm:79` (`Model.expandedRuns`)
`web/elm/src/Agent/Agent.elm:634-643` (`runsTable`, row key)
`web/elm/src/Agent/Agent.elm:659-710` (`runRow` / `runStepCell`)

The expansion set is keyed by build id, but the ledger renders **one row per
metric row** and a build contributes several (agent + harvest + …). Expanding
one step's summary expands every sibling step row of that build at once.

*Failure scenario.* Build 100 has an agent row and a harvest row. Clicking
"expand" on the agent row also expands the harvest row; collapsing either
collapses both.

*Correct behavior.* Each rendered row must toggle independently. Whatever
identity is used must also be stable across the 5s refetch that prepends newer
runs — so a list ordinal is equally wrong, and the existing comment claiming the
key is refetch-stable is only half right.

---

## F4 — MEDIUM — the `/agent` section nav is entirely dead

`web/elm/src/Agent/Agent.elm:576-594` (`sectionNav`)
`web/elm/src/Agent/Agent.elm:597-605` (`navLink`)

`navLink` renders `Html.a [ href ("#" ++ anchorId) ]`. The web app is a
`Browser.application`, which intercepts every internal link click and re-routes
it through `Routes`; `Routes` carries no fragment for this page, so the browser
never performs the jump. **Every entry in the new nav strip is a no-op** — the
feature ships 100% non-functional.

*Failure scenario.* Click any entry in the `/agent` section nav: nothing
happens. No scroll, no URL change, no error.

*Correct behavior.* Clicking an entry must actually move the console to that
section. Re-adding or "fixing" the fragment href cannot achieve that under
`Browser.application`; the jump has to be performed by the app itself.

---

## F5 — MEDIUM — an armed transition/dispatch confirm is not disarmed when the refetch reveals a state change

`web/elm/src/AgentTickets/AgentTicket.elm:130-163` (`handleCallback` → `AgentTicketFetched (Ok detail)`)

`pendingTransition` and `dispatchConfirm` are two-step confirms, armed against
the state the user was looking at. The 5s refetch replaces `model.detail`
without touching either, so a confirm armed for the old state stays armed and
keeps pointing at a decision that no longer applies.

*Failure scenario.* The operator arms "Abandon" on a `needs_review` ticket; a
refetch shows the dispatcher has re-queued it; the armed "Confirm Abandon"
button is still on screen, offering a transition that is no longer the one being
decided.

*Correct behavior.* When the refetched ticket's state differs from the one
already in the model, the armed confirms must be cleared so the user re-decides
against the fresh state.

*Severity note.* Confirming inside the race window is separately safe — the POST
carries the old state as `from` and the server's compare-and-set rejects it with
a 409. This is a UI-truth defect, not a data-integrity hole; do not "fix" it by
adding server-side machinery.

---

## F6 — MEDIUM — an open edit whose ticket goes terminal strands `editing = True` with no Cancel

`web/elm/src/AgentTickets/AgentTicket.elm:130-163` (`handleCallback` → `AgentTicketFetched (Ok detail)`)
`web/elm/src/AgentTickets/AgentTicket.elm:465` and `:870` (edit form suppressed for terminal states)

The edit form is suppressed whenever the ticket state is terminal. If a ticket
goes terminal while an edit is open, the refetch flips the render but leaves
`model.editing = True`: the form — and with it the only Cancel control —
vanishes. The page is stuck in a state the user cannot leave and cannot see,
with unsaved input still held silently in the model.

*Failure scenario.* The operator opens edit on a `needs_review` ticket and
starts typing; the merge lands and the ticket becomes `merged`; the form
disappears mid-keystroke with no explanation and no way back.

*Correct behavior.* Detect the terminal transition arriving under an open edit,
leave editing explicitly, and tell the user the ticket moved and their unsaved
changes were discarded.

---

## F7 — MEDIUM — the observations toggle stores a delta from the default, so a refetch crossing the threshold inverts the user's choice

`web/elm/src/Build/AgentReview.elm:163-195` (`observationsSection`), especially `:178`
`web/elm/src/AgentTickets/AgentTicket.elm:313-315` and `web/elm/src/Build/Build.elm:560-561` (`ToggleAgentReviewObservations`)

`showObservations` records "the user overrode the default", and the rendered
state is that flag XOR'd with the count-derived default (`count <= 5`). Reviews
refetch while the page is open. When a refetch pushes the observation count
across the five-item threshold, the default flips — and because only the *delta*
is stored, the panel the user explicitly opened closes (or the one they closed
springs open) with no interaction at all.

*Failure scenario.* A review with 5 observations renders open; the user folds
it; a refetch arrives carrying a 6th observation; the panel springs back open.

*Correct behavior.* Once the user has expressed a choice, that choice is
absolute and must survive any later change in the count. Only the *initial*
state should depend on the count. Note that this state is shared by two pages
and the toggle message is in the common message type — the fix has fan-out.

---

## F8 — MEDIUM — the unknown-step decoder's "name" branch is dead, so degraded rows render as anonymous "step"

`web/elm/src/Concourse.elm:771-778` (`decodeBuildStepUnknown`)

The fallback decoder tries `Json.Decode.field "name"` at the **top level** of the
step object, but public-plan steps are shaped
`{"id": …, "<type>": {…, "name": …}}` — the name is nested one level down, under
the type key. The branch can never match, so every unrecognized step falls
through to the literal string `"step"`. The durable fallback that this wave
added specifically so a new step type would degrade *legibly* instead degrades
*anonymously* — which is the same failure mode as the wave's own U1 harvest bug.

*Failure scenario.* `{"id":"1","fancy_step":{"name":"deploy"}}` decodes to an
unknown step labelled `"step"` instead of `"deploy"`.

*Correct behavior.* Label the step from the name nested under its unknown type
key when there is one, otherwise from the type key itself; fall back to a
generic label only for a genuinely typeless `{"id": …}` object. Concretely:
`{"id":"1","fancy_step":{"name":"deploy"}}` → `"deploy"`;
`{"id":"1","fancy_step":{}}` → `"fancy_step"`;
`{"id":"1"}` → a generic label.

---

## F10 — MEDIUM — the dashboard agent strip lets attention states take all 8 slots and hide live work

`web/elm/src/Dashboard/Dashboard.elm:1242-1274` (`agentTicketStrip`)

The strip sorts by `agentStateOrder` (attention states first) and then takes the
first 8. A backlog of undispositioned `needs_review`/`errored` tickets — exactly
what F9 guarantees will accumulate — fills every slot, so running and queued
tickets, the things actually moving right now, disappear from the dashboard
entirely.

*Failure scenario.* Nine `needs_review` tickets sit undispositioned; two runs
are executing; the dashboard strip shows only `needs_review` chips and no
evidence that anything is running.

*Correct behavior.* Live work must always keep a couple of the eight slots.
**Attention-first ranking is intended and must stay** — this is a
monopolisation cap, not a re-ranking. The full list remains on the queue page.

---

## F11 — LOW — the queue page runs a whole-window cost rollup on the 5s tick and re-renders on a 1s tick

`web/elm/src/AgentTickets/AgentTickets.elm:115-128` (`handleDelivery`)
`web/elm/src/AgentTickets/AgentTickets.elm:149-151` (`subscriptions`)

`FetchAgentTicketCosts` is a whole-window ledger aggregation. It is fired every
five seconds per open tab — 720 aggregations an hour — for numbers that move at
run granularity. Separately, a dedicated one-second tick updates `now`, which
re-filters and re-sorts the entire queue every second purely to advance the
"N ago" labels that the 5s refetch redraws anyway.

*Failure scenario.* Every open `/agent-tickets` tab issues 720 ledger
aggregations per hour and 3600 full-list re-renders.

*Correct behavior.* The rollup belongs on a minute cadence — its inputs move at
run granularity, not at tab-refresh granularity. The `now` advance does not need
a clock to itself either: the labels it feeds are approximate, and the page
already wakes up often enough to keep them honest. Neither change may cost the
page its live-updating behavior — ticket data must still refresh on its current
beat and the elapsed-time labels must keep advancing.

---

## F12 — LOW — the ticket page keeps polling after the ticket is terminal

`web/elm/src/AgentTickets/AgentTicket.elm:232-241` (`handleDelivery` → `ClockTicked FiveSeconds`)

The 5s refetch of ticket + metrics runs unconditionally for the life of the tab.
Once the ticket is terminal nothing can change server-side — no dispatch, frozen
runs and costs — so the page refetches identical bytes forever.

*Failure scenario.* A merged ticket left open in a background tab polls two
endpoints every five seconds indefinitely.

*Correct behavior.* Stop polling once the loaded ticket is terminal. Keep
polling while the detail is still unknown (an unloaded page must not deadlock
itself).

---

## F13 — LOW — `Plan.Public()` exhaustiveness is unpinned

`atc/public_plan.go:5-40` (`Plan.Public()`), `atc/plan.go:5-42` (`atc.Plan`)

`Plan.Public()` is a hand-maintained mirror of `atc.Plan`'s step fields. A step
type added to `atc.Plan` but not to `Public()` serializes as a typeless
`{"id": …}` object, which the UI can only render as an anonymous fallback step —
which is *exactly* how the harvest step went missing from the build page (this
wave's own U1, fixed on this branch in `atc/public_plan.go`).

This is a **gap, not a live defect**: every step field of `atc.Plan` is covered
by `Public()` at this ref. The wave fixed the instance and left the class
unguarded — nothing fails when the next step type is forgotten.

*Correct behavior.* Pin the class, not the instance: coverage that fails when a
step field is added to `atc.Plan` without a matching case in `Plan.Public()`.
Whatever form it takes, it must derive the set of step fields from `atc.Plan`
itself — a hand-written list of the fields to check has exactly the maintenance
hole this finding is about, and would need updating by the same person who
forgot `Public()`. This is the only finding whose remedy is a test rather than a
change.

---

## Also (minor, non-blocking)

- **`web/public/elm.js` is committed but never served.** `hack/build-web.sh`
  produces `elm.js` only as an unminified intermediate and deletes it; the
  served bundle is `elm.min.js`. The branch nonetheless carries ~55k lines of
  `elm.js` churn in-tree, which is pure review and diff noise. Drop it from the
  repository.
- **The branch scope doc lives at the repo root.** Every other plan in this repo
  lives under `docs/superpowers/plans/` with a dated slug; this one is a
  root-level SHOUTING_CASE file.

---

## Investigated and refuted

These were raised during the review and are **not** findings. They are recorded
so the same ground is not re-covered.

- **"The `BuildStepUnknown` generic catch-all should be removed / the decoder
  should fail loudly."** No — a bare `{"id": …}` step must still render as
  *something*. The defect is only that the named branches never fire (F8), not
  that a fallback exists. Keep the catch-all.
- **"The dashboard strip should stop ranking attention states first."** No —
  attention-first ranking is the intended behavior. Only monopolisation is the
  problem (F10).
- **"The 5s refetch clobbers the open edit form's unsaved input."** Already
  fixed on this branch by the earlier review round; `handleCallback` guards the
  edit fields behind `model.editing`. The residual defects in that block are
  F5 and F6.
- **"Blank/empty finding ids collide in the agent-review card."** Already fixed
  on the mainline and merged into this branch.
- **"An armed confirm that fires against a stale state could corrupt the
  ticket."** Overstated — the server transition is a compare-and-set on `from`,
  so a stale confirm 409s. See the severity note on F5.
