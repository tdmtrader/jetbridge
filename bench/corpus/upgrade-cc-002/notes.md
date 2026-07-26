# upgrade-cc-002 — curation record

## Provenance walk

Backed out of a merged upstream Concourse pull request. All SHAs resolved and
inspected in this repo (they are reachable from the `upstream` remote,
`https://github.com/concourse/concourse.git`); every claim below was checked
rather than taken from the mining candidate.

| Role | SHA | Committed | Author | Subject |
|---|---|---|---|---|
| terminal artifact | `750512af503e11d391f48e6ed097bbda4e28d5d0` | 2023-10-18T10:42:51-04:00 | Rui Yang | `Merge pull request #8843 from concourse/renovate/all` |
| merge parent 1 (master) | `44d233a1a76b9cd359ba660e162f4f10d799c450` | 2023-10-16T16:59:52-04:00 | Rui Yang | `Merge pull request #8842 from concourse/renovate/all` |
| merge parent 2 (branch tip) = **post_state** | `57e50564b6abf2c5954ee9084f3c50bdde386be2` | 2023-10-17T21:15:58-04:00 | Rui Yang | `fix mpb compilation error; stick to yaml.v2` |
| branch commit 2 (reverted attempt) | `091b31d8754967c2588a1b1e91cde41b4339fee1` | 2023-10-16T22:35:49-04:00 | Rui Yang | `manually major version bumps of libs` |
| branch commit 1 = **pre_state** | `45bf8658bfafcc68783ed08e01d2422bed40cdd6` | 2023-10-16T21:00:57+00:00 | renovate[bot] | `fix(deps): update all dependencies` |

Verification performed:

- `git rev-list --parents -n1 750512af50` → parents are exactly
  `44d233a1a7` and `57e50564b6`. It is a real two-parent merge of the
  `renovate/all` branch, i.e. the PR landed; `outcome: merged` is correct.
- `git rev-list --parents -n1 45bf8658bf` → its parent is `44d233a1a7`, so the
  bot commit sits directly on top of master. The branch is exactly three
  commits and contains no unrelated work.
- `git branch -a --contains 45bf8658bf` and `… 750512af50` both list
  `remotes/upstream/master` (plus every `release/*` branch since). Neither is
  an ancestor of this fork's `main` — the jetbridge fork diverged earlier —
  which is fine: the case materializes from upstream, not from the fork.
- `git show --stat 45bf8658bf` → **one file, `go.mod`, 3 insertions / 3
  deletions.** No Go source, no `go.sum`. The candidate's central claim (a
  pre_state that provably does not compile) is structurally sound.
- `git diff 44d233a1a7 57e50564b6 --stat` → 10 files. The reverted yaml
  migration is visible as the difference between the two branch commits:
  `091b31d875` touches `atc/atccmd/command.go`, `tsa/tsacmd/command.go` and
  `vars/template.go`; `57e50564b6` puts all three back and adds the
  `.github/renovate.json` pin. Net effect on those three files across the
  branch: zero. Confirmed by `git diff 45bf8658bf 57e50564b6 --stat`, which
  does not list them at all.
- Import sites enumerated in the pre_state tree rather than assumed:
  `git grep -l` finds mpb in 7 files (8 import lines) and `caarlos0/env` in
  exactly one — `topgun/k8s/k8s_suite_test.go`, a **test-only** file. Also
  found: `gopkg.in/yaml.v2` in four files
  (`atc/atccmd/command.go`, `atc/engine/build_step_delegate.go`,
  `tsa/tsacmd/command.go`, `vars/template.go`), which is what makes the third
  bump a decision rather than a rename.
- Read `fly/ui/progress/progress.go` at both ends in full. The four API
  changes claimed by the candidate are all present and are the entire
  behavioral content of the change; everything else in the file
  (`mpb.New(mpb.WithWidth(1))`, `decor.WC{…DSyncWidthR}`, `decor.OnComplete`,
  `bar.Abort(false)`, `bar.SetTotal(bar.Current(), true)`, the `errgroup`
  scaffolding) is byte-identical across the cut.

### One correction to the candidate

