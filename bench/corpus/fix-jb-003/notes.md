# Curation record — fix-jb-003

## Provenance walk

Backed out of a merged fix commit in this repo's jetbridge-era history.

| Step | SHA | Verified |
|---|---|---|
| Terminal artifact | `a07237787d05da53f7a44fb5eba077e79052faac` | `fix(harvest): StampTrailer must set its own git committer identity`, committed 2026-07-21T19:00:13-07:00 |
| Pre-state (its parent) | `3f4f161e10ce33ae17663e556c285fd347edd1d8` | `git rev-parse a072377^` returns exactly this |
| Reachability | — | terminal is contained in `origin/jetbridge` **and** `origin/main`; pre-state is an ancestor of both. Outcome = `merged`, confirmed, not just "a commit that exists" |
| Feature introduction | `2f9de88dcb` (2026-07-21T06:31:56-07:00) | `feat(harvest): stamp Agent-Ticket commit trailer before delivery` — the code under repair is ~12 hours old at the cut, which is why CI is the first environment to run it |
| `bench/` at pre-state | — | `git ls-tree 3f4f161e -- bench` is empty. The self-hosted-corpus caveat in the schema is satisfied: replaying this case cannot surface the corpus or its answers |

Diff scope: exactly two files, `agent/harvest/trailer.go` (+23) and
`agent/harvest/trailer_test.go` (+47). No unrelated churn to exclude —
`reference.diff` is the whole commit.

### Candidate corrections

The mining pass described the pre-state as "the platform-owned-merge landing
'feat' commit". It is not; `3f4f161e10` is
`chore: untrack the stray merge-runner binary; ignore step-runner builds`. The
SHA was right, the label was wrong. `case.yaml` carries the real subject.

### Empirical verification performed

All probes ran in throwaway repos under the scratchpad — neither project repo
was mutated, and no non-read-only git command touched either one.

1. **Post-state passes.** `go test ./agent/harvest/ -run 'TestStampTrailer'`
   at the current worktree HEAD: 7/7 pass. Confirmed `trailer.go` and
   `trailer_test.go` are byte-identical between `a072377` and HEAD, so this
   genuinely measures the post-state. Full package: `ok ... 10.1s`, no
   Postgres, no cluster.
2. **`--amend` preserves the author.** Seeded a commit authored+committed by
   `concourse-agent[bot]`, then amended with no explicit identity under an
   ambient `~/.gitconfig` of `Thomas Moore`. Result:
   `author=concourse-agent[bot]  committer=Thomas Moore`. This is the load-
   bearing fact behind rubric item 2 and behind the claim that the second
   added spec is a guard rather than a strict fail-to-pass.
3. **The pre-state failure is bidirectional.** From (2), on an identity-having
   machine the pre-state code stamps the *human* as committer, so
   `%cn != "concourse-agent[bot]"` and the new spec fails. Reproduced the
   identity-less path with `git -c user.useConfigOnly=true commit --amend`,
   which emits the `Committer identity unknown` advice block verbatim and
   exits non-zero, so `StampTrailer` returns an error and `t.Fatal` fires.
   Both environments fail, for different reasons. That is what makes the
   fail-to-pass assertion trustworthy.

## Task derivation and leakage analysis

### What the task exposes, and why each is safe

- **The verbatim error string `Committer identity unknown`.** Per curator
  guidance, and quoted verbatim from the fix commit message (authoritative).
  This is what a human triaging build 587725 actually had. It names the
  *symptom*, not the fix location or approach — the agent still has to find
  `StampTrailer`, work out that `--amend` is the only commit-creating call in
  the harvest path, decide *where* the identity should come from, and avoid
  the `--reset-author` trap sitting in that same error text.
- **The pointer to `00-shared-contracts.md` §1.11.** That document is in the
  snapshot at pre-state and defines the human-touch delta as keying on
  author. Pointing at it is a constraint ("don't disturb attribution"), not a
  hint; without it the over-correction guard would be unfair, since nothing
  else in the report suggests attribution is at stake.
