# Node Authoring Friction Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the two real obstacles to authoring a `repository-change/v1`, write down the two rules nothing states, and make every mechanically gradable `small-fix` corpus case actually gradable.

**Architecture:** Two waves. Wave 1 touches only docs, the sealed corpus, and the out-of-tree bench harness — nothing that runs in the cluster, so it lands with zero deploy risk. Wave 2 changes ATC and the output builder and carries a single two-image build/deploy, followed by live proofs.

**Tech Stack:** Go 1.25 (root module + `bench/harness` as a separate module), YAML corpus fixtures, `fly` CLI, Kubernetes/Helm on theborg.

**Spec:** `docs/superpowers/specs/2026-08-04-node-authoring-friction-design.md`

---

## Before you start

Read the spec's "Scope correction" section. Three of four originally-reported
issues did not exist; they were reported from inference instead of from running
one command. **Before implementing any task, re-verify its premise.** If a task's
premise turns out to be false, stop and say so rather than building it.

Environment:

- PostgreSQL must be running for root-module unit/integration tests: `pg_isready`.
- Never pass `--race` to `make test-unit` — it breaks parallel compilation.
- `bench/` is outside the root module. Its tests run via `make test-bench-harness`.
- This machine has no Docker daemon. Any Docker step goes through
  `./hack/borg-docker.sh` (see `CLAUDE.md`).
- Work directly on branch `jetbridge` in `/Users/tdmtrader/concourse/concourse`.
  Twelve unrelated files are already dirty there (`.dockerignore`, `AGENTS.md`,
  `CLAUDE.md`, `CONTRIBUTING.md`, `JETBRIDGE.md`, `Makefile`, `README.md`,
  `TESTING.md`, `docs/local-dev.md`, `docs/platform-guide.html`, plus untracked
  `.claude/launch.json`, `docs/docker-on-theborg.md`, `hack/borg-docker.sh`).
  **Never `git add -A` or `git commit -a`.** Stage explicit paths only.

## File Structure

**Wave 1**

| File | Responsibility |
|---|---|
| `bench/nodes/FIRST-USER-2026-08-04.md` | Modify — delete the two false findings |
| `docs/operations/reusable-node-definitions.md` | Modify — input mutability rule; sealing a historical revision |
| `bench/harness/casespec/grading.go` | Modify — parse the normalized `withheld_tests` shape |
| `bench/harness/casespec/grading_test.go` | Create — pin the parser against real corpus cases |
| `bench/harness/corpus_shape_test.go` | Create — pin the normalized grading shape across every case |
| `bench/harness/cmd/fixgrade/main.go` | Modify — pass_to_pass restores, negative cases, new shape |
| `bench/harness/cmd/fixgrade/main_test.go` | Modify — cover the above |
| `bench/corpus/fix-*/case.yaml`, `bench/corpus/neg-*/case.yaml` | Modify — normalize grading |
| `bench/corpus/*/ground_truth/withheld_tests/**` | Create — four specs never shipped |
| `bench/corpus/INDEX.md` | Modify — record the version bump; fix the 13/14 count |
| `fly/commands/agent_experiments.go` | Modify — node variant grammar + `--param` |
| `fly/commands/agent_experiments_test.go` | Modify — cover the grammar |

**Wave 2**

| File | Responsibility |
|---|---|
| `agent/outputbuilder/authority.go` | Modify — `InputAuthority.IntrinsicMetadata` |
| `agent/outputbuilder/builder.go` | Modify — `InputDescription.IntrinsicMetadata` |
| `agent/outputbuilder/outputbuilder_test.go` | Modify — describe surface |
| `atc/exec/output_builder_authority.go` | Modify — populate from the snapshot manifest |
| `atc/exec/output_builder_authority_test.go` | Modify/Create — propagation + size cap |
| `agent/snapshot/sealer.go` | Modify — declared bases on the direct-create path |
| `agent/api/snapshots/handler.go`, `types.go` | Modify — accept and authorize declared bases |
| `fly/commands/agent_snapshots.go` | Modify — `--base NAME=ID` |
| `bench/nodes/small-fix/prompts/fix.md` | Modify — drop the hash recipe |

---

# WAVE 1 — no deploy

## Task 1: Delete the two false findings from the findings doc

The doc currently claims node runs cannot be listed and that experiments cannot
target a node. Both are false, and leaving them there will send the next reader
building things that exist.

**Files:**
- Modify: `bench/nodes/FIRST-USER-2026-08-04.md`

- [ ] **Step 1: Verify the premise before editing**

```bash
fly -t home agent nodes runs small-fix 1
fly -t home agent experiments add-variant --help 2>&1 | tail -4
```

Expected: `runs` prints a table of runs. `add-variant`'s VARIANT argument
documents only `label=workflow@version` and `label=workflow@version#function-id`.

- [ ] **Step 2: Replace the run-ID bullet in §7**

Find the bullet beginning "**A node run's ID is only in `run --json`.**" and
replace the whole bullet with:

```markdown
- **Node run discovery exists; I failed to look for it.** `fly agent nodes runs
  NAME VERSION`, `show-run`, and `cancel-run` all ship, backed by
  `ListAgentNodeRuns` / `GetAgentNodeRun` / `CancelAgentNodeRun`
  (`atc/routes.go:396-398`). I recorded "there is no way to list runs" and "there
  is no cancel" from an eight-week-old note instead of running `--help`. Both
  claims were false. The only real gap is discoverability: `show-run` demands a
  RUN-ID and its error does not mention that `runs` will list them.
```

- [ ] **Step 3: Narrow the experiment claim in §6**

Find the sentence in the code-review section reading "Any claim about a judgment
node's configuration needs repeats" and leave it. Then find the §7 bullet or §9
item referencing experiments and ensure the only experiment claim in the document
is this one — add it to §7 if absent:

```markdown
- **Node A/B is first-class already; only fly cannot spell it.**
  `experiment.TargetNode` and `Target.NodeParameters` exist, are validated
  (`agent/experiment/types.go:113-148`), bound (`agent/workflowrun/binder.go:756`,
  `experiment_binder.go:53`) and runner-tested (`runner_test.go:599`). The
  hand-rolled shell matrix in §6 was avoidable. What is missing is the fly
  grammar: `add-variant` parses only `label=workflow@version` and
  `label=workflow@version#function-id`, with no way to pass node parameters.
