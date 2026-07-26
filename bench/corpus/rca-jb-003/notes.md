# Curation record — rca-jb-003

## Provenance walk

Backed out of a merged fix commit in this repo's jetbridge-era history.

| Role | SHA | Date | Subject |
|---|---|---|---|
| terminal artifact | `45bd9f34b217c5c7374928e16a68a73a04504c2f` | 2026-07-16T16:55:34-07:00 | `fix(jetbridge): terminal-end group kill was a silent no-op under dash/busybox sh` |
| pre_state (parent) | `44fdad0f64b079979b9573de8095ead6bbafff2b` | 2026-07-16T16:29:11-07:00 | `fix(agent-step+web): pure-CI gateway token + missing bad/error badge styles` |
| defect origin | `3b81488ef1` | 2026-07-12T01:31:30-06:00 | `fix(agent-step): Timed-out/aborted agent step: claude keeps running under the [review finding]` |

Verification performed (all read-only git; no checkout, no worktree):

- `git show -s` on the terminal SHA confirms the commit says exactly what the
  mining pass claimed: `kill -TERM -- "-$pgid"`, dash/busybox built-ins reject
  `--` before a negative pid (`Illegal number: -`), all three group operations
  fail silently under `2>/dev/null`, the script exits 0 having killed nothing,
  macOS `sh` is bash so the suite passed locally. It also records cross-shell
  verification (dash, busybox ash, bash 5, macOS bash 3.2) and a green run in a
  `golang:1.25` Linux container.
- `git diff --numstat` confirms the commit is exactly three files, +14/−6:
  `supervisor.go` (+7/−3 — three rewritten script lines plus a four-line doc
  comment), `supervisor_test.go` (+6/−2), `process_test.go` (+1/−1). No docs
  companion, no
  CHANGELOG, no version bump — `reference.diff` is the whole commit with nothing
  excluded.
- `git rev-parse 45bd9f34b2^` resolves to `44fdad0f64`, so pre_state is the
  literal parent; no gap, no intervening churn.
- `git branch -a --contains 45bd9f34b2` lists `jetbridge` (the mainline) among
  ~20 refs → merged. `git log 45bd9f34b2..jetbridge -- atc/worker/jetbridge/supervisor.go`
  is empty and the tip still carries the separator-less form, so the fix stuck.
- `git log -S 'kill -TERM -- "-$pgid"'` shows the buggy form was introduced by
  `3b81488ef1` on 2026-07-12 **together with the behavioural spec that catches
  it** — so the defect was latent for four days and CI was red on it that whole
  time. That is what makes the "it is not a recent regression" line in `task.md`
  a fact rather than a framing device.
- `git ls-tree 44fdad0f64 bench/` is empty: pre_state predates `bench/`, so the
  self-hosted-corpus caveat is satisfied — replay cannot expose the corpus.

Pre-state coherence: the parent is an unrelated agent-step/web fix that does not
touch `atc/worker/jetbridge/` at all. That is convenient rather than awkward —
the work item can honestly say the commit under test is innocent, which is how a
real triager would open this ticket.

## The environment fact that makes the case

`deploy/borg-pipeline.yml` job `k8s-runtime-tests` runs
`go test -v -count=1 -timeout 5m ./atc/worker/jetbridge/...` under
`rootfs_uri: docker:///golang:1.25-bookworm`. Debian bookworm's `/bin/sh` is
dash. macOS `/bin/sh` is bash 3.2.57. Verified locally that this is the whole
difference:

```
$ /bin/dash -c 'kill -0 -- "-999999"; echo exit=$?'
/bin/dash: 1: kill: Illegal number: -
exit=2
$ /bin/dash -c 'kill -0 "-999999"; echo exit=$?'
/bin/dash: 1: kill: No such process
exit=1
$ /bin/sh -c 'kill -0 -- "-999999"; echo exit=$?'      # macOS bash 3.2
/bin/sh: line 0: kill: (-999999) - No such process
exit=1                                                  # identical without `--`
```

