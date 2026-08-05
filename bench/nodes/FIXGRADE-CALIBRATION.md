# fixgrade calibration — every reference.diff through the normalized grader

2026-08-04. `bench/harness/cmd/fixgrade` was rewired for the normalized
`withheld_tests: [{source, destination}]` shape (T9/T10). This run feeds each
case's own `ground_truth/reference.diff` — the change the humans actually
merged — back through fixgrade to confirm the grader still recognizes a
known-good fix as `pass`. A grader that fails its own reference is a grading
defect, not a fix to make by loosening the grader; see below for the one case
that did not pass on the first try, and why it isn't that.

Build:

```bash
cd bench/harness && go build -o /tmp/fixgrade ./cmd/fixgrade
```

Go toolchain: `go1.25.6 darwin/arm64`. PostgreSQL was running but no case in
this workflow needs it (`small-fix` mechanical legs are hermetic Go/Elm test
runs). `elm` / `elm-test` at `/opt/homebrew/bin/`.

## Results

| case | verdict | red exit | keep leg(s) | green exit | blocker |
|---|---|---|---|---|---|
| fix-jb-001 | **pass** | 1 (panic) | keep: 0 | 0 | none |
| fix-jb-002 | **pass** | 1 | keep: 0 | 0 | none |
| fix-jb-003 | **pass** | 1 | keep-1: 0, keep-2: 0 | 0 | none |
| fix-jb-004 | **pass** | 1 | keep: 0 (fallback leg 2 correctly skipped, `role=fallback-only`) | 0 | none |
| fix-jb-005 | **pass** | 1 | keep-1: 0, keep-2: 0 | 0 | none |
| fix-jb-006 | **pass** | 1 | keep-1: 0, keep-2: 0 | 0 | none |
| fix-jb-007 | **pass** | 2 (elm-test) | keep: 0 | 0 | none |
| fix-cc-001 | **pass** | 1 | keep-1..4: 0, 0, 0, 0 (incl. `go build ./atc/... ./vars/... ./fly/...`) | 0 | none — see note below |
| fix-ld-001 | **pass** | 1 | keep-1: 0, keep-2: 0, keep-3: 0 (`go test -race ./...`) | 0 | none |
| fix-ld-002 | **fail** | 1 | keep-1: 1, keep-2: 1, keep-3: 1 (all build-failed) | 1 (build-failed) | case-data: `reference.diff` alone does not compile at pre-state (see diagnosis) |
| neg-jb-001 | refused (by design) | — | — | — | `pre_state.repository.materialize` directive; fixgrade exits 2 before any leg |
| neg-jb-002 | refused (by design) | — | — | — | same |
| neg-jb-004 | refused (by design) | — | — | — | same (also ships no `reference.diff` — correct outcome is zero commits) |
| neg-ld-001 | refused (by design) | — | — | — | same |

9 of 10 in-scope cases pass outright. The tenth (`fix-ld-002`) is a case-data
gap in the shipped `reference.diff`, not a grader defect — confirmed by
reconstructing the full merged change and re-running (below). All four
negative cases correctly refuse before running any leg, exactly as designed;
they remain out of scope for this pass.

## fix-ld-002 diagnosis: case-data problem, not a grading defect

**Verdict without intervention: `fail`.** All three `pass_to_pass` legs and
the `green` leg fail to build:

```
internal/eos/stage_internal_test.go:24:16: assignment mismatch: 2 variables but parseChannelList returns 3 values
internal/eos/stage_internal_test.go:36:15: assignment mismatch: 2 variables but parseChannelList returns 3 values
```

`ground_truth/reference.diff` changes `parseChannelList`'s signature from
`([]int, error)` to `([]int, bool, error)` in `internal/eos/stage.go`, but it
does not touch `internal/eos/stage_internal_test.go`, the pre-existing
(non-withheld) internal test that calls the old two-value form. The case's
own grading comments say exactly this:

> reference.diff alone does not compile at pre_state — it changes
> parseChannelList's arity, and the pre-existing
> internal/eos/stage_internal_test.go calls it with the old two-value
> signature ... Reproducing the reference state (validation only, NOT part of
> grading): apply BOTH ground_truth/reference.diff AND
> ground_truth/withheld_tests/human_stage_internal_test.diff.

