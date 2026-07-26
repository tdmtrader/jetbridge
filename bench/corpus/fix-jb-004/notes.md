# fix-jb-004 — curation record

## Provenance walk

Backed out of a merged fix commit in the private jetbridge-era history of this
repo.

| Role | SHA | Date | Subject |
|---|---|---|---|
| terminal artifact | `6e113b067b10bc6b426108fffac8297dd75e6151` | 2026-07-12T06:53:49-06:00 | `fix(agent-step): read flight NDJSON lines past the 64KiB scanner limit [review finding]` |
| pre_state (parent) | `b899579fc50a9e578483a61a98435d49486c68ae` | 2026-07-12T06:53:40-06:00 | `fix(agent-step): log swallowed flight-recorder StreamFile errors [review finding]` |
| grandparent | `0f2dc2133d892d920984f704f4bdf32ebe75f884` | — | `fix(agent-step): bump JetBridge HEAD migration pointer to 1773106062 [gate finding]` |

Verification performed:

- `git show -s` on both SHAs: parent link confirmed (`%P` of the terminal
  commit is exactly the pre_state SHA). Commit message matches the candidate's
  claim verbatim.
- `git merge-base --is-ancestor 6e113b06 main` → true. The fix is merged and
  reachable from `main` / `origin/jetbridge`; outcome is `merged`, not a
  dangling branch.
- `git show --stat 6e113b06` → exactly 2 files, +41/−1
  (`agent/schema/event_reader.go`, `agent/schema/event_io_test.go`). No
  same-commit companions (no docs, no CHANGELOG, no version bump, no go.mod
  or go.sum change — confirmed by an empty `git show 6e113b06 -- agent/schema/go.mod agent/schema/go.sum`).
  `reference.diff` is therefore the whole commit; nothing was filtered out.
- Pre-state source read directly (`git show <pre>:agent/schema/event_reader.go`):
  `NewEventReader` returns `&EventReader{scanner: bufio.NewScanner(r)}` with no
  `scanner.Buffer` call. Ground truth confirmed as described.
- The symptom chain was verified in the pre-state tree, not assumed:
  `git show <pre>:atc/exec/agent_step.go` (`ingestFlightRecorder`, ~:698)
  contains `reader := schema.NewEventReader(rc)` and
  `if err != nil { break // io.EOF or malformed tail — keep partial counts }`,
  followed by the `if !sawStepEnd { rm.Status = schema.RunStatusError }` rule.
  That is what turns a reader abort into a wrong terminal status — the case's
  premise holds at the cut.
- Call sites of the changed API at pre_state: `atc/exec/agent_step.go:698` and
  two in `agent/runner/runner_test.go`. Small blast radius, which is why the
  rubric treats "public signature preserved" as a *should*, not a *must*.

### Mechanical validation (done during extraction)

Performed read-only: both trees extracted with `git archive <sha> agent/schema | tar -x`
into a scratch dir. No checkout, no worktree, no mutation of the repo.

| Run | Result |
|---|---|
| pre_state, unmodified | `ok github.com/concourse/concourse/agent/schema 0.438s` (123 specs) |
| pre_state + post-cut `event_io_test.go` | **FAIL** — `[FAILED] Unexpected error: bufio.Scanner: token too long` at `event_io_test.go:191`; 123 passed / 1 failed |
| pre_state + additive `event_reader_bigline_test.go` | **FAIL** (same spec) |
| terminal sha, unmodified | `ok ... 1.433s` (124 specs) |
| terminal sha + additive withheld test | `ok ... 0.175s` |

So the case is genuinely fail-to-pass in both injection forms, and the
regression guard passes at both ends. Environment: go1.25.6 darwin/arm64, no
Postgres, no network, ~0.5s per run. `agent/schema` has its own `go.mod`
(module `github.com/concourse/concourse/agent/schema`), so it is invisible to
the root module's `./...` — grading must use `go test -C agent/schema ./...`
or `cd agent/schema && go test ./...`. The repo Makefile does the latter and
explicitly `--skip-package`s `agent/schema` from `make test-unit`.