## Leakage analysis

`withheld: []` — nothing *in the tree* at pre_state gives the answer away.
Checks run with `git grep <pattern> 44fdad0f64` (no checkout):

- `"Illegal number"` — zero hits anywhere in the tree.
- `-niE "\bdash\b.*(shell|sh\b|busybox)|busybox.*dash"` over `*.md`, `*.go`,
  `*.yml` — zero hits. Nothing in-tree discusses dash at all.
- `busybox` in `*.go` — hits are container image names in test fixtures plus the
  two supervisor doc comments that state the *constraint* ("needs only POSIX sh
  built-ins plus cat/sed/cut/sleep (busybox-compatible)"). That constraint is
  pre-existing production code, is quoted in `task.md` as a constraint on the
  fix, and names no mechanism.
- No in-tree plan or design doc covers the terminal-end kill's portability; the
  defect arrived as a review-finding fix (`3b81488ef1`), not from a written plan.

**Deliberately NOT withheld — a fair prior in the history.** `582f4aebe8`
(2026-07-04, an ancestor of pre_state) is
`fix(jetbridge): busybox-safe runner liveness check`, and its message says:
*"Found by running the script on a real busybox pod; local sh (bash) errors on
the empty pid, which is why unit script-execution specs passed."* That is the
same failure *shape* — a shell built-in behaving differently under bash, hidden
by local-only runs — applied to a different construct (`kill -0 ""`). It names
neither `--` nor dash. Withholding it would mean rewriting history to remove the
institutional memory a competent engineer on this codebase would actually have,
so it stays. It is the single largest assist available to an agent that reads
`git log`, and any leakage audit should weigh it explicitly.

**Deliberately withheld from the evidence bundle** (recorded per the schema's
honesty requirement; all omissions are subtractive — nothing false was added):

- *The dash error text.* `kill: Illegal number: -` is the answer, one string
  long. Withholding it is not a curator's convenience: the production script
  redirects the kill's stderr to `/dev/null`, so an operator triaging this
  genuinely would not have seen it. Reproducing it requires deliberately
  re-running the kill without the redirect — i.e. it is a *result of the
  investigation*, not an input to it.
- *Any statement that the difference is in `kill` specifically.* The evidence
  gives the shells and the observable ("exits 0, kills nothing"), never the verb.
- *`supervisor.go` line numbers or a pointer to the kill template.* `task.md`
  names only the failing spec, which is where a real report would start.

**Deliberately INCLUDED, as the solvability floor** (per curator guidance —
"include enough environment detail, which shell, where"):

- `ls -l /bin/sh` for both machines, inside a ~12-row environment table so it
  reads as a genuine two-machine comparison rather than a pointer. Without it
  the case is not solvable by reasoning, only by luck; with it the agent still
  has to know or discover the argument-parsing rule, which is the hard half.
- The exoneration of the `/proc` vs `ps -o pgid=` branch. This is the strongest
  red herring in the case — it is the *other* thing that differs between the two
  machines, and an agent that blames it produces a confident wrong answer. A real
  triager would have checked it first, so ruling it out is authentic, and leaving
  it open would make the case unfairly ambiguous rather than harder. Grounding
  for the claim: `/proc/<pid>/stat` is `pid (comm) state ppid pgrp …`, so
  `sed 's/^.*) //' | cut -d' ' -f3` selects `pgrp` correctly; and the terminal
  artifact records the full suite green in a `golang:1.25` Linux container after
  a change that touches only the `--`, which exonerates the procfs branch by
  construction.
- The observation that the supervised command survives to natural completion and
  writes its exit file. This says "the kill had zero effect" without saying why,
  and it is what distinguishes this from a timing bug. **Verified by hand**, not
  assumed: reproducing the pre-state kill script under `/bin/dash` against a real
  supervised subshell, the script exits 0, the runner is alive 1s later, and the
  state dir gains its `exit` file when the command finishes on its own.

**The reconstructed artefacts.** `task/evidence/ci-build-log.md` is a
reconstruction, not a captured log — the original Concourse build output for
`k8s-runtime-tests` was not preserved, and this machine cannot run the Debian
image (Docker is down). What is real in it: the Ginkgo failure block, timings,
spec/assertion line numbers and 372-passed/1-failed counts are the verbatim
output of running the pre_state suite with `sh` pointed at `/bin/dash`; the task
config is copied verbatim from `deploy/borg-pipeline.yml` at pre_state; the
passing laptop run is a verbatim host-shell run. What is fabricated: file paths
rewritten to a Concourse build dir, the wall-clock timestamps, and the build
numbers in the "related job status" table. `task/evidence/environment.md` mixes
verified facts (both `/bin/sh` identities, the no-op behaviour, the pgid field
layout) with two claims asserted from the terminal artifact's own verification
rather than re-observed here (the in-container `/proc` read, and "nothing is
missing in the container"). Anyone re-validating this case on Linux should
re-observe those two.

## Difficulty and discrimination

Graded `hard` on mechanical proxies — 3 lines of production change, but the root
cause is not in the language the code is written in, the failure is a silent
exit-0, and the repository's own tests argue the code is correct. The trap is
load-bearing: `supervisor_test.go:110-111` and `process_test.go:1577` at
pre_state assert `kill -TERM -- "-$pgid"` and `kill -KILL -- "-$pgid"`, they are
green in CI, and an agent that treats "tests agree with the code" as evidence of
correctness will confidently exonerate `supervisor.go` and go looking at the
harness. Expect this case to discriminate on shell-portability knowledge and on
resistance to a confirming-but-wrong oracle, not on localization.

## Open questions

- **The gate is environmental, and the schema has nowhere to say so.**
  `fail_to_pass` is a command string; the predicate that actually matters here is
  "`sh` must be dash". It is encoded as a PATH shim inside the command with a
  loud comment, but a harness that normalizes commands, or runs everything on a
  fixed macOS image, will run this case and report a clean pass at BOTH ends
  while learning nothing. That is the worst failure mode available to a bench and
  it argues for a first-class environment predicate in v2 of the schema.
  *(Fixup 2026-07-25: mitigated, not solved — `grading.preflight` now makes the
  predicate an executable command that fails loudly, so the silent-green mode
  requires a harness to skip a declared step rather than merely not read a
  comment. A schema-level `requires_shell` / matrix is still the right fix.)*
- **Two-shell matrix would be strictly better.** The honest gate is a matrix:
  {dash: fail→pass, bash: pass→pass}. The bash leg is what proves the case is a
  portability defect rather than a broken test. Expressed here as an extra
  `pass_to_pass` entry, which understates it.
- **`rubric: mechanical` undersells an RCA case.** The mechanical gate grades the
  *change*; the deliverable the work item asks for is the *diagnosis*, which only
  `ground_truth/rubric.md` §B can grade. The schema's single-valued `rubric` field
  cannot express "mechanical for the change, judge for the write-up". Same gap
  rca-jb-005 hit from the other direction. *(Fixup 2026-07-25: `rubric:` still
  reads `mechanical` — it is the schema's enum — but `grading.procedure` now
  states in the manifest that §B is judge-graded and that a mechanical-only score
  is an incomplete grade for this case.)*
- **Should the busybox-ash leg be graded too?** The terminal artifact claims
  verification on busybox ash; this case only gates dash. Adding an ash leg would
  cost a container and would catch a fix that happens to satisfy dash alone. Not
  done; noted so it is not mistaken for an oversight.
- **Sibling candidate not built:** `582f4aebe8` (the busybox `kill -0 ""`
  liveness bug) is the same shape from the same file two weeks earlier and would
  make a second cheap case — but building it would put *this* case's fair prior
  under a microscope, and the two would be near-duplicates for measurement
  purposes. Left unbuilt deliberately; if it is ever built, the pair should be
  split across dev/holdout rather than both landing in dev.

## Validation

### Extractor pre-check (informational — not the formal validation pass)

Run at build time to prove the case is real before sealing it, when `case.yaml`
still recorded `validation.status: unvalidated`. The formal pass below is what
promoted it to `validated`.

Method: `git archive <sha> | tar -x` into two throwaway trees (the repo was
treated as read-only — no checkout, no worktree), Go 1.25.6 darwin/arm64, module
cache warm, no network. The dash leg used a `sh -> /bin/dash` symlink first on
`PATH`; the bash leg used the host `/bin/sh` (bash 3.2.57).

| Tree | `sh` is | Command | Result |
|---|---|---|---|
| pre_state `44fdad0f64` | **dash** | full package, `-count=1` | **FAIL** — `Ran 373 of 373`, `372 Passed \| 1 Failed`; the single failure is `terminal-end kill tears down the still-running supervised process tree`, `Eventually` timing out after 10.0s at `supervisor_script_test.go:152` (`Expected an error, got nil` — the runner never dies) |
| pre_state `44fdad0f64` | bash 3.2 | full package, `-count=1` | **PASS** — `ok … 58.704s`, 373/373. The works-on-my-machine property, reproduced |
| terminal `45bd9f34b2` | **dash** | full package, `-count=1` | **PASS** — `ok … 58.696s`, 373/373 |
| pre_state + only `supervisor.go` from the fix | **dash** | focused `terminal-end kill` | **PASS** — confirms the 3-line script change alone is sufficient; the test-file edits in the commit are not load-bearing for the gate |
| pre_state + only the fix's `supervisor_test.go` and `process_test.go` | bash 3.2 | focused on the two text specs | **FAIL ×2** — the reference assertions are the inverse of the pre-state ones, so they also make a host-shell fail→pass pair (documented but not used as the primary gate, since it needs withheld files) |

Also verified by hand under `/bin/dash`, outside Ginkgo, against a real
supervised subshell: the pre-state kill script **exits 0 with empty output**, the
runner is still alive 1s later, and the state dir gains its `exit` file when the
command completes on its own — i.e. a complete no-op that reports success. And
directly: `dash -c 'kill -0 -- "-999999"'` → `Illegal number: -`, exit 2, versus
`dash -c 'kill -0 "-999999"'` → `No such process`, exit 1; under macOS bash both
forms are identical.

Fail-to-pass, pass-to-pass and the environment predicate are all confirmed; the
defect is real, the ground truth is real, and the case cannot be satisfied
trivially.

Environment gotchas found while doing this: (1) the shared scratchpad is not
private to one agent — a generically-named working directory (`pre/`) was deleted
mid-run by a concurrent process, which first presented as an inexplicable
`no required module provides package github.com/concourse/concourse/agent/schema`
error. Use case-prefixed temp directory names. (2) That error is also the genuine
symptom of materializing only a subtree: `agent/schema` is a separate Go module
wired in by a `require` + `replace` in the root `go.mod`, so the whole repository
must be materialized, not just `atc/`.

### Formal validation

- date: 2026-07-25
- validator: mechanical (detached worktrees, read-only git)
- worktrees: pre `44fdad0f64b079979b9573de8095ead6bbafff2b`, post `45bd9f34b217c5c7374928e16a68a73a04504c2f`
- outcome: **validated** (all three legs, including the load-bearing bash-vs-dash split)
- host: macOS, `/bin/sh` = GNU bash 3.2.57; `/bin/dash` present, shim `ln -sf /bin/dash /tmp/rca-jb-003-shim/sh`
- oracle spec is byte-identical at both SHAs (`git diff --stat <pre> <post> -- atc/worker/jetbridge/supervisor_script_test.go` -> empty), so no withheld tests are involved

### PRIMARY GATE (dash on PATH, focused)
`mkdir -p /tmp/rca-jb-003-shim && ln -sf "$(command -v dash || echo /bin/dash)" /tmp/rca-jb-003-shim/sh && PATH=/tmp/rca-jb-003-shim:$PATH go test ./atc/worker/jetbridge/ -count=1 -timeout 20m -args -ginkgo.focus='terminal-end kill tears down the still-running supervised process tree'`

PRE (FAIL, exit 1):
```
[FAILED] Timed out after 10.001s.
In [It] at: .../atc/worker/jetbridge/supervisor_script_test.go:152
Summarizing 1 Failure:
  [FAIL] Task exec supervisor script execution [It] terminal-end kill tears down the still-running supervised process tree
Ran 1 of 373 Specs in 10.138 seconds
FAIL! -- 0 Passed | 1 Failed | 372 Skipped
```
POST (PASS, exit 0): `ok  github.com/concourse/concourse/atc/worker/jetbridge  30.724s`

### Full-package form (dash on PATH)
PRE (FAIL, exit 1): `Ran 373 of 373 Specs in 37.315s / FAIL! -- 372 Passed | 1 Failed` — exactly one red spec, the terminal-end teardown one.
POST (PASS, exit 0): `ok ... 58.910s` (373/373).

### PASS-TO-PASS on a bash `/bin/sh` (no shim)
`go test ./atc/worker/jetbridge/ -count=1 -timeout 20m`
PRE `ok ... 59.095s`, POST `ok ... 58.979s`.
This is the evidence that the defect is shell-portability, not a broken test — and the reason a macOS-only harness (bash `/bin/sh`) must never grade this case.

- corrected_cmd: none — all three ran verbatim.
- notes: no Postgres, no cluster, no Docker. Focused leg ~40s incl. compile; full-package legs ~60s.
- delta since this pass: the `pass_to_pass` commands gained `-ginkgo.skip` scoping
  and a `preflight` predicate was added — see "Fixup 2026-07-25" for exactly what
  was re-executed and what still needs re-observation.

## Fixup 2026-07-25

Curator-fixup pass over the dual leakage audit (opus: pass; sonnet: pass). Both
auditors cleared the exposed material, so **nothing in `task/` was softened or
rewritten** — the evidence files are byte-identical and the trigger is untouched.
The single exposed-side edit is additive and non-leading: `task.md`'s Deliverable
section now names the channel for the written diagnosis (see item 5).

### Dissolved by the exposure contract (no action, deliberately)

The solver sees `pre_state − withheld + task/` and nothing else, so none of the
following is a leak and none was renamed or softened:

- `case.yaml`'s title, which names the mechanism outright.
- The `source.terminal` comment quoting the fix commit's own wording, the
  `focus_spec` naming the graded spec, and `curation.learnings` narrating the
  whole defect.
- The case id/path `rca-jb-003/` (neutral anyway).

Standing caveat from the schema: a **hand-run** must materialize `task/` into a
neutrally-named directory and must not hand the solver this case directory.

### Real defects fixed

1. **Spurious-pass gate (opus's curator note).** The mechanical gate is
   environment-conditional and a bash-`sh` harness reported green at *both* ends
   while learning nothing. Added `grading.preflight`: a mandatory, behaviour-based
   predicate asserting that `sh`'s built-in `kill` parses `-- "-<pid>"`
   differently from `"-<pid>"`. Verified on this host: dash shim → exit 0, host
   `/bin/sh` (bash 3.2.57) → exit 1. `expect` says in words that a failing
   preflight must ERROR the run, never score it, and `rubric.md` §A repeats it.
   Deliberately shell-agnostic (no `dash` string match) so busybox ash qualifies.
2. **`pass_to_pass` failed a correct minimal fix.** The old full-package command
   required `supervisor_test.go` and `process_test.go` to be green after the
   change — but those two specs pin the *buggy* literal and go red against ANY
   correct fix, while `rubric.md` §A lists updating them as credit-not-required.
   Scoped both legs with `-ginkgo.skip`; the unscoped run moved to a new
   `grading.informational` entry that spells out that 2 red literal-pinning specs
   are an unclaimed credit item, not a regression. Also skipped the oracle spec on
   the dash leg only: it is red at pre_state there, so the previous command was
   not literally "passes before and after" (the old comment admitted as much in
   prose). Both skip fragments and the oracle fragment were checked unique in the
   package at pre_state with `git show <pre>:<file> | grep`.
3. **Anti-gaming overlay clobbered graded work and pinned one fix form.**
   `cp -r ground_truth/withheld_tests/atc ./atc` ran in the graded tree,
   overwriting the agent's own assertion edits — which `rubric.md` §A explicitly
   credits ("adds a negative assertion so the old form cannot come back") — and
   the restored assertions pin the reference's exact literal
   (`kill -TERM "-$pgid"`) although the rubric accepts *any* form portable across
   dash/busybox/bash. Rewrote it as a discriminator that runs last, on a copy,
   with `expect` stating that a failure triggers adjudication rather than a
   verdict; added an "Alternative-form adjudication" procedure to `rubric.md` §A
   (behavioural: gate green under dash + all three call sites use the same
   construct + the produced form signals the group under busybox ash and bash).
   Added `grading.procedure` to fix the ordering (judge the agent's diff *before*
   overlaying) and `grading.caveats` recording that no fix location is pinned.
4. **Oracle-unchanged check assumed git history.** `git diff --exit-code <pre>`
   cannot run under the `git archive`/tarball materialization the case itself
   offers. Added `fallback_cmd`/`fallback_expect`: sha256 of
   `supervisor_script_test.go` at pre_state =
   `d3254398445a29d31bf77be0ec641961997e4278ce5cb94d97fdbb838085fa07`.
5. **No delivery channel for the judged deliverable.** `task.md` asked for "a
   short written diagnosis ... plus the change itself" without saying where it
   goes, while `rubric.md` §B grades that write-up — so the judge could be left
   hunting for it (or scoring a diagnosis that only ever existed in a transcript
   the harness did not keep). Added one neutral sentence to `task.md`'s Deliverable
   section naming `DIAGNOSIS.md` at the repository root, and a matching "Where to
   read it" paragraph in `rubric.md` §B that accepts any channel, forbids a
   placement deduction, and states that `DIAGNOSIS.md` is an expected artifact
   rather than scope creep. The sentence adds no information about the defect: it
   names no file under `atc/`, no shell, and no mechanism.
6. **notes.md internal inconsistency.** Two `## Validation` headings, an empty
   "Formal validation" stub immediately followed by a filled duplicate, and a
   sentence claiming `case.yaml` records `validation.status: unvalidated` when it
   records `validated`. Consolidated into one section; the stale sentence now
   describes the state at the time of the pre-check.

### Known leak channel

`known_leak_channels: [project-auto-memory]` declared. The curation host's project
auto-memory (`memory/project_wave2_agent_step_review.md`) states this case's root
cause, the buggy line, the fix's separator-less form and the terminal SHA
verbatim. Memory was not modified. A hand-run on this machine is invalid unless
project memory and session context are suppressed.

### Difficulty

Unchanged at `hard`. Neither auditor argued otherwise, and nothing in this pass
made the case easier: the exposed material is untouched, the trap specs still
pin the defect, and the shell-parsing rule still has to be known or discovered.

### Not re-executed (honest gap)

The scoped `pass_to_pass` commands and the rewritten anti-gaming command were
**not** run: the curation host has ~245 MB free on `/System/Volumes/Data`, and
materializing the repo plus a Go test build risks filling it. They are strict
narrowings of runs already recorded green above (skipping specs from a green run
cannot redden it, and a malformed skip regex makes Ginkgo error loudly rather
than pass quietly), and the spec-name fragments were verified unique at pre_state.
The next validation run on a Linux host should re-observe: the 3-skipped /
2-skipped counts, and the preflight on a real dash `/bin/sh`.
