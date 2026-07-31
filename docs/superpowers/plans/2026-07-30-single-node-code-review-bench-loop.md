# Single-Node `code-review` Bench Loop — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build one reusable atomic node (`code-review`), execute it as direct single-node runs against the bench corpus's code-review cases on theborg, and grade the results out-of-band — so we learn what a node needs to be before composing nodes into multi-step workflows.

**Architecture:** A node is already a first-class executable object: `CompiledNodeDefinition` is a one-leaf function (exactly one `task`, `agent`, or `publish_snapshot` step) with typed input/output ports and string parameters, and `POST /api/v1/agent/nodes/:name/versions/:version/runs` executes an exact version directly with snapshot-bound inputs. We therefore do **not** need a wrapper workflow to start — `fly agent nodes run` is the experiment vehicle. Grading is out-of-band: we download the node's `review/v1` output snapshot and score it against the corpus's withheld `expected-findings/v1` oracle using a new matcher tool. No changes to the experiment subsystem, no new snapshot types.

**Tech Stack:** Go 1.25 (new isolated `bench/harness` module), `fly` CLI (`agent nodes`, `agent snapshots`), theborg Kubernetes cluster (`concourse.home`), existing `agent/snapshot/contracts` `review/v1` types.

---

## Context an implementer needs

**What a node is.** `agent/workflow/node_definition.go` defines `CompiledNodeDefinition{SchemaVersion, Name, Description, Parameters, Function}`. `Validate()` enforces: schema version 1, exactly one plan leaf, that leaf being `*atc.TaskStep`, `*atc.AgentStep`, or `*atc.PublishSnapshotStep`, and every output mapping `From == Name`. Source form is `node.yaml` (`agent/workflow/manifest.go:30`) inside a source directory that may also carry `prompts/` and `skills/` files.

**How a node runs.** `agent/api/noderuns/handler.go` accepts `{inputs: {port: snapshot-id}, params: {NAME: value}, idempotency_key}` and binds a `DefinitionKindNode` run. The CLI is `fly agent nodes run NAME VERSION --input port=ID --param K=V`.

**Why out-of-band grading.** `agent/experiment/types.go:88` defines `TargetKind` as only `workflow` or `function` — an experiment **cannot** target a node. Adding `TargetNode` is a platform change we are deliberately not making yet. Running nodes directly and grading with a script sidesteps that entirely.

**The corpus.** `bench/corpus/` holds 34 sealed cases (see `bench/corpus/INDEX.md`). Six are `workflow: code-review`. Their declared output port types split two ways:

| case | inputs | output port | rubric | difficulty | validation |
|---|---|---|---|---|---|
| review-jb-001 | repository, change, work-item | `review/v1` | reference | hard | validated |
| review-jb-004 | repository, change, work-item | `review-findings/v1` | reference | hard | validated |
| neg-cc-001 | repository, change, work-item | `review/v1` | outcome | moderate | unvalidated |
| review-jb-002 | repository, change | `review-findings/v1` | reference | moderate | validated |
| review-jb-003 | before, after | `review/v1` | reference | moderate | validated |
| review-ld-001 | repository, work-item | `review-findings/v1` | reference | hard | unvalidated |

`review-findings/v1` is **not a real type** — `bench/corpus/review-jb-004/case.yaml:12` says so explicitly: "a curator-chosen port type name; no snapshot type registry exists yet". The registered `review/v1` (`agent/snapshot/contracts/review.go`) already carries `Finding.Evidence []Anchor` with `Locator{Kind, Path, Start, End}` — exactly the file+region anchoring the corpus wanted. Task 2 retargets those three cases to `review/v1`.

**Target signature for this node.** `{repository: repository/v1, change: repository-change/v1, work-item: work-item/v1} -> {review: review/v1}`. That serves review-jb-001, review-jb-004 and neg-cc-001 directly. review-jb-002 (no work-item) is reachable with a stub work-item. review-jb-003 (before/after) and review-ld-001 (no change) need different signatures and are **out of scope** for this plan.

**Run constraints (from `bench/corpus/INDEX.md`).**
- review-jb-001 must never run in the same session as feedback-jb-001 — the latter's exposure contains the former's ground truth.
- 24 of 34 cases carry `known_leak_channels: [project-auto-memory]`, including every case here. Local hand-runs on this machine are invalid. **Running on theborg is what makes these cases valid** — cluster agent pods do not mount this machine's memory.
- neg-cc-001 is `memorization_risk: high`. Never headline a result on it.

**Corpus versioning rule (`bench/README.md`).** Do not edit a case's *exposed* content once results exist against it. No results exist yet, and `case.yaml` is harness-side (never exposed to the solver), so Task 2's retarget is permitted — but it must be recorded in each case's `notes.md`, and every result must cite the corpus commit it ran against.

---

## File structure

| Path | Responsibility |
|---|---|
| `bench/harness/go.mod` | New isolated Go module. Keeps grader code out of the root module, mirroring `bench/corpus/go.mod`. |
| `bench/harness/reviewgrade/expected.go` | Parse `expected-findings/v1` YAML; parse `"482-498 (pre-state)"` line ranges. |
| `bench/harness/reviewgrade/match.go` | Match a `review/v1` finding against an expected finding by anchored file+region. |
| `bench/harness/reviewgrade/report.go` | Recall/precision report over one case. |
| `bench/harness/cmd/reviewgrade/main.go` | CLI: `reviewgrade -expected X.yaml -review record.json`. |
| `bench/harness/materialize.sh` | Turn one corpus case into typed snapshots on theborg; print the `--input` flags. |
| `bench/harness/README.md` | How to run the loop end to end. |
| `bench/nodes/code-review/node.yaml` | The node source definition. |
| `bench/nodes/code-review/prompts/review.md` | The reviewer prompt — the artifact we are actually iterating on. |
| `bench/results/` | Per-run result records, committed as evidence. |
| `Makefile` | Add `test-bench-harness` target. |

---

## Task 1: Preflight — confirm theborg can run nodes

**Files:** none (verification only; record findings in the commit message of Task 2)

- [ ] **Step 1: Confirm fly is logged in and the target is live**

```bash
fly -t theborg status
```

Expected: `logged in successfully`. If not, re-auth: `fly -t theborg login -c https://concourse.home -n main`.

- [ ] **Step 2: Confirm the node routes are deployed**

```bash
fly -t theborg agent nodes list
```

Expected: an empty table (headers only) or a list of existing nodes — **not** a 404. A 404 means theborg is running a build older than `e361c13f15` and must be upgraded before proceeding; stop and report.

- [ ] **Step 3: Confirm snapshots are enabled**

```bash
fly -t theborg agent snapshots list
```

Expected: an empty table or a list. A 404 or "snapshot support disabled" means the web needs `--agent-snapshot-enabled`; stop and report.

- [ ] **Step 4: Record the deployed commit**

```bash
fly -t theborg agent nodes list --json 2>/dev/null | head -5
kubectl --context theborg -n cicd get deploy concourse-web -o jsonpath='{.spec.template.spec.containers[0].image}'; echo
```

Write the image tag down — every result record in Task 7 must cite it alongside the corpus commit.

---

## Task 2: Retarget the placeholder `review-findings/v1` port to `review/v1`

**Files:**
- Modify: `bench/corpus/review-jb-004/case.yaml:19`
- Modify: `bench/corpus/review-jb-002/case.yaml` (its `outputs:` block)
- Modify: `bench/corpus/review-ld-001/case.yaml` (its `outputs:` block)
- Modify: `bench/corpus/review-jb-004/notes.md`, `review-jb-002/notes.md`, `review-ld-001/notes.md`
- Modify: `bench/corpus/INDEX.md`

