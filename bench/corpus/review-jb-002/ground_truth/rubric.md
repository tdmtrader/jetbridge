# Rubric — review-jb-002

Behavioral checklist for grading a review report against this change. Score
**intent and reasoning**, not wording or diff similarity. The agent under test
produces findings, not a patch; a proposed remedy is evidence that the finding is
understood, not the thing being graded.

Ground truth: `expected_findings.yaml` (F1 major, F2 minor, plus `non_findings`).
Reference remedy: `reference.diff`.

## 1. Recall — the two findings that were actually raised (primary axis)

### F1 — fail-open dispatcher mode on a settings read error (the major)

Award **full credit** only if the report contains all four elements:

- [ ] **Anchored** at the `modeResolver` error branch in
      `atc/atccmd/command.go` (or at `ResolveEffectiveMode`'s inability to
      express "could not read", which is the same defect seen from the library
      side). An unanchored "error handling could be better" does not count.
- [ ] **Names the fail-open direction**: on a read error the loop can end up
      *dispatching*, i.e. doing the dangerous thing, rather than declining.
- [ ] **Names the boot flag as the mechanism**: the error branch reuses the
      "no row exists" path, so a `--agent-dispatcher-enabled=true` deployment
      resolves a read fault to `active`.
- [ ] **Names the consequence**: an admin's persisted `paused`/`off` — a kill
      switch on a loop that spends budget, mints credentials and pushes
      branches — is silently overridden for the duration of the fault.

**Partial credit** (score as found-but-shallow) if the report flags the error
branch as suspicious or "should fail safe" without connecting it to the boot
flag or to overriding a persisted admin setting.

**No credit** for: generic "add retries / add a metric / log at a higher level"
around the same lines with no fail-open claim; or for flagging the *no-row*
boot-flag seeding (that is deliberate — see `non_findings`).

Severity: the human pass called this MAJOR. A report that finds it but grades it
`low` has under-called a security control failing open — deduct on calibration,
not on recall.

**Credit reasoning from evidence, not citation.** The pre_state tree deliberately
retains `docs/superpowers/plans/agentic-platform/remainders/2026-07-17-dispatcher-budget-reconciler.md`,
a genuine pre-existing house convention arguing that *budget* store errors must
propagate rather than fail open. It is exposed on purpose (it is the repo the real
reviewer worked in; it names neither `agent_settings` nor the mode resolver).
Consequences for grading: quoting that doc, or invoking "the repo's fail-closed
convention", is **not by itself** a match for F1. Full credit still requires the
four elements above, anchored in this change's own code and causal chain. A report
that reaches F1 by analogy from the convention AND then anchors it in the
`modeResolver` error branch has done the intended work and loses nothing for
having read the repo.

- [ ] Does **not** demand redesign of the three-mode model, the singleton row, or
      the admin-only PUT tier to state the finding.

### F2 — fabricated `updated_by` actor (the minor)

- [ ] **Anchored** at `Handler.Set` in `agent/api/dispatcher/handler.go` where an
      empty identity becomes the literal `"admin"`.
- [ ] **States the harm as audit-trail dishonesty**: the value is displayed as
      the actor who changed a security-relevant control, and the fabricated name
      is indistinguishable from a real user.
- [ ] Bonus (not required): notices that the shipped test
      `TestPutMissingUserDefaultsToAdmin` pins the wrong behavior as intended —
      i.e. the change codifies the defect.

**No credit** for merely noting the fallback exists, or for reframing it as a
missing-auth bug (auth is enforced in the wrappa layer; the handler correctly
trusts the request).

## 2. Precision — the honest-report axis

The recorded outcome is **two findings and two lenses explicitly reported
clean**. Treat that as the precision reference, with one caveat: the human pass
is a weak oracle for precision. An unmatched agent finding may still be true, and
must be judged on its own merits before it is counted against the report.

- [ ] Unmatched findings are judged individually. A *demonstrated* extra defect
      (with a test that actually fails at the head ref) is a bonus, not noise.
- [ ] Items from `non_findings` are not scored as recall matches. Raising one is
      not automatically wrong, but raising it at `high`/`critical` is a
      calibration failure.
- [ ] Penalize a report that manufactures several high-severity issues to look
      thorough — especially concurrency/race speculation about the hot read, or
      "the migration should be transactional"-class filler.
- [ ] Reward explicitly recording that an area holds up. The reference outcome
      says so out loud; a report that says "the wrappa tiers and the Elm decoder
      hold up" is doing the job, not padding.

## 3. Process

- [ ] Findings are proven or honestly downgraded. The task asks for a failing Go
      test per proven issue; a concern shipped as a proven issue with no
      supporting test, or with a test that would not actually fail at the head
      ref, is a process failure regardless of whether the concern is real.
- [ ] Anchors are real: file paths exist in the change and line references land
      near the cited code.
- [ ] Scope is respected: no findings about untouched code outside the diff
      unless the change makes it newly reachable.
- [ ] The report distinguishes proven issues from observations rather than
      flattening everything into one list.

**Do not penalize the agent's own evidence.** The proving tests the task asks for
land naturally in `agent/dispatch/mode_test.go` and
`agent/api/dispatcher/handler_test.go` — the same two paths the case-validation
overlay restores from the terminal artifact. Two consequences: (a) never apply
that overlay to a workspace the agent has worked in, or the agent's proof is
overwritten by the reference's and the reference is graded against itself; (b) on
a solved workspace those two packages are *expected* to be red before a fix is
applied — that is the task being carried out, not a regression. See
`case.yaml` → `grading.validation_only`, CAVEAT 2 and CAVEAT 3.

## 4. If the run is asked to fix as well as find

Only applies to a fix-after-review variant; the base case grades findings alone.
Grade such a variant by the behavioral bullets below — **not** by restoring the
ground-truth test files, which compile against one specific remedy and would fail
a correct alternative fix (and would delete the agent's own tests on the way).

- [ ] The read-error path must resolve to a mode that does **not** dispatch.
      `paused` (reference) and `off` both satisfy this; `paused` is better
      because the completion reconciler stays alive, and a report that notices
      that trade-off has understood the mode semantics.
- [ ] Must **not** change the exported signature or behavior of
      `dispatch.ResolveEffectiveMode`, `dispatch.ValidMode`, or the
      `GET/PUT /api/v1/agent/dispatcher` wire shape — the Elm decoder, the fly
      command and the fly integration tests are all pinned to that contract.
- [ ] Must not disable or gate the completion reconciler as a side effect of the
      fix.
- [ ] `updated_by` must remain a nullable string on the wire (the Elm decoder
      reads it via `optionalString`); replacing the fallback with a non-string or
      a required field breaks the UI.
- [ ] The exact function name `EffectiveModeFromRead` is **not** required. Any
      equivalent error-aware resolution is a pass; grade the behavior.