```

- [ ] **Step 4: Commit**

```bash
git add bench/nodes/FIRST-USER-2026-08-04.md
git commit -m "docs(bench): drop two findings that did not survive verification"
```

## Task 2: Document input mutability

**Files:**
- Modify: `docs/operations/reusable-node-definitions.md`

- [ ] **Step 1: Confirm the two facts still hold**

```bash
grep -n "ReadOnly" atc/exec/agent_step.go
grep -rn "OpenInput" agent/snapshot/contracts/*.go | grep -v _test
```

Expected: the only `ReadOnly: true` in `agent_step.go` is the `skills` mount;
the only `OpenInput` caller in contracts is `repository_change.go`.

- [ ] **Step 2: Add the section**

Insert after the "Create or capture exact typed inputs" section:

````markdown
## Writing to inputs

Typed inputs are mounted **writable**, and that is deliberate. A node that
cannot write to its repository input cannot build it, run its tests, or install
anything.

Editing an input cannot affect the sealed snapshot or any other run. The mount
is a per-run copy at `<artifactDaemonHostPath>/steps/<handle>/<subdir>`, keyed
by the step's own container handle, and the artifact daemon materializes it by
byte copy — no hardlinks, so no shared inodes with the cache. (Contrast a `cache`
volume, which is keyed stably by job and step precisely so that it *is* shared.)

**One exception.** An input named as the base subject of a
`repository-change/v1` output is re-read from its mount and re-canonicalized
when the record is written — `repository-change/v1` is the only contract that
reopens input content. If the tree no longer hashes to the digest it was given,
the write fails:

```
output builder: input "repository" canonical digest does not match its authority
```

This is not corruption; the sealed snapshot is untouched and other runs are
unaffected. It means the node broke *its own* output. A node that must both edit
a repository and seal a `repository-change/v1` against it should work in a copy:

```sh
cp -a repository work
chmod -R u+w work
cd work
```
````

- [ ] **Step 3: Commit**

```bash
git add docs/operations/reusable-node-definitions.md
git commit -m "docs(nodes): state the input mutability rule"
```

## Task 3: Document sealing a historical revision

**Files:**
- Modify: `docs/operations/reusable-node-definitions.md`

- [ ] **Step 1: Add the section**

Insert immediately after the section from Task 2:

````markdown
### Sealing a historical revision

`repository/v1` requires complete, non-shallow history. For a clone that means
**every ref**, so a naive clone of an old commit also ships every descendant of
it — including work you may not intend the consumer to see. A sealed repository
carries everything reachable from its refs; pruning is how you control that.

To seal exactly one revision and nothing after it:

```sh
git clone --no-local <source> pre-state
cd pre-state
git branch -f pre-state <TARGET-SHA>
git checkout pre-state
for b in $(git branch --format='%(refname:short)' | grep -v '^pre-state$'); do
  git branch -D "$b"
done
git remote remove origin
git tag -l | xargs -r git tag -d
git for-each-ref --format='%(refname)' refs/remotes | xargs -r -n1 git update-ref -d
git reflog expire --expire=now --all
git gc --prune=now
```

Then assert the prune worked. This check is the point of the procedure:

```sh
git cat-file -e <A-SHA-THAT-CAME-AFTER>   # MUST fail
```

Only then seal it:

```sh
fly -t TARGET agent snapshots create --type repository/v1 --from ./pre-state --json
```

`git clone` writes only `core.*`, `remote.*` and `branch.*` config keys, all of
which the validator's allowlist accepts; removing the remote leaves a clean
config. A 220 MB repository seals in roughly 40 seconds.
````

- [ ] **Step 2: Commit**

```bash
git add docs/operations/reusable-node-definitions.md
git commit -m "docs(nodes): document sealing an exact historical revision"
```

## Task 4: fixgrade — restore withheld specs on pass_to_pass legs

The corpus survey found `fix-jb-003` and `fix-ld-002` declare
`withheld_test_paths` on a **pass_to_pass** leg (an over-correction guard).
`fixgrade` restores only for the fail_to_pass leg, so it runs those guards
against a tree that never got the spec — a wrong result, silently.

**Files:**
- Modify: `bench/harness/cmd/fixgrade/main.go`
- Modify: `bench/harness/cmd/fixgrade/main_test.go`

- [ ] **Step 1: Verify the premise**

```bash
cd bench/corpus && grep -A3 "pass_to_pass:" fix-jb-003/case.yaml | head -8
```

Expected: a `pass_to_pass` leg carrying `withheld_test_paths`.

- [ ] **Step 2: Write the failing test**

Append to `bench/harness/cmd/fixgrade/main_test.go`:

```go
// An over-correction guard is a pass_to_pass leg that needs the withheld spec
// restored (fix-jb-003, fix-ld-002). Running it against a tree that never got
// the spec does not error, it silently reports the wrong thing.
func TestKeepLegRestoresItsOwnWithheldSpec(t *testing.T) {
	caseDir := t.TempDir()
	tree := t.TempDir()
	relative := "agent/harvest/trailer_test.go"
	source := filepath.Join(caseDir, "ground_truth", "withheld_tests", relative)
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("package harvest\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	leg := casespec.Leg{Cmd: "go test ./agent/harvest/", WithheldTestPaths: []string{relative}}
	if err := restoreWithheld(caseDir, tree, leg); err != nil {
		t.Fatalf("restoreWithheld() = %v", err)
	}
	if _, err := os.Stat(filepath.Join(tree, relative)); err != nil {
		t.Fatalf("keep-leg spec was not restored: %v", err)
	}
}
```

- [ ] **Step 3: Run it**

```bash
cd bench/harness && go test ./cmd/fixgrade/ -run TestKeepLegRestoresItsOwnWithheldSpec -v
```

Expected: PASS (`restoreWithheld` is already leg-generic). This test pins the
helper; the actual defect is in the caller, covered next.

- [ ] **Step 4: Give each keep leg its own tree**

In `main.go`, replace the pass_to_pass loop:

```go
	// keep: pass_to_pass on the delivered tree, untouched.
	for index, leg := range passLegs {
		name := "keep"
		if len(passLegs) > 1 {
			name = fmt.Sprintf("keep-%d", index+1)
		}
		result.Legs = append(result.Legs, execLeg(name, leg.Cmd, deliveredTree, true, caseDir, maxOutput))
	}
```

with:

```go
	// keep: pass_to_pass on the agent's tree as delivered. A leg that declares
	// withheld specs is an over-correction guard and needs them restored, so it
	// gets its own copy — a restore must never leak into a later leg or into the
	// delivered tree that other legs read.
	for index, leg := range passLegs {
		name := "keep"
		if len(passLegs) > 1 {
			name = fmt.Sprintf("keep-%d", index+1)
		}
		legTree := deliveredTree
		if len(leg.WithheldTestPaths) > 0 {
			legTree = filepath.Join(workDir, fmt.Sprintf("keep-%d", index+1))
			if err := copyTree(deliveredTree, legTree); err != nil {
				return nil, fmt.Errorf("copy delivered tree for %s: %w", name, err)
			}
			if err := restoreWithheld(caseDir, legTree, leg); err != nil {
				return nil, fmt.Errorf("restore withheld tests for %s: %w", name, err)
			}
		}
		result.Legs = append(result.Legs, execLeg(name, leg.Cmd, legTree, true, caseDir, maxOutput))
	}
```

- [ ] **Step 5: Run the suite**

```bash
cd bench/harness && go test ./... -count=1
```

Expected: all packages ok.

- [ ] **Step 6: Commit**

```bash
git add bench/harness/cmd/fixgrade/main.go bench/harness/cmd/fixgrade/main_test.go
git commit -m "fix(bench): restore withheld specs for over-correction guard legs"
```

## Task 5: fixgrade — report negative cases instead of erroring

Four `small-fix` cases are negatives (`neg-jb-001`, `neg-jb-002`, `neg-jb-004`,
`neg-ld-001`): declining is the correct answer, so they have no `fail_to_pass`
leg. `fixgrade` currently exits 2 with "has no fail_to_pass leg", which reads
like a harness bug rather than a case property.

**Files:**
- Modify: `bench/harness/cmd/fixgrade/main.go`
- Modify: `bench/harness/cmd/fixgrade/main_test.go`

- [ ] **Step 1: Write the failing test**

Append to `main_test.go`:

```go
func TestVerdictForANegativeCaseIsNotAFailure(t *testing.T) {
	// A negative case has no fail_to_pass leg because declining is correct.
	// Its mechanical legs can only establish that nothing was broken; whether
	// declining was right is a judge call fixgrade does not make.
	legs := []legResult{{Name: "keep", OK: true}}
	if got := verdictForLegs(legs, false); got != "kept-green-judge-required" {
		t.Fatalf("verdict = %q, want kept-green-judge-required", got)
	}
	broken := []legResult{{Name: "keep", OK: false}}
	if got := verdictForLegs(broken, false); got != "fail" {
		t.Fatalf("verdict = %q, want fail", got)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

```bash
cd bench/harness && go test ./cmd/fixgrade/ -run TestVerdictForANegativeCase -v
```

Expected: FAIL — `undefined: verdictForLegs`.

- [ ] **Step 3: Generalize verdict**

In `main.go`, replace `func verdict(legs []legResult) string` with:

```go
func verdict(legs []legResult) string { return verdictForLegs(legs, true) }

// verdictForLegs scores the run. hasRedLeg distinguishes an ordinary case from a
// negative one, where declining is the correct answer and there is no
// fail_to_pass leg to go green.
func verdictForLegs(legs []legResult, hasRedLeg bool) string {
	for _, leg := range legs {
		if leg.Name == "red" && !leg.OK {
			return "invalid-red-leg-passed"
		}
	}
	for _, leg := range legs {
		if !leg.OK {
			return "fail"
		}
	}
	if !hasRedLeg {
		// Nothing regressed, which is all the mechanical legs can establish here.
		return "kept-green-judge-required"
	}
	return "pass"
}
```

- [ ] **Step 4: Take the negative path in run()**

Replace:

```go
	if len(failLegs) == 0 {
		return nil, fmt.Errorf("case %s has no runnable fail_to_pass leg", caseID)
	}
	failLeg := failLegs[0]
```

with:

```go
	negative := len(failLegs) == 0
	if negative {
		result.Notes = append(result.Notes,
			"case declares no fail_to_pass leg: declining is a correct answer here, so the "+
				"mechanical legs can only show nothing regressed. Score the disposition against "+
				"the case's rubric.")
	}
```

Then guard the red leg and the green loop with `if !negative { … }`, and change
the final verdict call to `verdictForLegs(result.Legs, !negative)`. Where
`failLeg` was used for the red leg, use `failLegs[0]`.

- [ ] **Step 5: Run the suite**

```bash
cd bench/harness && go test ./... -count=1
```

Expected: all ok.

- [ ] **Step 6: Commit**

```bash
git add bench/harness/cmd/fixgrade/main.go bench/harness/cmd/fixgrade/main_test.go
git commit -m "feat(bench): report negative cases rather than erroring on them"
```

## Task 6: Pin the normalized grading shape BEFORE editing the corpus

Write the guard first so the normalization cannot drift and so a regrown dialect
fails loudly.

**Files:**
- Create: `bench/harness/corpus_shape_test.go`

- [ ] **Step 1: Write the test**

```go
package harness_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/concourse/concourse/bench/harness/casespec"
)

// Every withheld spec in the corpus must name both ends explicitly. The corpus
// previously spelled them four different ways — mirrored repo-relative,
// case-relative with the destination only implied, restored by the leg's own
// cmd, and declared but shipped nowhere — and a grader that guesses a
// destination does not error, it silently scores a correct fix as a miss.
func TestEveryWithheldSpecNamesSourceAndDestination(t *testing.T) {
	corpus := filepath.Join("..", "corpus")
	entries, err := os.ReadDir(corpus)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		caseID := entry.Name()
		if _, err := os.Stat(filepath.Join(corpus, caseID, "case.yaml")); err != nil {
			continue
		}
		grading, err := casespec.LoadGrading(corpus, caseID)
		if err != nil {
			t.Fatalf("%s: %v", caseID, err)
		}
		legs := append(append([]casespec.Leg{}, grading.FailToPass...), grading.PassToPass...)
		for index, leg := range legs {
			if len(leg.WithheldTestPaths) > 0 {
				t.Errorf("%s leg %d still uses the legacy withheld_test_paths spelling", caseID, index)
			}
			for _, spec := range leg.WithheldTests {
				if spec.Source == "" || spec.Destination == "" {
					t.Errorf("%s leg %d: withheld spec needs both source and destination, got %+v",
						caseID, index, spec)
					continue
				}
				if spec.SelfRestoring {
					continue
				}
				source := filepath.Join(corpus, caseID, spec.Source)
				if _, err := os.Stat(source); err != nil {
					t.Errorf("%s leg %d: withheld source %s does not exist", caseID, index, spec.Source)
				}
			}
		}
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

```bash
cd bench/harness && go test . -run TestEveryWithheldSpec -v
```

Expected: FAIL to compile — `casespec.Leg` has no `WithheldTests`.

- [ ] **Step 3: Commit the failing guard**

```bash
git add bench/harness/corpus_shape_test.go
git commit -m "test(bench): pin an explicit source/destination for every withheld spec"
```

## Task 7: Teach casespec the normalized shape

**Files:**
- Modify: `bench/harness/casespec/grading.go`
- Create: `bench/harness/casespec/grading_test.go`

- [ ] **Step 1: Add the type and parsing**

In `grading.go`, add above `Leg`:

```go
// WithheldSpec is one withheld test file with BOTH ends named. The corpus used
// to leave the destination implied, which a grader can only resolve by guessing.
type WithheldSpec struct {
	// Source is case-relative (normally under ground_truth/withheld_tests/).
	Source string
	// Destination is repository-relative.
	Destination string
	// SelfRestoring marks a spec the leg's own cmd puts in place, usually via
	// $CASE_DIR or `git show`. The grader then does not restore it, but the
	// declaration still records what the leg overwrites.
	SelfRestoring bool
}
```

Add to `Leg`:

```go
	// WithheldTests is the normalized form. WithheldTestPaths is the legacy
	// spelling, retained only so a stale case fails a shape test loudly instead
	// of being silently skipped.
	WithheldTests []WithheldSpec
```

In the anonymous decode struct, add to **both** the `FailToPass` and
`PassToPass` element structs:

```go
					WithheldTests []struct {
						Source        string `yaml:"source"`
						Destination   string `yaml:"destination"`
						SelfRestoring bool   `yaml:"self_restoring"`
					} `yaml:"withheld_tests"`
```

And in both copy loops, add:

```go
		specs := make([]WithheldSpec, 0, len(leg.WithheldTests))
		for _, spec := range leg.WithheldTests {
			specs = append(specs, WithheldSpec{
				Source: spec.Source, Destination: spec.Destination, SelfRestoring: spec.SelfRestoring,
			})
		}
```

assigning `WithheldTests: specs` in the appended `Leg`.

- [ ] **Step 2: Write the parser test**

Create `bench/harness/casespec/grading_test.go`:

```go
package casespec_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/concourse/concourse/bench/harness/casespec"
)

func TestLoadGradingReadsBothEndsOfAWithheldSpec(t *testing.T) {
	corpus := t.TempDir()
	caseDir := filepath.Join(corpus, "fix-test-001")
	if err := os.MkdirAll(caseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	document := `
schema: benchmark-case/v1
id: fix-test-001
workflow: small-fix
signature:
  inputs: {repository: repository/v1}
  outputs: {change: repository-change/v1}
pre_state:
  repository: {repo: jetbridge, ref: abc}
grading:
  fail_to_pass:
    - cmd: "go test ./pkg/ -count=1"
      withheld_tests:
        - source: ground_truth/withheld_tests/pkg/thing_test.go
          destination: pkg/thing_test.go
  pass_to_pass:
    - cmd: "go test ./pkg/ -run Guard -count=1"
      withheld_tests:
        - source: ground_truth/withheld_tests/pkg/thing_test.go
          destination: pkg/thing_test.go
`
	if err := os.WriteFile(filepath.Join(caseDir, "case.yaml"), []byte(document), 0o644); err != nil {
		t.Fatal(err)
	}
	grading, err := casespec.LoadGrading(corpus, "fix-test-001")
	if err != nil {
		t.Fatalf("LoadGrading() = %v", err)
	}
	for name, legs := range map[string][]casespec.Leg{
		"fail_to_pass": grading.FailToPass, "pass_to_pass": grading.PassToPass,
	} {
		if len(legs) != 1 || len(legs[0].WithheldTests) != 1 {
			t.Fatalf("%s: got %+v", name, legs)
		}
		spec := legs[0].WithheldTests[0]
		if spec.Source != "ground_truth/withheld_tests/pkg/thing_test.go" || spec.Destination != "pkg/thing_test.go" {
			t.Fatalf("%s: spec = %+v", name, spec)
		}
	}
}
```

- [ ] **Step 3: Run it**

```bash
cd bench/harness && go test ./casespec/ -run TestLoadGradingReadsBothEnds -v
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add bench/harness/casespec/grading.go bench/harness/casespec/grading_test.go
git commit -m "feat(bench): parse an explicit withheld-spec source and destination"
```

## Task 8: Recover the four withheld specs the corpus never shipped

`fix-jb-002`, `fix-jb-005` (two files) and `fix-jb-007` declare withheld specs
with no copy in the case. Extract each from its terminal commit.

**Files:**
- Create: `bench/corpus/fix-jb-002/ground_truth/withheld_tests/ci-agent/llm/client_test.go`
- Create: `bench/corpus/fix-jb-005/ground_truth/withheld_tests/atc/engine/engine_test.go`
- Create: `bench/corpus/fix-jb-005/ground_truth/withheld_tests/atc/builds/tracker_test.go`
- Create: `bench/corpus/fix-jb-007/ground_truth/withheld_tests/web/elm/tests/AgentStepDagTests.elm`

- [ ] **Step 1: Confirm every terminal is reachable**

```bash
for t in d21437fd10adae99b340dc8c7c233fa8f86c7886 \
         7c59cbbfa6ed5921d324793f2a03bb1ee4d12aa3 \
         6116d379ef0f22a094f28bd37114613e6036d69f; do
  git cat-file -e "$t^{commit}" && echo "$t ok"
done
```

Expected: three `ok` lines.

- [ ] **Step 2: Extract them**

```bash
cd /Users/tdmtrader/concourse/concourse
set -e
d=bench/corpus/fix-jb-002/ground_truth/withheld_tests/ci-agent/llm
mkdir -p "$d" && git show d21437fd10adae99b340dc8c7c233fa8f86c7886:ci-agent/llm/client_test.go > "$d/client_test.go"

d=bench/corpus/fix-jb-005/ground_truth/withheld_tests/atc/engine
mkdir -p "$d" && git show 7c59cbbfa6ed5921d324793f2a03bb1ee4d12aa3:atc/engine/engine_test.go > "$d/engine_test.go"

d=bench/corpus/fix-jb-005/ground_truth/withheld_tests/atc/builds
mkdir -p "$d" && git show 7c59cbbfa6ed5921d324793f2a03bb1ee4d12aa3:atc/builds/tracker_test.go > "$d/tracker_test.go"

d=bench/corpus/fix-jb-007/ground_truth/withheld_tests/web/elm/tests
mkdir -p "$d" && git show 6116d379ef0f22a094f28bd37114613e6036d69f:web/elm/tests/AgentStepDagTests.elm > "$d/AgentStepDagTests.elm"
```

- [ ] **Step 3: Verify each file is non-empty and is the expected kind**

```bash
for f in bench/corpus/fix-jb-002/ground_truth/withheld_tests/ci-agent/llm/client_test.go \
         bench/corpus/fix-jb-005/ground_truth/withheld_tests/atc/engine/engine_test.go \
         bench/corpus/fix-jb-005/ground_truth/withheld_tests/atc/builds/tracker_test.go \
         bench/corpus/fix-jb-007/ground_truth/withheld_tests/web/elm/tests/AgentStepDagTests.elm; do
  printf '%s  %s bytes  first line: ' "$f" "$(wc -c < "$f")"; head -1 "$f"
done
```

Expected: all non-empty; the three Go files start with `package …`, the Elm file
with `module …`.

- [ ] **Step 4: Confirm these files stay withheld**

```bash
grep -rn "withheld_tests" bench/corpus/README.md bench/schema/benchmark-case-v1.md | head
```

Expected: documentation confirming `ground_truth/` is never exposed to a solver.
If it is not stated there, stop and raise it — these files are answers.

- [ ] **Step 5: Commit**

```bash
git add bench/corpus/fix-jb-002/ground_truth bench/corpus/fix-jb-005/ground_truth bench/corpus/fix-jb-007/ground_truth
git commit -m "fix(corpus): ship the withheld specs three cases only referenced"
```

## Task 9: Normalize the ten mechanical small-fix cases

Mechanical edits only. **Do not touch any `task/`, `pre_state`, `ground_truth`
answer, or difficulty/rubric field** — nothing a solver sees may change.

**Files:**
- Modify: `bench/corpus/{fix-cc-001,fix-jb-001,fix-jb-002,fix-jb-003,fix-jb-004,fix-jb-005,fix-jb-006,fix-jb-007,fix-ld-001,fix-ld-002}/case.yaml`

Per-case replacement of every `withheld_test_paths:` block with a
`withheld_tests:` block. Destinations below are derived from each leg's own
command; **Step 2 verifies each one rather than trusting this table.**

| case | leg | source | destination | self_restoring |
|---|---|---|---|---|
| fix-cc-001 | f2p[0] | `ground_truth/withheld_tests/atc/exec/set_pipeline_step_test.go` | `atc/exec/set_pipeline_step_test.go` | no |
| fix-jb-001 | f2p[0] | `ground_truth/withheld_tests/ci-agent/devmcp/server_test.go` | `ci-agent/devmcp/server_test.go` | no |
| fix-jb-002 | f2p[0] | `ground_truth/withheld_tests/ci-agent/llm/client_test.go` | `ci-agent/llm/client_test.go` | no |
| fix-jb-003 | f2p[0], p2p[0] | `ground_truth/grading_tests/trailer_test.go` | `agent/harvest/trailer_test.go` | no |
| fix-jb-004 | f2p[0] | `ground_truth/withheld_tests/event_reader_bigline_test.go` | `agent/schema/event_reader_bigline_test.go` | **yes** |
| fix-jb-004 | f2p[1] | `` (from a git sha in the cmd) | `agent/schema/event_io_test.go` | **yes** |
| fix-jb-005 | f2p[0] | `ground_truth/withheld_tests/atc/engine/engine_test.go` | `atc/engine/engine_test.go` | no |
| fix-jb-005 | f2p[0] | `ground_truth/withheld_tests/atc/builds/tracker_test.go` | `atc/builds/tracker_test.go` | no |
| fix-jb-006 | f2p[0] | `ground_truth/withheld_tests/zz_bench_fix_jb_006_test.go` | `atc/configvalidate/zz_bench_fix_jb_006_test.go` | **yes** |
| fix-jb-007 | f2p[0] | `ground_truth/withheld_tests/web/elm/tests/AgentStepDagTests.elm` | `web/elm/tests/AgentStepDagTests.elm` | no |
| fix-ld-001 | f2p[0] | `ground_truth/withheld_tests/look_readfree_blackout_test.go` | `internal/eos/look_readfree_blackout_test.go` | no |
| fix-ld-002 | f2p[0], p2p[0] | `ground_truth/withheld_tests/stage_truncated_selection_test.go` | `internal/eos/stage_truncated_selection_test.go` | no |

Note `fix-jb-004` f2p[1] is the `role: fallback-only`, `destructive: true` leg.
It restores from a commit, so it has no in-case source; mark it `self_restoring:
true` with an empty `source` and record the destination it overwrites.

- [ ] **Step 1: Verify each inferred destination against the file itself**

For every Go spec, the package clause and the leg's `-run` name must match the
destination directory:

```bash
cd bench/corpus
head -1 fix-jb-003/ground_truth/grading_tests/trailer_test.go
grep -n "func TestStampTrailerSetsItsOwnCommitterIdentity" fix-jb-003/ground_truth/grading_tests/trailer_test.go
head -1 fix-ld-001/ground_truth/withheld_tests/look_readfree_blackout_test.go
head -1 fix-ld-002/ground_truth/withheld_tests/stage_truncated_selection_test.go
```

Expected: `fix-jb-003`'s file declares the `harvest` package (or
`harvest_test`) and contains that test function, matching destination
`agent/harvest/`. The two `ld` files declare the `eos` package (or `eos_test`),
matching `internal/eos/`. **If any package clause disagrees with the table, stop
and correct the table — do not proceed on a guess.**

- [ ] **Step 2: Edit one case and prove the shape parses**

In `bench/corpus/fix-jb-001/case.yaml`, replace:

```yaml
      withheld_test_paths:
        - ci-agent/devmcp/server_test.go
```

with:

```yaml
      withheld_tests:
        - source: ground_truth/withheld_tests/ci-agent/devmcp/server_test.go
          destination: ci-agent/devmcp/server_test.go
```

Then:

```bash
cd bench/harness && go test . -run TestEveryWithheldSpec -v 2>&1 | head -20
```

Expected: `fix-jb-001` no longer reported; other cases still reported.

- [ ] **Step 3: Edit the remaining nine cases per the table**

Keep every surrounding comment, `note:`, `role:`, `destructive:` and
`focus_spec:` field exactly as-is. Only the `withheld_test_paths` block changes.

- [ ] **Step 4: Run the shape guard**

```bash
cd bench/harness && go test . -run TestEveryWithheldSpec -v
```

Expected: PASS.

- [ ] **Step 5: Review the diff case by case**

```bash
cd /Users/tdmtrader/concourse/concourse
git diff --stat bench/corpus/
git diff bench/corpus/ | grep -E "^[-+]" | grep -v "withheld_test" | grep -vE "^[-+]{3}" | head -40
```

Expected: the last command prints **nothing**. Anything else means a line
outside the withheld blocks changed, which is out of scope — revert it.

- [ ] **Step 6: Commit**

```bash
git add bench/corpus
git commit -m "refactor(corpus): name both ends of every withheld spec"
```

## Task 10: fixgrade reads the normalized shape

**Files:**
- Modify: `bench/harness/cmd/fixgrade/main.go`
- Modify: `bench/harness/cmd/fixgrade/main_test.go`

- [ ] **Step 1: Write the failing test**

Append to `main_test.go`:

```go
func TestRestoreWithheldUsesTheDeclaredDestination(t *testing.T) {
	caseDir := t.TempDir()
	tree := t.TempDir()
	source := filepath.Join(caseDir, "ground_truth", "grading_tests", "trailer_test.go")
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("package harvest\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	leg := casespec.Leg{
		Cmd: "go test ./agent/harvest/",
		WithheldTests: []casespec.WithheldSpec{{
			Source:      "ground_truth/grading_tests/trailer_test.go",
			Destination: "agent/harvest/trailer_test.go",
		}},
	}
	if err := restoreWithheld(caseDir, tree, leg); err != nil {
		t.Fatalf("restoreWithheld() = %v", err)
	}
	if _, err := os.Stat(filepath.Join(tree, "agent/harvest/trailer_test.go")); err != nil {
		t.Fatalf("spec was not restored at its declared destination: %v", err)
	}
}

func TestRestoreWithheldSkipsASelfRestoringSpec(t *testing.T) {
	leg := casespec.Leg{
		Cmd: `cp $CASE_DIR/ground_truth/withheld_tests/x_test.go agent/schema/`,
		WithheldTests: []casespec.WithheldSpec{{
			Source: "ground_truth/withheld_tests/x_test.go", Destination: "agent/schema/x_test.go", SelfRestoring: true,
		}},
	}
	if err := restoreWithheld(t.TempDir(), t.TempDir(), leg); err != nil {
		t.Fatalf("restoreWithheld() = %v", err)
	}
}
```

- [ ] **Step 2: Run to verify failure**

```bash
cd bench/harness && go test ./cmd/fixgrade/ -run "TestRestoreWithheldUsesTheDeclared|TestRestoreWithheldSkipsASelf" -v
```

Expected: FAIL — the declared destination is not honored.

- [ ] **Step 3: Implement**

Replace the body of `restoreWithheld` with:

```go
func restoreWithheld(caseDir, tree string, leg casespec.Leg) error {
	if len(leg.WithheldTestPaths) > 0 {
		return fmt.Errorf(
			"leg uses the legacy withheld_test_paths spelling, which leaves the repository "+
				"destination implied; normalize the case to withheld_tests with an explicit "+
				"source and destination (paths: %v)", leg.WithheldTestPaths)
	}
	for _, spec := range leg.WithheldTests {
		if spec.SelfRestoring {
			continue
		}
		if spec.Source == "" || spec.Destination == "" {
			return fmt.Errorf("withheld spec needs both source and destination, got %+v", spec)
		}
		contents, err := os.ReadFile(filepath.Join(caseDir, spec.Source))
		if err != nil {
			return fmt.Errorf("withheld spec %s: %w", spec.Source, err)
		}
		destination := filepath.Join(tree, spec.Destination)
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(destination, contents, 0o644); err != nil {
			return err
		}
	}
	return nil
}
```

Update `moveAsideAgentTests` call sites to pass destinations:

```go
		moved, err := moveAsideAgentTests(overlayTree, filepath.Join(workDir, fmt.Sprintf("agent-tests-%d", index+1)),
			withheldDestinations(leg), result.ChangedFiles)
```

and add:

```go
func withheldDestinations(leg casespec.Leg) []string {
	destinations := make([]string, 0, len(leg.WithheldTests))
	for _, spec := range leg.WithheldTests {
		destinations = append(destinations, spec.Destination)
	}
	return destinations
}
```

Fix the earlier tests that construct `WithheldTestPaths` to use `WithheldTests`.

- [ ] **Step 4: Run the suite**

```bash
cd bench/harness && go test ./... -count=1
```

Expected: all ok.

- [ ] **Step 5: Commit**

```bash
git add bench/harness
git commit -m "feat(bench): restore withheld specs at their declared destination"
```

## Task 11: Recalibrate fixgrade on every case

A grader is only trustworthy if the case's own reference fix scores `pass`.

**Files:** none modified unless a calibration fails.

- [ ] **Step 1: Build**

```bash
cd bench/harness && go build -o /tmp/fixgrade ./cmd/fixgrade
```

- [ ] **Step 2: Prepare pre-state clones**

For each case, follow the prune procedure from Task 3, using the case's
`pre_state.repository.ref`. `fix-ld-*` pin the LightingDesign repository at
`/Users/tdmtrader/LightingDesign`; all others pin this one.

- [ ] **Step 3: Calibrate each case against its own reference**

```bash
cd /Users/tdmtrader/concourse/concourse
/tmp/fixgrade --corpus bench/corpus --case fix-jb-001 \
  --patch bench/corpus/fix-jb-001/ground_truth/reference.diff \
  --source-repo <pre-state-clone> | head -12
```

Expected: `verdict pass`, `red` leg `ok` with a nonzero exit.

Repeat for `fix-cc-001`, `fix-jb-002`, `fix-jb-003`, `fix-jb-004`, `fix-jb-005`,
`fix-jb-006`, `fix-ld-001`, `fix-ld-002`. Skip `fix-jb-007` unless the Elm
toolchain is installed — the agent-runner image dropped it, so `elm-test` is
likely absent; record it as environment-blocked rather than failing.

- [ ] **Step 4: Record the outcome**

Create `bench/nodes/FIXGRADE-CALIBRATION.md` with one row per case: verdict, red
leg exit code, and any environment blocker. A case whose reference fix does not
score `pass` is a grading defect — investigate before proceeding, and do not
"fix" it by weakening the grader.

- [ ] **Step 5: Commit**

```bash
git add bench/nodes/FIXGRADE-CALIBRATION.md
git commit -m "test(bench): record fixgrade calibration across the small-fix corpus"
```

## Task 12: Record the corpus version bump

**Files:**
- Modify: `bench/corpus/INDEX.md`

- [ ] **Step 1: Verify the count discrepancy**

```bash
cd bench/corpus && grep -c "^workflow: small-fix" */case.yaml | grep -c ":1$"
```

Expected: `14`, while `INDEX.md`'s counts line says `small-fix 13`.

- [ ] **Step 2: Update INDEX.md**

Change the counts line from `small-fix 13` to `small-fix 14` and append:

```markdown
## Corpus revisions