- [ ] **Step 1: Confirm the three cases and see the exact current text**

```bash
cd /Users/tdmtrader/concourse/concourse
grep -n 'review-findings/v1' bench/corpus/*/case.yaml
```

Expected: matches in `review-jb-002`, `review-jb-004`, `review-ld-001` (and `feedback-jb-002`, which is out of scope — leave it).

- [ ] **Step 2: Edit `review-jb-004/case.yaml`**

Replace lines 12-13 and 19. The `outputs:` block becomes:

```yaml
  outputs:
    findings: review/v1
```

And replace the stale comment on lines 12-13 with:

```yaml
  # Retargeted 2026-07-30: `review-findings/v1` was a curator placeholder. The
  # registered review/v1 contract (agent/snapshot/contracts/review.go) already
  # carries Finding.Evidence[].Locator{Path,Start,End}, which is the anchoring
  # this case needs. The port name stays `findings` — only the type changed.
```

- [ ] **Step 3: Apply the same output-type change to the other two**

In `bench/corpus/review-jb-002/case.yaml` and `bench/corpus/review-ld-001/case.yaml`, change `findings: review-findings/v1` to `findings: review/v1` and add the same four-line comment above the `inputs:` key.

- [ ] **Step 4: Record the change in each `notes.md`**

Append this section to all three `notes.md` files (substituting the case id):

```markdown
## Retarget 2026-07-30

The output port type changed from the curator placeholder `review-findings/v1`
to the registered `review/v1`. Nothing exposed changed: `case.yaml` is
harness-side, `task/` and the pre-state are byte-identical, and
`ground_truth/expected_findings.yaml` is untouched. No results existed against
this case at the time of the change, so the corpus-versioning rule in
bench/README.md is satisfied. Any result must cite the corpus commit it ran
against.
```

- [ ] **Step 5: Update the platform-gap roll-up in INDEX.md**

In `bench/corpus/INDEX.md`, under "About the platform (gaps this corpus surfaced)", replace item 4 with:

```markdown
4. `expected-findings` anchoring (file+region+direction matching semantics,
   `also_true` neutral sets, `non_findings` hard misses) — PARTIALLY CLOSED
   2026-07-30. `review/v1` does carry `Finding.Evidence[].Locator{Path,
   Start, End}`, so file+region anchoring is expressible and the four
   `review-findings/v1` placeholder ports were retargeted to `review/v1`.
   What is still missing is *matching semantics*: nothing in the platform
   scores a produced review against an oracle. bench/harness/reviewgrade is
   the out-of-band stand-in.
```

- [ ] **Step 6: Verify nothing else references the placeholder in a code-review case**

```bash
grep -n 'review-findings/v1' bench/corpus/review-jb-002/case.yaml bench/corpus/review-jb-004/case.yaml bench/corpus/review-ld-001/case.yaml
```

Expected: no output.

- [ ] **Step 7: Commit**

```bash
git add bench/corpus/
git commit -m "test(bench): retarget placeholder review-findings port to review/v1

review-findings/v1 was never a registered snapshot type - review-jb-004's own
case.yaml said so. The registered review/v1 contract already carries
Finding.Evidence[].Locator{Path,Start,End}, which is the anchoring these cases
need. Harness-side change only: task/, pre-state and expected_findings.yaml are
untouched, and no results existed against these cases."
```

---

## Task 3: Create the `bench/harness` module and parse the oracle

**Files:**
- Create: `bench/harness/go.mod`
- Create: `bench/harness/reviewgrade/expected.go`
- Test: `bench/harness/reviewgrade/expected_test.go`

- [ ] **Step 1: Create the module**

```bash
mkdir -p bench/harness/reviewgrade
cat > bench/harness/go.mod <<'EOF'
// Bench harness tooling: out-of-band graders and fixture materialization for
// bench/corpus. A separate module so `go list ./...` in the repository root
// never walks into it and `make test-unit` never compiles it, the same
// mechanism bench/corpus/go.mod uses for fixture data.
//
// Unlike bench/corpus this module DOES build and DOES have tests; run them
// with `make test-bench-harness`.
module github.com/concourse/concourse/bench/harness

go 1.25.6

require gopkg.in/yaml.v3 v3.0.1
EOF
```

- [ ] **Step 2: Write the failing test**

Create `bench/harness/reviewgrade/expected_test.go`:

```go
package reviewgrade_test

import (
	"testing"

	"github.com/concourse/concourse/bench/harness/reviewgrade"
)

const oracleYAML = `
schema: expected-findings/v1
case: review-jb-004
findings:
  - id: F1-linkage-unpinned
    required: true
    severity: major
    title: Destructive pipeline archival is driven by a caller-writable id
    file: atc/db/pipeline_run_factory.go
    region:
      anchor: "func terminalTicketLinkage() (string, []any)"
      lines: "482-498 (pre-state)"
      also:
        - {file: atc/runlifecycle/lifecycler.go, anchor: "Run()", lines: "81-105"}
  - id: F2-chip-noise
    required: false
    severity: minor
    title: Spend chip renders on non-agent builds
    file: web/elm/src/Build/Build.elm
    region:
      anchor: "viewSpendChip"
      lines: "1204"
`

func TestParseExpectedFindings(t *testing.T) {
	oracle, err := reviewgrade.ParseExpected([]byte(oracleYAML))
	if err != nil {
		t.Fatalf("ParseExpected: %v", err)
	}
	if oracle.Case != "review-jb-004" {
		t.Fatalf("Case = %q", oracle.Case)
	}
	if len(oracle.Findings) != 2 {
		t.Fatalf("len(Findings) = %d", len(oracle.Findings))
	}
	if !oracle.Findings[0].Required || oracle.Findings[1].Required {
		t.Fatalf("required flags = %v, %v", oracle.Findings[0].Required, oracle.Findings[1].Required)
	}
	if got := oracle.Required(); len(got) != 1 || got[0].ID != "F1-linkage-unpinned" {
		t.Fatalf("Required() = %#v", got)
	}
}

func TestExpectedFindingSitesIncludesPrimaryAndAlso(t *testing.T) {
	oracle, err := reviewgrade.ParseExpected([]byte(oracleYAML))
	if err != nil {
		t.Fatalf("ParseExpected: %v", err)
	}
	sites := oracle.Findings[0].Sites()
	if len(sites) != 2 {
		t.Fatalf("len(sites) = %d: %#v", len(sites), sites)
	}
	if sites[0].File != "atc/db/pipeline_run_factory.go" || sites[0].Start != 482 || sites[0].End != 498 {
		t.Fatalf("sites[0] = %#v", sites[0])
	}
	if sites[1].File != "atc/runlifecycle/lifecycler.go" || sites[1].Start != 81 || sites[1].End != 105 {
		t.Fatalf("sites[1] = %#v", sites[1])
	}
}

func TestSingleLineRegionParsesAsPointRange(t *testing.T) {
	oracle, err := reviewgrade.ParseExpected([]byte(oracleYAML))
	if err != nil {
		t.Fatalf("ParseExpected: %v", err)
	}
	sites := oracle.Findings[1].Sites()
	if len(sites) != 1 || sites[0].Start != 1204 || sites[0].End != 1204 {
		t.Fatalf("sites = %#v", sites)
	}
}

func TestUnparseableLinesYieldFileLevelSite(t *testing.T) {
	oracle, err := reviewgrade.ParseExpected([]byte(`
