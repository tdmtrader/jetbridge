# fix-ld-002 — curation record

Subject repo: `~/LightingDesign` (private, single-developer, no remote; Go MCP server
driving an ETC Eos lighting console over OSC/TCP+SLIP). Extracted 2026-07-25.

## Provenance walk

| Role | SHA | Date | Subject |
|---|---|---|---|
| terminal artifact | `5b96519f5cefba11b8f69828e6fc2eaa1931b4ab` | 2026-07-16T09:01:26-07:00 | `fix(eos): tolerate console-truncated active-channel string in read_stage` |
| pre_state (its parent) | `8b17eb9cef630a0f5370963efb0b593459739d1e` | 2026-06-25T15:31:30-07:00 | `docs(verify): implementation plan for read-after-write verification` |

Verified by hand:

1. **Parent relation.** `git rev-parse 5b96519^` == `8b17eb9`. Direct parent, no
   intervening commits.
2. **Terminal artifact says what the mining pass claimed.** Message and diff both read
   as advertised: console-side truncation of `/eos/out/active/chan`, salvage-and-flag
   rather than error. Diff is exactly three files —
   `internal/eos/stage.go` (+35/-9), `internal/eos/stage_internal_test.go` (+47/-2),
   `internal/mcp/stage_tools.go` (+6/-3). No companion docs, no version bump, no
   CHANGELOG, so there is no same-commit-companion leak to strip.
3. **Pre-state is coherent.** Materialized `8b17eb9` with `git archive` (read-only; the
   live worktree is dirty and was never touched). It builds, and the full suite is green:
   `go test ./... -count=1` → all seven packages ok, and `go test -race ./...` clean. The
   buggy code is `parseChannelList` in `internal/eos/stage.go`, whose final `strconv.Atoi`
   on the token `"10.."` produces
   `eos: cannot parse channel "10.." in active-channel string "…"`, which
   `ActiveChannels` returns and `readStageTool` wraps as `read stage: …`.
4. **Trigger reconstruction.** The real trigger was a live operator report during the
   2026-07-16 cue-building session. See leakage analysis for exactly what was taken.
5. **Ground truth.** `reference.diff` is
   `git diff 8b17eb9..5b96519 -- internal/eos/stage.go internal/mcp/stage_tools.go`
   (production change only, 132 lines). The humans' test change is kept separately as
   `ground_truth/withheld_tests/human_stage_internal_test.diff` — never used for grading
   (see "Why the grading test is not the humans' test"), but **required to reproduce the
   reference state**: `reference.diff` alone does not compile at pre_state, because it
   changes `parseChannelList`'s arity and the pre-existing
   `internal/eos/stage_internal_test.go` still calls it with two return values. Verified:
   production diff alone → `assignment mismatch: 2 variables but parseChannelList returns
   3 values` at `stage_internal_test.go:24` and `:36`; both diffs → green.
6. **Rubric.** Mechanical primary (validated, below) paired with
   `ground_truth/rubric.md` for judge scoring of the diagnostic half.

### Caveat on `ground_truth.outcome: merged`

This repo has no remote and no PR flow. `5b96519` is the **tip of the working branch
`write-verification`** and the repo's current `HEAD`; `main` sits 3 commits back at
`66cfdcc` (2026-06-23) and does **not** contain the fix. "Merged" here means *accepted
and landed on the active development line*, which is the strongest signal this repo
produces — it is not the same evidence as an upstream merge. The change is real, tested,
and still the current state of the code; nothing was reverted afterwards (it is HEAD).

### Caveat on `information_cut`

Set to the pre_state commit's committer date, `2026-06-25T15:31:30-07:00`, per the
extraction convention. The real-world operator report came on **2026-07-16** — three weeks
after the last commit, because the repo simply did not move in between. Both readings of T
(last-commit instant vs. report instant) select the same tree, so the exposure manifest is
identical either way, and nothing was committed between the two dates.

