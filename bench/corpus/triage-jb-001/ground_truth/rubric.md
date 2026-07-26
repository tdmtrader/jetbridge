# Rubric — triage-jb-001

The deliverable is a **routing document**, not a patch. Grade what the submission
decided and what it argued from, against `answer.md`. Two components:

- **Part A — routing accuracy (60 pts).** Mechanical per-finding scoring against
  the recorded classes.
- **Part B — behavioural checklist (40 pts).** Judged; this is where a good triage
  separates from a well-formatted list.

A submission may reach a *different* routing than the record and still score well
on Part B, but Part A is scored against the record. Where the record is known to
be wrong, that is called out explicitly below and the deviation is rewarded.

**Where to find the submission.** The task asks for a single new file `TRIAGE.md`
at the root of the checkout. Grade that file. If it is absent, grade whatever
routing document the submission produced (chat output included) — content is the
signal — and record "delivery instruction not followed" in the report rather than
deducting from Part A or Part B.

**How to weigh evidence — reasoning, not quotation.** Some of the conclusions this
triage has to reach are already written down in the exposed tree. Most directly,
`docs/superpowers/plans/agentic-platform/remainders/2026-07-17-delivery-outcomes.md:105`
says "no Elm toolchain in gates — do NOT dispatch this slice to the loop", restated
in `remainders/2026-07-17-platform-mcp-hitl.md`. Those documents are deliberately
exposed (they are a standing repo convention that predates the audit, and B3
presumes constraints are findable), but they mean a submission can *retrieve* the
case's central discrimination instead of deriving it. So: credit the causal chain —
what in the tree makes the rule true, and what follows from it for these findings —
not the citation. A submission that quotes a rule but cannot say what makes it true,
or that cites it without applying it to the presentation block, has retrieved and
not reasoned; score it as such wherever it occurs, and explicitly in B1 below.

---

## Part A — routing accuracy (60 pts)

Score each of the 34 findings. Ignore ordering, formatting, and whatever IDs the
submission chose; match on the finding.

**Scoring per finding:** exact class match = 1.0. Near-miss (see the adjacency
table) = 0.5. Anything else = 0. Unrouted finding = 0. Total × 60/34.

If the submission routes a finding to two classes, score the one it marks primary.
If it marks none primary, score the class that carries the substantive work
(the code change, not the copy tweak).

### Recorded classes

`ops` (6): F-01, F-29, F-30, F-31, F-32, F-34
`loop` (4): F-02, F-03, F-27, F-28
`wave` (16): F-06, F-07, F-08, F-09, F-11, F-12, F-14, F-15, F-19, F-20, F-21,
F-22, F-23, F-24, F-25, F-33
`plan` (8): F-04, F-05, F-10, F-13, F-16, F-17, F-18, F-26
`decision`: F-26 carries one, as the second class alongside `plan`

### Near-misses worth 0.5

| Finding | Recorded | Also accept at 0.5 | Why |
|---|---|---|---|
| F-01 | `ops` | `ops`+`loop` in either order scores **1.0** | The pair is the better answer; the record has it. `loop` alone = 0 — no code change can fix a stale deployed image. |
| F-04 | `plan` | `loop` | Defensible only if the submission also routes F-03 to `loop` and argues the viewer is small once events exist. Otherwise 0. |
| F-06 | `wave` | `loop` | Only if the submission scopes the *investigate* half (does dispatch need to create the uninstanced parent at all) as the server-side piece; the render half is still Elm. |
| F-17 | `plan` | `wave` | The endpoint exists, so "Elm-only, put it in the wave" is a real reading. |
| F-26 | `plan`+`decision` | `plan` alone or `decision` alone | Both halves named = 1.0. |
| F-33 | `wave` | `loop` | Only if the submission has *first* established that the loop cannot do Elm and then argues this one is server-side; otherwise it is self-contradictory and scores 0. |
| F-34 | `ops` | `plan` | Folding it into the workflow-lifecycle track instead of describing it now. |

### Hard zeros — these are what the case exists to detect

- **Any Elm/presentation finding routed to `loop`** without the submission having
  established an Elm capability first (F-07, F-08, F-09, F-11, F-12, F-14, F-15,
  F-19–F-25). Each scores 0. A submission that routes most of the presentation
  block to `loop` has failed the case's central discrimination regardless of its
  Part B score, and the grader should say so explicitly.
- **F-01 routed to anything but `ops`** (alone or paired). Scores 0.
- **Any of F-29/F-30/F-31/F-32 routed to `loop` or `wave`.** These are queue
  dispositions on a live system; there is no code in them.

### Distribution check (not scored, but report it)

Recorded distribution is wave 16 / plan 8 / ops 6 / loop 4 / decision 1. Report
the submission's distribution alongside. A near-uniform spread across five classes
is a tell that the classes were applied as labels rather than as capability calls.