The candidate described the pre_state as a go.mod that "pins mpb v8 and env
v9, so the agent does not have to choose versions". True, but it understates
how broken the file is. At `44d233a1a7`, `go.mod` already required **both**
majors of all three modules (`env/v6`+`env/v9`, `mpb/v4`+`mpb/v8`,
`yaml.v2`+`yaml.v3`) — Renovate had added the new majors in earlier PRs while
nothing imported them (`go.sum` at the cut has only `/go.mod` hashes, no `h1:`
hashes, for `mpb/v8` and `env/v9`). The bot's edit replaced each *old*-major
line with a *new*-major line that already existed one line below, so the
pre_state `go.mod` carries three literal duplicate `require` entries. That is
cosmetic for the parser but it is part of what a correct answer has to clean
up, and it is why the reference `go.mod` diff is 8 lines rather than 3.

### Mechanical validation performed during extraction

Read-only throughout: all three trees produced with
`git -C <repo> archive <sha> | tar -x -C <scratchdir>`. No checkout, no
worktree, no mutation of any repo. Toolchain go1.25.6 darwin/arm64, module
proxy reachable.

| Tree | Command | Result |
|---|---|---|
| pre `45bf8658bf` | `go build ./...` | **EXIT 1** — `go: updates to go.mod needed; to update it: go mod tidy` |
| pre `45bf8658bf` | `go build ./fly/...` | **EXIT 1** — same message |
| pre `45bf8658bf` | `go vet ./fly/... ./topgun/k8s/` | **EXIT 1** — same message |
| pre `45bf8658bf` | `go test ./vars/` | **EXIT 1** — same message |
| pre `45bf8658bf` | old-major grep invariant | PASS (v4/v6 absent) |
| post `57e50564b6` | `go build ./...` | **EXIT 0** |
| post `57e50564b6` | `go vet ./fly/... ./topgun/k8s/` | **EXIT 0** |
| post `57e50564b6` | `go test ./vars/` | **ok**, 0.166s |
| post `57e50564b6` | `go test ./fly/ui/... ./fly/version/... ./fly/rc/... ./fly/eventstream/...` | all **ok**, ~1.7s total |
| post `57e50564b6` | old-major grep invariant | PASS |
| pre + `go mod tidy` | `go mod tidy` | EXIT 0, and it *re-added* `mpb/v4 v4.12.2` and `env/v6 v6.10.1` (`go: found github.com/vbauerster/mpb/v4 …`, `go: found github.com/caarlos0/env/v6 …`), dropping v8/v9 |
| pre + `go mod tidy` | `go build ./...` | **EXIT 0** — i.e. the naive fix passes every build gate |
| pre + `go mod tidy` | old-major grep invariant | **FAIL** — correctly rejected |
| `091b31d875` (yaml.v3 attempt) | `go test ./vars/` | **FAIL** — 10 of 91 specs, e.g. `can interpolate a different data types into a byte slice`, `can interpolate values with strange newlines` |

Two findings from this that shaped the case:

1. The `go mod tidy` escape is real and complete. It is the action the Go
   toolchain's own error message instructs the agent to take, it makes every
   build/vet/test gate green, and it reverts the entire upgrade. `go build
   ./...` alone therefore grades a full revert as a pass. The anti-revert grep
   in `pass_to_pass` is not belt-and-braces; it is the case.
2. The yaml decision is mechanically checkable. `go test ./vars/` fails 10
   specs on the maintainer's own reverted attempt and passes on the accepted
   answer, so "did the agent decline the yaml.v3 bump" needs no judge.

`go build ./...` was additionally observed to fail with
`link: … no space left on device` on this machine once free space dropped
below ~1 GiB (shared scratch volume, other sessions). That is an environment
artifact, not a property of the case — hence the vet gate is listed first and
the disk requirement is called out in `grading.environment`.

## Leakage analysis

**The information cut** is `2023-10-16T21:00:57+00:00`, the pre_state commit's
timestamp. `bench/` did not exist in 2023 (`git ls-tree -d 45bf8658bf -- bench`
is empty), so the self-hosted-corpus caveat is satisfied trivially.

**Withheld (kept out of `task/`, lives only in `ground_truth/`):**

- Both human commit subjects. `fix mpb compilation error; stick to yaml.v2`
  names the broken library *and* announces the yaml decision — it is close to
  a full answer key in eleven words. `manually major version bumps of libs`
  is harmless but would reveal that a human intervened at all.
- Every file path the fix touches. `task/task.md` names no source file. It
  names the three modules, which is not a leak: those three lines are the
  visible content of the pre_state `go.mod`, and the first compiler error the
  agent sees will name them too.