**v0.1 — 2026-08-04.** Grading normalization. Every withheld spec now names both
its case-relative `source` and its repository-relative `destination` under
`withheld_tests:`, replacing four incompatible spellings of
`withheld_test_paths:` (mirrored repo-relative, case-relative with the
destination implied, restored by the leg's own cmd, and declared but shipped
nowhere). Four specs that were only referenced are now included, extracted from
their cases' terminal commits.

`bench/harness/corpus_shape_test.go` pins the shape so the dialects cannot grow
back.

**No task, pre-state, exposure, or ground-truth answer changed**, so results
citing the v0 seal `03c0982a88` remain comparable; only the harness-side grading
declaration moved. The small-fix count is also corrected from 13 to 14 — v0
undercounted.
```

- [ ] **Step 3: Commit**

```bash
git add bench/corpus/INDEX.md
git commit -m "docs(corpus): record the v0.1 grading normalization"
```

## Task 13: Probe whether the deployed server accepts a node experiment

The fly grammar is worthless against a server that rejects the definition. The
deployed web image predates this work.

**Files:** none.

- [ ] **Step 1: Get a node definition ID**

```bash
fly -t home agent nodes show small-fix 4 --json | python3 -c "import sys,json;d=json.load(sys.stdin);print(json.dumps(d)[:400])"
```

Record the node's definition ID and version.

- [ ] **Step 2: Attempt a node-target experiment**

```bash
cat > /tmp/node-experiment.json <<'EOF'
{
  "label": "smallfix-early-change",
  "targets": [
    {"kind": "node", "workflow_name": "small-fix", "definition_id": <ID>, "version": 4,
     "node_parameters": {"EARLY_CHANGE": "true"}},
    {"kind": "node", "workflow_name": "small-fix", "definition_id": <ID>, "version": 4,
     "node_parameters": {"EARLY_CHANGE": "false"}}
  ]
}
EOF
fly -t home agent experiments create --json < /tmp/node-experiment.json
```

Adjust the JSON to the exact shape `fly agent experiments create --help`
documents before running.

- [ ] **Step 3: Branch on the result**

- Accepted → proceed to Task 14 in wave 1.
- Rejected with an unknown-kind or validation error → the deployed image predates
  `TargetNode`. **Move Task 14 to wave 2**, after the deploy, and note it in the
  plan. Do not implement a fly grammar the server will refuse.

## Task 14: fly — express a node variant

Conditional on Task 13.

**Files:**
- Modify: `fly/commands/agent_experiments.go`
- Modify: `fly/commands/agent_experiments_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestParseAgentExperimentVariantReferenceAcceptsANodeTarget(t *testing.T) {
	reference, err := parseAgentExperimentVariantReference("early=node:small-fix@4")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if reference.Label != "early" || reference.WorkflowName != "small-fix" ||
		reference.Version != 4 || !reference.Node {
		t.Fatalf("reference = %+v", reference)
	}
	if reference.FunctionID != "" {
		t.Fatalf("a node variant selects no function, got %q", reference.FunctionID)
	}
}