schema: expected-findings/v1
case: x
findings:
  - id: F1
    required: true
    file: a/b.go
    region: {anchor: "whatever", lines: "throughout"}
`))
	if err != nil {
		t.Fatalf("ParseExpected: %v", err)
	}
	sites := oracle.Findings[0].Sites()
	if len(sites) != 1 || sites[0].File != "a/b.go" || sites[0].Start != 0 || sites[0].End != 0 {
		t.Fatalf("sites = %#v", sites)
	}
}
```

- [ ] **Step 3: Run the test to verify it fails**

```bash
cd bench/harness && go test ./reviewgrade/ -run TestParseExpectedFindings -count=1
```

Expected: FAIL — `undefined: reviewgrade.ParseExpected` (build failure).

- [ ] **Step 4: Implement `expected.go`**

Create `bench/harness/reviewgrade/expected.go`:

```go
// Package reviewgrade scores a produced review/v1 record against a bench
// corpus expected-findings/v1 oracle.
package reviewgrade

import (
	"fmt"
	"regexp"
	"strconv"

	"gopkg.in/yaml.v3"
)

// Expected is one parsed expected-findings/v1 oracle file.
type Expected struct {
	Schema   string            `yaml:"schema"`
	Case     string            `yaml:"case"`
	Findings []ExpectedFinding `yaml:"findings"`
}

// ExpectedFinding is one human-authored ground-truth defect.
type ExpectedFinding struct {
	ID       string `yaml:"id"`
	Required bool   `yaml:"required"`
	Severity string `yaml:"severity"`
	Class    string `yaml:"class"`
	Title    string `yaml:"title"`
	File     string `yaml:"file"`
	Region   Region `yaml:"region"`
}

// Region is the anchored location of an expected finding.
type Region struct {
	Anchor string     `yaml:"anchor"`
	Lines  string     `yaml:"lines"`
	Also   []AlsoSite `yaml:"also"`
}

// AlsoSite is an additional acceptable location for the same defect.
type AlsoSite struct {
	File   string `yaml:"file"`
	Anchor string `yaml:"anchor"`
	Lines  string `yaml:"lines"`
}

// Site is a resolved file+line-range location. Start == 0 means the oracle
// gave no parseable range, so the whole file is acceptable.
type Site struct {
	File  string
	Start int
	End   int
}

// ParseExpected decodes an expected-findings/v1 document.
func ParseExpected(raw []byte) (*Expected, error) {
	var expected Expected
	if err := yaml.Unmarshal(raw, &expected); err != nil {
		return nil, fmt.Errorf("parse expected findings: %w", err)
	}
	if expected.Schema != "expected-findings/v1" {
		return nil, fmt.Errorf("parse expected findings: schema is %q, want expected-findings/v1", expected.Schema)
	}
	if len(expected.Findings) == 0 {
		return nil, fmt.Errorf("parse expected findings: no findings")
	}
	for index, finding := range expected.Findings {
		if finding.ID == "" {
			return nil, fmt.Errorf("parse expected findings: findings[%d] has no id", index)
		}
	}
	return &expected, nil
}

// Required returns only the findings recall is scored over.
func (expected Expected) Required() []ExpectedFinding {
	required := make([]ExpectedFinding, 0, len(expected.Findings))
	for _, finding := range expected.Findings {
		if finding.Required {
			required = append(required, finding)
		}
	}
	return required
}

// Sites resolves the primary location plus every `also` location.
func (finding ExpectedFinding) Sites() []Site {
	sites := make([]Site, 0, 1+len(finding.Region.Also))
	if finding.File != "" {
		start, end := parseLineRange(finding.Region.Lines)
		sites = append(sites, Site{File: finding.File, Start: start, End: end})
	}
	for _, also := range finding.Region.Also {
		if also.File == "" {
			continue
		}
		start, end := parseLineRange(also.Lines)
		sites = append(sites, Site{File: also.File, Start: start, End: end})
	}
	return sites
}

// rangeExpr matches a leading "N-M" or bare "N", tolerating trailing prose
// such as "482-498 (pre-state)".
var rangeExpr = regexp.MustCompile(`^\s*(\d+)\s*(?:-\s*(\d+))?`)

// parseLineRange returns (0, 0) when the oracle's line text is prose the
// grader cannot pin, which callers treat as a file-level match.
func parseLineRange(text string) (int, int) {
	match := rangeExpr.FindStringSubmatch(text)
	if match == nil {
		return 0, 0
	}
	start, err := strconv.Atoi(match[1])
	if err != nil {
		return 0, 0
	}
	if match[2] == "" {
		return start, start
	}
	end, err := strconv.Atoi(match[2])
	if err != nil || end < start {
		return start, start
	}
	return start, end
}
```

- [ ] **Step 5: Vendor the yaml dependency and run the tests**

```bash
cd bench/harness && go mod tidy && go test ./reviewgrade/ -count=1
```

Expected: `ok  github.com/concourse/concourse/bench/harness/reviewgrade`. All four tests pass.

- [ ] **Step 6: Commit**

```bash
git add bench/harness/
git commit -m "feat(bench): parse expected-findings/v1 oracles in an isolated harness module"
```

---

## Task 4: Match a produced `review/v1` finding against the oracle

**Files:**
- Create: `bench/harness/reviewgrade/match.go`
- Test: `bench/harness/reviewgrade/match_test.go`

The produced record is a `review/v1` body. Rather than importing the root module (which `bench/harness` must not depend on — the root module is huge and `agent/schema` must never depend on it), declare the minimal read-only shape locally. It mirrors `agent/snapshot/contracts/review.go`.

- [ ] **Step 1: Write the failing test**

Create `bench/harness/reviewgrade/match_test.go`:

```go
package reviewgrade_test

import (
	"testing"

	"github.com/concourse/concourse/bench/harness/reviewgrade"
)

func anchor(path string, start, end int) reviewgrade.Anchor {
	return reviewgrade.Anchor{Locator: reviewgrade.Locator{Kind: "line-range", Path: path, Start: &start, End: &end}}
}

func expectedAt(file string, lines string) reviewgrade.ExpectedFinding {
	return reviewgrade.ExpectedFinding{
		ID: "F1", Required: true, File: file,
		Region: reviewgrade.Region{Lines: lines},
	}
}

func TestMatchesOnOverlappingRegionInSameFile(t *testing.T) {
	produced := reviewgrade.Finding{ID: "a", Evidence: []reviewgrade.Anchor{anchor("atc/db/x.go", 490, 495)}}
	if !reviewgrade.Matches(expectedAt("atc/db/x.go", "482-498"), produced, 0) {
		t.Fatal("expected overlapping region to match")
	}
}

func TestDoesNotMatchDifferentFile(t *testing.T) {
	produced := reviewgrade.Finding{ID: "a", Evidence: []reviewgrade.Anchor{anchor("atc/db/other.go", 490, 495)}}
	if reviewgrade.Matches(expectedAt("atc/db/x.go", "482-498"), produced, 0) {
		t.Fatal("different file must not match")
	}
}

func TestDoesNotMatchDistantRegionInSameFile(t *testing.T) {
	produced := reviewgrade.Finding{ID: "a", Evidence: []reviewgrade.Anchor{anchor("atc/db/x.go", 900, 910)}}
	if reviewgrade.Matches(expectedAt("atc/db/x.go", "482-498"), produced, 0) {
		t.Fatal("distant region must not match")
	}
}

func TestToleranceWidensTheAcceptedWindow(t *testing.T) {
	produced := reviewgrade.Finding{ID: "a", Evidence: []reviewgrade.Anchor{anchor("atc/db/x.go", 505, 507)}}
	if reviewgrade.Matches(expectedAt("atc/db/x.go", "482-498"), produced, 0) {
		t.Fatal("must not match at zero tolerance")
	}
	if !reviewgrade.Matches(expectedAt("atc/db/x.go", "482-498"), produced, 10) {
		t.Fatal("must match at tolerance 10")
	}
}

func TestFileLevelOracleMatchesAnyLineInThatFile(t *testing.T) {
	produced := reviewgrade.Finding{ID: "a", Evidence: []reviewgrade.Anchor{anchor("atc/db/x.go", 5000, 5001)}}
	if !reviewgrade.Matches(expectedAt("atc/db/x.go", "throughout"), produced, 0) {
		t.Fatal("unparseable oracle lines must degrade to a file-level match")
	}
}

func TestProducedFindingWithoutLineNumbersMatchesOnPathAlone(t *testing.T) {
	produced := reviewgrade.Finding{ID: "a", Evidence: []reviewgrade.Anchor{{Locator: reviewgrade.Locator{Kind: "file", Path: "atc/db/x.go"}}}}
	if !reviewgrade.Matches(expectedAt("atc/db/x.go", "482-498"), produced, 0) {
		t.Fatal("path-only evidence must match, credited generously")
	}
}

func TestMatchesViaAlsoSite(t *testing.T) {
	expected := reviewgrade.ExpectedFinding{
		ID: "F1", Required: true, File: "atc/db/x.go",
		Region: reviewgrade.Region{
			Lines: "482-498",
			Also:  []reviewgrade.AlsoSite{{File: "atc/runlifecycle/lifecycler.go", Lines: "81-105"}},
		},
	}
	produced := reviewgrade.Finding{ID: "a", Evidence: []reviewgrade.Anchor{anchor("atc/runlifecycle/lifecycler.go", 90, 92)}}
	if !reviewgrade.Matches(expected, produced, 0) {
		t.Fatal("an also-site must match")
	}
}

func TestFindingWithNoEvidenceNeverMatches(t *testing.T) {
	produced := reviewgrade.Finding{ID: "a"}
	if reviewgrade.Matches(expectedAt("atc/db/x.go", "482-498"), produced, 0) {
		t.Fatal("unanchored finding must not match")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
cd bench/harness && go test ./reviewgrade/ -run TestMatches -count=1
```

Expected: FAIL — `undefined: reviewgrade.Matches`, `undefined: reviewgrade.Finding`.

- [ ] **Step 3: Implement `match.go`**

Create `bench/harness/reviewgrade/match.go`:

```go
package reviewgrade

// The produced-review shapes below intentionally duplicate the read-only
// fields of agent/snapshot/contracts.ReviewBody rather than importing the
// root module. bench/harness must stay independent of the product module.

// Review is a produced review/v1 record body.
type Review struct {
	Conclusion string    `json:"conclusion"`
	Summary    string    `json:"summary"`
	Findings   []Finding `json:"findings"`
}

// Finding is one defect the agent reported.
type Finding struct {
	ID             string   `json:"id"`
	Severity       string   `json:"severity"`
	Blocking       bool     `json:"blocking"`
	Category       string   `json:"category"`
	Title          string   `json:"title"`
	Description    string   `json:"description"`
	Evidence       []Anchor `json:"evidence"`
	Recommendation string   `json:"recommendation,omitempty"`
}

// Anchor binds a finding to a location in a subject.
type Anchor struct {
	Subject string  `json:"subject"`
	Locator Locator `json:"locator"`
}

// Locator is the anchored position within a subject.
type Locator struct {
	Kind    string `json:"kind"`
	Path    string `json:"path,omitempty"`
	Start   *int   `json:"start,omitempty"`
	End     *int   `json:"end,omitempty"`
	Pointer string `json:"pointer,omitempty"`
	Value   string `json:"value,omitempty"`
}

// Matches reports whether produced is a location-plausible match for expected.
//
// This is deliberately a LOCATION test only. It answers "did the agent point
// at the right code?", never "did the agent say the right thing" — that
// judgment stays with a human or a judge, per each case's rubric.md. A match
// here is a CANDIDATE, and Report labels it as such.
//
// tolerance widens the accepted line window on both sides, absorbing the
// ordinary drift between an oracle's hand-written range and an agent's
// citation.
func Matches(expected ExpectedFinding, produced Finding, tolerance int) bool {
	if tolerance < 0 {
		tolerance = 0
	}
	for _, site := range expected.Sites() {
		for _, evidence := range produced.Evidence {
			if evidence.Locator.Path == "" || evidence.Locator.Path != site.File {
				continue
			}
			// The oracle gave no pinnable range: any hit in the file counts.
			if site.Start == 0 {
				return true
			}
			// The agent cited a file but no lines: credit it generously.
			if evidence.Locator.Start == nil || evidence.Locator.End == nil {
				return true
			}
			if overlaps(*evidence.Locator.Start, *evidence.Locator.End, site.Start-tolerance, site.End+tolerance) {
				return true
			}
		}
	}
	return false
}

func overlaps(aStart, aEnd, bStart, bEnd int) bool {
	if aEnd < aStart {
		aStart, aEnd = aEnd, aStart
	}
	return aStart <= bEnd && bStart <= aEnd
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
cd bench/harness && go test ./reviewgrade/ -count=1
```

Expected: `ok` — all twelve tests across both files pass.

- [ ] **Step 5: Commit**

```bash
git add bench/harness/reviewgrade/
git commit -m "feat(bench): match produced review/v1 findings to oracle sites by anchor"
```

---

## Task 5: Score a whole case and expose it as a CLI

**Files:**
- Create: `bench/harness/reviewgrade/report.go`
- Test: `bench/harness/reviewgrade/report_test.go`
- Create: `bench/harness/cmd/reviewgrade/main.go`
- Modify: `Makefile`

- [ ] **Step 1: Write the failing test**

Create `bench/harness/reviewgrade/report_test.go`:

```go
package reviewgrade_test

import (
	"testing"

	"github.com/concourse/concourse/bench/harness/reviewgrade"
)

func twoRequiredOracle() *reviewgrade.Expected {
	return &reviewgrade.Expected{
		Schema: "expected-findings/v1", Case: "c",
		Findings: []reviewgrade.ExpectedFinding{
			{ID: "F1", Required: true, File: "a.go", Region: reviewgrade.Region{Lines: "10-20"}},
			{ID: "F2", Required: true, File: "b.go", Region: reviewgrade.Region{Lines: "30-40"}},
			{ID: "F3", Required: false, File: "c.go", Region: reviewgrade.Region{Lines: "50-60"}},
		},
	}
}

func TestScoreCountsRecallOverRequiredOnly(t *testing.T) {
	review := reviewgrade.Review{
		Conclusion: "changes-required",
		Findings: []reviewgrade.Finding{
			{ID: "x", Evidence: []reviewgrade.Anchor{anchor("a.go", 12, 14)}},
			{ID: "y", Evidence: []reviewgrade.Anchor{anchor("c.go", 55, 56)}},
			{ID: "z", Evidence: []reviewgrade.Anchor{anchor("zz.go", 1, 2)}},
		},
	}
	report := reviewgrade.Score(twoRequiredOracle(), review, 0)

	if report.RequiredTotal != 2 || report.RequiredMatched != 1 {
		t.Fatalf("recall = %d/%d", report.RequiredMatched, report.RequiredTotal)
	}
	if len(report.MissedRequired) != 1 || report.MissedRequired[0] != "F2" {
		t.Fatalf("MissedRequired = %v", report.MissedRequired)
	}
	// c.go matched a non-required finding: credited, never penalized.
	if len(report.MatchedOptional) != 1 || report.MatchedOptional[0] != "F3" {
		t.Fatalf("MatchedOptional = %v", report.MatchedOptional)
	}
	// zz.go matched nothing in the oracle: reported for human judgment.
	if len(report.UnmatchedProduced) != 1 || report.UnmatchedProduced[0] != "z" {
		t.Fatalf("UnmatchedProduced = %v", report.UnmatchedProduced)
	}
}

func TestRecallFractionIsOneWhenAllRequiredMatch(t *testing.T) {
	review := reviewgrade.Review{
		Findings: []reviewgrade.Finding{
			{ID: "x", Evidence: []reviewgrade.Anchor{anchor("a.go", 11, 12)}},
			{ID: "y", Evidence: []reviewgrade.Anchor{anchor("b.go", 31, 32)}},
		},
	}
	report := reviewgrade.Score(twoRequiredOracle(), review, 0)
	if report.Recall() != 1.0 {
		t.Fatalf("Recall() = %v", report.Recall())
	}
}

func TestRecallIsZeroWithNoRequiredFindings(t *testing.T) {
	oracle := &reviewgrade.Expected{Schema: "expected-findings/v1", Case: "c",
		Findings: []reviewgrade.ExpectedFinding{{ID: "F3", Required: false, File: "c.go"}}}
	report := reviewgrade.Score(oracle, reviewgrade.Review{}, 0)
	if report.RequiredTotal != 0 || report.Recall() != 0 {
		t.Fatalf("report = %#v", report)
	}
}

func TestOneProducedFindingCanOnlyClaimOneExpected(t *testing.T) {
	oracle := &reviewgrade.Expected{Schema: "expected-findings/v1", Case: "c",
		Findings: []reviewgrade.ExpectedFinding{
			{ID: "F1", Required: true, File: "a.go", Region: reviewgrade.Region{Lines: "10-20"}},
			{ID: "F2", Required: true, File: "a.go", Region: reviewgrade.Region{Lines: "12-18"}},
		}}
	review := reviewgrade.Review{Findings: []reviewgrade.Finding{
		{ID: "x", Evidence: []reviewgrade.Anchor{anchor("a.go", 13, 14)}},
	}}
	report := reviewgrade.Score(oracle, review, 0)
	if report.RequiredMatched != 1 {
		t.Fatalf("one finding must not satisfy two overlapping expected findings: %d", report.RequiredMatched)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
cd bench/harness && go test ./reviewgrade/ -run TestScore -count=1
```

Expected: FAIL — `undefined: reviewgrade.Score`.

- [ ] **Step 3: Implement `report.go`**

Create `bench/harness/reviewgrade/report.go`:

```go
package reviewgrade

import "sort"

// Report is the location-level scorecard for one case.
//
// Every match is a CANDIDATE: it proves the agent anchored at the right code,
// not that it described the right defect. Confirm candidates against the
// case's ground_truth/rubric.md before recording a result.
type Report struct {
	Case              string      `json:"case"`
	Tolerance         int         `json:"tolerance"`
	RequiredTotal     int         `json:"required_total"`
	RequiredMatched   int         `json:"required_matched"`
	Matches           []MatchPair `json:"matches"`
	MissedRequired    []string    `json:"missed_required"`
	MatchedOptional   []string    `json:"matched_optional"`
	UnmatchedProduced []string    `json:"unmatched_produced"`
	Conclusion        string      `json:"conclusion"`
}

// MatchPair records which produced finding claimed which expected finding.
type MatchPair struct {
	ExpectedID string `json:"expected_id"`
	ProducedID string `json:"produced_id"`
	Required   bool   `json:"required"`
}

// Recall is matched-required over total-required, 0 when nothing is required.
func (report Report) Recall() float64 {
	if report.RequiredTotal == 0 {
		return 0
	}
	return float64(report.RequiredMatched) / float64(report.RequiredTotal)
}

// Score pairs produced findings to expected findings greedily, at most one
// produced finding per expected finding and vice versa, in oracle order.
func Score(expected *Expected, review Review, tolerance int) Report {
	report := Report{Case: expected.Case, Tolerance: tolerance, Conclusion: review.Conclusion}
	claimed := make(map[int]bool, len(review.Findings))

	for _, target := range expected.Findings {
		if target.Required {
			report.RequiredTotal++
		}
		matchedIndex := -1
		for index, produced := range review.Findings {
			if claimed[index] {
				continue
			}
			if Matches(target, produced, tolerance) {
				matchedIndex = index
				break
			}
		}
		if matchedIndex < 0 {
			if target.Required {
				report.MissedRequired = append(report.MissedRequired, target.ID)
			}
			continue
		}
		claimed[matchedIndex] = true
		report.Matches = append(report.Matches, MatchPair{
			ExpectedID: target.ID, ProducedID: review.Findings[matchedIndex].ID, Required: target.Required,
		})
		if target.Required {
			report.RequiredMatched++
		} else {
			report.MatchedOptional = append(report.MatchedOptional, target.ID)
		}
	}

	for index, produced := range review.Findings {
		if !claimed[index] {
			report.UnmatchedProduced = append(report.UnmatchedProduced, produced.ID)
		}
	}
	sort.Strings(report.UnmatchedProduced)
	return report
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
cd bench/harness && go test ./reviewgrade/ -count=1
```

Expected: `ok`, all tests pass.

- [ ] **Step 5: Write the CLI**

Create `bench/harness/cmd/reviewgrade/main.go`:

```go
// Command reviewgrade scores a produced review/v1 record against a bench
// corpus expected-findings/v1 oracle.
//
//	reviewgrade -expected bench/corpus/review-jb-004/ground_truth/expected_findings.yaml \
//	            -review  /tmp/run-1/record.json
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/concourse/concourse/bench/harness/reviewgrade"
)

func main() {
	expectedPath := flag.String("expected", "", "path to expected_findings.yaml (required)")
	reviewPath := flag.String("review", "", "path to the produced review/v1 record.json (required)")
	tolerance := flag.Int("tolerance", 10, "line-window tolerance on each side of an oracle region")
	asJSON := flag.Bool("json", false, "print the report as JSON")
	flag.Parse()

	if *expectedPath == "" || *reviewPath == "" {
		flag.Usage()
		os.Exit(2)
	}

	expectedRaw, err := os.ReadFile(*expectedPath)
	if err != nil {
		fail(err)
	}
	oracle, err := reviewgrade.ParseExpected(expectedRaw)
	if err != nil {
		fail(err)
	}

	reviewRaw, err := os.ReadFile(*reviewPath)
	if err != nil {
		fail(err)
	}
	var review reviewgrade.Review
	if err := json.Unmarshal(reviewRaw, &review); err != nil {
		fail(fmt.Errorf("parse review record: %w", err))
	}

	report := reviewgrade.Score(oracle, review, *tolerance)

	if *asJSON {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			fail(err)
		}
		return
	}

	fmt.Printf("case            %s\n", report.Case)
	fmt.Printf("conclusion      %s\n", report.Conclusion)
	fmt.Printf("candidate recall %d/%d  (%.0f%%, tolerance %d lines)\n",
		report.RequiredMatched, report.RequiredTotal, 100*report.Recall(), report.Tolerance)
	for _, match := range report.Matches {
		flag := "optional"
		if match.Required {
			flag = "REQUIRED"
		}
		fmt.Printf("  matched  %-28s <- %-20s [%s]\n", match.ExpectedID, match.ProducedID, flag)
	}
	for _, missed := range report.MissedRequired {
		fmt.Printf("  MISSED   %s\n", missed)
	}
	for _, extra := range report.UnmatchedProduced {
		fmt.Printf("  unmatched produced finding: %s (judge on its own merits)\n", extra)
	}
	fmt.Println("\nMatches are LOCATION candidates only. Confirm each against the case's")
	fmt.Println("ground_truth/rubric.md before recording a result.")
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "reviewgrade: %v\n", err)
	os.Exit(1)
}
```

