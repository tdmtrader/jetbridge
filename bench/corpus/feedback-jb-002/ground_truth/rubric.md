# Judge rubric — feedback-jb-002

Score the agent's change against these behavioural criteria, **not** against
`reference.diff` line-for-line. The reference is one correct answer; several shapes
are acceptable. Each criterion is pass / partial / fail. F1-* and F2-* are the two
findings; C-* are cross-cutting.

The single most useful discriminator in this case is **C-3 (honesty about residual
limits)** — both halves of the reference fix are as much about writing down what the
code cannot do as about changing what it does. An agent that silently produces the
right path set and the right rollback, but leaves the overclaiming comments in place,
has done roughly half the job the humans did.

## Scoring notes

Read these before scoring; they set what each criterion is worth as *signal*.

**Discriminating vs. dictated.** The review text the solver received names both symptoms
and both secondary asks explicitly — a real reviewer does. So F2-5 and the two C-3
admissions are *requested*, and an agent that merely paraphrases the request back is not
demonstrating much. Award them only for substance: C-3(a) requires the solver to name the
concrete case where diff-presence and regeneration come apart (the task says only that
"at least one reachable case" exists); C-3(b) requires the specific residual limit of
*its own* rollback, correctly identified; F2-5 requires the help text to be corrected
without changing flag semantics. The criteria that actually separate runs are **F1-3,
F1-4, F2-1, F2-2 and C-2** — weight the overall verdict toward those, and say in the
score which criteria carried it.

**Credit reasoning from evidence, not from documents.** `docs/superpowers/plans/agentic-platform/2026-07-19-wf2-elm-build-gate.md`
is in the solver's snapshot on purpose and the task points at its Open Decision #1. That
plan prescribes the *buggy* `web/elm/**` rule, so quoting it is not evidence of anything.
Score the solver on whether it reasoned from the code and the changed-path set to which
inputs can actually leave the committed bundle stale. Conversely, do not penalise a
solver for contradicting the plan — contradicting it is the correct move here.

**Grade C-2 from the preserved copies, not the graded tree.** The mechanical run replaces
`agent/harvest/gates_elm_test.go` and `fly/integration/agent_tickets_test.go` with the
withheld oracle specs, which are the same files a solver would naturally write into. Per
`case.yaml` `grading.overlay_protocol` the runner preserves the solver's versions under
`solution_tests/`; judge C-2 against those. If they were not preserved, record C-2 as
*unscoreable* rather than failing it — a post-overlay tree carries no information about
what the solver wrote.

**Mechanical reds are not automatically judge fails.** See F2-2's note; the same applies
anywhere the solver's shape differs from the reference while the behaviour matches. State
the divergence explicitly so the two signals can be reconciled.

---

## Finding 1 — elm-build gate applicability

**F1-1 (required). The unsatisfiable failure is gone.**
A push whose only `web/elm` changes are under `web/elm/tests/**`, under
`web/elm/benchmarks/**`, or to a top-level `web/elm` dotfile (`.gitignore`,
`.agignore`) must make the gate report `ok` with a "not applicable" detail, without
invoking `elm` at all.
*Fail if:* any such path still requires `web/public/elm.min.js` in the diff, or the
gate reaches the compile step for them.

**F1-2 (required). The guard still fires where it matters.**
A change under `web/elm/src/**` without a regenerated `web/public/elm.min.js` must
still be a `failed` outcome naming the stale-bundle guard. The compile step must still
run for applicable changes, and a non-zero `elm make` must still be `failed` (vs
`error` for a toolchain fault).
*Fail if:* the applicability narrowing was achieved by weakening or removing the
diff-check, or by turning `failed` into a warning.

**F1-3 (required). The dependency manifest is treated as bundle-feeding.**
`web/elm/elm.json` changes the compiled output, so a diff that touches it without
regenerating the bundle must still `fail`.
*Fail if:* the agent narrowed to `web/elm/src/**` alone and let a dependency bump
through. This is the most common wrong answer — it satisfies the stated symptom while
opening a new hole, and it is the reason `TestElmGateAppliesToElmJson` is in the
grading set.

**F1-4 (preferred, not required). Allow-list, not deny-list.**
The applicability rule should be expressed as "these paths feed the bundle" rather
than "these paths are known not to". A deny-list of `tests/` + `benchmarks/` passes
F1-1 only if it also happens to catch dotfiles, and it silently re-breaks the moment
someone adds `web/elm/review/` or `web/elm/docs/`.
*Partial credit:* a deny-list that is complete today.
*Full credit:* an allow-list of bundle-feeding prefixes/paths.