---

## Part B — behavioural checklist (40 pts)

**B1 — The Elm capability argument, from evidence (10 pts).**
The submission must state that agent-dispatchable work cannot include Elm *at this
commit*, and ground it in at least two of these four, **at least one of which must
be a mechanical artifact** (the first three):
`deploy/agent-runner/Dockerfile` (no Elm compiler in the image — note Node *is*
there, which is the distractor); `agent/harvest/gates.go` (gate vocabulary is
exactly `go build` / `go test` / `go vet` — nothing can observe Elm breakage); the
committed `web/public/elm.min.js` bundle (an un-regenerated bundle ships a no-op
change that passes every gate); or, as corroboration only, the rule as already
written in `remainders/2026-07-17-delivery-outcomes.md` /
`remainders/2026-07-17-platform-mcp-hitl.md`.

Scoring: capability argument grounded in two-plus artifacts, with the consequence
drawn for the presentation findings = 10. One artifact plus the written rule = 7.
**The written rule alone — including quoting it from both documents, which are the
same rule restated — caps at 4**: that is retrieval of a convention, not a
capability finding, and it leaves the submission unable to say what would change if
the runner image changed. Asserting "Elm needs a human" with no evidence at all = 3.
No such argument = 0, and Part A's presentation block will have collapsed anyway.

Report which of these the submission used, so the retrieval-vs-derivation split
stays visible across runs — this case's largest known difficulty deflator is that
the rule is quotable in-tree.

**B2 — The root cause behind F-01 (10 pts).**
Connects the deployed image tag `v0.2.167` to the flight-recorder code being newer
than that tag, and concludes the fix is a rebuild + GitOps bump rather than a code
change. Full credit requires the version→commit step actually being *checked*
(e.g. `git tag --contains` on the flight-recorder commit, or observing that
`agent/harvest/flight.go` postdates the tag), not merely guessed. Also required
for full credit: naming F-01's action as a **prerequisite** for F-03/F-04 — until
the runner can write events there is nothing to persist or display.
Names the skew but does not check it = 5. Treats F-01 as a code bug = 0, and Part
A already zeroed it.

**B3 — Coordination constraints, discovered not invented (8 pts).**
Names at least three of the five recorded constraints (`render.go` refusal-chain
chokepoint; claim a migration number in the registry first; push → settle →
dispatch, never during a self-upgrade; self-contained ticket bodies; regenerate
the committed Elm bundle) **and** shows at least one of them actually shaping a
routing decision. Constraints listed but not used = half. Constraints invented
without a source in the repo = 0 for that constraint.

**B4 — Loop ticket bodies are genuinely self-contained (7 pts).**
For each item routed `loop`, a body that an agent could execute with only the body
and a checkout: wanted behaviour, expected files, out-of-scope, and a
verify-before-you-start step. Full credit requires at least one body to carry an
explicit **stop condition** — a check the agent must perform first and abort on if
it fails (the record's example: verify no CHECK constraint restricts the status
column before adding a value, and stop if one exists). Bodies that read as
one-line summaries = 2.

**B5 — Execution order with real dependencies (5 pts).**
An order in which F-01's ops action comes before the loop dispatches and before
anything depending on event data, and in which the cheapest high-trust structural
item is pulled forward. Arbitrary or purely severity-ordered sequencing = 0.

---

## Bonus (up to 12 pts, can offset losses elsewhere)

**X1 (+6) — Catches the record's error.** Notices that
`atc/db/migration/migrations/1773106060_create_agent_run_metrics.up.sql:11`
declares `CHECK (status IN ('ok','failed','error'))` (re-stated at
`1773106061`), and therefore states that F-02's item **does** need a migration and
a registry slot. The recorded routing asserted the opposite and the live dispatch
stopped on exactly this. Award this even though it disagrees with the record.

**X2 (+3) — Proposes making the skew visible** so F-01 cannot silently recur
(a warning when the running step-image tag is not from the web's version family).

**X3 (+3) — Notices the gap behind F-30**: nothing explains how a ticket reaches
"queued with no workflow", and there is no CLI verb to set the workflow after
creation, so such a ticket strands un-dispatchable. The recorded triage missed
this entirely and it was found the same day during execution.

Also worth noting in the grader's report (no points): proposing an `elm-build`
gate + Elm toolchain in the runner image so future presentation waves become
loop-dispatchable, and a pre-dispatch lint against the refusal vocabulary that
burned F-29's two tickets. Both were in the record as emergent items.

---

## Reporting

Report: whether `TRIAGE.md` was produced at the repo root, Part A score with the
per-finding table, the distribution comparison, Part B item scores with the quoted
evidence for each, which B1 groundings were used (artifacts vs. the written rule),
bonus items, and one sentence on whether the submission's routing would have
*worked* — i.e. whether an operator handed it could have executed it without coming
back with questions.