func TestParseAgentExperimentVariantReferenceRejectsANodeFunction(t *testing.T) {
	// A node is one leaf; naming a function inside it is meaningless.
	if _, err := parseAgentExperimentVariantReference("early=node:small-fix@4#review"); err == nil {
		t.Fatal("a node variant must not accept a function ID")
	}
}
```

- [ ] **Step 2: Run to verify failure**

```bash
cd /Users/tdmtrader/concourse/concourse && go test ./fly/commands/ -run TestParseAgentExperimentVariantReference -v
```

Expected: FAIL — `reference.Node` undefined.

- [ ] **Step 3: Implement**

Add `Node bool` to `agentExperimentVariantReference`. In
`parseAgentExperimentVariantReference`, after splitting the label:

```go
	if rest, isNode := strings.CutPrefix(target, "node:"); isNode {
		if strings.Contains(rest, "#") {
			return agentExperimentVariantReference{}, fmt.Errorf(
				"a node variant selects no function: use label=node:NAME@VERSION")
		}
		target = rest
		reference.Node = true
	}
```

then let the existing `@`-splitting run. Set `Kind` to `experiment.TargetNode`
where the request is built, and add to `AgentExperimentsAddVariantCommand`:

```go
	Param []string `long:"param" description:"Node parameter as KEY=VALUE (repeatable; node variants only)"`