- [ ] **Step 6: Verify the CLI builds and its usage works**

```bash
cd bench/harness && go build ./... && go run ./cmd/reviewgrade -h
```

Expected: build succeeds; usage lists `-expected`, `-review`, `-tolerance`, `-json`.

- [ ] **Step 7: Add a Makefile target**

In `Makefile`, immediately after the `test-dev-mcp` target, add:

```make
.PHONY: test-bench-harness
test-bench-harness:
	cd bench/harness && go test ./... -count=1
```

- [ ] **Step 8: Run it**

```bash
make test-bench-harness
```

Expected: `ok  github.com/concourse/concourse/bench/harness/reviewgrade`.

- [ ] **Step 9: Commit**

```bash
git add bench/harness/ Makefile
git commit -m "feat(bench): score review/v1 output against an expected-findings oracle"
```

---

## Task 6: Materialize one case into typed snapshots on theborg

**Files:**
- Create: `bench/harness/materialize.sh`
- Create: `bench/harness/README.md`

This turns a corpus case's pinned pre-state into the three snapshots the node needs. It must never expose `case.yaml`, `notes.md` or `ground_truth/` — only `task/` and the pre-state tree.

- [ ] **Step 1: Write the script**

Create `bench/harness/materialize.sh`:

```bash
#!/usr/bin/env bash
# Materialize one bench corpus case into typed snapshots on a fly target and
# print the `fly agent nodes run` input flags.
#
#   ./materialize.sh review-jb-004 theborg
#
# EXPOSURE CONTRACT: only task/ and the pre-state tree are uploaded. case.yaml,
# notes.md and ground_truth/ are harness-side and never leave this machine.
set -euo pipefail

CASE_ID="${1:?usage: materialize.sh CASE-ID [FLY-TARGET]}"
TARGET="${2:-theborg}"

REPO_ROOT="$(git rev-parse --show-toplevel)"
CASE_DIR="$REPO_ROOT/bench/corpus/$CASE_ID"
[ -d "$CASE_DIR" ] || { echo "no such case: $CASE_ID" >&2; exit 1; }

# Pull the pinned refs out of case.yaml. `repository` is the pre-state tree the
# reviewer sees; `terminal` is the artifact the case was backed out of and is
# NEVER materialized.
PRE_REF="$(awk '/^pre_state:/{p=1} p && /ref:/{print $2; exit}' "$CASE_DIR/case.yaml")"
[ -n "$PRE_REF" ] || { echo "could not read pre_state ref from $CASE_ID/case.yaml" >&2; exit 1; }

WORK="$(mktemp -d)"
trap 'git -C "$REPO_ROOT" worktree remove --force "$WORK/repo" 2>/dev/null || true; rm -rf "$WORK"' EXIT

echo "# case $CASE_ID  pre_state $PRE_REF" >&2

# 1. repository/v1 - the tree at pre-state, with .git stripped so no later
#    commit is reachable (bench/README.md "branch contamination").
git -C "$REPO_ROOT" worktree add --detach "$WORK/repo" "$PRE_REF" >&2
rm -rf "$WORK/repo/.git"
REPO_ID="$(fly -t "$TARGET" agent snapshots create --type repository/v1 --from "$WORK/repo" --json | jq -r '.id')"
echo "# repository/v1        $REPO_ID" >&2

# 2. work-item/v1 - the scrubbed trigger content.
mkdir -p "$WORK/work-item"
cp "$CASE_DIR/task/task.md" "$WORK/work-item/"
WORK_ITEM_ID="$(fly -t "$TARGET" agent snapshots create --type work-item/v1 --from "$WORK/work-item" --json | jq -r '.id')"
echo "# work-item/v1         $WORK_ITEM_ID" >&2

# 3. repository-change/v1 - the change under review, when the case exposes one.
CHANGE_FLAG=""
if [ -f "$CASE_DIR/task/change.diff" ]; then
  mkdir -p "$WORK/change"
  cp "$CASE_DIR/task/change.diff" "$WORK/change/"
  CHANGE_ID="$(fly -t "$TARGET" agent snapshots create --type repository-change/v1 --from "$WORK/change" --json | jq -r '.id')"
  echo "# repository-change/v1 $CHANGE_ID" >&2
  CHANGE_FLAG=" --input change=$CHANGE_ID"
fi

echo "--input repository=$REPO_ID --input work-item=$WORK_ITEM_ID$CHANGE_FLAG"
```

- [ ] **Step 2: Make it executable and check its shell syntax**

```bash
chmod +x bench/harness/materialize.sh
bash -n bench/harness/materialize.sh && echo "syntax ok"
```

Expected: `syntax ok`.

- [ ] **Step 3: Verify the exposure contract holds by dry-reading what it would upload**

```bash
ls bench/corpus/review-jb-004/task/
```

Expected: `change.diff` and `task.md` only. If any other file is present, read it and confirm it is exposed-by-design in `case.yaml` before proceeding.

- [ ] **Step 4: Run it against theborg for review-jb-004**

```bash
./bench/harness/materialize.sh review-jb-004 theborg
```

Expected on stderr: four `#` lines naming the case, pre-state ref and three snapshot IDs. Expected on stdout: a single line of `--input` flags. Save that line — Task 8 uses it.

If `jq` is missing, install it (`brew install jq`) rather than hand-parsing JSON.

- [ ] **Step 5: Confirm the snapshots exist and are typed correctly**

```bash
fly -t theborg agent snapshots list
```

Expected: three new rows with types `repository/v1`, `work-item/v1`, `repository-change/v1`.

- [ ] **Step 6: Write the harness README**

Create `bench/harness/README.md`:

```markdown
# bench/harness — out-of-band bench tooling

Tooling that runs bench corpus cases against the platform and grades the
results outside it. A separate Go module so the root module never compiles it;
run its tests with `make test-bench-harness`.

## Why out-of-band

`agent/experiment` cannot target a node: `TargetKind` is `workflow` or
`function` only (agent/experiment/types.go). Rather than change the experiment
subsystem before we know what a node should look like, we execute nodes
directly (`fly agent nodes run`) and grade with `cmd/reviewgrade`.

## The loop

```bash
# 1. Materialize a case into typed snapshots. Prints the --input flags.
./materialize.sh review-jb-004 theborg

# 2. Run the node with those inputs.
fly -t theborg agent nodes run code-review 1 <flags-from-step-1>

# 3. Download the produced review/v1 snapshot and extract record.json.
fly -t theborg agent snapshots download <output-id> --to /tmp/review.tar
mkdir -p /tmp/review && tar -xf /tmp/review.tar -C /tmp/review

# 4. Grade it.
go run ./cmd/reviewgrade \
  -expected ../corpus/review-jb-004/ground_truth/expected_findings.yaml \
  -review /tmp/review/record.json
