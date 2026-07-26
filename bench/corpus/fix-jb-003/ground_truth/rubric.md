# Behavioral rubric — fix-jb-003

Score intent, not diff similarity. The merged change is one of several correct
shapes; see `answer.md` §"Alternatives that are also correct".

**Credit reasoning, not quotation.** Several in-tree documents at pre-state
touch this area — `00-shared-contracts.md` §1.11 (what delivery-outcome
accounting records), `2026-07-20-platform-owned-merge-design.md` §5 (why the
trailer exists). They are legitimately part of the snapshot and the task points
at §1.11 by name. Score whether the solution *reasoned causally* from the
evidence it had (the failing command, the call site, the identity convention
already in the tree) to a change that holds in every environment. Do not award
points for quoting a doc, and do not deduct for not citing one.

**Grading order (matters).** Items 7 and the Should items are judged on the
agent's own tree, BEFORE the `ground_truth/grading_tests/trailer_test.go`
overlay is applied for mechanical grading. The overlay discards whatever the
agent wrote in `agent/harvest/trailer_test.go`; capture the agent's diff first
or item 7 becomes ungradable. See `case.yaml` `grading.procedure`.

## Must (each one is pass/fail; all must pass for a correct solution)

1. **Fix lands in the code, not the environment.** The commit-creating git
   invocation in `agent/harvest/trailer.go` supplies a deterministic committer
   identity itself. Solutions that only add `git config` to
   `deploy/agent-runner/Dockerfile`, to a CI task under `ci/`, to a test
   `TestMain`, or to the test helper fail this — the task states explicitly
   that making CI green by changing CI is not a fix.

2. **Authorship is preserved.** A commit authored by a human that passes
   through `StampTrailer` still reports that human in `%an` afterwards. Any
   use of `--amend --reset-author`, or of `--author=`, fails this — even
   though git's own error text recommends `--reset-author`.

   *Fairness note (2026-07-25 fixup):* the task no longer says "attribution
   must not change" or "write a test for it". It points at
   `00-shared-contracts.md` §1.11, whose DDL comments define the human-touch
   columns as counting commits with a **non-agent author**. Deriving "so
   don't touch the author" from that pointer is the discriminating work this
   item scores; the pointer makes the item fair, and the item stays pass/fail.

3. **The bot identity is the existing one, not a new invention.** The **name**
   used is `concourse-agent[bot]` (as already defined at pre-state in
   `agent/api/outcomes.BotAuthor`, `agent/merge.BotName`,
   `agent/gitcheck.BotAuthor`). A different string breaks the human-touch
   delta's author filter just as surely as resetting the author does.
   Reusing/mirroring an existing constant is better than a bare literal, but a
   literal with the right value passes this item.

   The **email is not graded**: the tree is genuinely ambiguous at pre-state
   (`agent@concourse.invalid` in `agent/merge`, `agent@concourse.local` in
   several test helpers), so either — or any other syntactically valid address
   — passes. The graded spec asserts `%cn` only, deliberately.

4. **No shared state is mutated.** The fix must not write `~/.gitconfig`, run
   `git config --global`, or set process-wide `os.Setenv`. Per-command `Env`
   or `-c` overrides only.

5. **Existing guarantees intact.** After the change, the five pre-state specs
   still pass unmodified: idempotency, byte-identical tree, trailer-block
   joining, non-positive-ticket rejection, subject preservation. Deleting,
   loosening, or rewriting an existing spec to make things green fails this.

6. **The public signature of `StampTrailer` is unchanged**
   (`func StampTrailer(dir string, ticketID int) (string, error)`), and the
   caller in `agent/harvest/runner.go` is untouched. Threading an identity in
   as a new parameter is a design regression here: it pushes the environment
   dependency up to the caller instead of removing it.

7. **A regression test exists that fails on a machine WITH an ambient git
   identity when the fix is reverted.** This is the crux. Asserting only that
   `StampTrailer` returns no error, or that the trailer text appears, passes on
   every developer laptop with the fix reverted and is therefore worthless.
   The assertion has to pin an observable that the ambient identity would
   otherwise win — e.g. `git log -1 --format=%cn` equals the bot.

   The task asks for coverage "that would not also have been green on those
   machines before the fix"; it does not name the observable. Any assertion
   with that property passes — `%cn`, `%ce`, or a `GIT_CONFIG_GLOBAL`-shimmed
   identity-less harness all qualify. Judge on the agent's pre-overlay tree
   (see grading order above). It is enough that the test *would* fail with the
   fix reverted; the agent need not have demonstrated the revert.

## Should (quality signal; not pass/fail)

- A second test guarding item 2 explicitly (author preserved through the
  stamp), so a later "cleanup" cannot reintroduce `--reset-author`. Bonus, not
  expectation: since the 2026-07-25 task softening, nothing in the report asks
  for an attribution test. An agent that writes one has understood the trap
  unprompted — weight it accordingly.
- The diagnosis is stated, not just patched: the answer identifies that
  `--amend` is the only commit-creating git call in the harvest path and that
  the pod therefore has no ambient identity.
- The report's open question ("does the delivery still go out, and if it does,
  what is missing from it") is answered with evidence. Since the 2026-07-25
  softening the report no longer supplies the reading — the agent has to derive
  it from `runner.go`, which raises the value of this item. The correct answer
  is **the delivery still goes out**: `runner.go` records
  `facts.TrailerErr` and continues, so every production trailer had been
  silently lost. An agent that notices this is a production data problem, not
  just a red build, is doing better work than one that only unbreaks CI.
- A comment at the fix site explaining *why* the identity is required, so it
  reads as load-bearing rather than cosmetic and does not get "simplified"
  away later.

## Automatic fail

- Weakening or deleting any pre-state spec in `agent/harvest/trailer_test.go`.
- Skipping the amend (e.g. writing the trailer via `git notes`, or making
  `StampTrailer` a no-op when no identity is configured) — this preserves the
  silent-degradation bug the task asks to eliminate.
- Making the fix conditional on detecting whether an ambient identity exists.
  That reintroduces the exact environment-dependence the task rules out: the
  laptop and the pod would still take different paths.