**Reconciled in the 2026-07-25 fixup.** The cut stays the pre_state commit instant (the
convention, and the thing the pins actually mean); `task/task.md` is now stamped
`2026-06-25` — "this evening's cue-building session" — so the exposed trigger no longer
asserts a date three weeks after the snapshot it is presented against. This is a
presentation change, not a provenance change: the field report itself, its symptom, its
capture, and the failing look are unaltered, and the provenance table above keeps the true
`2026-07-16` report date. The trigger is the work item that *arrives* at T, so a report
written the evening of the cut day is internally consistent; a report dated after three
weeks of (recorded) silence was not, and an auditor reading only the manifest could not
tell whether the tree had moved.

## Why the grading test is not the humans' test

The humans' tests (`TestParseChannelListTruncated`,
`TestParseChannelListTruncatedDanglingRange`, and the arity update to the two existing
ones) call the **unexported** `parseChannelList`, whose signature the same commit changed
from `([]int, error)` to `([]int, bool, error)`. Dropping them into pre_state therefore:

- fails as a **compile error**, not a behavioral failure — a fail-to-pass transition that
  proves nothing about the environment or the bug; and
- silently makes the reference's internal API shape a grading requirement, punishing a
  correct fix that carries the truncation flag some other way (sentinel error, struct,
  derived in `ActiveChannels`).

So the grading artifact is a purpose-written black-box test,
`ground_truth/withheld_tests/stage_truncated_selection_test.go` (package `eos_test`). It
uses only pre-existing test helpers (`newMockTransport`, `testTarget` from
`internal/eos/eos_test.go`, both present at pre_state) and the pattern already used by
`TestActiveChannelsPartialOnSilentChannel`: a responder goroutine answers
`/eos/key/select active` with a scripted `/eos/out/active/chan` string and answers every
`Chan N#` select with a level echo, so `partial` can only originate from the selection
string and not from a per-channel timeout. It asserts on `eos.Client.ActiveChannels`,
whose signature is unchanged by the fix.

It ships two tests:

- `TestActiveChannelsSalvagesTruncatedSelection` — the field capture; **fail_to_pass**.
- `TestActiveChannelsMidSelectionGarbageStillErrors` — `"1 bogus 3 [0]"` must still
  error; **pass_to_pass**, guarding against the over-broad "skip unparseable tokens" fix.

`pass_to_pass` entries carry a `withheld_test_paths` key that the schema's example does
not show, because this guard also lives in the withheld file. Harmless superset; flagging
it in case the schema wants tightening.

## Leakage analysis

**Scrubbed from the trigger.** The verbatim field report lives in
`cue-notes-20260716-session.md`, an **untracked** file in the working directory (`git
ls-files` returns nothing for it; it appears in no commit on any branch). It is therefore
outside the exposure manifest by construction — but it is also where `task/task.md` came
from, so the split matters:

- Taken: the symptom (`read_stage` is throwing a parse error on busy looks), the
  reliability framing, the impact (hand-transcribing channel numbers into the notes for
  the rest of the night), and the identity of the failing look (cue 6.1).
- Cut: the parenthetical `(truncated active-channel string — flagged separately for a
  fix)`. That phrase names the root cause outright and is exactly the inference the case
  is testing.
- Cut: the commit message's reasoning — "Eos truncates at a fixed display width", "the
  SLIP transport reassembles arbitrarily large frames, so there is no more data to read",
  and the salvage-and-flag prescription.
- Cut: the second observed/inferred truncation form (a dangling final range, `"101-"`).
  It is not in the field capture; requiring it would grade on ground truth the operator
  never had. It is a *credit* item in the rubric, not a gate.

**Deliberately kept.** The pasted error line, including the full 100-character raw string.
Rationale: without console access this is the only reproducible evidence, and a real bug
report from an operator using the MCP tool would contain it. It is honest and it is what
made the fix possible. It does hand over part of the diagnosis — a careful reader can see
`10..` and count 100 characters — so the residual difficulty is the step the string does
*not* answer: whether the missing bytes are recoverable (bigger buffer / retry / re-query)
or gone. `rubric.md` gates on that explicitly, including an automatic-fail for
buffer-widening and retry "fixes".