```

Reject `--param` on non-node variants, matching `agent/experiment/types.go:148`:

```go
	if len(command.Param) > 0 && !reference.Node {
		return errors.New("--param applies only to node variants")
	}
```

Update the positional-arg description on line 183 to
`label=workflow@version, label=workflow@version#function-id, or label=node:name@version`.

- [ ] **Step 4: Run the tests**

```bash
cd /Users/tdmtrader/concourse/concourse && go test ./fly/commands/ -run TestParseAgentExperimentVariantReference -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add fly/commands/agent_experiments.go fly/commands/agent_experiments_test.go
git commit -m "feat(fly): express a node variant and its parameters"
```

---

# WAVE 2 — one two-image deploy

## Task 15: Carry intrinsic metadata on the input authority

**Files:**
- Modify: `agent/outputbuilder/authority.go`
- Modify: `agent/outputbuilder/builder.go`
- Modify: `agent/outputbuilder/outputbuilder_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/outputbuilder/outputbuilder_test.go`:

```go
// A repository-change body must restate repository_id, which the server derives
// privately. Without this the only way an agent can produce it is to
// re-implement the hash in shell.
func TestDescribeOutputReturnsEachInputsIntrinsicMetadata(t *testing.T) {
	metadata := []byte(`{"repository_id":"sha256:` + strings.Repeat("a", 64) + `","head_sha":"` + strings.Repeat("b", 40) + `"}`)
	builder, cleanup := newTestBuilderWithInputMetadata(t, "repository", metadata)
	defer cleanup()

	description, err := builder.DescribeOutput(context.Background(), "change")
	if err != nil {
		t.Fatalf("DescribeOutput() = %v", err)
	}
	for _, input := range description.Inputs {
		if input.Name != "repository" {
			continue
		}
		if string(input.IntrinsicMetadata) != string(metadata) {
			t.Fatalf("intrinsic metadata = %s, want %s", input.IntrinsicMetadata, metadata)
		}
		return
	}
	t.Fatal("repository input was not described")
}
```