Also verified empirically (go1.25.6) for the rubric's "wrong answers": after
`bufio.ErrTooLong` fires, the next `Scan()` returns one garbage 64 KiB prefix
of the oversized line and every call after that returns false. The rest of the
stream is genuinely unreachable — so "just don't break out of the ingestion
loop" does not recover the dropped events. This matters because it is the
plausible-looking wrong fix an agent might reach for.

## Leakage analysis

`bench/` does not exist at the pre_state tree (`git ls-tree -d <pre> -- bench`
is empty; the corpus was created 2026-07-25, thirteen days after the cut), so
the self-hosted-corpus caveat in the schema is satisfied — replaying this case
cannot surface its own answer.

**Scrubbed from the task (the real trigger leaked all of this):**

- The commit subject names the mechanism and the number
  ("read flight NDJSON lines past the 64KiB scanner limit"). Withheld.
- The commit body names `NewEventReader`, `bufio.Scanner`, `bufio.ErrTooLong`,
  and the chosen 5 MiB remedy. Withheld — it is effectively the answer key,
  and lives only in `ground_truth/answer.md`.
- The file paths `agent/schema/event_reader.go` / `event_io_test.go` are never
  mentioned in `task/task.md`, per curator guidance to frame the work item at
  the observability layer. This is a deliberate departure from the real
  trigger, which was a code-inspection review finding that named the function
  directly; the case therefore tests symptom→cause navigation, which the real
  reviewer did not have to do. Noted here rather than silently.
- The added spec's own name ("reads a line larger than the default 64KiB
  scanner limit") and its inline comment name the mechanism. The test is
  grading-only, in `ground_truth/withheld_tests/`, and must never be placed
  in the exposure manifest.

**Searched for and not found (nothing added to `withheld`):**

- `git grep -iE "ErrTooLong|64 ?KiB|64KB|MaxScanTokenSize|scanner buffer"`
  over all `*.md|*.yml|*.yaml|*.txt|*.json` at pre_state: one hit,
  `docs/superpowers/plans/agentic-platform/12-delivery-outcomes.md:100`, which
  is an unrelated 64 KiB *per-file diff patch cap* for the ticket-diff API.
  Not a leak.
- `git grep -iE "oversized|too long|token limit|line limit"` over the same set:
  hits are playground skill templates and a harvest-plan spec about truncating
  diffs. Unrelated.
- `REVIEW.md` (`docs/superpowers/plans/agentic-platform/REVIEW.md`, 414 lines)
  is the in-tree findings register for this exact review campaign and is the
  most dangerous candidate leak. Grepped for
  `EventReader|ndjson|scanner|64|oversiz|large line|truncat`: it discusses F4
  (ingestion on a cancelled context), F31 (park lifetimes), F40 (resolver TLS)
  — **not** this finding. This one never made it into the register.
- `ci/dogfood/FINDINGS.md`: no relevant hits.
- `docs/superpowers/plans/agentic-platform/07-agent-step.md` contains a full
  code listing of `ingestFlightRecorder` including `NewEventReader` and the
  break-on-error loop. This is a *mirror of the shipped code*, not a
  description of the fix, so it is not a leak — an agent reading it learns
  exactly what it would learn from `atc/exec/agent_step.go`. Left exposed
  deliberately; it is in-scope context for the diagnosis.

**Deliberate solvability affordances added to the task** (flagged so a leakage
auditor can judge them rather than discover them):

1. "the tally stops partway through the file" — the discriminating symptom.
   Without it the task degenerates into a needle hunt. It points at *where*
   ingestion loses data, not at *why*, so the diagnosis hop is preserved.
2. The "Correlation" section (affected steps ran commands with very verbose
   output). A real on-call report would carry this correlation, and it is the
   kind of clue that exists before anyone knows the cause. It is the closest
   thing in the task to a hint about size. A harder variant of this case could
   drop it; recorded here so that variant is a deliberate choice.