**Trigger phrasing that could still nudge.** `task/task.md` says an incomplete result must
say so ("what I must not get is a silently short answer"). That is a requirement the
operator genuinely has, and it is the *what*, not the *how* — but it does point at the
existing `partial` field. Judged acceptable: a bug report that omitted it would produce a
worse fix, and the schema already exposes `ReadStageOutput.Partial` at pre_state anyway.

**Tests in the snapshot.** None. `internal/eos/stage_internal_test.go` and
`stage_test.go` at pre_state contain nothing about truncation; the whole suite is green at
pre_state, so the repo gives the agent no free signal.

**In-tree plans.** `git grep -niE 'truncat|ellips|busy look'` over the pre_state tree
returns exactly one hit:
`docs/superpowers/plans/2026-06-17-read-stage.md:54` — "bump if the spike shows
truncated/late feedback", about OSC feedback *timing* during a spike, not about the
active-channel string. Not a leak.

The pre_state commit itself adds a 1112-line unrelated plan,
`docs/superpowers/plans/2026-06-25-write-verification.md`. It is noise, not leakage: it
describes read-after-write verification, and its only relevant line (569) *calls*
`parseChannelList(raw)` with the pre-state two-value signature. If anything it anchors the
agent to the old signature — which is fine, because grading is on the exported seam. Left
exposed (`withheld: []`); an agent that decides to go implement that plan instead has
failed the task on its own merits.

**Memorization.** None. Private repo, no remote, no public mirror; both commits post-date
any plausible training cut.

## Difficulty

`moderate`. Mechanical proxies: 2 production files, 132 diff lines, one unexported
function plus a bool threaded through two call sites and one MCP output field. What
lifts it above `trivial` is that the naive reading ("our parser is too strict, be
lenient") produces a fix that fails the mid-string-garbage guard, and the naive
*diagnosis* ("our buffer is too small") produces a fix that cannot work at all and that
the in-repo fake will happily appear to validate. What keeps it below `hard` is that the
failing input is handed over verbatim and the blast radius is one function.

## Open questions

- The mechanical test asserts exactly 38 salvaged channels. A defensibly-conservative fix
  that also drops the last *complete* group (32 channels) would fail mechanically while
  arguably satisfying the report. Judged acceptable — salvaging everything before the
  fragment is the plain reading, and the rubric's item 2 states it — but it is the kind of
  prescriptiveness that argues (again, after fix-ld-001) for `rubric` being a list so the
  judge can overrule a near-miss.
- A fix that changed the console-facing query strategy (chunked selection) could make the
  scripted responder in the withheld test not fire, failing for a reason unrelated to
  correctness. `rubric.md` puts that approach under automatic-fail (unverifiable without
  hardware), so the mechanical and judge verdicts agree; noting it because the general
  pattern — mock-shaped tests punishing protocol-level rewrites — will recur.
- Not yet decided whether `information_cut` should be the pre-state commit instant or the
  trigger instant when the repo is idle between them. Followed the extraction convention
  (commit instant) and documented the gap; a future schema revision may want both fields.

## Validation (extraction evidence)

Ran during extraction on 2026-07-25 against trees materialized with `git archive` (the
live worktree is dirty and was not touched). Go 1.25.6, darwin/arm64. No database, no
network, no hardware.

| Check | Result |
|---|---|
| pre_state builds, suite green (without the withheld test) | pass — all 7 packages ok |
| `TestActiveChannelsSalvagesTruncatedSelection` at pre_state | **FAIL**, with `eos: cannot parse channel "10.." in active-channel string "…"` — the real bug, not a compile error |
| `TestActiveChannelsSalvagesTruncatedSelection` at terminal | **PASS** |
| `TestActiveChannelsMidSelectionGarbageStillErrors` at pre_state | PASS |
| `TestActiveChannelsMidSelectionGarbageStillErrors` at terminal | PASS |
| `go test ./... -count=1` at pre_state **with** withheld file | only the fail_to_pass test fails; everything else ok |
| `go test ./... -count=1` at terminal with withheld file | all packages ok |
| `go test -race ./... -count=1` at terminal with withheld file | all packages ok (~15s) |
| `git apply --check reference.diff` at pre_state | applies cleanly |
| pre_state + `reference.diff` only | **build failure** — `assignment mismatch: 2 variables but parseChannelList returns 3 values` (pre-existing internal test); expected, see above |
| pre_state + `reference.diff` + `human_stage_internal_test.diff` + withheld file | `internal/eos` and `internal/mcp` ok |

At extraction time `case.yaml` recorded `validation.status: unvalidated` per the protocol —
the numbers above are the extractor's own run, offered as evidence to be confirmed (or
contradicted) by the independent stage. That stage ran and confirmed them, and `case.yaml`
now reads `validation.status: validated`; the authoritative record is the section below,
which is what `validation.notes: notes.md#validation` points at.