Add the `newTestBuilderWithInputMetadata` helper alongside the file's existing
builder helpers, constructing a `NodeAuthority` whose `repository` input carries
the metadata and whose `change` output is `repository-change/v1`.

- [ ] **Step 2: Run to verify failure**

```bash
go test ./agent/outputbuilder/ -run TestDescribeOutputReturnsEachInputs -v
```

Expected: FAIL — `InputAuthority` has no `IntrinsicMetadata`.

- [ ] **Step 3: Implement**

In `authority.go`, add to `InputAuthority`:

```go
	// IntrinsicMetadata is the sealed snapshot's server-derived metadata,
	// forwarded verbatim. It is propagated rather than re-derived here on
	// purpose: the mount is writable, so deriving it from the tree would hand
	// back a value computed from a mutated repository - wrong in exactly the case
	// that matters, and it would hide the mistake the seal gate exists to catch.
	IntrinsicMetadata json.RawMessage `json:"intrinsic_metadata,omitempty"`
```

In `builder.go`, add the same field to `InputDescription` and populate it in
`DescribeOutput`:

```go
		inputs = append(inputs, InputDescription{
			Name: name, Type: input.Ref.Type, Digest: input.Ref.Digest,
			Candidate: input.Candidate, IntrinsicMetadata: input.IntrinsicMetadata,
		})
```

