# fix-ld-001 — curation record

Subject repo: `~/LightingDesign` (private, Go, MCP server driving an ETC Eos lighting
console over OSC/TCP+SLIP). Extracted 2026-07-25.

## Provenance walk

| Role | SHA | Date | Subject |
|---|---|---|---|
| terminal artifact | `7d48580013ad63963dacc8c81e4b6593d217cc22` | 2026-06-20T08:55:51-07:00 | `fix(eos): set_look blacks out read-free (wide-range zero)` |
| pre_state (its parent) | `0eee2d88c3d827e7c0ad30e05d009068e3a5414d` | 2026-06-20T08:41:57-07:00 | `forge(venue-profile): record pending hardware smoke (console offline)` |

Verified: `git rev-parse 7d48580^` == `0eee2d8` (direct parent, no intervening commits).
`7d48580` is reachable from `main`. Diff is exactly two files —
`internal/eos/look.go` (-22/+34) and `internal/eos/look_test.go` (+37) — no companion
docs, no version bump, no CHANGELOG, so there is no same-commit-companion leak to strip
from the ground truth.

How the case was backed out:

1. **Terminal artifact** — a merged one-commit fix with a test added in the same commit.
   Read its message and diff; confirmed it says what the mining pass claimed (the
   `ActiveChannels`-then-zero path is replaced with an unconditional
   `SetChannelLevel(ctx, "1 Thru 1000", 0)`, and the now-dead `channelsToSpec` helper is
   deleted).
2. **Pre-state** — the parent tree. Confirmed coherent: it builds, and the whole suite is
   green under `-race` (`go test -race ./...`, ~12s, all seven packages ok). The buggy
   code is `look.go`'s `live, _, err := c.ActiveChannels(ctx, nil)` followed by
   `if len(live) > 0 { ... SetChannelLevel(channelsToSpec(live), 0) }`.