That companion diff exists (`ground_truth/withheld_tests/human_stage_internal_test.diff`)
and is exactly the missing piece — it updates
`stage_internal_test.go` to the new three-value signature. But nothing in
`case.yaml`'s structured `grading` block declares it as part of any leg; it is
mentioned only in prose, and the run recipe in this task
(`--patch ground_truth/reference.diff`) has no way to pick it up. **This is
the same gap as the caveat comment describes — production diff and
compile-compatibility diff were split into two files during curation, and
only the split-off production file is what a literal `--patch` run sees.**

**Confirmation the fix itself is correct: concatenating the two diffs and
re-running produces `pass` cleanly.**

```bash
cat bench/corpus/fix-ld-002/ground_truth/reference.diff \
    bench/corpus/fix-ld-002/ground_truth/withheld_tests/human_stage_internal_test.diff \
    > /tmp/fix-ld-002-combined.diff
/tmp/fixgrade --corpus bench/corpus --case fix-ld-002 \
  --patch /tmp/fix-ld-002-combined.diff \
  --source-repo /Users/tdmtrader/LightingDesign --json
```

Result: `verdict: pass`, red=1, keep-1/2/3=0, green=0 — every leg goes the
right way once the compile-compatibility diff rides along.

**Conclusion:** this is a case-data problem in `fix-ld-002`, not a fixgrade
defect and not something to fix by loosening the grader. `reference.diff` as
currently shipped is not, on its own, the full merged change (it omits a
signature-following edit to a pre-existing test file that the terminal commit
also contains). No file under `bench/corpus/` or `bench/harness/` was
modified to reach this conclusion; the combined patch was written only to
`/tmp` for diagnosis. Fixing it properly is a corpus-curation change (either
fold `human_stage_internal_test.diff` into `reference.diff`, since it is a
same-commit companion edit and not itself a withheld artifact, or teach
`case.yaml`/fixgrade a declared way to apply a compile-compatibility diff
before `pass_to_pass`/`green` legs) — out of scope for this measurement task.

## fix-cc-001: not environment-blocked, calibrates cleanly

