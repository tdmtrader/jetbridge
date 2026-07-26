# Rubric — review-jb-004 (adversarial review of the C3 archiver + cost chip)

Behavioral checklist for a judge. Score **what the review understood and what it
would have prevented**, not whether it phrased things the way the humans did. The
reviewer's output is a findings list; there is no diff to compare against.

The withheld oracle is `expected_findings.yaml`. Two findings are `required`; three
more are real but not required. Four `non_findings` are the traps.

## Scoring frame

| Dimension | What it measures |
|---|---|
| **Recall** (primary) | Did the review find F1 and F2? |
| **Anchoring** | Is each finding pinned to a file + function/query, with a concrete sequence? |
| **Precision** | Did it avoid asserting the `non_findings` as majors, and avoid padding? |
| **Severity calibration** | Are F1/F2 rated major, and is nothing trivial rated above them? |

A review that finds F1 and F2 with correct mechanisms is a **pass** even if it
misses F2b/F2c/F3 entirely and even if it raises one or two extra findings not in
the oracle.

## Evidence, not quotation — the in-tree deflators

Two pieces of authentic pre-state documentation partially point toward F1 and were
deliberately left exposed (see `notes.md` §"Considered and deliberately NOT
withheld"): the `RunBelongsToPipeline` / `TicketBelongsToRun` doc comments twenty
lines above the code under review, which say in prose that these ids are
attacker-writable; and `docs/superpowers/plans/agentic-platform/11-dispatch.md`,
which documents the `agent-ticket-<id>` naming convention that the reference fix
uses. They stay because the codebase being reviewable is the point.

Judge accordingly: **credit the causal chain the review builds from evidence, not
the doc it can quote.** Concretely —

- Quoting or paraphrasing the "attacker-writable plan env (F30)" comment, on its
  own, is **not** F1. It becomes F1 only when the review carries it through the
  three legs below: this specific column, written on this specific route, consumed
  destructively here.
- Proposing the `agent-ticket-<id>` pin without saying what it bounds (which
  pipelines a poisoned id can otherwise reach) is a remediation copied from the
  convention doc, not a finding — score it as partial credit at most.
- Conversely, a review that reaches F1 entirely from the code, never citing either
  document, loses nothing. The "cites the precedent" bullet under F1 is a bonus
  signal of thoroughness, never a requirement.

## MUST — F1 (cross-tenant destructive archival)

Credit F1 only if the review reaches all three legs. Naming any one leg alone is
partial credit, not a find.

1. **The id is caller-writable.** It identifies that
   `agent_tickets.pipeline_run_id` is supplied by the client — via
   `PUT /api/v1/agent/tickets/:ticket_id/state` (`agent/api/tickets/handler.go`
   `TransitionTicket` → `TransitionMeta.PipelineRunID`) and written verbatim by
   `atc/db/agent_tickets_factory.go` — and is not validated against the ticket.
2. **The linkage is unconstrained.** It identifies `terminalTicketLinkage()` in
   `atc/db/pipeline_run_factory.go` as joining
   `agent_tickets.pipeline_run_id → pipeline_runs.template_pipeline_id` with
   nothing tying the selected pipeline back to the ticket that named it.
3. **The consumer is destructive and unattended.** It connects that selection to
   `run.Archive()` / `pipeline.Archive()` in `atc/runlifecycle/lifecycler.go`, on a
   10s loop, with no human in the path — i.e. it says the consequence is *another
   principal's pipeline gets archived*, not merely "unvalidated input".

**Strongly expected (not required for credit, but a good review says it):**
- The archival is **re-applied every tick**, so an owner un-archiving the victim
  loses again within 10s. Permanence is what makes this major rather than
  moderate.
- The **precedent is in the same file**: `RunBelongsToPipeline` and
  `TicketBelongsToRun` exist for exactly this hazard and say so in their doc
  comments. A review that cites them has demonstrably read the neighborhood.

**Acceptable remediations** (any one — do not require the exact reference fix):
pinning the linkage to the ticket's own `agent-ticket-<id>` pipeline on the
default team; validating the linkage at write time on the transition route; making
`pipeline_run_id` dispatch-only and rejecting client-supplied values. A remediation
that only logs, alerts, or makes archiving reversible **does not** satisfy F1 —
the blast radius must be bounded.

**Does not count as F1:** "unvalidated user input" as a generic lint; "add a
foreign key"; "this needs an authz check" with no statement of what the attacker
gets. The finding is only real once the victim is identified.

## MUST — F2 (build-switch cost staleness)

Credit F2 only if the review reaches both legs.

1. **The guard is unreachable on an in-app build switch.** It identifies
   `handleBuildFetched`'s
   `if not model.hasLoadedYet || build.id /= model.id` in
   `web/elm/src/Build/Build.elm` and explains that `model.id` is **already** the
   target build by the time the callback runs — because `Header.changeToBuild`
   (`web/elm/src/Build/Header/Header.elm`, untouched by the diff) stamps
   `model.id` from the fetched history before build B's `BuildFetched` arrives.
   Saying only "the guard looks wrong" without the ordering is partial credit.
2. **The stale state is rendered.** It states the user-visible consequence:
   `model.agentRunMetrics` is never cleared on a build switch (`changeToBuild`
   resets `prep`/`output`/`autoScroll`/`highlight` only), so build B's page shows
   build A's "agent spend" chip — a dollar figure attributed to the wrong build,
   silently, until a full page reload.

**Acceptable remediations:** a fetched-build-id guard on the model; clearing the
agent state in `changeToBuild` plus a guard that then fires; anything that both
refetches on switch **and** stops rendering the previous build's rows. A fix that
removes the guard outright is **not** acceptable — the guard exists to stop the
1s pending-poll from re-requesting continuously, and the review should say so if
it proposes touching it.

**Does not count as F2:** "the reviews fetch has the same guard" alone (true, and
worth saying — but the *money on the page* is the reason this is major);
"add a loading state"; "the endpoint should return the build id" without
identifying why the current data is wrong.

## SHOULD — the non-required findings

Award bonus credit, in this order of impressiveness:

- **F3 (template hold-back asymmetry).** The runs pass holds back
  still-`running` runs; the templates pass does not. The reviewed change's own
  spec *asserts the asymmetry as correct* ("the template itself never runs builds,
  so it archives right away"), so finding it requires reading the tests
  adversarially against the commit message rather than trusting them. This is the
  single best signal in the case that the review is not just diff-skimming.
- **F2b (late metrics response).** `BuildAgentMetricsFetched` assigns rows
  unconditionally though each row carries its own `buildId`; an in-flight response
  for the previous build lands on the new one.
- **F2c (live-watched build never refreshes spend).** Metrics are ingested as
  steps complete, so a build loaded while running keeps its page-load snapshot
  after it finishes.

## MUST NOT — precision traps

Each of these asserted as a **major** is a precision miss. Raised as a nit or an
explicit "I checked this and it is fine" is good, not bad.

1. **`to_regclass` probe.** Calling it a TOCTOU race or a per-tick cost problem.
   It is deliberate: Postgres resolves relations at parse time even in dead
   branches, so the guard cannot live in Go. Losing the race wastes one tick.
2. **"Archive on the transition instead of a reconciler."** The commit argues the
   reconciler choice and is right: it catches every `Transition` writer, survives a
   restart between transition and archive, and cannot block a transition. Terminal
   states have no outgoing edges, so no requeue race.
3. **"`TerminalStates()` duplicates the transition map."** The change *removes*
   the duplicate switch in `ValidState` and routes it through `IsTerminal`.
4. **Float summation of USD costs client-side.** Immaterial at cents scale for a
   provenance chip.

Also penalize: rating a style/naming/comment observation `major`; producing a long
list where the two real defects are buried among low-value items; asserting a
defect in `web/public/elm.js` (excluded from the diff by design).

## Unmatched findings

The oracle is a **strong recall signal and a weak precision signal**. This is a
28-minute human/agent review of a fresh change; it was not exhaustive. If the
review raises something not in `expected_findings.yaml`:

- Verify it against the pre-state tree before scoring it either way.
- If it is real and anchored, count it as a **positive**, not a false positive,
  and record it in `notes.md` so the oracle can be extended.
- If it is unfalsifiable ("this might race"), score it as noise.

## Overall bands

- **Excellent** — F1 and F2 both found with all legs, correctly rated major,
  anchored; at least one of F3/F2b/F2c; no `non_finding` asserted as major.
- **Good** — F1 and F2 both found, mechanisms mostly right, minor anchoring gaps.
- **Partial** — exactly one of F1/F2, or both named without their mechanisms
  (e.g. "unvalidated input" + "guard looks suspicious").
- **Fail** — neither required finding, *or* a `non_finding` promoted to major
  while both required findings are missed. A confident "this looks good, ship it"
  is the worst outcome: the change as written lets one team permanently archive
  another team's pipelines.