- **`deploy/agent-runner/Dockerfile` by name.** Present at pre-state, installs
  `git`, configures no identity. A real report would name the image.
- **`2f9de88dcb` as the feature's landing commit.** Narrows the search to
  recent code, which every real bug report does. Does not indicate the defect.

### What was deliberately withheld

- **The fix commit's body.** It states the mechanism outright ("--amend
  PRESERVES the original author", "the harvest pod never commits — it only
  pushes"). That is the answer; it lives in `ground_truth/answer.md`.
- **Any prose naming a committer identity as the cause.** The task never says
  "set the committer", "configure a git identity", or "GIT_COMMITTER_*". The
  word "committer" appears only inside the quoted machine output.
- **The `runner.go` best-effort behaviour** is *pointed at* ("read the call
  site", "confirm or refute") rather than asserted, so noticing the silent
  production degradation remains work the agent has to do. Rubric §Should
  scores it.

### Pre-state leakage sweep (all negative)

| Grep over `3f4f161e10` | Result |
|---|---|
| `"committer identity"` (case-insensitive, all files) | 0 hits |
| `StampTrailer` outside `agent/harvest/{trailer.go,trailer_test.go,runner.go}` | 0 hits — no doc or plan describes it |
| `GIT_COMMITTER` in `*.md` | 6 hits, all inside **test-helper** snippets in plan docs (`09-harvest-step.md`, `12-delivery-outcomes.md`, `2026-07-19-wf2-elm-build-gate.md`). Same pattern the pre-state test file already uses for its own seed commits. Not a prescription for production code |
| `2026-07-20-platform-owned-merge-design.md` §5 (the trailer's design doc) | Describes *what* the trailer is for and the tree-identical safety argument. Says nothing about identity, environment, or the amend's requirements |

`withheld: []` is therefore correct — nothing at pre-state prescribes the fix.

### The strong precedent question

`agent/merge/merge.go` at pre-state already declares
`BotName = "concourse-agent[bot]"` / `BotEmail = "agent@concourse.invalid"`
for exactly this purpose, and `agent/gitcheck/mirror.go` and
`agent/api/outcomes/types.go` both carry `BotAuthor`. An agent that finds any
of these gets the *value* for free.

This is **not** leakage and was left in place. It is the correct in-repo
convention, it is what a competent engineer would find, and the case would be
worse without it — rubric item 3 exists precisely to require that the agent
find and reuse it rather than invent `agent@example.com`. The discriminating
work is elsewhere: recognising the environment dependence, resisting
`--reset-author`, and writing a spec that fails on a laptop.

## Difficulty and quality

`moderate`. Two files, +70 lines, no cross-cutting change, and the symptom
string points at the right concept. What lifts it above `trivial`: the correct
diagnosis requires reasoning about the *execution environment* rather than the
code; the obvious remediation (git's own `--reset-author` hint) is wrong and
silently breaks an unrelated metric; and the "add a regression test" ask has a
non-obvious correct answer, since the natural test passes on every developer
machine with the fix reverted.

Quality 5. Real merged fix, private post-cutoff repo, mechanically gradable,
fast and hermetic (no Postgres, no cluster — only `git` on PATH), two files,
and a built-in wrong-but-plausible alternative that separates good solutions
from lucky ones.

## Open questions

1. **The CI log excerpt is a faithful reconstruction, not an archived
   artifact.** Build 587725's log was not retained anywhere in the repo
   (`git log --all --grep=587725` finds only the fix commit itself). Provenance
   of each part of the excerpt in `task/task.md`:
   - `Committer identity unknown` — verbatim from the fix commit message.
   - The `*** Please tell me who you are.` advice block — reproduced verbatim
     locally (probe 3 above).
   - The four failing spec names and their `trailer_test.go` line numbers
     (49/70/82/103) — derived from the pre-state file; those are the exact
     `t.Fatal(err)` sites reached through `StampTrailer`.
   - `TestStampTrailerRejectsNonPositiveTicket` passing — correct; it returns
     before the amend.
   - The final `fatal: unable to auto-detect email address (got
     'agent@harvest-2f9c1e.(none)')` line — **reconstructed**. macOS derives an
     identity from gecos and so cannot produce this variant; the locally
     reproducible variant ends `fatal: no email was given and auto-detection is
     disabled`, which is config-driven and would have misdirected the agent
     toward `user.useConfigOnly`. The chosen line is git's standard
     passwd-less-container message. Nothing in the diagnosis turns on it. If a
     later pass wants zero reconstruction, run the pre-state package in a
     Linux container and paste the real output.
2. **`pass_to_pass` is environment-conditional.** In an identity-less
   environment four of the five pre-state specs fail *at pre-state* — that
   failure is the bug report. So `go test ./agent/harvest/` can only serve as a
   before-and-after regression guard on a host that has an ambient git
   identity. Recorded in `case.yaml`; worth a schema-level convention for
   "pass_to_pass valid only under environment E".
3. **Grading needs a file overlay, which the schema cannot express.**
   `agent/harvest/trailer_test.go` exists at pre-state with five specs and the
   ground truth appends two, so `withheld` (a path list) cannot describe
   "withhold these two functions". `ground_truth/grading_tests/trailer_test.go`
   is the full post-state file for the harness to overlay. If sub-file
   withholding becomes common, the schema should grow a `withheld_regions`
   concept.
4. **Second-order risk in the fail-to-pass command.** If the grading
   environment exports `GIT_COMMITTER_NAME=concourse-agent[bot]` into the test
   process, the pre-state code would inherit it and
   `TestStampTrailerSetsItsOwnCommitterIdentity` would *pass* at pre-state,
   destroying the signal. The validation harness should clear `GIT_COMMITTER_*`
   and `GIT_AUTHOR_*` from its own environment before running, and assert a
   non-bot ambient identity is configured.

## Curator pre-check (informational, pre-validation)

_Written while `case.yaml` still carried `validation.status: unvalidated`; the
validation stage below superseded it and `case.yaml` now reads `validated`.
Retained as evidence, not as a verdict: it ran on one host (macOS, ambient
`~/.gitconfig` identity), not in the harness. Heading renamed 2026-07-25 so
that `notes.md#validation` resolves to the real validation section._

### Curator pre-check run (2026-07-25, informational)

Pre-state materialized read-only via `git archive 3f4f161e10 | tar -x` into the
scratchpad — no checkout, no repo mutation — then
`ground_truth/grading_tests/trailer_test.go` copied over
`agent/harvest/trailer_test.go`, then
`go test ./agent/harvest/ -run 'TestStampTrailer' -count=1 -v`:

```
--- PASS: TestStampTrailerAppendsTicketTrailer
--- PASS: TestStampTrailerLeavesTreeIdentical
--- PASS: TestStampTrailerIsIdempotent
--- PASS: TestStampTrailerJoinsAnExistingTrailerBlock
--- PASS: TestStampTrailerRejectsNonPositiveTicket
    trailer_test.go:138: committer = "Thomas Moore"; StampTrailer must set its
                         own identity, not inherit an ambient one
--- FAIL: TestStampTrailerSetsItsOwnCommitterIdentity
--- PASS: TestStampTrailerPreservesTheOriginalAuthor
FAIL	github.com/concourse/concourse/agent/harvest	0.649s
```

Every prediction in `case.yaml` holds: the fail-to-pass spec fails at
pre-state on an identity-having host (the ambient human wins the committer
slot); the over-correction guard passes at pre-state, as documented; all five
pre-state specs pass, so `pass_to_pass` is clean at pre. At post-state (HEAD,
byte-identical to `a072377` for both files) all seven pass in 0.67s.

### Checklist for the validation stage

- [ ] fail-to-pass confirmed at `3f4f161e10` with the ground-truth test file
      overlaid (expect `TestStampTrailerSetsItsOwnCommitterIdentity` FAIL)
- [ ] pass confirmed at `a072377` (expect 7/7 PASS)
- [ ] pass-to-pass confirmed at both SHAs on an identity-having host
- [ ] environment hygiene checked per open question 4 (clear `GIT_COMMITTER_*`
      / `GIT_AUTHOR_*`; assert a non-bot ambient identity is configured)
- [ ] optional: re-run the fail-to-pass leg in a Linux container with no git
      identity, which exercises the other failure mode (`StampTrailer` returns
      an error) and would let open question 1 be closed with a real log

## Validation

- date: 2026-07-25
- validator: mechanical (detached worktrees, read-only git)
- worktrees: pre `3f4f161e10ce33ae17663e556c285fd347edd1d8`, post `a07237787d05da53f7a44fb5eba077e79052faac`
- outcome: **validated** (all four legs behaved exactly as recorded)
- host identity note: this host has NO `~/.gitconfig`, but git still auto-derives an identity from the passwd GECOS field, so it behaves as an identity-HAVING host. The pre-state failure message is verbatim `committer = "Thomas Moore"`, matching the curator's pre-check. No `GIT_CONFIG_GLOBAL` shim was needed.

### PRIMARY fail_to_pass
`git show a07237787d05da53f7a44fb5eba077e79052faac:agent/harvest/trailer_test.go > agent/harvest/trailer_test.go && env -u GIT_COMMITTER_NAME -u GIT_COMMITTER_EMAIL -u GIT_AUTHOR_NAME -u GIT_AUTHOR_EMAIL go test ./agent/harvest/ -run '^TestStampTrailerSetsItsOwnCommitterIdentity$' -count=1`

PRE (FAIL, exit 1):
```
--- FAIL: TestStampTrailerSetsItsOwnCommitterIdentity (0.09s)
    trailer_test.go:138: committer = "Thomas Moore"; StampTrailer must set its own identity, not inherit an ambient one
FAIL	github.com/concourse/concourse/agent/harvest	0.234s
```
POST (PASS, exit 0): `ok  github.com/concourse/concourse/agent/harvest  0.211s`

### OVER-CORRECTION GUARD (`TestStampTrailerPreservesTheOriginalAuthor`)
PRE `ok ... 0.208s` / POST `ok ... 0.209s` — passes at both ends as designed. Confirms it is a `--reset-author` trap for the agent's tree, not a before/after signal.

### pass_to_pass (five pre-existing specs, no overlay)
`env -u ... go test ./agent/harvest/ -run '^TestStampTrailer(AppendsTicketTrailer|LeavesTreeIdentical|IsIdempotent|JoinsAnExistingTrailerBlock|RejectsNonPositiveTicket)$' -count=1`
PRE `ok ... 0.532s` / POST `ok ... 0.504s` (pre tree restored to its own trailer_test.go first; `git status` clean).

### Broad regression guard
`go test ./agent/harvest/ -count=1` — PRE `ok ... 10.407s` / POST `ok ... 10.386s`.

- corrected_cmd: none — all four commands ran verbatim. (Shell note: the `env -u ...` prefix must be typed inline; expanding it from a shell variable fails under zsh.)
- notes: no Postgres, no cluster; needs `git` on PATH. Primary leg ~0.2s, broad leg ~10s.

## Fixup 2026-07-25

Curator-fixup pass over the dual leakage audit (opus `borderline`, sonnet
`fail`). Every audit item is resolved below into one of: fixed, dissolved by
the schema's exposure contract, recalibrated, or declared as a leak channel.

### REAL DEFECTS — fixed

1. **`task/task.md` was leading; rewritten.** Four edits, each removing a
   curator-supplied inference while keeping the report authentic (nothing
   asserted is false, and no clue a real reporter would have had was removed):

   | Before | After | Why |
   |---|---|---|
   | "Expected behaviour §2 … **That is the actual defect here**; a fix that only makes CI green by changing CI is not a fix." | "## Acceptance §2 — passes on a developer machine *for the same reason* it passes in the container … we will not take a change to the `unit` task, `deploy/agent-runner/Dockerfile`, or the test scaffolding" | The old text named the root cause (environment dependence) outright. The new text is a demand, not a diagnosis, and still grounds rubric Must-1 and the automatic-fail on identity-detecting fixes. |
   | "Nothing about how a delivered commit is **attributed** may change as a side effect … §1.11 defines what downstream accounting keys on … **and there should be a test that says so**." | "Harvest feeds delivery-outcome accounting. `00-shared-contracts.md` §1.11 is the contract for what gets recorded about a delivered commit; everything it defines has to come out of a stamped commit exactly as it would today." | The old text pre-empted the `--reset-author` trap and described the ground-truth guard spec. The new text points at the contract; §1.11's own DDL comments (`human_commit_count … non-agent author`) are the evidence, so the inference is now the solver's work. Verified §1.11 says this at pre_state. |
   | "It has to fail on a developer laptop too, not only in CI — a spec that passes locally when the fix is reverted is worthless" | "It got in past a green local run and a review, so a test that would also have been green on those machines before the fix is not coverage." | Same requirement, derived from the report's own narrative instead of handed over as a spec property. Stops describing the graded spec. |
   | "## Why this is worse than a red build … a failure in this path does **not** fail the delivery. It is recorded and swallowed, by design … Confirm or refute." | "## Open question: is this only a CI problem? … read `agent/harvest/runner.go` and work out what a delivery actually looks like when this call fails … State which, with evidence." | The old section asserted the production-impact finding that rubric Should-3 scores. Now genuinely open. |

2. **Over-correction guard reclassified.** `TestStampTrailerPreservesTheOriginalAuthor`
   moved from `grading.fail_to_pass` to `grading.pass_to_pass`. It passes at
   pre_state *and* post_state on the reference (identity-having) host — filing
   it as a fail-to-pass made a correct run look like a missing transition. Its
   real job (fail any `--reset-author` solution) is unchanged and now stated
   as such; the identity-less environment's extra failure mode is recorded as
   a bonus.

3. **Grading collisions written down as `grading.procedure`** (5 steps): judge
   the agent's own tree *before* the overlay (the overlay destroys the test
   file rubric item 7 scores); quarantine-and-record agent-added `_test.go`
   files that collide with overlay helpers rather than scoring a build break
   as a failed solution; clear `GIT_*` from the harness env; and run
   `pass_to_pass` **with** the overlay applied, which closes the spurious-pass
   hole where a solution that deleted a pre_state spec would still be green.
   Mirrored in `ground_truth/rubric.md` ("Grading order (matters)").

4. **`%cn`-only / email ungraded, recorded.** Opus was right that the bot email
   is ambiguous at pre_state (`agent@concourse.invalid` in `agent/merge/merge.go`
   vs `agent@concourse.local` in `agent/gitcheck`, `agent/outcomewatcher`, and
   the harvest test helper). The graded spec already asserts `%cn` only; that
   is now explicit in the `fail_to_pass` note and in rubric item 3, so a judge
   cannot dock a correct solution for picking the other address.

5. **Manifest/notes inconsistency fixed.** `notes.md` had two `## Validation`
   headings (so `validation.notes: notes.md#validation` resolved to the stub)
   and the stub still claimed `case.yaml` says `unvalidated` while it says
   `validated`. First heading renamed to "Curator pre-check (informational,
   pre-validation)" with the stale sentence corrected.

6. **Rubric fairness notes added** where the softening changed what the task
   asks: item 2 (attribution) now records that the pointer to §1.11 is what
   makes it fair; item 7 records that any assertion with the fails-locally
   property counts; Should-1 (attribution test) is demoted to a bonus, since
   the report no longer asks for it; Should-3 re-worded to match the new open
   question. Also added a judge instruction to credit causal reasoning from
   evidence, never doc-quotation.

### DISSOLVED BY CONTRACT — no action

- Sonnet's `fail` rests entirely on `case.yaml` content: `source.terminal`
  quoting the fix commit's subject, `fail_to_pass` naming both test names, the
  note spelling out the `--reset-author` trap, `curation.learnings` narrating
  the mechanism. Per `bench/schema/benchmark-case-v1.md` §"The exposure
  contract", the solver sees exactly *(pre_state − withheld) + task/*;
  `case.yaml`, `notes.md`, `ground_truth/` and the case id/path are
  harness-side and are never exposed. Titles and grading configs may state the
  answer freely. **Nothing renamed, nothing retitled, no metadata scrubbed.**
  The residual obligation is on whoever hand-runs the case: materialize
  `task/` into a neutrally-named directory and hand over nothing else.
- The in-tree docs that partially inform the answer
  (`00-shared-contracts.md` §1.11, `2026-07-20-platform-owned-merge-design.md`
  §5) stay exposed — authentic pre_state history, and §1.11 is the *evidence*
  the trap resolution must be reasoned from, not a statement of the fix.
  Recorded as a deliberate keep in `case.yaml` above `difficulty`.
- `agent/merge`'s `BotName`/`BotEmail` prior art stays exposed for the reasons
  already argued in "The strong precedent question" above. Confirmed it is a
  genuine discriminator, not a giveaway: see the trap probe below.

### DIFFICULTY — recalibrated (reaffirmed)

`moderate`, unchanged. Opus argued "medium, not hard" and the manifest already
said `moderate`, so there was nothing to change; the softening in (1) raises
the work but not a whole band — the log excerpt still names the failing
command and the exact git error, and the fix is ~10 lines in one file. The
reasoning is now recorded inline in `case.yaml`.

### KNOWN LEAK CHANNEL — declared

- `known_leak_channels: [project-auto-memory]`. This machine's project memory
  file `memory/project_platform_owned_merge.md` ("GIT IDENTITY GOTCHA (cost a
  CI failure, build 587725)") states the root cause, the build number, the
  `GIT_COMMITTER_*` remedy **and** the `--amend` preserves-the-author
  resolution of the trap — i.e. the entire answer including the discriminator.
  Memory was not touched; a hand-run of this case on this machine is invalid
  unless project memory, session context, and conversation history are
  suppressed. Nothing in the repo snapshot leaks any of it.

### Re-verification after the rework (2026-07-25)

Pre_state materialized read-only again (`git archive 3f4f161e10 | tar -x` into
the scratchpad; no checkout, no repo mutation), ground-truth test file
overlaid, `GIT_AUTHOR_*`/`GIT_COMMITTER_*` cleared inline:

| Leg | Result |
|---|---|
| `fail_to_pass` primary at pre_state | FAIL — `committer = "Thomas Moore"; StampTrailer must set its own identity` (0.21s) |
| guard at pre_state | `ok … 0.205s` (confirms the pass_to_pass reclassification) |
| five pre_state specs at pre_state | `ok … 0.537s` |
| post-state `trailer.go` (`a072377`) + overlay | 7/7 PASS, `ok … 0.699s` |
| **trap probe (new)**: post-state fix *plus* `--amend --reset-author` | primary spec PASSes, guard FAILs with `author = "concourse-agent[bot]", want it preserved` | 

The trap probe is the important one: it proves empirically that a solution
taking git's own remediation hint still satisfies the primary assertion and is
caught only by the guard — so the discriminating property survived the task
softening, and the guard is load-bearing rather than decorative.

**Solvability re-check after softening.** The chain is intact inside the
snapshot: log excerpt names `git commit --amend --no-verify` and `Committer
identity unknown` → `agent/harvest/trailer.go` `trailerGitRun` sets `cmd.Dir`
and no `cmd.Env` → `agent/merge/merge.go:37-41,159-161` is the in-repo
convention for exactly this (`BotName`/`BotEmail` + per-command `Env`) →
`00-shared-contracts.md` §1.11 (pointed at by the task) defines the human-touch
columns over **non-agent authors**, which is what rules out `--reset-author` →
`agent/harvest/runner.go:143-150` records `facts.TrailerErr` and continues,
which answers the open question. Every link verified present at
`3f4f161e10`. **Honesty re-check**: every factual claim in the rewritten
`task.md` holds — 4-of-5 specs fail in a bare container, `2f9de88dcb` landed
the stamp the same day (06:31 vs the 18:48 cut), harvest runs from
`deploy/agent-runner/Dockerfile`, §1.11 is the accounting contract. The one
reconstructed element remains the CI log excerpt (open question 1 above),
unchanged by this pass.

Residual verdict: **pass**.