```

## Rules that are not optional

- **Run on the cluster, not this machine.** 24 of 34 cases carry
  `known_leak_channels: [project-auto-memory]`; this machine's auto-memory
  states their answers verbatim. Cluster agent pods are naturally clean.
- **Never co-run review-jb-001 and feedback-jb-001.** The latter's exposure is
  the former's ground truth.
- **Never headline a result on a `cc` case.** They are `memorization_risk: high`.
- **Cite the corpus commit and the deployed web image** in every result record.
- **A `reviewgrade` match is a location candidate, not a scored finding.**
  Confirm against the case's `ground_truth/rubric.md`.
```

- [ ] **Step 7: Commit**

```bash
git add bench/harness/materialize.sh bench/harness/README.md
git commit -m "feat(bench): materialize corpus cases into typed snapshots"
```

---

## Task 7: Author the `code-review` node

**Files:**
- Create: `bench/nodes/code-review/node.yaml`
- Create: `bench/nodes/code-review/prompts/review.md`

The prompt is the artifact we are iterating on. Keep it deliberately plain in v1 — a strong first prompt hides what the node structure does and does not give the agent.

- [ ] **Step 1: Write the node definition**

Create `bench/nodes/code-review/node.yaml`:

```yaml
schema_version: 1
name: code-review
description: Review one repository change and produce an anchored review/v1 record
inputs:
  - {name: repository, type: repository/v1}
  - {name: change, type: repository-change/v1}
  - {name: work-item, type: work-item/v1}
outputs:
  - {name: review, type: review/v1}
parameters:
  - {name: MINIMUM_SEVERITY, default: minor}
step:
  agent: review
  prompt_file: prompts/review.md
  model: claude-sonnet
  max_turns: 40
  budget_slice_usd: 5.0
```

Three notes on why this is spelled the way it is:

- **No `from:` on outputs.** `snapshot.Port` (`agent/snapshot/types.go:127`) has
  only `name`, `type`, `optional`, `description` — there is no `from` field to
  set. `node_parse.go:189` assigns `From: port.Name` automatically, which is
  exactly what `CompiledNodeDefinition.Validate()` requires.
- **`claude-sonnet`, not opus.** Nothing server-side validates the model string,
  and `claude-sonnet` is the only value with precedent in the repo. An
  unrecognized string would fail at run time for a reason unrelated to what we
  are measuring. Model choice is a variable to sweep later — and note that
  because `model` is a step field, sweeping it means importing a **new node
  version** per model. That coupling is itself a finding; Task 9 asks about it.
- **`capabilities` and `skills` omitted on purpose.** v1 measures the bare agent
  step. `max_turns` and `budget_slice_usd` are present only as spend guards on a
  live cluster, not as tuning.

- [ ] **Step 2: Write the prompt**

Create `bench/nodes/code-review/prompts/review.md`:

```markdown
You are reviewing one change to a repository.

Inputs available to you:

- `repository/` — the full source tree at the tip of the change.
- `change/change.diff` — the change under review, as a unified diff.
- `work-item/task.md` — the review request describing what to look at.

Produce a `review/v1` record with:

- `conclusion` — `accept`, `changes-required`, or `inconclusive`. Use
  `changes-required` only when at least one finding is `blocking: true`; use
  `accept` only when none are.
- `summary` — a short statement of what you reviewed and what you concluded.
- `findings` — one entry per defect, each with:
  - `id` — a short stable slug, e.g. `unpinned-linkage`.
  - `severity` — `observation`, `minor`, `major`, or `critical`.
  - `blocking` — whether this alone should stop the change merging.
  - `category` — e.g. `correctness`, `security`, `performance`.
  - `title` and `description`.
  - `evidence` — **required**. One or more anchors, each naming the `path` the
    defect is in and the `start`/`end` line numbers in that file. Line numbers
    are against the tree you were given. A finding with no anchor cannot be
    acted on.
  - `recommendation` — what to do about it.

Report findings at or above severity `${MINIMUM_SEVERITY}`.

Anchor every finding. If you believe the change is correct, say so with
`conclusion: accept` and no blocking findings — a review that invents defects
to look thorough is worse than one that finds none.
```

- [ ] **Step 3: Verify the node compiles locally before importing**

Write a throwaway check that runs the real compiler over the real source:

```bash
cd /Users/tdmtrader/concourse/concourse
cat > /tmp/nodecheck_test.go <<'EOF'
package workflow_test

import (
	"os"
	"testing"

	"github.com/concourse/concourse/agent/workflow"
)

func TestBenchCodeReviewNodeCompiles(t *testing.T) {
	source, err := os.ReadFile("../../bench/nodes/code-review/node.yaml")
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := os.ReadFile("../../bench/nodes/code-review/prompts/review.md")
	if err != nil {
		t.Fatal(err)
	}
	manifest := workflow.Manifest{
		workflow.NodeFileName: string(source),
		"prompts/review.md":   string(prompt),
	}
	node, err := workflow.CompileNodeDefinition(manifest)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if err := node.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if node.Name != "code-review" {
		t.Fatalf("name = %q", node.Name)
	}
}
EOF
cp /tmp/nodecheck_test.go agent/workflow/nodecheck_test.go
go test ./agent/workflow/ -run TestBenchCodeReviewNodeCompiles -count=1 -v
```

Expected: PASS. If it fails, the error names the exact rule broken (schema version, leaf count, output mapping, or a live-surface rejection) — fix `node.yaml` and rerun.

- [ ] **Step 4: Remove the throwaway check**

```bash
rm agent/workflow/nodecheck_test.go /tmp/nodecheck_test.go
```

This check is deliberately not kept: `bench/` is excluded from the root module's test run, so a permanent test in `agent/workflow` reaching into `bench/` would couple them. Task 8 proves the same thing against the real server.

- [ ] **Step 5: Commit**

```bash
git add bench/nodes/
git commit -m "feat(bench): author the code-review reusable node"
```

---

## Task 8: Import, release, and run the node on theborg

**Files:**
- Create: `bench/results/README.md`
- Create: `bench/results/review-jb-004-run-001.md`

- [ ] **Step 1: Import the node source**

```bash
fly -t theborg agent nodes import bench/nodes/code-review
```

Expected: a created node version, version `1`. Record the content hash it prints.

- [ ] **Step 2: Confirm what the server compiled**

```bash
fly -t theborg agent nodes show code-review 1
```

Expected: the node header with name, version and hash, then the `node.yaml` source. Confirm inputs/outputs match Task 7.

- [ ] **Step 3: Release the version**

```bash
fly -t theborg agent nodes release code-review 1 --compatibility compatible
```

Expected: success. A node must be released before a workflow can reference it; releasing now also keeps the direct-run and future compose paths consistent.

- [ ] **Step 4: Materialize review-jb-004 and run the node**

```bash
FLAGS="$(./bench/harness/materialize.sh review-jb-004 theborg)"
echo "$FLAGS"
fly -t theborg agent nodes run code-review 1 $FLAGS --json
```

Expected: JSON containing a run id. Record it.

- [ ] **Step 5: Watch the run to completion**

```bash
fly -t theborg agent nodes show-run code-review <RUN-ID> --json
```

Repeat until the status is terminal. Expected: `succeeded` with an output snapshot id for the `review` port. If it fails, read the build output in the web UI — the most likely first failures are a missing model credential on the agent step, or the agent emitting a `review/v1` body that fails `ReviewBody.Validate()` (for example `changes-required` with no blocking finding). Both are real findings about the node; record them in Step 8 rather than silently patching around them.

- [ ] **Step 6: Download and extract the produced review**