3. **Trigger reconstruction** — the real trigger was a live hardware smoke, not a written
   ticket, so `task/task.md` is a reconstruction. Its factual content comes from the cgx
   entry committed in **`8d464dda2f1b5cbbbbd52d86085e78956177bf9a`** ("record Nomad smoke
   + blackout fix in cgx/plan"), which is one commit *after* the fix and therefore after
   the cut — see the leakage analysis below for exactly what was taken and what was left.
4. **Ground truth** — `reference.diff` is `git diff 0eee2d8..7d48580 -- internal/eos/look.go`
   (production change only, 65 lines). The test the humans wrote in the same commit is
   split out into `ground_truth/withheld_tests/` so it can be injected at pre_state
   without conflicting with the agent's own tests; see "Harness note" below.
5. **Rubric** — mechanical primary (fail-to-pass), with `ground_truth/rubric.md` as the
   behavioral checklist for judge pairing.

## Why this backs out cleanly

The bug is invisible to the repo's own test suite by construction: the `osctest` fake
answers Select Active synchronously and truthfully, so `ActiveChannels` never returns a
spuriously empty set in-process. Every pre_state test passes. That means the agent gets
**zero free signal** from a red suite and has to reason from the operator symptom to
"this code path must not depend on a read" — which is the skill the case is meant to
measure. It also means `pass_to_pass` is trivially satisfiable at pre_state, which makes
the fail-to-pass transition unambiguous.

## Leakage analysis

**Scrubbed from the trigger.** The commit message and the post-fix cgx entry state the
root cause and the fix in one breath ("`ActiveChannels` … intermittently empty … black
out read-free by zeroing a wide channel range"). `task/task.md` carries only the
*operator-observable* half of that paragraph:

- kept: the repro (prior look `1 Thru 10` @80, `set_look "3 + 4 + 5"` @50, prior look
  stayed up, target added on top), no error returned, not reliably reproducible;
- kept: the separately-observable fact that `read_stage` intermittently reports an empty
  stage on this console even when channels are visibly up (the operator saw this
  directly, and a near-identical observation is already in the pre_state cgx, so it is
  not post-cut information);
- **removed**: that `SetLook` reads the active set at all, that the empty read is *why*
  the blackout was skipped, the words "read-free" / "wide-range zero" / "Chan 1 Thru
  1000", the file name `internal/eos/look.go`, the helper name `channelsToSpec`, and the
  `7d48580` reference.

The judgement call worth flagging for the leakage auditors: including the
"`read_stage` sometimes reports empty" observation shortens the distance to the answer.
It was kept deliberately — without it the case is unsolvable by reasoning (the agent
cannot touch hardware, and the suite is green), and it is a symptom the operator genuinely
had at T. If an auditor considers it a tell, the remedy is to delete that bullet from
`task.md` and downgrade the case rather than to add hints elsewhere. Note that removing it
would change exposed content, so it must be done before results exist against this case.

**Nothing withheld from the pre_state tree** (`withheld: []`). Checked the in-tree forge
track that owns this work, `forge/tracks/venue_profile_20260619/`, at `0eee2d8`:

- `plan.md` describes the *buggy* design as implemented ("`ActiveChannels` → bring live
  channels to 0 (skip on blackout)") and leaves the hardware smoke unchecked. It points
  at the bug, not away from it, but it does not identify or fix it.
- `cgx.md` at `0eee2d8` records the smoke as **not run** (console unreachable) and lists
  the open hardware questions as "does the blackout-then-set transition bump?" — the
  cosmetic question, not this bug. It also contains the pre-existing "per-channel readback
  chased ghosts" note and, notably, advice that points the *wrong* way for this bug
  ("prefer `read_stage`/`ActiveChannels` … over per-channel selects").

So the exposure manifest contains a design doc describing the broken approach, which is
realistic and non-leaking. The post-fix cgx/plan update lives in `8d464dd` (after the
cut) and in `6d72869` (`complete(venue_profile_20260619)`); neither is reachable from the
pinned pre_state ref.

**Other checklist items.** Tests-in-snapshot: the grading test does not exist at
pre_state (added by `7d48580`). Future state: none — no lockfile or dependency change in
the fix; `go.mod`/`go.sum` untouched. Branch contamination: pre_state is pinned to a SHA,
not a branch; the working tree of `~/LightingDesign` is currently on an unrelated branch
(`write-verification`) with uncommitted edits, and was never touched — every read used
`git -C … show/archive`, and all validation ran against `git archive` extractions in the
scratchpad. Memorization: private repo, commits dated June 2026, `memorization_risk: none`.

## Harness note (why the withheld test is a separate file)

In `7d48580` the grading test was appended to the existing `internal/eos/look_test.go`,
which **is present at pre_state**. Injecting it as a patch would either conflict with the
agent's own edits to that file or require a fuzzy apply. It is therefore delivered as a
standalone file in the same external test package (`package eos_test`) —
`ground_truth/withheld_tests/look_readfree_blackout_test.go` — to be copied into
`internal/eos/` after materializing pre_state. Verified gofmt-clean and compiling on its
own. **Do not copy it onto the terminal SHA**: `look_test.go` there already defines
`TestSetLookBlackoutIsReadFree` and the duplicate would be a compile error. To reproduce
"pass at post" without that collision, either run the terminal SHA as-is, or apply
`reference.diff` to pre_state (as the validation below did).

## Open questions

1. **Prescriptive mechanical oracle.** The withheld test asserts the literal wire form
   `Chan 1 Thru <N> At 0#` and its ordering before the target set. An equally read-free
   alternative (enumerated sweep, `At Out` over a wide selection) is a correct fix that
   fails the mechanical check. `rubric.md` names the accepted alternatives and asks the
   judge to report the split rather than average it — but `case.yaml` can only carry one
   `rubric:` value. Should the schema allow `rubric: [mechanical, judge]`?
2. **Is the "empty `read_stage`" bullet a tell?** Deliberate inclusion; flagged above for
   the leakage auditors to rule on.
3. **Reference diff excludes the human's test.** `reference.diff` is production code only.
   If a future harness wants "the change humans actually made", it should take
   `git show 7d48580` rather than this file. Recorded here so the omission isn't read as
   an oversight.
4. **Non-Go-toolchain assumption.** Grading needs Go 1.25+ and nothing else — no
   Postgres, no network, no Docker. This makes fix-ld-001 a useful hermeticity smoke case
   for a runner, but it does mean the runner must have a Go toolchain for `lightingdesign`
   cases at all.

## Validation (extraction-time observations)

Environment: Go 1.25.1 darwin/arm64; pre/post trees materialized with `git archive` into
the session scratchpad (the real working tree was never modified). No database, no
network, no external services. Full package suite runs in ~12s under `-race`.

Recorded observations from extraction-time checks (left for the validation stage to
confirm and to set `validation.status`):

- pre_state (`0eee2d8`) + withheld test →
  `go test ./internal/eos/ -run '^TestSetLookBlackoutIsReadFree$' -count=1` **FAILS** with
  `expected a read-free wide-range blackout (Chan 1 Thru N At 0#); none sent` — a semantic
  failure, not a compile error; the file compiles unchanged against the parent as the
  mining pass claimed.
- pre_state alone → `go test -race ./...` **all green** (7 packages), and
  `go test ./internal/eos/ -run 'TestSetLook'` green for the three pre-existing SetLook
  tests. Confirms pass_to_pass and confirms the suite gives the agent no free signal.
- pre_state + `reference.diff` + withheld test → focused test **PASSES**;
  `go test -race ./...` **all green**; `go vet ./...` clean; `gofmt -l .` clean.
- terminal SHA (`7d48580`) as-is → `go test -race ./...` **all green**.

- [ ] validation stage: re-run the above in the harness and set `validation.status`.

## Validation

Independent validation stage (mechanical validator, separate from extraction).

- **Date:** 2026-07-25
- **Environment:** Go 1.25.1 darwin/arm64. No Postgres, no network, no Docker.
- **Trees:** detached `git worktree` checkouts under the session scratchpad. The repo's own
  working tree (branch `write-verification`, dirty) was never touched; both worktrees were
  removed with `git worktree remove --force` afterwards.
  - pre worktree SHA: `0eee2d88c3d827e7c0ad30e05d009068e3a5414d`
  - post worktree SHA: `7d48580013ad63963dacc8c81e4b6593d217cc22`
- **Outcome: validated** (both cases; no command correction needed).

### Case 1 — fail_to_pass

Command (unchanged from `case.yaml`):

```
go test ./internal/eos/ -run '^TestSetLookBlackoutIsReadFree$' -count=1
```

Setup honored exactly as documented: `ground_truth/withheld_tests/look_readfree_blackout_test.go`
copied into `internal/eos/` **at pre only**. The "do not copy at post" caveat was confirmed
independently — at `7d48580` the test is already defined at `internal/eos/look_test.go:63`,
so copying would be a redeclaration compile error.

pre (`0eee2d8`) + withheld test → **FAIL**, semantic (not a compile error):

```
--- FAIL: TestSetLookBlackoutIsReadFree (0.52s)
    look_readfree_blackout_test.go:49: expected a read-free wide-range blackout (Chan 1 Thru N At 0#); none sent
FAIL	github.com/tdmtrader/lightingdesign/internal/eos	0.645s
```

post (`7d48580`), tree as-is → **PASS**:

```
--- PASS: TestSetLookBlackoutIsReadFree (0.26s)
ok  	github.com/tdmtrader/lightingdesign/internal/eos	0.393s
```

### Case 2 — pass_to_pass / regression guard

Command (unchanged from `case.yaml`):

```
go test -race ./...
```

Run on both trees **as materialized**, before the withheld test was copied in, so the guard
measured the untouched trees. Green at both ends, 7 packages, ~12s each.

pre (`0eee2d8`) → **PASS**:

```
?   	github.com/tdmtrader/lightingdesign/cmd/eosping	[no test files]
ok  	github.com/tdmtrader/lightingdesign/internal/eos	5.022s
ok  	github.com/tdmtrader/lightingdesign/internal/mcp	1.559s
(all 7 packages ok, exit 0)
```

post (`7d48580`) → **PASS**:

```
ok  	github.com/tdmtrader/lightingdesign/internal/eos	4.636s
ok  	github.com/tdmtrader/lightingdesign/internal/mcp	1.279s
(all 7 packages ok, exit 0)
```

Extraction-time claims confirmed: hermetic, Go-toolchain-only, and the pre-state suite
gives the agent no free signal about the bug.

## Fixup 2026-07-25

Curator-fixup pass over the two v0 leakage audits (opus: borderline, sonnet: fail),
run against the schema's new **exposure contract** (solver sees pre_state − withheld +
`task/`; `case.yaml`, `notes.md`, `ground_truth/` and the case id/path are harness-side
and never exposed). Every audit item is resolved below into one of four buckets.
Residual verdict: **pass**.

### 1. Dissolved by contract (no action; nothing renamed or retitled)

- `fail_to_pass` names `TestSetLookBlackoutIsReadFree` (opus, sonnet) — grading configs
  are harness-side; the test name is never visible to the solver, and the file is copied
  in only for the grading run.
- The withheld filename `look_readfree_blackout_test.go` "self-spoils" (opus) — same:
  `ground_truth/` is never exposed, and the header comment inside it exists precisely to
  stop a human from mis-handling it.
- `curation.learnings` quotes the root cause, the exact wire form, the rejected
  alternatives, and a post-fix SHA (sonnet's entire FAIL basis) — `case.yaml` is
  harness-side and may state the answer freely. Learnings were **kept verbatim** (and
  extended); they are the case's teaching value.
- `title:` ("set_look fails to replace the look on a real console") — harness-side, kept.

Sonnet's FAIL therefore dissolves in full: it explicitly found `task.md` and pre_state
clean, and rested entirely on harness-side text.

### 2. Real defects fixed

1. **`task/task.md` — mechanism hint softened.** Opus's one solver-channel item: the
   "other observations" bullet paired the empty-`read_stage` symptom with its mechanism
   ("Eos only streams params for selected, non-zero channels, and confirmation reads have
   chased ghosts before"), handing over much of the analytic leap. Replaced with a pointer
   the operator would plausibly write: "(Feedback flakiness on this rig isn't new — there's
   a note about it in the track cgx.)" The symptom itself is **kept** — without it the case
   is unsolvable by reasoning (the agent cannot touch hardware and the suite is green) —
   and the mechanism is still reachable in-tree at pre_state
   (`forge/tracks/venue_profile_20260619/cgx.md`, "Per-channel readback chased ghosts").
   Nothing was falsified: the trigger still reports only what the operator observed.
   Solvability re-checked: symptom → `SetLook`'s clear path → the `if len(live) > 0` guard
   is intact as the reasoning chain; the deleted sentence was a restatement of an in-tree
   note, not a required premise. Safe to edit now — no results exist against this case
   (corpus v0, sealed by the commit that adds it).
2. **Prescriptive oracle vs. an open task (opus).** The task names no wire form and no
   ceiling, but `fail_to_pass` pins `Chan 1 Thru <N> At 0#`. Confirmed the counter-example
   is real, not hypothetical: at pre_state `internal/eos/levels.go` already exports
   `ReleaseChannels` → `Chan <spec> At Out#` (with its own green test in `adjust_test.go`),
   so the *more* idiomatic read-free fix fails the oracle on a suffix. Flexibility moved
   into `ground_truth/rubric.md` — the "Accepted alternatives" section now names
   `ReleaseChannels`/`At Out#` and the unspecified ceiling `N` as first-class alternatives
   and mandates split (never averaged) reporting — and the caveat is recorded as a comment
   on `grading:` in `case.yaml`. The withheld test was left **verbatim** (it is human
   ground truth and is what the validation stage ran); widening its matcher would have
   invalidated the recorded fail→pass evidence for no grading benefit now that the rubric
   carries the flexibility.
3. **Grading-overlay collision rule added.** The task asks the agent to write its own
   failing test (TDD), so the injected file can in principle clash with it. `case.yaml`
   now states that a redeclaration/compile error on injection is a harness collision — rename
   the *injected* copy and re-run — never a `fail_to_pass` failure, and the agent's test is
   never deleted to make room.
4. **Spurious-fail gate closed.** `pass_to_pass` (`go test ./internal/eos/`,
   `go test -race ./...`) would fail at pre_state if run with the overlay present, since the
   overlay *is* the failing test. `case.yaml` now requires the guards to be evaluated on a
   tree without the overlay (this is what the validation stage actually did; the ordering was
   only implicit before).
5. **Judge instruction: reason, don't quote.** `rubric.md` item 3 now requires the diagnosis
   to be argued from evidence the agent can point at (symptom, the read in the clear path,
   the `len(...) > 0` guard, the fake's truthful Select Active), and explicitly says an agent
   that contradicts the in-tree track docs — which recommend preferring `ActiveChannels` — with
   an evidenced argument should be credited for it, not penalized.
6. **Duplicate `## Validation` heading** in this file disambiguated (the extraction-time
   section is now "Validation (extraction-time observations)"), so `validation.notes:
   notes.md#validation` resolves to the independent validation stage.
7. **Stale header banner.** The `# BORDERLINE: needs human spot-check` comment at the top of
   `case.yaml` was replaced with a resolved-status note pointing at this section.

Manifest date consistency re-checked and left as-is: `information_cut`
(`2026-06-20T08:41:57-07:00`) is the pre_state commit timestamp; `task.md` reports
"2026-06-20", the same day and before the terminal commit at 08:55:51 — internally
consistent, nothing to reframe.

### 3. Difficulty recalibration

**Kept `moderate`.** Neither auditor argued for a different level; both agreed the
diagnosis is a genuine (not trivial) inference and that the repo gives no free signal
(green suite). The softening in fix 1 removes a hint and if anything nudges effort up,
but the chain remains short and fully in one file once the agent looks at `SetLook` —
`hard` would overstate it. Recorded here rather than changing the field.

### 4. Known leak channels

**None declared.** Checked the operator environment on this machine: the project
auto-memory (`~/.claude/projects/-Users-tdmtrader-concourse-concourse/memory/`) mentions
LightingDesign only as a corpus *source* (`project_bench_corpus_v0.md`, `MEMORY.md`
index) — no `set_look`, no blackout, no `ActiveChannels`, no wire form, nothing about
this incident. This case's subject repo is not the machine's own project, which is why
it escapes the channel that affects the jetbridge-harvested cases. `known_leak_channels`
is therefore omitted (the standing README rule still applies: a replay harness must not
mount project memory or session history).

### Priced-deflator decision (in-tree docs)

`withheld: []` **kept**. The pre_state forge track (`plan.md`, `cgx.md`) describes the
*buggy* design as implemented and actively recommends `ActiveChannels`/`read_stage` over
per-channel selects — it points at the bug and away from the fix, so it is authentic
history that raises rather than lowers difficulty. The one deflating sentence it contains
(the readback-ghosts mechanism) is now the only place that mechanism appears, and
`rubric.md` instructs the judge to credit causal reasoning from evidence rather than
doc-quotation, so an agent that merely recites it earns nothing.

### Files touched

- `task/task.md` — one bullet softened (only exposed-content change).
- `ground_truth/rubric.md` — item 3 evidence clause; "Accepted alternatives" rewritten.
- `case.yaml` — header banner, `grading:` caveats (collision rule, overlay ordering,
  prescriptive-oracle caveat), `leakage_audit` curator-fixup entry, `curation.learnings`
  extended. `difficulty`, `withheld`, `rubric`, `validation` unchanged.
- `notes.md` — this section; first `## Validation` heading disambiguated.
- Not touched: `ground_truth/reference.diff`, `ground_truth/withheld_tests/`.