The task brief flagged this as a plausible environment blocker (upstream
Concourse history, "corpus validation status is already only partial", a
disk-space failure recorded in the case's own validation notes). On this
machine, with `go1.25.6 darwin/arm64` (matching the toolchain the case
validated against) and 23–25 GiB free, it ran end to end with no blocker:

- `red` leg: `go test ./atc/exec/ -count=1` fails exactly as the case
  predicts — **471 Passed | 3 Failed**, the three named specs (`should log
  'no changes to apply'`, `should send a set pipeline changed event`, `should
  update the job and build id`), matching `case.yaml`'s SHAPE NOTE verbatim.
- `keep-4`, the `go build ./atc/... ./vars/... ./fly/...` leg the case's own
  validation notes recorded as `ENOSPC`-blocked previously, passed cleanly
  here (34.2s) with no network errors surfaced in output — the module cache
  was already warm.
- `green` leg (withheld spec restored over the reference fix) passes at exit 0.

Verdict: `pass`. The corpus's own `validation.status: partial` for this case
remains accurate as a general statement (it documents a specific historical
disk-space failure on a different run), but it is not a blocker on this
machine, at this time, with this environment.

## fix-jb-001's grading-prose caveat: not triggered, recorded for completeness

`fix-jb-001` carries the documented caveat that a fix whose only failing
assertion is the message-wording substring `"panicked"` should still be
scored a pass (task.md leaves the exact wording open; the withheld spec pins
it). **This caveat was not needed to reach `pass`** — the reference fix uses
that exact wording, so the green leg passed outright with no failing
assertions at all. Recorded here because the task instructions asked to
flag when a case's prose changes how its result should be read; in this run,
it didn't need to.

## Negative cases: refused by design, out of scope

All four negative cases (`neg-jb-001`, `neg-jb-002`, `neg-jb-004`,
`neg-ld-001`) pin an explicit `pre_state.repository.materialize:` directive
(a `git clone` + detach + reflog-expire + gc sequence, or a `git archive`
extraction) whose purpose is to withhold history a plain `git worktree add`
checkout would otherwise leave reachable (e.g. `neg-jb-001`'s answer key
sitting three commits downstream on a branch a plain checkout would not
prune). fixgrade refuses before running any leg:

```
fixgrade: case neg-jb-001 pins an explicit repository materialize directive: a plain
checkout would expose history this case deliberately withholds, and grading it
correctly requires running that materialize procedure, which this command does not
implement.
```

Confirmed with a direct run (`neg-jb-001`, exit code 2, no worktrees created).
This is a known, intentional limitation of fixgrade as documented in its own
package comment (`main.go` lines 40–45) — plain-checkout-only, so it fails
closed on a directive it cannot honor rather than guessing. `neg-jb-004`
additionally ships no `reference.diff` at all (its
`ground_truth.outcome: no-change-correct` — the correct answer is zero
commits), so it could not have been fed to `--patch` regardless. Not
attempted further, per the task's scope.

## A minor fixgrade hygiene defect found along the way (not fixed, reported only)

Every successful `fixgrade` run leaves its own scratch git-worktree
*administrative entries* (`.git/worktrees/<name>` in the source repo)
registered until a **later** invocation's `git worktree prune` — or a manual
one — cleans them up. Observed directly: after several sequential runs
against `/Users/tdmtrader/concourse/concourse`, `git worktree list` showed
entries marked `prunable` for already-completed runs, and (from one run that
was killed by an outer 2-minute timeout mid-flight) a pair of worktrees that
were never even marked prunable because their directories were still fully
on disk.

Root cause, in `bench/harness/cmd/fixgrade/main.go`:

```go
168  if workDir == "" {
169      workDir, err = os.MkdirTemp("", "fixgrade-"+caseID+"-")
...
173      if !keepWork {
174          defer os.RemoveAll(workDir)       // registered first
175      }
176  } else if err := os.MkdirAll(workDir, 0o755); err != nil {
177      return nil, err
178  }
...
183  defer func() { _, _ = gitOutput(sourceRepo, "worktree", "prune") }()  // registered second
```

Go defers run LIFO, so on a normal return the **prune runs before
RemoveAll**: `git worktree prune` executes while the worktree directories
still physically exist (nothing looks missing, so it prunes nothing for this
run's own worktrees), and only afterward does `RemoveAll` delete them out
from under git's bookkeeping — leaving `.git/worktrees/<name>` orphaned until
some *subsequent* invocation's prune call (which by then finds the previous
run's directory genuinely missing) or a manual `git worktree prune` catches
up. This does not affect any verdict in this report — the reported legs all
ran inside the still-present tree, correctly — but it means unattended /
scripted fixgrade runs quietly accumulate registered worktrees in whatever
repo `--source-repo` points at. Swapping the two defers' registration order
(prune after RemoveAll, i.e. register RemoveAll last) would fix it. Not
changed here, per this task's "measure and record, do not modify
bench/harness" constraint — flagged for a follow-up task.

Both repos were confirmed clean of leftover worktrees at the end of this
pass (`git worktree list` in both `/Users/tdmtrader/concourse/concourse` and
`/Users/tdmtrader/LightingDesign` shows only the pre-existing, unrelated
worktrees that predate this task).

## Overall read

- The T9/T10 normalization holds up: every case's `withheld_tests:
  [{source, destination}]` declarations resolved correctly, `self_restoring`
  legs (fix-jb-004's primary leg) worked, `role: fallback-only` legs were
  correctly skipped (fix-jb-004's leg 2), and `destructive` legs were
  correctly skipped. No legacy `withheld_test_paths` spelling was
  encountered in any of the ten cases.
- `moveAsideAgentTests` was exercised for real (not just synthetically) by
  two cases whose own `reference.diff` touches a pre-existing test file in
  the same package as the withheld spec (`fix-jb-004`:
  `agent/schema/event_io_test.go`; `fix-jb-006`:
  `atc/configvalidate/validate_test.go`) — both moved aside cleanly and the
  green leg still passed.
- One real corpus defect found (`fix-ld-002`'s incomplete `reference.diff`)
  and one real (minor, non-verdict-affecting) fixgrade hygiene defect found
  (worktree-prune defer ordering). Neither was fixed here, per scope.