3. The memory-bounded constraint. Mildly directional (it hints the answer
   involves a bound) but it is a genuine engineering constraint on this code
   path — the flight recorder is self-reported from inside the agent pod — and
   without it a passing-but-wrong `io.ReadAll` answer could not be graded
   against anything.

**Memorization:** `none`. Private repo, jetbridge-era history, post-cutoff
(2026-07-12). Caveat for whoever reads the roll-up: the *remedy* here
(`scanner.Buffer` for `bufio.Scanner`'s 64 KiB default) is widely-known Go
lore, so once an agent reaches `NewEventReader` the fix is near-instant. That
is general knowledge, not memorization of this repo, and it is why the rubric
weights diagnosis (item 1) rather than the constant.

## Scope boundary — do not drift to the follow-up

`f83ca7a1909a9dd43d637a40ad3e568c39b6dca4` (2026-07-16,
"fix(agent-step): close budget fail-opens, survive oversized NDJSON lines")
later reworks this reader to *skip and resync* past lines above the 5 MiB cap.
That is four days past this cut and answers a different question. The expected
answer for fix-jb-004 is the pre_state→terminal transition only; an agent that
independently produces skip-and-resync is scored fully correct (rubric bonus
item 10), not penalized for "not matching the reference".

## Open questions

- The additive grading test is Ginkgo, matching the package's form at this cut.
  `3294f724ef` later converts `agent/schema` tests to stdlib `testing`. If a
  future replay materializes a *different* pre_state, or if an agent under test
  converts the package's test style as part of its change, the additive file
  would need converting too. Harness should prefer the additive form but fall
  back to the overwrite form on a compile error.
- Should the rubric's "must" #6 (agent wrote its own test) really be a must?
  It is orthogonal to the behavioral fix and a judge may score it as process
  compliance. Flagged for the first judge calibration pass.
- Not yet decided corpus-wide: whether cases like this should ship a
  `pass_to_pass` set beyond the owning package. Here the change is confined to
  a leaf module with three call sites, so the package suite is a sufficient
  regression guard; a case touching `atc/db` would not be.

## Validation — extraction-time evidence

Status at extraction: `unvalidated`, pending the validation stage. **Superseded
by the section below**, which records the stage's own run; `case.yaml` reads
`validation.status: validated` on the strength of that. (The stale
`unvalidated` line contradicting the manifest was corrected 2026-07-25.)

Extraction-time evidence is recorded above (fail-at-pre / pass-at-post
reproduced for both injection forms on go1.25.6, from `git archive` copies).

| Date | Stage | Result |
|---|---|---|
| 2026-07-25 | extraction | fail-at-pre / pass-at-post confirmed (see table above) |

## Validation

- date: 2026-07-25
- validator: mechanical (detached worktrees, read-only git)
- worktrees: pre `b899579fc50a9e578483a61a98435d49486c68ae`, post `6e113b067b10bc6b426108fffac8297dd75e6151`
- outcome: **validated** (all three legs)

### fail_to_pass — variant A (overwrite the withheld spec)
`git show 6e113b067b10bc6b426108fffac8297dd75e6151:agent/schema/event_io_test.go > agent/schema/event_io_test.go && go test -C agent/schema ./... -count=1`

PRE (FAIL, exit 1):
```
[FAILED] Unexpected error:
    bufio.Scanner: token too long
Summarizing 1 Failure:
  [FAIL] EventReader [It] reads a line larger than the default 64KiB scanner limit
Ran 124 of 124 Specs in 0.003 seconds
FAIL! -- 123 Passed | 1 Failed | 0 Pending | 0 Skipped
```
POST (PASS, exit 0): `ok  github.com/concourse/concourse/agent/schema  0.170s`

### fail_to_pass — variant B (additive file, no clobber)
`cp $CASE_DIR/ground_truth/withheld_tests/event_reader_bigline_test.go agent/schema/ && go test -C agent/schema ./... -count=1`
(CASE_DIR = bench/corpus/fix-jb-004)

PRE (FAIL, exit 1):
```
bufio.Scanner: token too long
Summarizing 1 Failure:
  [FAIL] EventReader oversized lines (bench fix-jb-004) [It] reads a line larger than the default 64KiB scanner limit
Ran 124 of 124 Specs in 0.003 seconds
```
POST (PASS, exit 0): `ok  github.com/concourse/concourse/agent/schema  0.168s`

### pass_to_pass (no overlay)
`go test -C agent/schema ./... -count=1` — PRE `ok ... 0.192s` / POST `ok ... 0.170s`.

- corrected_cmd: none — all three ran verbatim (`$CASE_DIR` resolved to the corpus checkout).
- notes: no Postgres, no network; ~0.2s per leg. `-C agent/schema` is mandatory (own go.mod).

## Fixup 2026-07-25

Curator-fixup pass over the two audit entries in `case.yaml`. Every item is
resolved into one of four buckets below. Nothing in `pre_state`, the reference
diff, or the validated commands changed, so the validation above still stands
(the one `task.md` edit is prose in the constraints list and touches no
graded behaviour).

### REAL DEFECT — fixed

1. **`task/task.md`, constraint 2** — "a runaway (or prompt-injected) agent can
   emit an **arbitrarily long line**" → "**an arbitrary amount of output**".
   This was the only answer-shaped phrase in the solver-facing text: "long
   line" names the failure's granularity, which is most of the mechanism, and
   it echoed the vocabulary of `atc/exec/agent_cost_observer.go`'s bounded-line
   comment. The memory-bounded constraint itself is genuine (the flight
   recorder is self-reported from inside the agent pod) and is the grading hook
   for rubric must-3, so it stays — only the granularity hint is gone. The
   trigger remains authentic: an on-call reporter who knew it was a *line*
   problem would already have the diagnosis.

2. **Grading overlay clobbered the test the task asks for.** `task.md` ends
   with "Cover the fix with a test" and rubric must-6 scores whether the agent
   wrote one — but the first `fail_to_pass` entry overwrote
   `agent/schema/event_io_test.go` wholesale, deleting exactly that artefact
   before it could be scored. Fixed by promoting the additive form (own file,
   own Describe block) to `role: primary` and demoting the overwrite to
   `role: fallback-only`/`destructive: true`, to be used only if the additive
   file fails to compile. Both forms were already validated; only their order
   and status changed. `ground_truth/rubric.md` must-6 now also says to score
   from the submitted diff, never the post-overlay tree.

3. **`fail_to_pass` pinned a fix location `task.md` leaves open** (opus). The
   gate compiles against `schema.NewEventReader(io.Reader)` and runs only
   `agent/schema`, yet the task never says where the fix must live — and rubric
   should-7 makes signature preservation a *should*. A correct fix in
   `atc/exec/agent_step.go`, or one that changes the reader's signature, would
   fail or fail-to-compile through no fault of its own. The flexibility moved
   into a new `ground_truth/rubric.md` § "Judge guidance" (route such a run to
   hand scoring against musts 1–6; only a fix that still aborts on an oversized
   line fails must-2), and the collision is recorded in `case.yaml`
   `grading.caveats`.

4. **Spurious-pass gate.** `rubric: mechanical` alone accepts
   `scanner.Buffer(..., math.MaxInt)` / `io.ReadAll`, which pass the withheld
   test while violating must-3. Recorded as the first `grading.caveats` entry
   and as the opening paragraph of rubric.md § "Judge guidance": the gate
   proves must-2 only; a green gate must never be reported as a solve on its
   own.

5. **Internal inconsistency in this file.** Two `## Validation` headings, the
   first asserting `Status: unvalidated` while `case.yaml` says `validated`.
   The first is now retitled "Validation — extraction-time evidence" and
   explicitly marked superseded.

### DISSOLVED BY CONTRACT — no action

6. **`case.yaml` title / grading note / `curation.learnings` name the
   mechanism** (sonnet's primary FAIL ground: "oversized NDJSON line",
   "bufio.Scanner: token too long", "bufio.Scanner's 64 KiB default"). Per the
   schema's exposure contract, the solver sees `pre_state` − `withheld` +
   `task/`; `case.yaml`, `notes.md` and `ground_truth/` are harness-side and
   never exposed, so titles and grading configs may state the answer freely.
   Nothing renamed or retitled.

7. **The withheld filename `event_reader_bigline_test.go` self-spoils on a
   directory listing** (opus). Same contract: the file lives in
   `ground_truth/withheld_tests/`, which is never materialised into the
   solver's tree. The only real obligation is the one the schema already
   states — a hand-run must copy `task/` into a neutrally-named directory,
   which is also why the `fix-jb-004` path itself is not a leak.

### PRICED DEFLATOR — kept, with the rubric adjusted

8. **`atc/exec/agent_cost_observer.go`** (sonnet). It is un-withheld and does
   implement a 5 MiB bounded, discard-and-continue line buffer for the
   analogous stdout-envelope problem. Kept, for three reasons: (a) it is live
   production code wired into `agent_step.go:538` (`newAgentCostObserver`) —
   withholding it does not compile, so the alternative is falsifying the
   snapshot, not scrubbing it; (b) it is a *pattern* precedent, not the remedy:
   it never touches `bufio.Scanner` or the event reader, and the actual remedy
   (`scanner.Buffer`) is Go common knowledge an agent has for free once it
   reaches `NewEventReader`; (c) reusing a local precedent is the behaviour
   rubric should-8 already rewards. Mitigation: rubric.md § "Judge guidance"
   now tells the judge to credit the **causal chain** and to score a
   bounded-buffer answer with no account of why the stream stopped as a
   partial, not a solve.

9. **`ingestFlightRecorder`'s `break // io.EOF or malformed tail` comment**
   (sonnet) and the `07-agent-step.md` mirror of that loop. This is the code
   under diagnosis; an agent must read it to solve the case at all, and it
   exposes the swallow, not the cause. Kept — already argued in "Leakage
   analysis" above and unchanged by this pass.

10. **The three task-side affordances** (opus): the `event stream ended without
    step.end` literal, "the events exist on disk", and the verbose-run
    Correlation section. All three are what a real on-call report carries
    before anyone knows the cause, and the literal greps to the *symptom* site
    (`ingestFlightRecorder`), not the *cause* site (`agent/schema/event_reader.go`)
    — the diagnosis hop survives it. Kept as authentic. The harder variant that
    drops the Correlation section remains available as a deliberate choice
    (recorded above under "Deliberate solvability affordances").

### DIFFICULTY RECALIBRATION

11. Opus argued effective difficulty is "easy, not moderate". The schema enum
    is `trivial | moderate | hard`, with no "easy", so the question is whether
    this drops to `trivial`. It does not: the agent must cross from a DB-row
    symptom to `ingestFlightRecorder`, then one module further to
    `agent/schema/event_reader.go` (a separate Go module, invisible to the root
    `./...`), and then choose a *bounded* remedy over the two plausible-wrong
    ones the answer key enumerates. `trivial` is for cases with no diagnosis
    hop. **Difficulty stays `moderate`**, with one of the four compounding
    hints (defect 1) now removed. Recorded rather than silently kept, and
    `curation.learnings` already states the honest ceiling: the remedy is free,
    so this case measures navigation only.

### KNOWN LEAK CHANNEL

12. `known_leak_channels: [project-auto-memory]` added. Neither auditor raised
    it, but this machine's project auto-memory
    (`~/.claude/projects/-Users-tdmtrader-concourse-concourse/memory/project_wave2_agent_step_review.md`)
    states the answer's substance verbatim — "NDJSON 5MiB bump didn't fix
    break-on-any-error ingestion" and "rewrites EventReader to skip >5MiB
    NDJSON lines and resync", naming both `EventReader` and the 5 MiB cap. Per
    `bench/README.md` § "Operator-environment leakage", a local hand-run of this
    case on this machine is invalid unless project memory, session context and
    conversation history are suppressed. Memory itself was not modified.