```bash
fly -t theborg agent snapshots download <OUTPUT-SNAPSHOT-ID> --to /tmp/review.tar
mkdir -p /tmp/review && tar -xf /tmp/review.tar -C /tmp/review
ls /tmp/review
```

Expected: a `record.json` among the extracted files.

- [ ] **Step 7: Grade it**

```bash
cd bench/harness && go run ./cmd/reviewgrade \
  -expected ../corpus/review-jb-004/ground_truth/expected_findings.yaml \
  -review /tmp/review/record.json
```

Expected: a candidate-recall line over review-jb-004's 2 required findings, plus any unmatched produced findings.

- [ ] **Step 8: Record the result**

Create `bench/results/README.md`:

```markdown
# bench results

One file per node run. Every record must cite the corpus commit, the deployed
web image, the node name/version/hash, and the exact input snapshot ids, so a
result stays interpretable after any of them move.

Recall numbers here are LOCATION candidates from bench/harness/reviewgrade,
confirmed by hand against each case's ground_truth/rubric.md. They are not
platform-scored measurements — nothing in the platform scores a review yet.

v0 corpus is all-dev (no holdout). These numbers guide iteration; they do not
support an efficacy claim.
```

Create `bench/results/review-jb-004-run-001.md` and fill in every field from
the actual run:

```markdown
# review-jb-004 — code-review@1 — run 001

| field | value |
|---|---|
| corpus commit | `<git rev-parse HEAD>` |
| web image | `<from Task 1 Step 4>` |
| node | `code-review` version 1, hash `<from Step 1>` |
| inputs | repository=`<id>` change=`<id>` work-item=`<id>` |
| run id | `<id>` |
| output snapshot | `<id>` |
| conclusion | `<accept|changes-required|inconclusive>` |
| candidate recall | `<matched>/<required>` |

## Confirmed against rubric.md

<For each candidate match: expected id, produced id, and whether the produced
finding actually describes the same defect. A location match with a wrong
explanation is NOT a hit — say so.>

## Unmatched produced findings

<For each: is it true-but-unlisted, or a false positive? review-jb-004's oracle
note says unmatched findings must be judged on their own merits.>

## What this says about the NODE

<The point of the exercise. Did the node give the agent what it needed? Was the
diff readable? Did it know the line numbering base? Did it have to guess the
output schema? What would a second node in this workflow need from this one?>
```

- [ ] **Step 9: Commit**

```bash
git add bench/results/
git commit -m "test(bench): record code-review@1 single-node run against review-jb-004"
```

---

## Task 9: Repeat across the signature-compatible cases and roll up

**Files:**
- Create: `bench/results/review-jb-001-run-001.md`
- Create: `bench/results/neg-cc-001-run-001.md`
- Create: `bench/results/ROLLUP-2026-07-30.md`

- [ ] **Step 1: Run review-jb-001**

```bash
FLAGS="$(./bench/harness/materialize.sh review-jb-001 theborg)"
fly -t theborg agent nodes run code-review 1 $FLAGS --json
```

**Before running:** confirm no feedback-jb-001 run is in flight or in this
session's history. review-jb-001's `case.yaml` also requires the
refs-suppressed materialization the script already does (`.git` is stripped).

Grade and record exactly as Task 8 Steps 5-9, into `bench/results/review-jb-001-run-001.md`.

- [ ] **Step 2: Run neg-cc-001**

```bash
FLAGS="$(./bench/harness/materialize.sh neg-cc-001 theborg)"
fly -t theborg agent nodes run code-review 1 $FLAGS --json
```

This is a **negative**: the correct answer is a reasoned `accept`, not a defect
list. `reviewgrade` recall is meaningless here — grade it on `conclusion` and on
whether any produced finding is a fabrication. Record in
`bench/results/neg-cc-001-run-001.md`, and note prominently that it is
`memorization_risk: high` and cannot support a claim on its own.

- [ ] **Step 3: Write the roll-up**

Create `bench/results/ROLLUP-2026-07-30.md` answering, with evidence from the
three runs:

```markdown
# Single-node code-review — first roll-up

Corpus commit: <sha>   Node: code-review@1   Web image: <tag>

## Per-case outcomes

| case | conclusion | confirmed recall | notes |
|---|---|---|---|
| review-jb-004 | | | |
| review-jb-001 | | | |
| neg-cc-001 | | | negative; memorization high |

## What we learned about NODES

1. **Input adequacy** — did `repository` + `change` + `work-item` give the agent
   what it needed, or did it need something we did not model (build logs, test
   results, the base tree)?
2. **Output contract friction** — how often did the agent produce a body that
   failed `ReviewBody.Validate()`? Which rule bit? Does `review/v1` need a
   `non_findings` or `also_true` channel, as INDEX.md predicted?
3. **Anchoring** — did the agent anchor findings at all, and were line numbers
   usable? What tolerance was needed? This directly answers whether
   `review/v1` anchoring is sufficient for recall grading.
4. **Parameterization** — did `MINIMUM_SEVERITY` do anything observable? And
   note what is *not* parameterizable: `model` is a step field, so a model
   sweep needs one node version per model. Is that the right boundary, or
   should model be a node parameter?
5. **Composability** — what would a downstream node (e.g. `implement-fix`
   consuming this `review/v1`) need that this node does not emit?

## What we learned about the PLATFORM

- Does the direct node-run path need anything the workflow path has?
- Is `TargetNode` in `agent/experiment` now worth adding, or is a one-node
  wrapper workflow sufficient? (A one-node workflow is already supported and
  tested — see `agent/workflow/node_reference_test.go`.)

## What we learned about the CORPUS

- Which of the three cases produced usable signal, and which did not?
- Did the retarget to `review/v1` (Task 2) hold up in practice?
```

- [ ] **Step 4: Update the corpus memory note**

Append the roll-up's headline findings to
`bench/corpus/INDEX.md` under "What v0 taught us", and update
`/Users/tdmtrader/.claude/projects/-Users-tdmtrader-concourse-concourse/memory/project_bench_corpus_v0.md`
with the fact that the corpus is now executable via `bench/harness`.

- [ ] **Step 5: Commit**

```bash
git add bench/results/ bench/corpus/INDEX.md
git commit -m "test(bench): roll up first single-node code-review runs"
```

---

## Out of scope (deliberately)

- **`TargetNode` in `agent/experiment`.** Task 9 Step 3 decides whether it is
  worth adding. Until then, no experiment-subsystem change.
- **An evaluator function.** Grading stays out-of-band. Promoting `reviewgrade`
  into a `measurements/v1` evaluator is the follow-on track.
- **review-jb-003 and review-ld-001.** Different signatures (`before`/`after`,
  and no `change`). They need either a second node or optional ports.
- **The other five workflow shapes.** `implement-change` is the obvious second
  node (13 corpus cases, 12 mechanically graded) but is a separate plan.
- **Multi-node workflows.** The whole point is to learn from one node first.

## Risks

- **The agent may not emit valid `review/v1`.** Most likely first failure.
  `ReviewBody.Validate()` enforces conclusion/blocking consistency and unique
  finding ids. This is a real finding about node design, not an obstacle to
  route around.
- **Line numbers may be unusable.** If the agent anchors against the diff's
  line numbers rather than the file's, recall will read near zero for the wrong
  reason. Check the raw `record.json` before believing a 0/2.
- **Three cases is a thin base.** review-jb-001 and review-jb-004 are the only
  solid ones (neg-cc-001 is memorization-high and unvalidated). Treat the first
  roll-up as qualitative learning about node shape, not measurement.
- **Cluster spend is real.** Each run is a full agent invocation on hard cases.
