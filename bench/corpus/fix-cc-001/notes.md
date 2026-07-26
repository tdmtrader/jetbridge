# Curation record — fix-cc-001

## Provenance walk

Backed out of a merged upstream Concourse pull request. Every SHA below was
resolved in this repository (the jetbridge fork keeps `upstream/*` remote refs,
so the objects are local); git was used read-only throughout — no checkout, no
worktree, no fetch.

| Role | SHA | Date | Subject |
|---|---|---|---|
| terminal artifact (merge) | `0e6b0c62d7fd86729f0e62d05bddb18ae83f7d7a` | 2025-11-24T12:08:15-05:00 | `Merge pull request #9362 from aeijdenberg/fix/gh-9356` / `fix(set_pipeline): evaluate InstanceVars ahead of other variables` |
| fix commit (2nd parent) | `a2e2367cb8161ad47ba31311e2152a7c2ef6ebe0` | 2025-11-17T11:40:40+11:00 | `fix(set_pipeline): evaluate InstanceVars ahead of other variables during set_pipeline` |
| withheld-test commit | `ef5857d889bf91bb7e2206095669fd0d47b99df2` | 2025-11-17T11:38:39+11:00 | `test(set_pipeline): add test for instance vars overriding other vars` — body: *"This test demonstrates the current broken behaviour."* |
| **pre_state** (test commit's parent) | `7105d31847abb17e0627244900561f0068f3217b` | 2025-11-16T16:34:24-05:00 | `Merge pull request #9343 from ramonskie/xhr` (unrelated web change) |
| merge 1st parent (mainline) | `b314b5ca837e4dacd25e4333bf1d80c1d90aeda5` | 2025-11-24T12:06:29-05:00 | `Merge pull request #9366 … renovate/go-golang.org-x-crypto-vulnerability` |

Verification performed:

- `git log -1` on the merge confirms the candidate's claim verbatim: PR #9362,
  branch `aeijdenberg/fix/gh-9356`, subject
  *"fix(set_pipeline): evaluate InstanceVars ahead of other variables"*.
- `git log -1 --format=%P` on the merge yields `b314b5c a2e2367`, so the fix
  commit is the second parent. `git rev-parse a2e2367^` → `ef5857d`, and
  `git rev-parse ef5857d^` → `7105d31`. The chain the candidate described is
  exactly the chain in the object store; there are no intervening commits.
- `git show --stat` per commit: the test commit is `atc/exec/set_pipeline_step_test.go`
  only (+3/−2); the fix commit is `atc/exec/set_pipeline_step.go` only (+6/−4).
  `git diff --stat 7105d31 a2e2367` is those two files and nothing else — no
  docs companion, no version bump, no CHANGELOG, nothing to strip.
- The merge introduced no further edits: `set_pipeline_step.go` and
  `set_pipeline_step_test.go` are byte-identical at `a2e2367` and at
  `0e6b0c62` (`diff` of `git show` output, both empty).
- `git branch -a --contains 0e6b0c62` lists `upstream/master`,
  `upstream/release/8.0.x` and `upstream/release/8.1.x` — merged and shipped, so
  `ground_truth.outcome` is `merged`.
- `git ls-tree 7105d31 bench/` is empty. The pre_state long predates this
  corpus, so the schema's self-hosted-corpus caveat is satisfied.

**The defect, confirmed by reading the pre_state source** (not taken on the
candidate's word):

`atc/exec/set_pipeline_step.go`, `setPipelineSource.FetchPipelineConfig` builds
`staticVars []vars.Variables` by appending `plan.Vars` (line 270), then one entry
per `plan.VarFiles` (line 283), then `plan.InstanceVars` (line 287), and hands
the slice to `vars.NewTemplateResolver(config, staticVars).Resolve(false)`.
`vars/template_resolver.go` passes it straight to `NewMultiVars`, whose `Get`
(`vars/multi_vars.go`) loops the sources and **returns on the first one that has
the key**. Earliest wins; instance vars were appended last, so they lost. The
fix moves the six-line `InstanceVars` block above the `plan.Vars` block. The
`maps.Clone` defensive copy is carried along unchanged, and `maps` is already
imported at pre_state, so the change needs no import edit.

**Why the fail-to-pass lands where it does** (traced through the step, since the
withheld test adds no new spec). The withheld commit edits shared fixtures: the
`pipelineContent` const now renders `args: [((branch))]`, `pipelineObject` now
expects `Args: []string{"feature/foo"}`, and the outer `spPlan` `BeforeEach`
gains `Vars: {"branch": "feature/this-should-be-overridden-by-instance-var-with-same-name"}`
alongside the pre-existing `InstanceVars: {"branch": "feature/foo"}`. At
pre_state the step therefore renders the *step-var* value, so
`existingConfig.Diff(stdout, atcConfig)` returns true and `Run` takes the save
path instead of the no-diff path. Three long-standing specs in the
*"when specified pipeline exists already / when no diff"* context flip red:
`should log 'no changes to apply'` (the string is only printed on the no-diff
path), `should send a set pipeline changed event` (expects `changed == false`;
the save path passes `true`), and `should update the job and build id`
(`SetParentIDs` is only called on the no-diff path). Recorded in
`case.yaml#grading` because a grader looking for a spec named after instance
vars would find none and wrongly conclude nothing ran.

**Pre-state coherence.** `7105d31` is an unrelated mainline merge (an XHR change
in the web UI) that landed ~19 hours before the contributor's branch. It touches
nothing in `atc/exec` or `vars/`, so the tree is a plausible "the day before
someone noticed" snapshot.

## Leakage analysis

`withheld: []` — nothing at pre_state gives the answer away.

Checks run against the pre_state tree (all `git grep <pattern> 7105d31`, no
checkout):

- `-iE "instance[_ ]?vars?"` over `*.md`: **zero hits**. Upstream Concourse keeps
  its user documentation in a separate repository (`concourse/docs`), so the
  in-tree prose that this corpus normally has to worry about simply does not
  exist here. The whole tree has 25 `.md` files, all boilerplate (`CONTRIBUTING`,
  `SECURITY`, per-package `README`s, issue templates).
- `-iE "(precede|precedence|override|takes priority|wins)"` over `*.md`, filtered
  to lines mentioning vars: zero hits.
- `InstanceVars` in `atc/exec/` at pre_state: three files —
  `set_pipeline_step.go` (the defect), `set_pipeline_step_test.go` (pre-fixture
  version), `step_metadata.go`. No commentary about precedence anywhere.
- `staticVars` across the tree: only the five lines in the defective function
  plus unrelated locals in `fly/commands/internal/templatehelpers` and
  `vars/template_resolver_test.go`.

**Deliberately *not* withheld** (recorded because they look like leaks and are
not): `vars/multi_vars.go`, its doc comment on `NewTemplateResolver`
(*"they will be tried for variable lookup in the provided order"*),
`vars/multi_vars_test.go`'s spec *"return found value as soon as one source
succeeds"*, and `vars/template_resolver_test.go`'s pair of specs that evaluate
`{staticVars, staticVars2}` and `{staticVars2, staticVars}`. These state the
resolution semantics an agent must discover — they are the evidence the task is
*about*, not a statement of the answer. None of them mentions instance vars or
`set_pipeline`; knowing that the slice is first-wins is necessary but not
sufficient (you still have to find that instance vars are appended last, and
decide that they ought to win). Withholding them would have made the case
unsolvable-by-reasoning rather than harder.

**The wrong fix the gate must reject.** Inverting `vars.MultiVars.Get` to
last-source-wins turns the withheld specs green while reversing precedence for
`atc/exec/task_config_source.go`, `atc/db/pipeline.go:1120` and `fly`. Verified
that `go test ./vars/` catches it: `vars/multi_vars_test.go` asserts *"return
found value as soon as one source succeeds"* and that the later source's `Get` is
never called. Also verified that `fly/commands/internal/templatehelpers/yaml_template.go`
depends on first-wins by construction — it walks `--load-vars-from` in reverse so
later flags land earlier in the slice — though its own specs do not assert
multi-file precedence, so that leg is a weaker guard. Both are wired into
`pass_to_pass` and both are called out in `case.yaml#grading` CAVEAT 2.

**Grading test.** `atc/exec/set_pipeline_step_test.go` exists at pre_state, but
its post-cut form is the oracle. It lives only in `ground_truth/` — as a patch
(`test.diff`) and as the full post-state file
(`withheld_tests/atc/exec/set_pipeline_step_test.go`, byte-identical at
`ef5857d`, `a2e2367` and the merge, verified) so a grader can restore it
verbatim.

**What I deliberately withheld beyond the commit** (per the schema's honesty
requirement):

- *The issue and PR numbers.* The real trigger was
  `concourse/concourse#9356`, and the branch name is `fix/gh-9356`. Naming either
  in `task.md` would be a direct memorization handle for a public 2025 artifact.
  `task.md` carries no issue reference.
- *The upstream commit subjects.* `fix(set_pipeline): evaluate InstanceVars ahead
  of other variables` is a one-line statement of the answer, and the test
  commit's body (*"demonstrates the current broken behaviour"*) frames the
  fixture edit as an oracle. Neither appears anywhere in `task/`.
- *The fixture var value.* The withheld test names its step-level var
  `feature/this-should-be-overridden-by-instance-var-with-same-name`, which
  states the expected semantics outright. It is unavoidable in the oracle, but it
  is in `ground_truth/` only and no part of it bled into `task.md`.
- *Locality.* `task.md` never mentions `staticVars`, `TemplateResolver`,
  `MultiVars`, append order, or the word "precedence". It names the component
  (`the in-build set_pipeline step`), which any real report would, and stops.

`task.md` is a reframing, not a transcript: I could not read issue #9356 (no
network was used for this build), so the report is written as the bug report an
operator running instanced pipelines would have filed for this symptom. The
`across`-step fan-out framing, the two-day silent-wrong-branch detail and the
`fly pipelines` observation are invented-but-faithful colour — they restate the
symptom and the requested behavior and add no information the fix depends on.
Everything technical in it (the rendering, the identity being correct, the
absence of any error) was verified against the pre_state source.

**Memorization.** `memorization_risk: high`, and unusually so even for an
upstream case: public repo, November 2025, well inside the model's training
window; the answer is a six-line move; the commit subject is a sentence-long
statement of it. Per `bench/README.md` this case must never anchor an efficacy
claim on its own. `ground_truth/rubric.md` § "Pricing the memorization risk"
tells the judge to credit the derivation over the edit.

**Operator environment.** `~/.claude/projects/-Users-tdmtrader-concourse-concourse/memory/`
and the repo `CLAUDE.md` were grepped for
`instance_vars|instancevars|set_pipeline|9356|9362|template ?resolver|multivars`.
Two hits, both unrelated: an Elm route note about `?vars.run=N` on instanced
pipelines and a design line about layering pipeline runs over instance vars.
Neither states anything about variable precedence, so `known_leak_channels` is
**not** set. The general README rule still holds — replay harnesses must not
mount project memory or session history.

## Open questions

- **Is the task's stated expectation too much of a gift?** `task.md` says
  outright that instance vars must win. That is the *request*, not the fix — the
  work is establishing which end of the `staticVars` slice is the winning end,
  which is two files away and counter-intuitive (the common reading of `append`
  is that later entries override). But it does mean an agent cannot get the
  direction wrong by disagreeing with the requirement, only by mis-reading the
  code. If pilot runs pass near-100%, the lever to pull is a variant task that
  states only the symptom ("all our instances get the same config") and leaves
  the desired precedence to be argued — not a rewrite of this one.
- **The `pipelineObject` fixture is mutated across specs.** The
  *"when there are some diff"* context assigns
  `pipelineObject.Jobs[0].PlanSequence[0].Config.(*atc.TaskStep).Config.Run.Args`
  in a `BeforeEach`, permanently mutating a `Describe`-scoped `var` that the
  earlier *"when no diff"* context depends on. Ginkgo's default randomization is
  top-level-container-only, so ordering holds today, but a harness that runs with
  `--randomize-all` will see this case behave differently. Pre-existing upstream
  fragility, inherited by the oracle; flagged so it is not misread as a case
  defect.
- **Difficulty is direction-bound, not localization-bound.** Unlike fix-jb-001
  (where the work was finding the one bad call site), here the file is obvious
  the moment you read the task; the work is deciding which way to move the block
  and being right about why. Graded `moderate`. Rubric item 6 exists precisely so
  a lucky-but-unreasoned pass is visible in the score.
- **Sibling candidate not built.** The candidate mining pass proposed an easier
  variant with `pre_state = ef5857d` (the test commit), where the failing test is
  already in the tree and the task is "the suite is red, fix it". The curator
  chose the harder cut. If the corpus later wants a reproduce-then-fix or a
  red-suite-triage shape, that variant is free to build from the same walk.

## Validation (build-time record — SUPERSEDED, see "## Validation" at end of file)

- date: (not run)
- validator: —
- outcome: **unvalidated**

Not run at build time. The dev machine's root volume hit 100% full mid-build
(`ENOSPC` on every write, including the tool harness's own scratch files, which
briefly made all Bash and Write calls fail); ~244 MB was free once it recovered.
`go test ./atc/exec/` against a Nov-2025 upstream dependency graph would need
module downloads plus build-cache growth well beyond that, and filling the volume
again would break sibling processes on this machine. Materialization was
therefore abandoned deliberately rather than attempted and failed — no `go`
command was ever run against this case.

What that means for the record: the fail-to-pass transition below is **reasoned
from source, not observed**, and the validator must confirm it.

Commands for the validation pass (repo = this checkout, which has the upstream
objects; materialize with `git archive`, never checkout/worktree):

```
# tree A — pre_state
git archive 7105d31847abb17e0627244900561f0068f3217b | tar -x -C <A>
# tree B — pre_state + withheld oracle
git archive 7105d31847abb17e0627244900561f0068f3217b | tar -x -C <B>
git show ef5857d889bf91bb7e2206095669fd0d47b99df2:atc/exec/set_pipeline_step_test.go \
  > <B>/atc/exec/set_pipeline_step_test.go
# tree C — reference (fix commit)
git archive a2e2367cb8161ad47ba31311e2152a7c2ef6ebe0 | tar -x -C <C>

(cd <A> && go test ./atc/exec/ ./vars/ ./fly/commands/internal/templatehelpers/ -count=1)  # expect PASS
(cd <B> && go test ./atc/exec/ -count=1)                                                    # expect FAIL
(cd <C> && go test ./atc/exec/ ./vars/ ./fly/commands/internal/templatehelpers/ -count=1)  # expect PASS
```

Expected FAIL detail to check in tree B (this is the prediction to falsify):
three specs red in `SetPipelineStep … when specified pipeline exists already …
when no diff` — `should log 'no changes to apply'`, `should send a set pipeline
changed event`, `should update the job and build id`. A red run that names *other*
specs, or a green run, invalidates the analysis above and the case should be
re-cut rather than patched.

Each tree is ~35 MB uncompressed. Needs ~1–2 GB free for the module and build
caches; go 1.25.x (go.mod declares `go 1.25.0`; go1.25.6 darwin/arm64 is the
toolchain on this machine). No Postgres, no cluster.

---

## Validation

- date: 2026-07-25
- validator: merger agent (fix-cc-001), read-only git + `git archive` materialization
- outcome: **partial** — the fail_to_pass transition is now **observed**, exactly as
  predicted; one pass_to_pass leg (`go build`) is **environment-blocked** by disk,
  not failing.

This supersedes the build-time "(not run)" record above. The prediction that
record asked a validator to falsify **held in every particular** — same three
spec names, same context, no other specs red.

### Method (deviations from the prescribed recipe, and why)

The build-time recipe called for three trees (A = pre, B = pre + oracle,
C = reference). **One tree was used instead**, with the two files flipped in
place. This is exact, not an approximation: `git diff --stat 7105d31 a2e2367`
is `atc/exec/set_pipeline_step.go` + `atc/exec/set_pipeline_step_test.go` and
nothing else, so pre_state with both files replaced by their `a2e2367` blobs
*is* post_state byte-for-byte (verified per file with `diff` against
`git show a2e2367:<path>` — both OK). This mattered: it kept peak disk to one
39 MB tree and let the dependency closure compile once instead of three times,
which is the only reason the run completed at all.

Materialized with `git archive 7105d31… | tar -x` into
`/private/tmp/bench-fix-cc-001/tree`. No checkout, no worktree, no fetch; every
other git call was `show`/`diff`/`log`/`cat-file`. Tree deleted afterwards.

Two further deviations, both recorded because they are load-bearing for anyone
reproducing this:

1. **`-ldflags='-s -w'` on the post-fix `atc/exec` run.** The unstripped
   `atc/exec.test` binary could not be linked at ~198 MB free
   (`link: mapping output file failed: no space left on device` — the linker
   mmaps the whole output). Stripping DWARF/symbol tables is semantically inert
   for test outcomes and made it fit. The pre-state runs of the same package
   linked unstripped when ~285 MB was free. **This is a disk artifact, not a
   property of the case.**
2. **`GOFLAGS=-p=2`** throughout, to slow the rate of build-cache growth so a
   disk watchdog could react. Also inert.

### Legs, commands, outcomes

All commands run with CWD = the materialized tree.

| # | Leg | Command | @ pre | @ post |
|---|---|---|---|---|
| 1 | fail_to_pass (oracle overlaid) | `go test ./atc/exec/ -count=1` | **FAIL** (3 specs) | **PASS** (474/474) |
| 2 | pass_to_pass | `go test ./atc/exec/ -count=1` | **PASS** | **PASS** |
| 3 | pass_to_pass | `go test ./vars/ -count=1` | **PASS** | **PASS** |
| 4 | pass_to_pass | `go test ./fly/commands/internal/templatehelpers/ -count=1` | **PASS** | **PASS** |
| 5 | pass_to_pass | `go build ./atc/... ./vars/... ./fly/...` | **blocked** | **blocked** |

Note on legs 1 and 2 at post: at `a2e2367` the oracle *is* the in-tree test file,
so the two legs are the same command on the same tree and the single green run
satisfies both.

### Evidence (trimmed)

Overlay applied exactly as `case.yaml#grading` prescribes
(`git show ef5857d…:atc/exec/set_pipeline_step_test.go > …`), and confirmed
byte-identical to `ground_truth/withheld_tests/atc/exec/set_pipeline_step_test.go`.
The three-part fixture edit is precisely what the curation record describes:

```
53c53  <          - hello              >          - ((branch))
72c72  <  Args: []string{"hello"}      >  Args: []string{"feature/foo"}
184a185                                >  Vars: map[string]any{"branch": "feature/this-should-be-overridden-by-instance-var-with-same-name"},
```

**Leg 1 @ pre_state — red, and red in exactly the predicted place:**

```
Summarizing 3 Failures:
  [FAIL] SetPipelineStep when file is configured when pipeline file is good
         when specified pipeline exists already when no diff
         [It] should log 'no changes to apply'          set_pipeline_step_test.go:326
  [FAIL] … [It] should send a set pipeline changed event  set_pipeline_step_test.go:332
  [FAIL] … [It] should update the job and build id        set_pipeline_step_test.go:336

Ran 474 of 474 Specs in 0.577 seconds
FAIL! -- 471 Passed | 3 Failed | 0 Pending | 0 Skipped
FAIL	github.com/concourse/concourse/atc/exec	0.848s
```

**Leg 1 @ post_state — green, whole suite:**

```
Ran 474 of 474 Specs in 0.578 seconds
SUCCESS! -- 474 Passed | 0 Failed | 0 Pending | 0 Skipped
ok  	github.com/concourse/concourse/atc/exec	0.822s
```

The applied fix was diffed against `ground_truth/reference.diff`: hunks match
exactly.

**Legs 2–4:**

```
@pre  (no overlay) ok  github.com/concourse/concourse/atc/exec   0.843s
@pre  ok  github.com/concourse/concourse/vars                                        0.167s
@pre  ok  github.com/concourse/concourse/fly/commands/internal/templatehelpers       0.162s
@post ok  github.com/concourse/concourse/vars                                        0.164s
@post ok  github.com/concourse/concourse/fly/commands/internal/templatehelpers       0.158s
```

**Leg 5 — environment-blocked, NOT a failure:**

```
github.com/concourse/concourse/atc/builds: mkdir …/T/go-build494319814/b706/: no space left on device
# github.com/concourse/concourse/atc/api
compile: writing output: write $WORK/b263/_pkg_.a: no space left on device
… (≈20 packages, all ENOSPC)
```

Every message is `no space left on device`; not one is a compile diagnostic.
Building all of `./atc/... ./vars/... ./fly/...` stages hundreds of packages
through `$WORK` before caching and needs on the order of 1 GB of transient
space. It was abandoned. Partial corroboration exists: legs 1–4 compile
`atc/exec`, `vars`, `fly/commands/internal/templatehelpers` and their closures
green at both ends, and the reference fix touches one file inside `atc/exec`, so
a broad compile break is implausible — but it is **not observed**, and a
validator with disk should run it before promoting this case to `validated`.

### Corrections to `case.yaml#grading.environment`

Two claims there are wrong or overstated, discovered by running it. Left in
place (the merge brief scoped edits to `leakage_audit` and `validation`), but a
future editor should fix them:

- **Toolchain.** The note says "validated toolchain on this machine is go1.25.6
  darwin/arm64". Not so for this tree. The corpus tree's `go.mod` declares
  `go 1.25.0`, which the host toolchain already satisfies, so **no `GOTOOLCHAIN`
  switch happens** and the build runs under Homebrew **go1.25.1 darwin/arm64**
  (visible in the linker path, `/opt/homebrew/Cellar/go/1.25.1/libexec/…`).
  go1.25.6 is what the *jetbridge worktree* selects, because its own `go.mod`
  demands newer — a different module, not this one. All results above are
  go1.25.1.
- **`network_required: true` is overstated on a warm machine.** Of the 254
  `require` entries in pre_state's `go.mod`, **252 were already in `GOMODCACHE`**;
  the only two absent were `github.com/containerd/containerd@v1.7.29` and
  `github.com/containerd/containerd/v2@v2.1.5`, both Linux-only and needed by
  none of these legs on darwin. **No network was used and no module was
  downloaded.** The flag is right for a cold machine and should stay, but the
  practical prerequisite is a warm cache, not connectivity.

### Operational warning for the next validator

The disk hazard the build-time record describes is real and this run hit it
three times. The volume held **239 MB free at start** and the run drove it to
**0**, at which point the *tool harness itself* could not open its output files
and every Bash call failed until orphaned `T/go-build*` temp dirs and the tree
were removed (recovered to ~220 MB). Guidance:

- Do not attempt this with under **~2 GB** free. The whole matrix is comfortable
  there and needs none of the workarounds above.
- Run legs cheapest-first (`./vars/`, then templatehelpers, then `atc/exec`,
  `go build` last). `./vars/` cost 2 MB and is a good warm-cache probe.
- Free space is *not* monotonic: a failed link releases its partial output, so a
  retry after an ENOSPC often succeeds where the first attempt did not (leg 4
  did exactly this).
- Delete the materialized tree as soon as the last leg finishes.

### What remains unproven

- Leg 5 (`go build` breadth) — environment-blocked, above.
- The `--randomize-all` fragility flagged under "Open questions" (the
  `pipelineObject` mutation shared across contexts) was **not** probed; this run
  used Ginkgo's default ordering, under which all 474 specs pass at post. That
  open question stands.
