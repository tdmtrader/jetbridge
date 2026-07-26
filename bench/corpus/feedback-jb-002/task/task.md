# Review feedback — UX4 Wave A: `elm-build` harvest gate (WF-2) and `fly agent tickets --workflow` (WF-5)

Wave A is merged onto the integration branch. A review pass over its two behavioural
changes turned up two problems that need fixing before this goes any further.

Neither is a crash, and neither is caught by the tests that shipped with the change —
in both cases the code does exactly what its comments say it does, and what the
comments say is wrong. Fix both, with tests that would have caught them.

`change.diff` (alongside this file) is the change under review, scoped to the two
features in question.

---

## Context — what landed

**WF-2, the `elm-build` gate.** The harvest terminal step runs a fixed set of gates
against the in-pod workspace between the clean-tree check and the push-by-sha.
Wave A added a fourth gate name, `elm-build`, whose executor is diff-aware: it reads
the `base..HEAD` changed-path set, decides whether it applies, runs `elm make
--optimize` to prove the source still compiles, and then checks the committed
bundle. It exists to kill the chronic "web/elm edited, `web/public/elm.min.js` not
rebuilt, deployed web serves a stale bundle" failure mode. The runner image gained
an Elm 0.19.1 toolchain in the same wave so agents can actually satisfy it.
Implementation plan:
`docs/superpowers/plans/agentic-platform/2026-07-19-wf2-elm-build-gate.md`.

**WF-5, `--workflow` / `--workflow-version` on `queue` and `dispatch`.** Tickets can
be created without a workflow ("decided at dispatch"), but dispatch rejects an empty
workflow and, before Wave A, no `fly` verb could set one afterwards — the ticket
stranded and the only recovery was a hand-written `PUT /api/v1/agent/tickets/:id`.
Wave A closes that by letting `queue` and `dispatch` assign the workflow first and
then do their own job.

---

## Finding 1 — the gate can demand something a correct rebuild cannot produce
**Severity: major** (blocks otherwise-correct work; the failure is unsatisfiable)

The gate decides it applies as soon as *anything* under `web/elm` is in the pushed
diff, and then requires `web/public/elm.min.js` to be in that same diff.

But not everything under `web/elm` ends up in that bundle. The bundle is what
`npm run build-elm` produces, and that compiles `src/Main.elm`. Consider a ticket
whose whole job is "add an elm-test case for `Views.AgentDiff`": the agent edits only
`web/elm/tests/...`, then dutifully runs the rebuild — and gets a byte-identical
`elm.min.js`. There is nothing to commit, so nothing appears in the diff, so the gate
fails the push and tells the agent to "run `npm run build-elm` and commit the rebuilt
bundle" — advice it has already followed and which can never satisfy this gate. The
same trap catches the other subtrees under `web/elm` that are not part of that
compile graph, and the housekeeping files that sit alongside them.

An unsatisfiable gate failure is worse than no gate: there is no correct action the
author can take to clear it, and the message actively misleads. Make the gate apply
only where a change can genuinely leave the committed bundle stale — and keep it
firing on everything that genuinely can. Do not weaken it for real source changes;
the stale-bundle failure mode is the entire reason it exists, and a gate that stops
catching it has no purpose.

**Secondary, same function.** The doc comment says the gate "DIFF-CHECKS that
`web/public/elm.min.js` was regenerated in the same diff". It does not check that the
bundle was regenerated; it checks that the bundle is *present in the diff*. Those come
apart in at least one reachable case, and Open Decision #1 in the WF-2 plan accepted
that tradeoff deliberately and for good reasons. The tradeoff is fine. The comment
overclaiming it is not — anyone reading this function should be told what it does not
catch, not just what it does.

---

## Finding 2 — a failed dispatch leaves the ticket worse than it found it
**Severity: major**

`fly agent tickets dispatch --id N --workflow X` (and the same flags on `queue`) writes
the workflow assignment first and performs the dispatch/transition second. When the
second step fails, the first is left standing.

Walk it through: ticket #7 already names a perfectly good workflow. The operator
re-dispatches it and fat-fingers `--workflow deploi`. The `PUT` succeeds. The dispatch
`422`s ("workflow definition not found"). `fly` exits non-zero — and #7 now names
`deploi`. A typo in a command that *failed* has destroyed a working assignment, the
error message says nothing about it, and the only way back is the hand-written `PUT`
that this very feature was added to make unnecessary.

A command that fails must not leave the ticket in a worse state than the one it found.
The success path is fine and must keep working exactly as it does now (including the
existing empty-workflow hint on dispatch failure). Be honest in the code about any
prior state you cannot faithfully restore, rather than pretending the rollback is
total.

**Secondary, same flag pair.** `--workflow-version` is documented as
`"Pin a workflow definition version (0 = live)"`. Under go-flags an omitted int flag
and an explicit `--workflow-version=0` both arrive as `0`, and the code treats `<= 0`
as "not supplied" — so the documented value is not reachable by any invocation. The
help text describes a value that does not exist; say what the flag actually does.

---

## Constraints

- No wire-format changes, no new API routes, no migrations. Everything needed is
  already on `concourse.Client`.
- Keep `agent/harvest` free of new external toolchain requirements: the gate's tests
  must stay runnable without an Elm compiler installed (the existing `HARVEST_ELM_CLI`
  seam is there for exactly that).
- Both findings need regression tests. `agent/harvest` uses plain `testing` with real
  git fixtures; `fly/integration` uses Ginkgo against the `ghttp` mock ATC.
- Do not touch `agent/dispatch/render.go` — it is a contended chokepoint across three
  in-flight tracks.