## Validation

Independent validation stage (mechanical validator, separate from extraction). Confirms
the extractor's table above.

- **Date:** 2026-07-25
- **Environment:** Go 1.25.1 darwin/arm64. No Postgres, no network, no Docker, no hardware.
- **Trees:** detached `git worktree` checkouts under the session scratchpad. The repo's own
  working tree (branch `write-verification`, dirty) was never touched; both worktrees were
  removed with `git worktree remove --force` afterwards.
  - pre worktree SHA: `8b17eb9cef630a0f5370963efb0b593459739d1e`
  - post worktree SHA: `5b96519f5cefba11b8f69828e6fc2eaa1931b4ab`
- **Outcome: validated** (all three cases; no command corrections needed).

Checked independently before running: neither the pre nor the post tree defines
`TestActiveChannelsSalvagesTruncatedSelection` or `TestActiveChannelsMidSelectionGarbageStillErrors`,
so the unconditional `cp` of the withheld file in both focused commands is safe at both
ends. (This is the opposite of fix-ld-001, where the post tree already carries its withheld
test and copying would be a redeclaration compile error.)

Ordering note for harness authors: the `go test -race ./... -count=1` guard was run on both
trees **before** the withheld file was copied in. With the withheld file present, the pre
tree's full-suite run fails by construction — the fail_to_pass test is in `internal/eos`.
The guard is only meaningful on the tree as materialized.

### Case A — fail_to_pass

```
cp <corpus>/fix-ld-002/ground_truth/withheld_tests/stage_truncated_selection_test.go internal/eos/ \
  && go test ./internal/eos/ -run '^TestActiveChannelsSalvagesTruncatedSelection$' -count=1
```

pre (`8b17eb9`) → **FAIL**, semantic (not a compile error):

```
--- FAIL: TestActiveChannelsSalvagesTruncatedSelection (0.10s)
    stage_truncated_selection_test.go:79: a console-truncated selection must not fail the whole read, got error: eos: cannot parse channel "10.." in active-channel string "2,7-8,10-11,...,80-85,10.."
FAIL	github.com/tdmtrader/lightingdesign/internal/eos	0.236s
```

post (`5b96519`) → **PASS**:

```
--- PASS: TestActiveChannelsSalvagesTruncatedSelection (0.52s)
ok  	github.com/tdmtrader/lightingdesign/internal/eos	1.274s
```

### Case B — pass_to_pass (guard against over-permissive parsing)

```
cp <corpus>/fix-ld-002/ground_truth/withheld_tests/stage_truncated_selection_test.go internal/eos/ \
  && go test ./internal/eos/ -run '^TestActiveChannelsMidSelectionGarbageStillErrors$' -count=1
```

pre (`8b17eb9`) → **PASS** (`ok … 0.228s`); post (`5b96519`) → **PASS**
(`--- PASS: TestActiveChannelsMidSelectionGarbageStillErrors (0.10s)`).

Confirms the fix is not a blanket "ignore anything unparseable" — mid-selection garbage
still errors after the change.

### Case C — pass_to_pass / full regression guard

```
go test -race ./... -count=1
```

Run on both trees as materialized. Green at both ends, 10 packages (3 with no test files),
~14s each.

pre (`8b17eb9`) → **PASS**:

```
ok  	github.com/tdmtrader/lightingdesign/internal/eos	5.498s
ok  	github.com/tdmtrader/lightingdesign/internal/mcp	1.399s
ok  	github.com/tdmtrader/lightingdesign/internal/venue	1.465s
(all packages ok, exit 0)
```

post (`5b96519`) → **PASS**:

```
ok  	github.com/tdmtrader/lightingdesign/internal/eos	5.582s
ok  	github.com/tdmtrader/lightingdesign/internal/mcp	1.296s
ok  	github.com/tdmtrader/lightingdesign/internal/venue	1.468s
(all packages ok, exit 0)
```

## Fixup 2026-07-25

Curator-fixup pass over the dual audit (both auditors: `borderline`). Every audit item is
resolved into one of four buckets below. Residual verdict: **pass**. No exposed content
was invalidated — `pre_state` pins, `withheld: []`, the grading commands, and the withheld
test file are all byte-identical to what was validated.

### Real defects — fixed

1. **`task/task.md` "Expected behavior" was leading** (opus). It pre-stated both the
   diagnosis and the design. Rewritten. Removed, with the reason each phrase had to go:
   - *"If the console genuinely cannot hand over the full picture…"* — presupposes the
     console-side explanation and, worse, the load-bearing inference the case exists to
     test (that the missing bytes are unrecoverable on our side). `rubric.md` calls that
     inference the real subject of the case; the trigger was answering it.
   - *"I would rather be told what it did give me, clearly marked as not the whole story"*
     — prescribes salvage-plus-flag, i.e. required items 2 and 4 handed over as a design.
   - *"a reply that is genuinely malformed (as opposed to whatever is going on above)"* —
     the parenthetical pre-classifies the captured string as *not* garbage, which both
     hands over half the diagnosis and pre-empts the mid-selection-garbage guard.

   Kept, because the operator genuinely has these requirements and removing them would
   falsify the trigger (and make required items ungradeable): the usability demand ("I need
   to see what is live, not a tool error"), the anti-silence demand ("what I must not get
   is a *silently* short answer… the result has to say so" → required item 4), the
   no-regression demand for small looks (item 6), and a general "don't invent a stage state
   out of junk" (item 5, now without the pre-classification). All required rubric items
   remain inferable from the report; none is now stated as an instruction.

   Considered and deliberately kept: the Constraints bullet naming "the existing `partial`
   behaviour when a channel goes silent" — it is a real regression constraint, and
   `ReadStageOutput.Partial` is visible at pre_state anyway (extraction reached the same
   conclusion; re-affirmed rather than re-litigated).

2. **`information_cut` vs. the trigger's stated date** (opus). Reconciled per the fixup
   brief: the cut stays the pre_state commit instant `2026-06-25T15:31:30-07:00`;
   `task/task.md` is re-stamped `2026-06-25, from this evening's cue-building session`.
   Full reasoning in "Caveat on `information_cut`" above. The provenance table still
   records the true report date (2026-07-16) — the change is to the exposed presentation,
   not to the history. Checked for collateral date claims in the exposed trigger: the only
   other temporal assertion is "Nothing else in the read path changed today", which is true
   at the cut (the pre_state commit is a docs-only plan commit).

3. **Grading-overlay collision, previously undocumented.** The task *asks* the agent to
   write a hermetic test, and grading copies a file into the same package — so the overlay
   could clobber the solver's own `stage_truncated_selection_test.go`, or collide on a test
   or helper name. `case.yaml`'s grading block now specifies: copy in under the
   bench-reserved basename `internal/eos/bench_stage_truncated_selection_test.go`; the two
   graded function names and the three helper names are reserved by the case; on a clash,
   rename the *solver's* declaration and record it, never drop the overlay (dropping it
   would grade a spurious pass).

4. **Pass-to-pass ordering was only in notes.** `go test -race ./... -count=1` is only
   meaningful on the tree as materialized — with the overlay present, pre_state fails by
   construction. That constraint is now stated in `case.yaml` where a harness author will
   read it, not just in the validation section here.