- [ ] **Step 4: Run**

```bash
go test ./agent/outputbuilder/ -count=1
```

Expected: ok.

- [ ] **Step 5: Commit**

```bash
git add agent/outputbuilder
git commit -m "feat(outputbuilder): describe each input's sealed intrinsic metadata"
```

## Task 16: ATC populates the metadata

**Files:**
- Modify: `atc/exec/output_builder_authority.go`
- Modify: `atc/exec/output_builder_authority_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestAuthorityCarriesTheSealedIntrinsicMetadataOfEachInput(t *testing.T) {
	// The agent cannot look this up: nothing in the pod can reach the snapshot
	// store, and the mount does not contain it.
	metadata := []byte(`{"repository_id":"sha256:` + strings.Repeat("a", 64) + `"}`)
	authority := buildTestAuthority(t, testInput{Name: "repository", Type: "repository/v1", IntrinsicMetadata: metadata})
	if string(authority.Inputs["repository"].IntrinsicMetadata) != string(metadata) {
		t.Fatalf("intrinsic metadata = %s, want %s",
			authority.Inputs["repository"].IntrinsicMetadata, metadata)
	}
}
```

Follow the file's existing helper conventions for constructing the authority.

- [ ] **Step 2: Run to verify failure**

```bash
go test ./atc/exec/ -run TestAuthorityCarriesTheSealed -v
```

Expected: FAIL.

- [ ] **Step 3: Implement**

In `output_builder_authority.go`, where the `InputAuthority` is constructed for
each declared input, set `IntrinsicMetadata` from the snapshot manifest ATC
already holds for that ref. Precedent for reading it server-side:
`atc/api/agentchildexecutions/sealer.go:86`. If the manifest is not currently
threaded to this function, thread it alongside `inputs.refs` rather than
re-fetching per input.

- [ ] **Step 4: Add the size-cap test**

The authority file is capped at `maxAuthorityFileBytes` (1 MiB) and
`root_commits` is the only unbounded field.

```go
func TestAuthorityRejectsIntrinsicMetadataThatWouldExceedTheFileCap(t *testing.T) {
	huge := append(append([]byte(`{"root_commits":["`), bytes.Repeat([]byte("a"), 1<<20)...), []byte(`"]}`)...)
	_, err := buildTestAuthorityErr(t, testInput{Name: "repository", Type: "repository/v1", IntrinsicMetadata: huge})
	if err == nil {
		t.Fatal("an oversized authority must be refused at build time, not truncated at mount time")
	}
}
```

Implement the guard: total encoded authority must stay under
`outputbuilder.MaxAuthorityFileBytes`; exceed it and return an error naming the
input.

- [ ] **Step 5: Run**

```bash
go test ./atc/exec/ -run TestAuthority -count=1
```

Expected: ok.

- [ ] **Step 6: Commit**

```bash
git add atc/exec/output_builder_authority.go atc/exec/output_builder_authority_test.go
git commit -m "feat(atc): forward sealed input metadata to the output builder"
```

## Task 17: Accept declared bases when sealing a directly-created snapshot

`agent/snapshot/sealer.go:288` builds `NewValidationContext(nil, nil)` on the
direct-create path, so no contract that reopens an input can ever be created
there. `BatchSealer.validationContext` (line 233) already shows the shape.

**Files:**
- Modify: `agent/snapshot/sealer.go`
- Modify: `agent/snapshot/sealer_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestDirectCreateSealsARepositoryChangeAgainstADeclaredBase(t *testing.T) {
	// repository-change/v1 binds its base as a declared input. Without a way to
	// declare one, the type can only ever be produced as a node output.
	sealer, base := newTestSealerWithRepository(t)
	_, err := sealer.Seal(context.Background(), DirectSealRequest{
		Type:  "repository-change/v1",
		Bases: map[string]SnapshotRef{"repository": base},
		// ... archive containing record.json + content/change.patch
	})
	if err != nil {
		t.Fatalf("seal with a declared base: %v", err)
	}
}

func TestDirectCreateRefusesABaseTheCallerCannotRead(t *testing.T) {
	sealer, base := newTestSealerWithRepository(t)
	sealer.teamID = otherTeamID
	_, err := sealer.Seal(context.Background(), DirectSealRequest{
		Type: "repository-change/v1", Bases: map[string]SnapshotRef{"repository": base},
	})
	if err == nil {
		t.Fatal("a base the caller cannot read must be refused before validation runs")
	}
}
```

Adapt names to the file's actual request type.

- [ ] **Step 2: Run to verify failure**

```bash
go test ./agent/snapshot/ -run TestDirectCreate -v
```

Expected: FAIL.

- [ ] **Step 3: Implement**

Give the direct-create request a declared-bases map and build its context with
the same `inputOpener(teamID)` the batch path uses, replacing
`NewValidationContext(nil, nil)`. Resolve and authorize every base **before**
validation: a base the caller's team cannot read is rejected up front.

- [ ] **Step 4: Run**

```bash
go test ./agent/snapshot/ -count=1
```

Expected: ok.

- [ ] **Step 5: Commit**

```bash
git add agent/snapshot
git commit -m "feat(snapshot): seal a directly created record against declared bases"
```

## Task 18: API and fly accept `--base`

**Files:**
- Modify: `agent/api/snapshots/handler.go`, `agent/api/snapshots/types.go`
- Modify: `agent/api/snapshots/handler_test.go`
- Modify: `fly/commands/agent_snapshots.go`

- [ ] **Step 1: Write the API test**

```go
func TestCreateAcceptsDeclaredBasesAsQueryParameters(t *testing.T) {
	// POST /api/v1/agent/snapshots?type=repository-change/v1&base=repository%3D23
	// The body is the tar; bases cannot travel in it.
	request := newCreateRequest(t, "repository-change/v1", map[string]string{"repository": "23"})
	response := serve(t, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body)
	}
}

func TestCreateRejectsAMalformedBaseDeclaration(t *testing.T) {
	for _, raw := range []string{"repository", "=23", "repository=", "repository=abc"} {
		if code := serveRawBase(t, raw).Code; code != http.StatusBadRequest {
			t.Fatalf("base %q: status = %d, want 400", raw, code)
		}
	}
}
```

- [ ] **Step 2: Run to verify failure**

```bash
go test ./agent/api/snapshots/ -run TestCreate -v
```

Expected: FAIL.

- [ ] **Step 3: Implement the handler**

Parse repeated `base=NAME=ID` query parameters into a map, reject malformed and
duplicate names with 400, and pass them into the seal request.

- [ ] **Step 4: Add the fly flag**

In `AgentSnapshotsCreateCommand`:

```go
	Base []string `long:"base" description:"Declare a base input as NAME=SNAPSHOT-ID (repeatable); required for types whose seal gate reopens an input, such as repository-change/v1"`
```

Encode each into the query alongside `type`:

```go
	values := url.Values{"type": []string{typeRef.String()}}
	for _, raw := range command.Base {
		name, id, found := strings.Cut(raw, "=")
		if !found || name == "" || id == "" {
			return fmt.Errorf("--base must be NAME=SNAPSHOT-ID, got %q", raw)
		}
		values.Add("base", name+"="+id)
	}
	path := agentSnapshotsPath(target) + "?" + values.Encode()
```

- [ ] **Step 5: Run**

```bash
go test ./agent/api/snapshots/ ./fly/commands/ -count=1
```