**F1-5 (required). The "not applicable" message tells the truth.**
The detail string emitted on the not-applicable path must describe the narrowed rule,
not the old `web/elm/**` one. An operator reading the flight record must be able to
tell why the gate did not apply.

---

## Finding 2 — compensating rollback on `queue` / `dispatch`

**F2-1 (required). A failed dispatch restores the prior workflow.**
Given a ticket already carrying workflow `deploy`, `dispatch --workflow deploi` where
the dispatch call fails must leave the ticket naming `deploy` again. The same must
hold for `queue` when the transition fails.
*Fail if:* only one of the two commands was fixed, or the rollback is attempted but
the prior value was never captured (e.g. it "restores" to empty).

**F2-2 (required). The prior value is read before the assignment is written.**
Restoring requires knowing what was there. Any read that yields the ticket's current
`workflow_name`/`workflow_version` before the `PUT` is acceptable; inventing a
default, or relying on the caller to pass the old value, is not.
*Note for the judge:* the mechanical `fly/integration` gate pins one shape of this
(a `GET /api/v1/agent/tickets/:id` immediately before the `PUT`). A behaviourally
equivalent read through a different route is a **pass** here even if it reddens that
test — say so explicitly in the score so the mechanical and judge signals can be
reconciled.

**F2-3 (required). The success path is unchanged.**
No-flag invocations must still issue exactly the dispatch/transition call and nothing
else. `--workflow` on a successful dispatch must still print the same assignment line
and must not roll anything back. The existing empty-workflow hint on dispatch failure
("the ticket names no workflow and none was supplied") must survive.
*Fail if:* the rollback fires on the success path, or an extra read is issued when
neither flag was given.

**F2-4 (required). Rollback failure never masks the real error.**
The command's exit status and returned error must be the dispatch/transition failure.
A failed rollback may warn (stderr) but must not replace or swallow the original
error, and must not panic.

**F2-5 (required). `--workflow-version` help text is corrected.**
The `0 = live` claim must go, on both `queue` and `dispatch`, replaced by text that
describes what the flag actually does (omit it to use the live version).
*Fail if:* only one of the two commands was updated, or the agent "fixed" it by making
`0` meaningful — that changes flag semantics, which the task did not ask for and which
the existing `<= 0` handling would have to be reworked around.

---

## Cross-cutting

**C-1 (required). Both findings addressed.**
A change that fixes one and ignores the other is at most half credit regardless of
quality.

**C-2 (required). Regression tests that would have caught the bugs.**
At least one new test per finding, exercising the *defect* rather than the fix's
internals: a non-bundle-feeding `web/elm` path yielding "not applicable", and a failed
dispatch leaving the prior workflow intact. `agent/harvest` tests must not require a
real `elm` binary.
*Fail if:* tests were added only for the happy paths, or only assert on internal
constants/helpers.
*Score this from `solution_tests/`* (the solver's own versions, preserved before the
oracle overlay — see Scoring notes). A solver that restructured or renamed those files is
fine; what matters is that a test exercising each defect exists somewhere in its change.
If the preserved copies are missing, mark C-2 **unscoreable**, not failed.

**C-3 (required). Honesty about what is still not covered.**
Two specific admissions, both explicitly requested by the review:
  (a) the diff-presence check proves the bundle is *in the diff*, not that it
      reproduces the current source — and the multi-commit case where that comes apart
      is named; and
  (b) whatever the rollback cannot faithfully restore is stated at the rollback site.
      (In the reference: a version pin added on top of a ticket that had no pin cannot
      be expressed back through the partial `UpdateRequest`, so it is left in place and
      that is said out loud.)
*Fail if:* the overclaiming comment on `runElmBuildGate` survives unchanged, or the
rollback is documented as total when it is not. An agent that discovers a *different*
residual limit and documents that honestly passes.

**C-4 (required). Scope discipline.**
No wire-format change, no new route, no migration, no edit to
`agent/dispatch/render.go`, no new external toolchain dependency in the harvest tests.
Refactoring the assignment helper's name/signature is fine — it is unexported and has
two callers.

**C-5 (informational). Over-reach.**
Note any additional defect the agent fixed. Wave A had other soft spots (the gate's
paths are hard-wired to one repo; an unimported module under `web/elm/src/**` still
over-triggers). Fixing those is not required and not penalised, but a change that
generalises the gate to per-repo build metadata has left the review's scope and should
be flagged rather than rewarded.