- The nature of the mpb break. The task never mentions `AddSpinner`,
  spinners, decorators, progress bars, or that only one of the seven mpb files
  needs more than an import rewrite.
- That yaml is the odd one out. The task's yaml-shaped constraint is stated
  as a general policy ("do not edit tests to accommodate a dependency", "if
  any of the three proposed moves cannot be adopted, back that one out") and
  never says which one, or that any of them will actually turn out that way.
- `.github/renovate.json`'s yaml.v2 pin, including its `_context` string
  explaining the indentation problem. It does not exist at pre_state, so no
  path needed withholding — but a grader must not paste it into the prompt.

**`withheld: []` — searched for and found nothing to add.** The pre_state tree
has no `docs/` directory at all (top-level dirs are `.github atc cmd fly
go-concourse hack integration screenshots skymarshal testflight topgun tracing
tsa vars web worker`), so the in-tree-plan leak that dogs the jetbridge cases
cannot occur here. `.github/renovate.json` at pre_state contains only the
pre-existing `client-go` rules and no mention of yaml. `go.sum` at pre_state
carries `/go.mod` hashes for `mpb/v8` and `env/v9` but no `h1:` hashes and no
trace of the new indirect deps (`mattn/go-runewidth`, `rivo/uniseg`) that the
answer adds — no future state has leaked backwards into the snapshot.

**Deliberate solvability affordances in `task/task.md`** (flagged so a leakage
auditor judges them rather than discovers them):

1. The three-row table of version moves. Faithful — a Renovate PR body is
   literally a table of exactly this — and redundant with the pre_state
   `go.mod`. Without it the task would be a guessing game about which of ~200
   requirements moved.
2. The verbatim `go: updates to go.mod needed` error. This is what the agent
   sees on its first command; withholding it would buy nothing.
3. **REMOVED in the 2026-07-25 fixup** — see `## Fixup 2026-07-25`. The
   original text and its defence are kept here as the record of the decision
   that was reversed. "a bare `go mod tidy` … puts the **old** majors back —
   that is the opposite of what this PR is for." This was the biggest
   concession in the case: it
   pre-warns the agent about the trap that the primary grading invariant
   tests. It is in because the alternative is a case where a *reasonable*
   reading of the toolchain's own instruction scores zero, which measures
   prompt-luck rather than capability, and because the intent ("the version
   must actually move") is exactly what a real maintainer would put in the
   ticket. A harder variant of this case would delete this paragraph; record
   that as a deliberate choice, not an oversight, and expect it to move scores
   a lot.
4. **REMOVED in the 2026-07-25 fixup** — see `## Fixup 2026-07-25`. "Not
   everything in this repo is reachable from `go build` — some packages
   are test-only, and they are part of the gate." Same reasoning at lower
   stakes: it warns that the `go build ./...` headline is insufficient without
   naming `topgun`. Removing it makes item 3 of the rubric considerably
   harder.
5. The task states acceptance criteria (`go build`, `go vet`, unit tests,
   tidy-clean go.mod). Legitimate work-item content, and it is what "green"
   meant for this PR.

**Memorization: `high`, and it is the case's main weakness.** Public repo,
October 2023, comfortably inside training data. Two distinct exposures: a
model may recall PR #8843 itself, and — more likely and harder to argue away —
the `mpb` v4→v8 API delta is documented general knowledge that a strong model
can simply *know* without reading the library. That is not cheating, but it
means a pass here says little about upgrade capability on unfamiliar
dependencies. Per `bench/README.md`, this case must never anchor an efficacy
claim on its own.

**Materialization warning (applies to every case backed out of git history).**
The pre_state must be handed to the agent as a *tree*, or as a clone truncated
at `45bf8658bf`. If the harness materializes a full clone of
`concourse/concourse` and checks out the SHA, then `git log --all`,
`git show 57e50564b6` and the `renovate/all` remote branch put the complete
answer one command away. `git archive <sha> | tar -x` (what extraction used)
has this property by construction.

## Open questions

- **Port naming.** This case declares `work-item: work-item/v1` for
  consistency with every other case in the corpus, but a version-upgrade
  function arguably wants a distinct `upgrade-request/v1` input carrying the
  target versions as data rather than prose. Deferred until a second upgrade
  case exists to compare against; flagged for whoever defines the harvest
  adapter's port vocabulary.
- **`pass_to_pass` when the pre_state builds nothing.** The regression-guard
  test set cannot run at pre_state, so its "before" is measured at
  `baseline_ref` (`44d233a1a7`). The schema does not have a field for this; a
  `baseline_ref` key was added inline. If other broken-pre_state cases appear,
  this should become schema v2 rather than a per-case convention.
- **Network.** This is the first corpus case that cannot run offline: the
  module proxy must serve `mpb/v8 v8.6.2` and `env/v9 v9.0.0` zips, whose
  `h1:` hashes are absent from the pre_state `go.sum`. A hermetic replay needs
  a pre-warmed `GOMODCACHE` or a vendored proxy snapshot. Worth solving once,
  centrally, before more dependency-upgrade cases land.
- **Should `.github/renovate.json` be a "must"?** The task asks for the
  decline to be recorded and enforced, and the humans did it, but it is
  policy-shaped rather than behavior-shaped and a judge may weight it oddly.
  It is a `should` (rubric item 10) pending the first calibration pass.
- **No test covers `fly/ui/progress`.** The package has zero `_test.go` files,
  so rubric items 5 and 6 (spinner still left-positioned, decorators intact,
  exported signatures unchanged) are judge-only. A synthetic golden-output test
  could make them mechanical, but writing one would be corpus-authored ground
  truth rather than harvested, which v0 avoids.

## Validation

Status: `partial` — matches `case.yaml#validation.status`. The stale
`unvalidated` that stood here was written before the validation run recorded
below and was corrected in the 2026-07-25 fixup.

Extraction-time evidence is in the table above: fail-at-pre / pass-at-post
reproduced for all three `fail_to_pass` commands, the anti-revert invariant
shown to reject the `go mod tidy` escape, and the yaml guard shown to reject
the maintainer's own reverted attempt. The validation stage should reproduce
this from a clean materialization of `45bf8658bfafcc68783ed08e01d2422bed40cdd6`
with the reference diff applied, on a machine with ≥2 GiB free scratch if it
intends to run `go build ./...`.

| Date | Stage | Result |
|---|---|---|
| 2026-07-25 | extraction | fail-at-pre / pass-at-post confirmed for all gates; escape-hatch and yaml-trap both confirmed rejected (go1.25.6, darwin/arm64) |

### Validation run — 2026-07-25 (mechanical)

- date: 2026-07-25
- validator: mechanical (detached worktrees, read-only git)
- worktrees: pre `45bf8658bfafcc68783ed08e01d2422bed40cdd6`, post `57e50564b6abf2c5954ee9084f3c50bdde386be2`
- outcome: **partially validated — environment-blocked (disk) on the two heavy legs**
  - `go test ./vars/ -count=1` — **validated** end to end (FAIL at pre, PASS at post)
  - `go vet ./fly/... ./topgun/k8s/` — fail-at-pre **confirmed**; pass-at-post **environment-blocked**
  - `go build ./...` — fail-at-pre **confirmed**; pass-at-post **environment-blocked**

### PRE state (all three legs, exit 1, identical message)
```
go: updates to go.mod needed; to update it:
	go mod tidy
```
Root cause diagnosed read-only with `go mod tidy -diff` (writes nothing; `git status` stayed clean):
```
go: finding module for package github.com/caarlos0/env/v6
go: finding module for package github.com/vbauerster/mpb/v4/decor
diff current/go.mod tidy/go.mod
-	github.com/caarlos0/env/v9 v9.0.0
-	github.com/caarlos0/env/v9 v9.0.0      <-- duplicated require line
+	github.com/caarlos0/env/v6 v6.10.1
-	github.com/vbauerster/mpb/v8 v8.6.2
-	github.com/vbauerster/mpb/v8 v8.6.2    <-- duplicated require line
+	github.com/vbauerster/mpb/v4 v4.12.2
```
i.e. the pre commit bumped go.mod (with duplicate requires) without updating the imports, so *every* build/vet/test in the module fails before it compiles anything. Grade on exit code — all three legs emit the same generic message, none of them reaches the mpb/yaml.v2 compile error.

### POST state
- `go mod tidy -diff` -> exit 0 (module graph consistent again)
- `go test ./vars/ -count=1` -> `ok  github.com/concourse/concourse/vars  0.163s` (exit 0)
- `go vet ./fly/... ./topgun/k8s/` and `go build ./...` -> **could not be evaluated**: the host ran out of disk mid-compile.
```
# github.com/aws/aws-sdk-go/service/ssm
compile: writing output: write $WORK/b684/_pkg_.a: no space left on device
github.com/concourse/concourse/atc/db: open .../vet.cfg: no space left on device
```
`df` at that moment: 279Mi available on /System/Volumes/Data (100% full). This is a host condition, not a case defect.

### Environment requirement to finish this case
Needs roughly **5-8 GB free disk** (cold GOCACHE for the k8s client-go + aws-sdk dependency graph at these SHAs) plus network/warm GOMODCACHE for the upstream module versions (mpb/v4, caarlos0/env/v6, …). Network resolution itself worked here.

- corrected_cmd: none — the recorded commands are right; only the heavy two need a machine with disk headroom.

## Fixup 2026-07-25

Curator-fixup pass over the two audit verdicts (opus `borderline`, sonnet
`fail`). Every audit item is resolved below into one of: dissolved by the
exposure contract, real defect (fixed), difficulty recalibration, known leak
channel. Residual verdict: **pass**.

### Dissolved by the exposure contract — no action, deliberately

The exposure contract (`bench/schema/benchmark-case-v1.md` §"Semantics that
matter") says the solver sees exactly `pre_state − withheld + task/`;
`case.yaml`, `notes.md`, `ground_truth/` and the case id/path are harness-side
and never exposed. All three of sonnet's FAIL grounds are in that harness-side
set:

1. **Title states the resolution verbatim** ("port fly to mpb v8, topgun to
   caarlos0/env v9, and decline yaml.v3") — dissolved. Title **not** changed:
   a manifest title that names the answer is a feature, it makes the corpus
   index readable.
2. **`grading.fail_to_pass[1].note` gives away the yaml decision and cites the
   post-cut in-branch revert SHA `091b31d875`** — dissolved. Grading configs may
   state the answer freely; the note is what tells a future runner *why* the
   `vars` gate is the yaml discriminator. Left verbatim.
3. **`grading.fail_to_pass[0].note` names the trap count and location**
   ("seven mpb call sites AND the test-only caarlos0/env site in
   `topgun/k8s/k8s_suite_test.go`") — dissolved, same reason. Left verbatim.

Nothing was renamed or retitled. Sonnet's own reading of the exposed content —
"task/task.md is well-specified and non-leading, treats all three bumps
symmetrically, never says which must be declined" — stands, and after the edits
below it is more true, not less.

### Real defects — fixed

4. **`task/task.md`: pre-announced the `go mod tidy` trap** (opus). The
   paragraph explained the mechanism the primary grading invariant exists to
   detect. Rewritten to state the *requirement* without the mechanism:

   - was: "Be aware that a bare `go mod tidy` makes the build pass by putting
     the **old** majors back into `go.mod` — that is the opposite of what this
     PR is for. When this work is finished, the superseded major versions must
     be gone from `go.mod`, not restored."
   - now: "The point of this PR is that the versions actually move forward.
     When the work is finished the superseded major versions must be gone from
     `go.mod`, not restored — a tree that is green because it is back on the old
     majors is not a fix."

   Authenticity preserved (a maintainer opening this PR would absolutely say the
   versions have to move) and the anti-revert grep stays fair, because the
   outcome it checks is still stated in the trigger. What is gone is the
   narration of *how* an agent would accidentally revert.

5. **`task/task.md`: pre-announced the test-only site** (opus). Deleted the
   parenthetical "(Not everything in this repo is reachable from `go build` —
   some packages are test-only, and they are part of the gate.)" from the "Done
   means" list. `go vet ./...` stays in the list, so the acceptance criteria
   still reach `topgun/k8s`; an agent that validates only with the `go build`
   headline now fails item 3 on its own merits. Solvability re-checked: the
   trigger still hands over the three version moves, the exact toolchain error,
   the four acceptance gates and the no-test-edits constraint — nothing needed
   to solve the case was removed, only two warnings.

6. **Decline had no defined delivery channel.** The task said "record why in
   the change", which a judge could read as satisfied by a chat reply. Task now
   asks for the reason "in the change itself — in the repository, where the next
   person to hit this will find it, not only in a reply to this ticket", and
   `ground_truth/rubric.md` item 10 was rewritten to match: the bot rule plus a
   reason recorded anywhere durable in the tree (rule `_context`, commit message,
   or a short note file at repo root) all count; chat-only is a miss. This is
   the sub-decision channel only — `ground_truth.outcome` here is `merged`, not
   a negative case.

7. **`ground_truth/rubric.md`: judge instructions added.** (a) Credit causal
   reasoning from evidence over recall — mandatory here because
   `memorization_risk: high` and the mpb v4→v8 delta is public documented
   knowledge; a correct-but-unevidenced answer still passes the Must items but
   the judgement must say so. (b) Do not credit the agent for warnings it was
   never given: items 2 and 3 are now genuine discoveries after edits 4-5.

8. **Branch reachability** (opus's third flag). The answer commit
   `57e50564b6` and the `renovate/all` branch are reachable from
   `upstream/master` in any clone of `concourse/concourse`, so a clone-and-
   checkout materialization puts the full answer one `git log --all` away. The
   warning existed only in prose here; it is now a machine-readable obligation,
   `pre_state.repository.materialization: archive-tree` (inline extension, same
   status as `baseline_ref`), with the rationale as a comment.

9. **Grading caveats recorded in `case.yaml#grading.caveats`**: the narrowed
   `go vet ./fly/... ./topgun/k8s/` versus the task's `go vet ./...` (a cost
   concession, not a narrower spec); `fail_to_pass` being free at pre_state and
   therefore not evidence of difficulty, with the instruction to report the
   anti-revert grep explicitly in any results table; no gate pins a fix
   location (packages only — file choice is left open and judged from
   `rubric.md`, items 5-6 judge-only since `fly/ui/progress` has no tests); and
   that results against the pre-fixup task text are not comparable.

10. **Internal inconsistency in `notes.md`**: two `## Validation` headings, the
    first declaring `unvalidated` while `case.yaml` and the second section say
    `partial`. First corrected to `partial`, second demoted to
    `### Validation run — 2026-07-25 (mechanical)` so the `notes.md#validation`
    anchor is unambiguous. Manifest dates re-checked against git and found
    consistent: `git log -1 --format=%cI 45bf8658bf` → `2023-10-16T21:00:57+00:00`,
    exactly `information_cut`; no reframing needed.

11. **Affordance list corrected.** Items 3 and 4 of "Deliberate solvability
    affordances" now carry a REMOVED marker pointing here, with the original
    defence kept as the record of a reversed decision rather than silently
    deleted.

### Difficulty recalibration — considered, held at `moderate`

Opus's leading-text finding is implicitly a difficulty argument (the trigger
made the case easier than the artifact was). Removing both affordances raises
effective difficulty and argues for `hard`. Against: the exact three version
moves are still handed over, the anti-revert *intent* is still stated as an
acceptance criterion (deliberately — grading a requirement the trigger never
states measures prompt-luck), and `memorization_risk: high` means a model may
recall the mpb API delta outright rather than derive it. `moderate` is the label
that survives both arguments; recorded as a comment above the field so the next
pass does not relitigate it blind.

### Known leak channels — none

The project auto-memory on this machine is entirely jetbridge/Concourse-K8s
material and says nothing about `mpb`, `caarlos0/env`, `gopkg.in/yaml`,
Renovate or upstream PR #8843. No `known_leak_channels` entry added. The
case's real exposure risk is the declared `memorization_risk: high` (public
October-2023 repo), which is a weights channel, not an operator-environment one.

### Priced-deflator in-tree docs — none exist

`withheld: []` is confirmed correct and unchanged: the pre_state tree has no
`docs/` directory at all, and `.github/renovate.json` at the cut carries only
the pre-existing `client-go` rules with no yaml mention. The judge instruction
to credit reasoning from evidence rather than quotation was added anyway (edit
7), because the memorization channel here does the same damage a spoiler doc
would.

### Files touched

- `bench/corpus/upgrade-cc-002/task/task.md` — three edits (4, 5, 6). This is
  exposed content; it changed before any results exist against the case, which
  `bench/README.md#corpus-versioning` permits. Any future rerun must cite a
  corpus commit at or after this fixup.
- `bench/corpus/upgrade-cc-002/ground_truth/rubric.md` — judge instructions (7),
  item 10 rewritten (6).
- `bench/corpus/upgrade-cc-002/case.yaml` — header comment replaced, `materialization`
  (8), `grading.caveats` (9), difficulty rationale comment, `curation.learnings`
  lessons (5) and (6), `leakage_audit` curator-fixup entry.
- `bench/corpus/upgrade-cc-002/notes.md` — this section, plus (10) and (11).

No repository state outside this case directory was touched; git was used
read-only (`git log -1 --format` on the pre_state SHA).