Expected: ok.

- [ ] **Step 6: Commit**

```bash
git add agent/api/snapshots fly/commands/agent_snapshots.go
git commit -m "feat(fly,api): declare a base when creating a snapshot"
```

## Task 19: Broad suites before any deploy

**Files:** none.

- [ ] **Step 1: Confirm PostgreSQL**

```bash
pg_isready
```

- [ ] **Step 2: Run the tiers**

```bash
make test-unit
make test-integration
make test-fly-integration
make test-bench-harness
helm lint deploy/chart
```

Expected: all pass. `make test-unit` takes roughly 8 minutes; do not add
`--race`. K8s suites are CI-only from this machine and are not a blocker.

- [ ] **Step 3: Record the results**

Note each command and its outcome; a failing tier blocks the deploy.

## Task 20: Build and deploy

Both images are required: ATC writes the authority, and `cmd/agent-output` —
which serves `describe_output` — ships in the agent-runner image.

**Files:** none in this repo; possibly `home-infra` for digest pins.

- [ ] **Step 1: Push and let the release chain build**

```bash
git push origin jetbridge
```

Watch the self-build pipeline on `concourse.home`. Prefer the normal chain over
hand-built images.

- [ ] **Step 2: Verify both images moved**

```bash
kubectl --context theborg -n cicd get deploy concourse-web \
  -o jsonpath='{.spec.template.spec.containers[0].image}'; echo
kubectl --context theborg -n cicd get deploy concourse-web \
  -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="CONCOURSE_AGENT_STEP_IMAGE")].value}'; echo
```

Expected: both digests differ from
`sha256:8a7630c6dcd59b5902e7b25c9e12f4bb83ad56fbb9dfb7990b69716b855a13cf` and
`sha256:95d7657ec67aed9a0ac9496db22b39a790a206f2276a2e9cca221b44fb16b339`.

- [ ] **Step 3: Confirm the pods are healthy**

```bash
kubectl --context theborg -n cicd get pods
```

Expected: `concourse-web` and `concourse-artifact-daemon` Running with no
restarts.

## Task 21: Live proofs

**Files:**
- Modify: `bench/nodes/small-fix/prompts/fix.md`

- [ ] **Step 1: Prove `--base` works**

```bash
fly -t home agent snapshots create --type repository-change/v1 \
  --from ./change-dir --base repository=23 --json
```

Expected: a sealed `repository-change/v1` with `changed_files` populated.

- [ ] **Step 2: Delete the hash recipe from the prompt**

Remove the `OBJECT_FORMAT` / `REPOSITORY_ID` shell block and the paragraph
explaining the derivation. Replace with:

```markdown
`describe_output` returns each declared input's sealed `intrinsic_metadata`.
Take `repository_id` from the `repository` input's metadata — do not compute it.
```

- [ ] **Step 3: Import and run**

```bash
fly -t home agent nodes import ./bench/nodes/small-fix
fly -t home agent nodes run small-fix <VERSION> \
  --input repository=23 --input work-item=24 \
  --param EARLY_CHANGE=true --param VERIFY_LEVEL=test \
  --idempotency-key=smallfix-p1-proof --json
```

Expected: `succeeded`, with a sealed `repository-change/v1` whose
`repository_id` is
`sha256:7512be4d32ceb3cfad08410d2b0c53e67e2aa3b59ee24351c21c139ae4246713`.

- [ ] **Step 4: Grade it**

```bash
/tmp/fixgrade --corpus bench/corpus --case fix-jb-001 \
  --patch <downloaded>/content/change.patch --source-repo <pre-state-clone>
```

Expected: `verdict pass`.

- [ ] **Step 5: Re-run the matrix as a real experiment**

Using the Task 14 grammar, rebuild this session's configuration matrix as an
experiment and compare its scorecard against the table in
`bench/nodes/FIRST-USER-2026-08-04.md` §6. Any disagreement is a finding about
one of the two measurement paths — investigate rather than picking a winner.

- [ ] **Step 6: Commit**

```bash
git add bench/nodes/small-fix/prompts/fix.md
git commit -m "refactor(bench): take repository_id from the platform, not a shell hash"
```

## Task 22: Record the outcome

**Files:**
- Modify: `bench/nodes/FIRST-USER-2026-08-04.md`

- [ ] **Step 1: Update §9**

Mark each of the five "what to fix next" items resolved or still open, with the
commit that closed it.

- [ ] **Step 2: Update §3**

The `repository_id` friction is closed; replace the section body with the
`describe_output` answer, keeping the historical note that it once required
re-implementing the hash.

- [ ] **Step 3: Commit**

```bash
git add bench/nodes/FIRST-USER-2026-08-04.md
git commit -m "docs(bench): record which authoring friction is now closed"
```

---

## Self-review

**Spec coverage.** §2 P1 → Tasks 15, 16, 21. §3 D1 → Task 2. §4 P2 → Tasks 17,
18, 21. §5 D2 → Task 3. §6 P5 → Tasks 13, 14. §7 H1 → Tasks 6–12. Scope
correction → Task 1. Wave-2 test/deploy gates → Tasks 19, 20. All covered.

**Two additions beyond the spec**, both found by the Task-9 survey that the spec
asked for and neither previously known:

- Task 4 — `withheld_test_paths` also appears on `pass_to_pass` legs in
  `fix-jb-003` and `fix-ld-002`. `fixgrade` ignored them, so those guards ran
  against a tree with no spec restored. This is a live grader defect.
- Task 5 — four `small-fix` cases are negatives with no `fail_to_pass` leg;
  `fixgrade` errored on them.

**Corrections to the spec's own numbers.** The spec says 13 `small-fix` cases and
four restore dialects. There are **14** cases (`INDEX.md` undercounts) and
**four resolution classes across five spellings**, plus four specs never shipped
at all. Task 9's table and Task 12 carry the corrected figures.

**Known risks left explicit.** `fix-jb-007` needs an Elm toolchain the
agent-runner image deliberately dropped, so Task 11 records it as
environment-blocked rather than failing. `fix-jb-005` is documented as needing
PostgreSQL although its `environment:` block does not say so — Task 11 will
surface it; normalizing `environment:` is deliberately out of scope.

---

## Addendum — scope correction found during execution (2026-08-04)

Tasks 6 and 7 landed the shape guard, which immediately proved the plan's own
survey too narrow. The guard checks the **whole corpus**, not the small-fix
subset the plan surveyed, and it flags **20 legs across 15 cases** — five more
than Task 9 lists: `feedback-jb-001`, `feedback-jb-002`, `rca-jb-004`,
`rca-jb-005`, `review-jb-001`.

Surveying those five found a **sixth** restore dialect the plan did not know
about:

- `rca-jb-005` ships `ground_truth/withheld_tests/agent_step_test.patch` — a
  *patch*, not a file — applied by the leg's own cmd via `git apply
  ${CASE_DIR}/...`, while its `withheld_test_paths` names
  `atc/exec/agent_step_test.go`, the file that patch modifies. Source and
  destination are not the same artifact kind.

Four of the five others are the already-known mirrored dialect.

Two consequences:

1. **Task 9's scope grows from 10 cases to 15.** Normalizing only the small-fix
   subset would leave the guard permanently red, which would train everyone to
   ignore it.
2. **The guard needs one refinement.** `fix-jb-004`'s `fallback-only` leg
   restores from a git SHA and has no in-case source at all, so requiring a
   non-empty `source` on every spec is wrong. The rule becomes: `destination` is
   always required; `source` is required only when the spec is not
   `self_restoring`. Destination is what the grader's move-aside logic needs;
   source only matters when the grader itself restores.

This is the second time the "survey before designing" step changed the design.
The lesson is not that the survey was skipped — it was run — but that it was
scoped to the cases the author already had in mind.