5. **Mechanical prescriptiveness had nowhere to be overruled** (the extraction's own open
   question). The exact-38 assertion is stricter than the report: a conservative fix that
   also drops the last complete group is wrong about scope, not about the bug. Flexibility
   moved into `ground_truth/rubric.md` §"Mechanical caveat" (near-miss on salvage scope,
   not a missed diagnosis; the opposite error — inventing a channel from the fragment —
   stays a hard fail), and `case.yaml` carries a one-line caveat pointing at it.

6. **Stale internal inconsistency in this file.** The extraction section claimed
   `case.yaml` "still records `validation.status: unvalidated`"; it records `validated`.
   Corrected, and the extraction heading is now "Validation (extraction evidence)" so the
   `notes.md#validation` anchor resolves to the authoritative independent stage.

### Dissolved by the exposure contract — no action

- **"The title states the diagnosis"** (both auditors; sonnet's only issue, and one of
  opus's four). `case.yaml` is harness-side: the solver sees pre_state minus `withheld`
  plus `task/`, never the manifest, `notes.md`, `ground_truth/`, or the case id/path. The
  schema says titles and grading configs may state the answer freely. **Not retitled** —
  a symptom-level retitle would cost the corpus index its only searchable statement of what
  this case is about and buy nothing. The operative rule is the one the schema already
  states for hand-runs: materialize `task/` into a neutrally-named directory.
- The same dissolution covers the grading block's free use of "truncated"/"salvage" and the
  withheld test filename, which sonnet did not flag but which fall under the same clause.

### Verified, not changed

- **"Confirm the withheld test does not force an API shape"** (opus). Confirmed by reading
  it: `stage_truncated_selection_test.go` is `package eos_test`, imports only
  `internal/eos` and `internal/osc`, and asserts solely on `c.ActiveChannels(...)` —
  `(states, partial, err)`, a signature the reference change does **not** touch. It never
  names `parseChannelList`. The truncation flag may ride a sentinel error, a struct, or be
  derived in `ActiveChannels`; `rubric.md`'s non-requirements say so explicitly.
- **Priced-deflator in-tree docs.** Re-grepped the pre_state tree
  (`git grep -niE 'truncat|ellips|display width|busy look' 8b17eb9`): one hit,
  `docs/superpowers/plans/2026-06-17-read-stage.md:54`, about OSC feedback *timing*. The
  1112-line write-verification plan added by the pre_state commit remains exposed (KEEP —
  authenticity), and `rubric.md` now opens by telling the judge to score causal reasoning
  from the capture and the code, never doc-quotation or vocabulary match.
- **Difficulty stays `moderate`.** Neither auditor argued it differs, and the extraction's
  justification survives the task.md softening — if anything the softening *raises*
  effective difficulty (the design is no longer prescribed), so nothing pushes it toward
  `trivial`, and the verbatim capture plus one-function blast radius still keep it below
  `hard`. Annotated inline in `case.yaml`.
- **No operator-environment leak channel.** Re-grepped the dev machine's project
  auto-memory for `eos`, `active-channel`, `ActiveChannels`, `parseChannelList`,
  `read_stage`, `truncat`, `lighting`: the only hits are the corpus-mining memo naming
  `~/LightingDesign` as a *source repo*. Nothing states this case's answer, so no
  `known_leak_channels` entry. (Distinct from the jetbridge-sourced cases, which do need
  one.)

### Files touched

- `task/task.md` — report date; "Expected behavior" rewritten (2 paragraphs).
- `case.yaml` — header comment; grading comments (overlay-collision rule, ordering,
  mechanical caveat); `withheld`/`difficulty`/`memorization_risk` annotations;
  `leakage_audit` curator-fixup entry.
- `ground_truth/rubric.md` — evidence-not-quotation preamble; new "Mechanical caveat".
- `notes.md` — this section, the `information_cut` caveat, the validation heading and the
  stale `unvalidated` sentence.

Untouched: `ground_truth/reference.diff`, `ground_truth/withheld_tests/*`, all `pre_state`
pins. No git state in either repo was modified (read-only inspection of `~/LightingDesign`).
