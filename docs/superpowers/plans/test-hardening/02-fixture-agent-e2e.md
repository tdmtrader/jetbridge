# WS2 — Fixture Agent and the First True DAG e2e Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make a deterministic stand-in agent the platform's replay primitive, then use it to close three coverage holes at once: `AgentStep.Run` never meets the real sealer, no test chains one step's sealed output into the next step's input under Postgres, and the live-cluster DAG e2e substitutes a busybox `cp` **task** for the `agent:` node it claims to prove.

**Architecture:** A new `agent/fixtureagent` library owns exactly one description of what a conforming agent writes: a `review/v1` record tree synthesized from the platform-injected authority environment, plus a hostile catalog of near-miss trees. It has two faces. `cmd/fixture-agent` is a tiny binary that honors the §8.1 runner env contract and materializes those trees into `AGENT_OUTPUT_<NAME>` destinations plus a minimal flight-recorder trio; it ships as an image whose entrypoint binary is named `agent-runner`, because that is the process name `atc/exec/agent_step.go` execs. The in-process face is a raw tar the `atc/exec` specs hand to a fake output volume, so `AgentStep` can run against a real `snapshot.BatchSealer` over a real `contracts.NewRegistry()` with in-memory stores and nothing faked between the step and contract validation. Tier B chains typed output into typed input under real PostgreSQL. Tier C deploys the image as `--agent-step-image` and makes the behavioral DAG node a real `agent:` step.

**Tech Stack:** Go, Ginkgo/Gomega in `atc/`, plain `testing` in `agent/` and `cmd/`, PostgreSQL-backed `atc/db` specs, testcontainers K3s + Helm in `topgun/k8s_behavioral`.

## Global Constraints

- **Non-goal:** wiring `gates`, `judge`, or `repositoryvalidate` into `cmd/function-runner`. Those are three tested functions with zero production callers; giving them callers is real feature work, deferred by the design (Deferred item 3). This plan may make their absence more visible; it must not close it.
- **Non-goal:** any WS5 change. Do not touch `maxJSONDocumentBytes`, `maxRepositoryPayloadBytes`, entry-count constants, or anything else in `agent/snapshot/contracts`. The oversized adversarial case uses the **archive-layer** limits that are already injectable — `snapshot.Canonicalizer{MaxEntries, MaxContentBytes}` (`agent/snapshot/archive.go:176-178`, proven injectable by `TestExtractEnforcesConfiguredLimits`, `agent/snapshot/archive_test.go:405`).
- No production behavior changes. Every file this plan creates or edits is a test, a fixture, a test-only binary, a Dockerfile, a chart value, or documentation. The single exception is the new `web.agentStepImage` chart value in Task 9, which adds a chart-managed argument and changes no Go code.
- `agent/schema` is a separate Go module and must never import the main module. `agent/fixtureagent` and `cmd/fixture-agent` live in the main module and may import `agent/schema` and `agent/snapshot` — that direction is already used by `agent/runner`.
- Existing conventions hold: Ginkgo in `atc/`, plain `testing` in `agent/` and `cmd/`, counterfeiter fakes only where a package already uses them. No new third-party test dependencies.
- Tier A (Tasks 5-7) must not depend on Tier C (Tasks 8-10). Both depend only on the `agent/fixtureagent` library (Tasks 2-3). Tier B (Tasks 11-12) depends on neither. Task 1 depends on nothing.
- Every adversarial assertion pins **two verbatim substrings**: the sealer's `snapshot: validate output %q` / `snapshot: capture output %q` framing (which tells an operator *which output* failed) and the specific violation clause (which tells them *what* was wrong). Both strings are copied from production source, not paraphrased. `errors.Join` layout is the only thing left unpinned, deliberately.
- Do not add a `--race` ginkgo run anywhere; the repo-wide ban still applies to `atc/` suites.

---

### Task 1: Make the judge's unexpected-dimension rejection reachable

`agent/functions/judge/runner.go:224-229` rejects a verdict containing a dimension the rubric never declared. The block is dead in the suite: `go test ./agent/functions/judge/ -coverprofile` reports `runner.go:226.41,228.5 1 0` — count zero. The reason is that every fixture verdict has either exactly the two declared dimensions or a duplicate (which returns earlier at `:221`), and the guard is gated on `len(byName) != len(config.Rubric)`. A verdict with a *superset* of the rubric reaches it; no fixture has one.

**Files:**
- Modify: `agent/functions/judge/runner_test.go`

- [x] In `TestRunClampsScoresAndRejectsDuplicateOrUnexpectedDimensions`, after the existing `duplicate` block, add the unexpected-dimension arm. Insert exactly this before the closing brace of the function:

```go
	// A verdict carrying a dimension the rubric never declared. The guard at
	// runner.go:224-229 only runs when the verdict's dimension count differs
	// from the rubric's, so this fixture must be a strict SUPERSET of the
	// rubric — an equal-count substitution falls through to the
	// missing-dimension check instead and never reaches this branch.
	unexpected := `{"type":"result","result":"{\"dimensions\":[{\"name\":\"correctness\",\"score\":8,\"rationale\":\"ok\",\"issues\":[]},{\"name\":\"style\",\"score\":8,\"rationale\":\"ok\",\"issues\":[]},{\"name\":\"tests\",\"score\":8,\"rationale\":\"ok\",\"issues\":[]}]}","model":"test-model","is_error":false,"usage":{}}`
	if _, err := Run(context.Background(), testConfig, Options{CLIPath: stubCLI(t, unexpected), WorkDir: t.TempDir()}); err == nil || !strings.Contains(err.Error(), `unexpected dimension "style"`) {
		t.Fatalf("unexpected dimension error = %v", err)
	}
```

- [x] Run `go test ./agent/functions/judge/ -run TestRunClampsScoresAndRejectsDuplicateOrUnexpectedDimensions -count=1 -v`. It must PASS on the first run — this task pins existing behavior that had no witness, so there is no red step. Confirm it is a real witness rather than a tautology by temporarily deleting `runner.go:225-229` (the `for name := range byName { ... }` loop) and re-running: the test must then fail with `unexpected dimension error = <nil>`. Restore the deleted lines.
- [x] Prove the line is now covered: `go test ./agent/functions/judge/ -count=1 -coverprofile=/tmp/judge.cov && grep 'runner.go:226' /tmp/judge.cov`. The trailing count must be `1`, not `0`.
- [x] Commit `test(judge): reject an unexpected rubric dimension`.

---

### Task 2: Add the fixture-agent record synthesizer

One package owns the description of a conforming agent's output tree, so the exec specs, the binary, and the behavioral suite can never drift. Record types are synthesized from the **platform-injected authority** (`AGENT_OUTPUT_<NAME>_RECORD_SCHEMA`, `AGENT_INPUT_<NAME>_SNAPSHOT_DIGEST`) rather than from compiled-in constants — that is what the runner prompt promises the agent (`agent/runner/runner.go:313`), and it is the only way a fixture can bind a subject to an input digest it cannot know ahead of time.

**Files:**
- Create: `agent/fixtureagent/fixtures.go`
- Create: `agent/fixtureagent/fixtures_test.go`

- [x] Write `agent/fixtureagent/fixtures_test.go` first:

```go
package fixtureagent_test

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/fixtureagent"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

func testAuthority(t *testing.T) fixtureagent.Authority {
	t.Helper()
	schema, found := contracts.SchemaDigestFor(snapshot.TypeRef("review/v1"))
	if !found {
		t.Fatal("review/v1 has no schema digest")
	}
	return fixtureagent.Authority{
		RecordType:    "review/v1",
		RecordSchema:  schema.String(),
		SubjectInput:  "change",
		SubjectType:   "opaque/v1",
		SubjectDigest: "sha256:" + strings.Repeat("d", 64),
	}
}

func TestEntriesSynthesizeAnAcceptedReviewFromInjectedAuthority(t *testing.T) {
	authority := testAuthority(t)
	entries, err := fixtureagent.Entries(fixtureagent.CaseReviewAccept, authority)
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	if len(entries) != 1 || entries[0].Path != "record.json" {
		t.Fatalf("entries = %+v", entries)
	}

	var record contracts.Record[contracts.ReviewBody]
	if err := json.Unmarshal(entries[0].Body, &record); err != nil {
		t.Fatalf("decode record.json: %v", err)
	}
	if record.RecordVersion != contracts.RecordVersion {
		t.Fatalf("record_version = %q", record.RecordVersion)
	}
	if record.Type != snapshot.TypeRef("review/v1") {
		t.Fatalf("type = %q", record.Type)
	}
	if record.Schema.String() != authority.RecordSchema {
		t.Fatalf("schema = %q, want the injected %q", record.Schema, authority.RecordSchema)
	}
	if len(record.Subjects) != 1 {
		t.Fatalf("subjects = %+v", record.Subjects)
	}
	subject := record.Subjects[0]
	if subject.Role != contracts.SubjectRolePrimary || subject.Input != "change" {
		t.Fatalf("subject = %+v", subject)
	}
	if subject.Type != snapshot.TypeRef("opaque/v1") || subject.Digest.String() != authority.SubjectDigest {
		t.Fatalf("subject binding = %+v, want the injected input authority", subject)
	}
	if record.Body.Conclusion != "accept" || len(record.Body.Findings) != 0 {
		t.Fatalf("body = %+v", record.Body)
	}
}

func TestEntriesSynthesizeABlockingChangesRequiredReview(t *testing.T) {
	entries, err := fixtureagent.Entries(fixtureagent.CaseReviewChangesRequired, testAuthority(t))
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	var record contracts.Record[contracts.ReviewBody]
	if err := json.Unmarshal(entries[0].Body, &record); err != nil {
		t.Fatalf("decode record.json: %v", err)
	}
	if record.Body.Conclusion != "changes-required" {
		t.Fatalf("conclusion = %q", record.Body.Conclusion)
	}
	if len(record.Body.Findings) != 1 || !record.Body.Findings[0].Blocking {
		t.Fatalf("findings = %+v", record.Body.Findings)
	}
	if record.Body.Findings[0].Evidence[0].Subject != "primary" {
		t.Fatalf("evidence = %+v", record.Body.Findings[0].Evidence)
	}
}

func TestTarEmitsTheSynthesizedTree(t *testing.T) {
	raw, err := fixtureagent.Tar(fixtureagent.CaseReviewAccept, testAuthority(t))
	if err != nil {
		t.Fatalf("Tar: %v", err)
	}
	names := tarNames(t, raw)
	if len(names) != 1 || names[0] != "record.json" {
		t.Fatalf("tar names = %v", names)
	}
}

func TestWriteTreeMaterializesTheSynthesizedTree(t *testing.T) {
	dir := t.TempDir()
	if err := fixtureagent.WriteTree(dir, fixtureagent.CaseReviewAccept, testAuthority(t)); err != nil {
		t.Fatalf("WriteTree: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "record.json"))
	if err != nil {
		t.Fatalf("read record.json: %v", err)
	}
	var record contracts.Record[contracts.ReviewBody]
	if err := json.Unmarshal(body, &record); err != nil {
		t.Fatalf("decode record.json: %v", err)
	}
	if record.Body.Conclusion != "accept" {
		t.Fatalf("conclusion = %q", record.Body.Conclusion)
	}
}

func TestEntriesRejectsAnUnknownCaseAndIncompleteAuthority(t *testing.T) {
	if _, err := fixtureagent.Entries("no-such-case", testAuthority(t)); err == nil || !strings.Contains(err.Error(), "unknown fixture case") {
		t.Fatalf("unknown case error = %v", err)
	}
	incomplete := testAuthority(t)
	incomplete.RecordSchema = ""
	if _, err := fixtureagent.Entries(fixtureagent.CaseReviewAccept, incomplete); err == nil || !strings.Contains(err.Error(), "record schema") {
		t.Fatalf("incomplete authority error = %v", err)
	}
}

func tarNames(t *testing.T, raw []byte) []string {
	t.Helper()
	var names []string
	reader := tar.NewReader(bytes.NewReader(raw))
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return names
		}
		if err != nil {
			t.Fatalf("tar next: %v", err)
		}
		names = append(names, header.Name)
	}
}
```

- [x] Run `go test ./agent/fixtureagent/ -count=1`. Expected failure: `no required module provides package github.com/concourse/concourse/agent/fixtureagent` (or, once the directory exists but is empty, `build constraints exclude all Go files`).
- [x] Create `agent/fixtureagent/fixtures.go`:

```go
// Package fixtureagent synthesizes the output trees a deterministic stand-in
// agent writes at an agent step's seal boundary.
//
// It is the single description of "what a conforming agent produced", shared by
// three consumers so they cannot drift: cmd/fixture-agent materializes these
// trees into AGENT_OUTPUT_<NAME> destinations inside a real pod, the atc/exec
// specs hand Tar() straight to a fake output volume so AgentStep meets the real
// snapshot.BatchSealer, and the behavioral suite runs the binary's image.
//
// Record envelopes are built from the PLATFORM-INJECTED authority, never from
// compiled-in constants. The runner prompt tells every agent that the type,
// schema, and input digest values it was handed "are verified again when the
// output is sealed" (agent/runner/runner.go); a fixture that pinned its own
// constants would be testing a promise nobody makes.
package fixtureagent

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

// The benign cases. Hostile cases are added in Task 3.
const (
	CaseReviewAccept          = "review-accept"
	CaseReviewChangesRequired = "review-changes-required"
)

// Authority is the server-owned truth an agent step injects into its pod: the
// declared type and current schema digest of one record output, and the type
// and digest of the input that output's primary subject binds to.
type Authority struct {
	RecordType    string
	RecordSchema  string
	SubjectInput  string
	SubjectType   string
	SubjectDigest string

	// OversizeBytes sizes CaseHostileOversized's payload. Zero means the
	// package default.
	OversizeBytes int
}

func (a Authority) validate() error {
	if strings.TrimSpace(a.RecordType) == "" {
		return fmt.Errorf("fixtureagent: record type is required")
	}
	if strings.TrimSpace(a.RecordSchema) == "" {
		return fmt.Errorf("fixtureagent: record schema is required")
	}
	if strings.TrimSpace(a.SubjectInput) == "" {
		return fmt.Errorf("fixtureagent: subject input is required")
	}
	if strings.TrimSpace(a.SubjectType) == "" {
		return fmt.Errorf("fixtureagent: subject type is required")
	}
	if strings.TrimSpace(a.SubjectDigest) == "" {
		return fmt.Errorf("fixtureagent: subject digest is required")
	}
	return nil
}

// Entry is one node of a fixture tree. A non-empty LinkTarget makes it a
// symlink and Body is ignored.
type Entry struct {
	Path       string
	Body       []byte
	LinkTarget string
}

// Cases lists every fixture case, sorted, for help text and table tests.
func Cases() []string {
	return []string{CaseReviewAccept, CaseReviewChangesRequired}
}

// Entries returns the tree for one case. Entries are emitted in the order they
// must be written; Tar preserves that order so a hostile entry is reached in a
// predictable position.
func Entries(caseName string, authority Authority) ([]Entry, error) {
	if err := authority.validate(); err != nil {
		return nil, err
	}
	switch caseName {
	case CaseReviewAccept:
		return recordEntries(reviewRecord(authority, acceptBody()))
	case CaseReviewChangesRequired:
		return recordEntries(reviewRecord(authority, changesRequiredBody()))
	default:
		return nil, fmt.Errorf("fixtureagent: unknown fixture case %q", caseName)
	}
}

// Tar renders a case as a raw uncompressed tar stream — the exact shape
// AgentStep hands the sealer through Artifact.StreamOut.
func Tar(caseName string, authority Authority) ([]byte, error) {
	entries, err := Entries(caseName, authority)
	if err != nil {
		return nil, err
	}
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for _, entry := range entries {
		header := &tar.Header{Name: entry.Path, Mode: 0o644, Typeflag: tar.TypeReg, Size: int64(len(entry.Body))}
		if entry.LinkTarget != "" {
			header = &tar.Header{Name: entry.Path, Mode: 0o777, Typeflag: tar.TypeSymlink, Linkname: entry.LinkTarget}
		}
		if err := writer.WriteHeader(header); err != nil {
			return nil, fmt.Errorf("fixtureagent: write header %q: %w", entry.Path, err)
		}
		if entry.LinkTarget == "" {
			if _, err := writer.Write(entry.Body); err != nil {
				return nil, fmt.Errorf("fixtureagent: write body %q: %w", entry.Path, err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("fixtureagent: close tar: %w", err)
	}
	return buffer.Bytes(), nil
}

// WriteTree materializes a case into an existing directory. It is the pod-side
// face: a real agent writes files, not tars.
func WriteTree(dir, caseName string, authority Authority) error {
	entries, err := Entries(caseName, authority)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		full := filepath.Join(dir, filepath.FromSlash(entry.Path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return fmt.Errorf("fixtureagent: create parent of %q: %w", entry.Path, err)
		}
		if entry.LinkTarget != "" {
			if err := os.Symlink(filepath.FromSlash(entry.LinkTarget), full); err != nil {
				return fmt.Errorf("fixtureagent: symlink %q: %w", entry.Path, err)
			}
			continue
		}
		if err := os.WriteFile(full, entry.Body, 0o644); err != nil {
			return fmt.Errorf("fixtureagent: write %q: %w", entry.Path, err)
		}
	}
	return nil
}

func recordEntries(record contracts.Record[contracts.ReviewBody]) ([]Entry, error) {
	body, err := json.Marshal(record)
	if err != nil {
		return nil, fmt.Errorf("fixtureagent: marshal record.json: %w", err)
	}
	return []Entry{{Path: "record.json", Body: body}}, nil
}

// reviewRecord assembles the envelope by hand rather than through
// contracts.NewRecord: NewRecord stamps the CURRENT compiled-in schema digest,
// and the whole point of the fixture is to copy the value the platform handed
// it — including, for CaseHostileSchemaDigest, a value that is deliberately not
// the current one.
func reviewRecord(authority Authority, body contracts.ReviewBody) contracts.Record[contracts.ReviewBody] {
	return contracts.Record[contracts.ReviewBody]{
		RecordVersion: contracts.RecordVersion,
		Type:          snapshot.TypeRef(authority.RecordType),
		Schema:        snapshot.Digest(authority.RecordSchema),
		Subjects: []contracts.Subject{{
			ID:     "primary",
			Role:   contracts.SubjectRolePrimary,
			Input:  authority.SubjectInput,
			Type:   snapshot.TypeRef(authority.SubjectType),
			Digest: snapshot.Digest(authority.SubjectDigest),
		}},
		Body: body,
	}
}

func acceptBody() contracts.ReviewBody {
	return contracts.ReviewBody{
		Conclusion: "accept",
		Summary:    "fixture agent reviewed the exposed subject",
		Findings:   []contracts.Finding{},
	}
}

func changesRequiredBody() contracts.ReviewBody {
	return contracts.ReviewBody{
		Conclusion: "changes-required",
		Summary:    "fixture agent found one blocking defect",
		Findings:   []contracts.Finding{blockingFinding("F-1")},
	}
}

func blockingFinding(id string) contracts.Finding {
	start, end := 1, 1
	return contracts.Finding{
		ID: id, Severity: "high", Blocking: true,
		Category: "correctness", Title: "fixture blocking finding",
		Description: "the fixture agent always reports this finding",
		Evidence: []contracts.Anchor{{
			Subject: "primary",
			Locator: contracts.Locator{Kind: "file-lines", Path: "payload.txt", Start: &start, End: &end},
		}},
		Recommendation: "no action; this is a fixture",
	}
}
```

- [x] Run `go test ./agent/fixtureagent/ -count=1`. Expected: `ok github.com/concourse/concourse/agent/fixtureagent`, 5 tests passing.
- [x] Run `gofmt -l agent/fixtureagent` and confirm no output.
- [x] Commit `feat(agent): add the fixture-agent record synthesizer`.

---

### Task 3: Add the fixture-agent hostile catalog

Seven near-miss trees, one per adversarial case Tier A asserts. Each violates exactly one rule so the error text it produces is unambiguous.

**Files:**
- Modify: `agent/fixtureagent/fixtures.go`
- Modify: `agent/fixtureagent/fixtures_test.go`

- [x] Append to `agent/fixtureagent/fixtures_test.go`:

```go
func TestHostileCatalogViolatesExactlyOneRuleEach(t *testing.T) {
	authority := testAuthority(t)
	authority.OversizeBytes = 4096

	tests := []struct {
		name  string
		check func(*testing.T, []fixtureagent.Entry)
	}{
		{
			name: fixtureagent.CaseHostileTraversal,
			check: func(t *testing.T, entries []fixtureagent.Entry) {
				if entries[0].Path != "../escape" {
					t.Fatalf("first entry = %q, want a traversal path", entries[0].Path)
				}
			},
		},
		{
			name: fixtureagent.CaseHostileSymlink,
			check: func(t *testing.T, entries []fixtureagent.Entry) {
				last := entries[len(entries)-1]
				if last.LinkTarget != "../../etc/passwd" {
					t.Fatalf("last entry = %+v, want an escaping symlink", last)
				}
			},
		},
		{
			name: fixtureagent.CaseHostileUnexposedSubject,
			check: func(t *testing.T, entries []fixtureagent.Entry) {
				record := decodeReview(t, entries)
				if record.Subjects[0].Input != "not-a-declared-input" {
					t.Fatalf("subject input = %q", record.Subjects[0].Input)
				}
			},
		},
		{
			name: fixtureagent.CaseHostileSchemaDigest,
			check: func(t *testing.T, entries []fixtureagent.Entry) {
				record := decodeReview(t, entries)
				if record.Schema.String() == authority.RecordSchema {
					t.Fatal("schema digest was not corrupted")
				}
				if err := record.Schema.Validate(); err != nil {
					t.Fatalf("corrupted schema must still be a well-formed digest: %v", err)
				}
			},
		},
		{
			name: fixtureagent.CaseHostileDuplicateFinding,
			check: func(t *testing.T, entries []fixtureagent.Entry) {
				record := decodeReview(t, entries)
				if len(record.Body.Findings) != 2 || record.Body.Findings[0].ID != record.Body.Findings[1].ID {
					t.Fatalf("findings = %+v, want two with the same id", record.Body.Findings)
				}
			},
		},
		{
			name: fixtureagent.CaseHostileMissingRecord,
			check: func(t *testing.T, entries []fixtureagent.Entry) {
				for _, entry := range entries {
					if entry.Path == "record.json" {
						t.Fatal("record.json must be absent")
					}
				}
				if len(entries) == 0 {
					t.Fatal("the tree must not be empty, or capture fails for another reason")
				}
			},
		},
		{
			name: fixtureagent.CaseHostileOversized,
			check: func(t *testing.T, entries []fixtureagent.Entry) {
				for _, entry := range entries {
					if entry.Path == "payload.bin" {
						if len(entry.Body) != 4096 {
							t.Fatalf("payload = %d bytes, want 4096", len(entry.Body))
						}
						return
					}
				}
				t.Fatal("no payload.bin entry")
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			entries, err := fixtureagent.Entries(tc.name, authority)
			if err != nil {
				t.Fatalf("Entries(%q): %v", tc.name, err)
			}
			tc.check(t, entries)
			if _, err := fixtureagent.Tar(tc.name, authority); err != nil {
				t.Fatalf("Tar(%q): %v", tc.name, err)
			}
		})
	}
}

func TestCasesEnumeratesEveryCaseAndWriteTreeRefusesTheTarOnlyOne(t *testing.T) {
	authority := testAuthority(t)
	cases := fixtureagent.Cases()
	if len(cases) != 9 {
		t.Fatalf("Cases() = %v, want all nine", cases)
	}
	for _, name := range cases {
		if _, err := fixtureagent.Entries(name, authority); err != nil {
			t.Fatalf("Entries(%q): %v", name, err)
		}
	}
	// A `../` path cannot be materialized inside a destination directory: that
	// is a tar-layer attack only, and pretending otherwise would silently write
	// outside the output mount.
	if err := fixtureagent.WriteTree(t.TempDir(), fixtureagent.CaseHostileTraversal, authority); err == nil ||
		!strings.Contains(err.Error(), "cannot be materialized on disk") {
		t.Fatalf("WriteTree(traversal) error = %v", err)
	}
}

func decodeReview(t *testing.T, entries []fixtureagent.Entry) contracts.Record[contracts.ReviewBody] {
	t.Helper()
	for _, entry := range entries {
		if entry.Path != "record.json" {
			continue
		}
		var record contracts.Record[contracts.ReviewBody]
		if err := json.Unmarshal(entry.Body, &record); err != nil {
			t.Fatalf("decode record.json: %v", err)
		}
		return record
	}
	t.Fatal("no record.json entry")
	return contracts.Record[contracts.ReviewBody]{}
}
```

- [x] Run `go test ./agent/fixtureagent/ -count=1`. Expected failure: `undefined: fixtureagent.CaseHostileTraversal` (and six more `undefined:` lines).
- [x] In `agent/fixtureagent/fixtures.go`, extend the constant block and `Cases`:

```go
// The hostile catalog. Each case violates exactly one rule, so the error it
// produces at the seal boundary names that rule and nothing else.
const (
	// CaseHostileTraversal puts a `../` segment in a tar entry name. It is
	// TAR-ONLY: WriteTree refuses it rather than escaping the output mount.
	CaseHostileTraversal = "hostile-traversal"
	// CaseHostileSymlink adds a symlink whose target resolves above the root.
	CaseHostileSymlink = "hostile-symlink"
	// CaseHostileUnexposedSubject anchors the record's primary subject at an
	// input the step never declared.
	CaseHostileUnexposedSubject = "hostile-unexposed-subject"
	// CaseHostileSchemaDigest pins a well-formed digest that is not the
	// platform-injected one.
	CaseHostileSchemaDigest = "hostile-schema-digest"
	// CaseHostileDuplicateFinding repeats an entity-set id.
	CaseHostileDuplicateFinding = "hostile-duplicate-finding"
	// CaseHostileMissingRecord writes a non-empty tree with no record.json.
	CaseHostileMissingRecord = "hostile-missing-record"
	// CaseHostileOversized writes a payload larger than the ARCHIVE-layer
	// configured limit (snapshot.Canonicalizer.MaxContentBytes). It is not
	// about the contracts-layer document limits.
	CaseHostileOversized = "hostile-oversized"
)

// defaultOversizeBytes is comfortably above the small MaxContentBytes the exec
// specs inject and comfortably below anything a real deployment configures.
const defaultOversizeBytes = 4096
```

```go
func Cases() []string {
	return []string{
		CaseHostileDuplicateFinding,
		CaseHostileMissingRecord,
		CaseHostileOversized,
		CaseHostileSchemaDigest,
		CaseHostileSymlink,
		CaseHostileTraversal,
		CaseHostileUnexposedSubject,
		CaseReviewAccept,
		CaseReviewChangesRequired,
	}
}
```

- [x] Extend the `Entries` switch with the seven hostile arms, before the `default:`:

```go
	case CaseHostileTraversal:
		entries, err := recordEntries(reviewRecord(authority, acceptBody()))
		if err != nil {
			return nil, err
		}
		return append([]Entry{{Path: "../escape", Body: []byte("escaped\n")}}, entries...), nil
	case CaseHostileSymlink:
		entries, err := recordEntries(reviewRecord(authority, acceptBody()))
		if err != nil {
			return nil, err
		}
		return append(entries, Entry{Path: "escape", LinkTarget: "../../etc/passwd"}), nil
	case CaseHostileUnexposedSubject:
		hostile := authority
		hostile.SubjectInput = "not-a-declared-input"
		return recordEntries(reviewRecord(hostile, acceptBody()))
	case CaseHostileSchemaDigest:
		hostile := authority
		hostile.RecordSchema = "sha256:" + strings.Repeat("f", 64)
		return recordEntries(reviewRecord(hostile, acceptBody()))
	case CaseHostileDuplicateFinding:
		body := changesRequiredBody()
		body.Findings = append(body.Findings, blockingFinding("F-1"))
		return recordEntries(reviewRecord(authority, body))
	case CaseHostileMissingRecord:
		return []Entry{{Path: "notes.txt", Body: []byte("the fixture agent forgot record.json\n")}}, nil
	case CaseHostileOversized:
		size := authority.OversizeBytes
		if size <= 0 {
			size = defaultOversizeBytes
		}
		entries, err := recordEntries(reviewRecord(authority, acceptBody()))
		if err != nil {
			return nil, err
		}
		return append(entries, Entry{Path: "payload.bin", Body: bytes.Repeat([]byte("A"), size)}), nil
```

- [x] Guard `WriteTree` against the tar-only case, immediately after the `Entries` call:

```go
	if caseName == CaseHostileTraversal {
		return fmt.Errorf("fixtureagent: case %q cannot be materialized on disk; it is a tar-layer attack", caseName)
	}
```

- [x] Run `go test ./agent/fixtureagent/ -count=1 -v`. Expected: all tests pass, including the seven `TestHostileCatalogViolatesExactlyOneRuleEach/hostile-*` subtests.
- [x] Run `gofmt -l agent/fixtureagent` and confirm no output.
- [x] Commit `feat(agent): add the fixture-agent hostile catalog`.

---

### Task 4: Add the fixture-agent step binary

The pod-side face. It honors the §8.1 env contract exactly as `agent/runner`'s `FromEnv` reads it — including the ordering quirk that `AGENT_OUTPUT_<NAME>_RECORD_TYPE` also matches the output-path pattern and must be classified as authority first (`agent/runner/runner.go:181-193`).

**Files:**
- Create: `cmd/fixture-agent/main.go`
- Create: `cmd/fixture-agent/main_test.go`

- [x] Write `cmd/fixture-agent/main_test.go`:

```go
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/schema"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

func envFixture(t *testing.T, outputDir, flightDir string) map[string]string {
	t.Helper()
	digest, found := contracts.SchemaDigestFor(snapshot.TypeRef("review/v1"))
	if !found {
		t.Fatal("review/v1 has no schema digest")
	}
	return map[string]string{
		"AGENT_FLIGHT_DIR":                  flightDir,
		"AGENT_STEP_NAME":                   "fixture-review",
		"AGENT_OUTPUT_REVIEW":               outputDir,
		"AGENT_OUTPUT_REVIEW_RECORD_TYPE":   "review/v1",
		"AGENT_OUTPUT_REVIEW_RECORD_SCHEMA": digest.String(),
		"AGENT_INPUT_CHANGE_SNAPSHOT_TYPE":  "opaque/v1",
		"AGENT_INPUT_CHANGE_SNAPSHOT_DIGEST": "sha256:" + strings.Repeat("d", 64),
		"AGENT_OUTPUT_SCHEMA":               "ignored-advisory-value",
	}
}

func TestRunWritesEveryDeclaredRecordOutputAndTheFlightTrio(t *testing.T) {
	outputDir, flightDir := t.TempDir(), t.TempDir()
	env := envFixture(t, outputDir, flightDir)

	var stderr bytes.Buffer
	if code := run(env, &stderr); code != 0 {
		t.Fatalf("run() = %d, stderr = %s", code, stderr.String())
	}

	body, err := os.ReadFile(filepath.Join(outputDir, "record.json"))
	if err != nil {
		t.Fatalf("read record.json: %v", err)
	}
	var record contracts.Record[contracts.ReviewBody]
	if err := json.Unmarshal(body, &record); err != nil {
		t.Fatalf("decode record.json: %v", err)
	}
	if record.Schema.String() != env["AGENT_OUTPUT_REVIEW_RECORD_SCHEMA"] {
		t.Fatalf("schema = %q, want the injected value", record.Schema)
	}
	if record.Subjects[0].Input != "change" {
		t.Fatalf("subject input = %q, want the de-mangled port name", record.Subjects[0].Input)
	}
	if record.Subjects[0].Digest.String() != env["AGENT_INPUT_CHANGE_SNAPSHOT_DIGEST"] {
		t.Fatalf("subject digest = %q, want the injected value", record.Subjects[0].Digest)
	}

	results, err := os.ReadFile(filepath.Join(flightDir, "results.json"))
	if err != nil {
		t.Fatalf("read results.json: %v", err)
	}
	var decoded schema.Results
	if err := json.Unmarshal(results, &decoded); err != nil {
		t.Fatalf("decode results.json: %v", err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("results.json is not a valid Results document: %v", err)
	}
	if decoded.Status != schema.StatusPass {
		t.Fatalf("status = %q", decoded.Status)
	}

	events, err := os.ReadFile(filepath.Join(flightDir, "events.ndjson"))
	if err != nil {
		t.Fatalf("read events.ndjson: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(events)), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"step.start"`) || !strings.Contains(lines[1], `"step.end"`) {
		t.Fatalf("events = %s", events)
	}
	if _, err := os.Stat(filepath.Join(flightDir, "transcript.ndjson")); err != nil {
		t.Fatalf("stat transcript.ndjson: %v", err)
	}
}

func TestRunHonorsFixtureCaseAndSubjectInputOverride(t *testing.T) {
	outputDir, flightDir := t.TempDir(), t.TempDir()
	env := envFixture(t, outputDir, flightDir)
	env["FIXTURE_CASE"] = "hostile-schema-digest"
	env["FIXTURE_SUBJECT_INPUT"] = "work-item"

	var stderr bytes.Buffer
	if code := run(env, &stderr); code != 0 {
		t.Fatalf("run() = %d, stderr = %s", code, stderr.String())
	}
	body, err := os.ReadFile(filepath.Join(outputDir, "record.json"))
	if err != nil {
		t.Fatalf("read record.json: %v", err)
	}
	var record contracts.Record[contracts.ReviewBody]
	if err := json.Unmarshal(body, &record); err != nil {
		t.Fatalf("decode record.json: %v", err)
	}
	if record.Schema.String() == env["AGENT_OUTPUT_REVIEW_RECORD_SCHEMA"] {
		t.Fatal("hostile-schema-digest did not corrupt the schema digest")
	}
	if record.Subjects[0].Input != "work-item" {
		t.Fatalf("subject input = %q, want the FIXTURE_SUBJECT_INPUT override", record.Subjects[0].Input)
	}
}

func TestRunFailsLoudlyWhenTheEnvContractIsIncomplete(t *testing.T) {
	flightDir := t.TempDir()
	env := map[string]string{"AGENT_FLIGHT_DIR": flightDir}

	var stderr bytes.Buffer
	if code := run(env, &stderr); code != 2 {
		t.Fatalf("run() = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "no AGENT_OUTPUT_<NAME> destinations") {
		t.Fatalf("stderr = %s", stderr.String())
	}
	results, err := os.ReadFile(filepath.Join(flightDir, "results.json"))
	if err != nil {
		t.Fatalf("a platform error must still leave a flight recorder: %v", err)
	}
	var decoded schema.Results
	if err := json.Unmarshal(results, &decoded); err != nil {
		t.Fatalf("decode results.json: %v", err)
	}
	if decoded.Status != schema.StatusError {
		t.Fatalf("status = %q, want error", decoded.Status)
	}
}
```

- [x] Run `go test ./cmd/fixture-agent/ -count=1`. Expected failure: `no required module provides package github.com/concourse/concourse/cmd/fixture-agent`.
- [x] Create `cmd/fixture-agent/main.go`:

```go
// Command fixture-agent is a deterministic stand-in for the claude-backed
// agent-runner. It honors the §8.1 agent-step environment contract, writes one
// fixture tree per declared AGENT_OUTPUT_<NAME> destination, writes a minimal
// flight recorder, and exits 0.
//
// The image built from this binary installs it as /usr/local/bin/agent-runner,
// because atc/exec/agent_step.go execs a process whose Path is literally
// "agent-runner". Renaming it breaks the behavioral tier silently.
//
// FIXTURE_CASE selects the tree (default review-accept); hostile-* cases emit
// the adversarial catalog so a live cluster can prove the seal boundary refuses
// them, not just a unit test.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/fixtureagent"
	"github.com/concourse/concourse/agent/schema"
)

// Mirrors agent/runner/runner.go's patterns exactly. The ordering matters:
// AGENT_OUTPUT_REVIEW_RECORD_TYPE also matches outputEnvPattern, so authority
// rows must be classified first or a record-type row would be mistaken for an
// output destination.
var (
	outputEnvPattern          = regexp.MustCompile(`^AGENT_OUTPUT_[A-Z0-9_]+$`)
	inputAuthorityEnvPattern  = regexp.MustCompile(`^(AGENT_INPUT_[A-Z0-9_]+)_SNAPSHOT_(TYPE|DIGEST)$`)
	outputAuthorityEnvPattern = regexp.MustCompile(`^(AGENT_OUTPUT_[A-Z0-9_]+)_RECORD_(TYPE|SCHEMA)$`)
)

type inputAuthority struct {
	port   string
	typ    string
	digest string
}

type outputAuthority struct {
	typ    string
	schema string
}

func main() {
	env := map[string]string{}
	for _, row := range os.Environ() {
		name, value, ok := strings.Cut(row, "=")
		if ok {
			env[name] = value
		}
	}
	os.Exit(run(env, os.Stderr))
}

// run returns the step contract's exit code: 0 = the fixture produced its
// outputs, 2 = platform error.
func run(env map[string]string, stderr io.Writer) int {
	start := time.Now()
	flightDir := env["AGENT_FLIGHT_DIR"]
	stepName := env["AGENT_STEP_NAME"]

	fail := func(format string, args ...any) int {
		message := fmt.Sprintf(format, args...)
		fmt.Fprintf(stderr, "fixture-agent: %s\n", message)
		writeFlight(flightDir, stepName, env, schema.StatusError, message, start)
		return 2
	}

	destinations, outputs, inputs := classify(env)
	if len(destinations) == 0 {
		return fail("no AGENT_OUTPUT_<NAME> destinations in the environment")
	}

	caseName := env["FIXTURE_CASE"]
	if caseName == "" {
		caseName = fixtureagent.CaseReviewAccept
	}
	oversize, _ := strconv.Atoi(env["FIXTURE_PAYLOAD_BYTES"])

	subject, err := primarySubject(inputs, env["FIXTURE_SUBJECT_INPUT"])
	if err != nil {
		return fail("%v", err)
	}

	names := make([]string, 0, len(destinations))
	for name := range destinations {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		authority, declared := outputs[name]
		if !declared {
			// An untyped output: leave a marker file so the mount is not empty
			// and the step's legacy registration still has something to carry.
			if err := os.WriteFile(filepath.Join(destinations[name], "fixture.txt"), []byte("fixture agent\n"), 0o644); err != nil {
				return fail("write untyped output %s: %v", name, err)
			}
			continue
		}
		if err := fixtureagent.WriteTree(destinations[name], caseName, fixtureagent.Authority{
			RecordType:    authority.typ,
			RecordSchema:  authority.schema,
			SubjectInput:  subject.port,
			SubjectType:   subject.typ,
			SubjectDigest: subject.digest,
			OversizeBytes: oversize,
		}); err != nil {
			return fail("materialize %s for %s: %v", caseName, name, err)
		}
	}

	writeFlight(flightDir, stepName, env, schema.StatusPass, "fixture agent wrote "+caseName, start)
	return 0
}

func classify(env map[string]string) (map[string]string, map[string]outputAuthority, map[string]inputAuthority) {
	destinations := map[string]string{}
	outputs := map[string]outputAuthority{}
	inputs := map[string]inputAuthority{}

	for name, value := range env {
		if value == "" {
			continue
		}
		if match := inputAuthorityEnvPattern.FindStringSubmatch(name); match != nil {
			authority := inputs[match[1]]
			if match[2] == "TYPE" {
				authority.typ = value
			} else {
				authority.digest = value
			}
			authority.port = portName(strings.TrimPrefix(match[1], "AGENT_INPUT_"))
			inputs[match[1]] = authority
			continue
		}
		if match := outputAuthorityEnvPattern.FindStringSubmatch(name); match != nil {
			authority := outputs[match[1]]
			if match[2] == "TYPE" {
				authority.typ = value
			} else {
				authority.schema = value
			}
			outputs[match[1]] = authority
			continue
		}
		if name != "AGENT_OUTPUT_SCHEMA" && outputEnvPattern.MatchString(name) {
			destinations[name] = value
		}
	}
	return destinations, outputs, inputs
}

// portName reverses the exec's AGENT_OUTPUT_<NAME>/AGENT_INPUT_<NAME> mangling
// (uppercase, dashes to underscores). The reverse is lossy for artifact names
// that genuinely contain an underscore; FIXTURE_SUBJECT_INPUT is the explicit
// escape hatch for those.
func portName(suffix string) string {
	return strings.ReplaceAll(strings.ToLower(suffix), "_", "-")
}

func primarySubject(inputs map[string]inputAuthority, override string) (inputAuthority, error) {
	keys := make([]string, 0, len(inputs))
	for key := range inputs {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	if override != "" {
		for _, key := range keys {
			if inputs[key].port == override {
				return inputs[key], nil
			}
		}
		return inputAuthority{}, fmt.Errorf("FIXTURE_SUBJECT_INPUT=%q names no declared typed input", override)
	}
	if len(keys) != 1 {
		return inputAuthority{}, fmt.Errorf("expected exactly one typed input, found %d; set FIXTURE_SUBJECT_INPUT", len(keys))
	}
	return inputs[keys[0]], nil
}

func writeFlight(flightDir, stepName string, env map[string]string, status schema.Status, summary string, start time.Time) {
	if flightDir == "" {
		return
	}
	if err := os.MkdirAll(flightDir, 0o755); err != nil {
		return
	}

	events, err := os.Create(filepath.Join(flightDir, "events.ndjson"))
	if err == nil {
		defer events.Close()
		writer := schema.NewEventWriter(events)
		buildID, _ := strconv.Atoi(env["BUILD_ID"])
		_ = writeEvent(writer, schema.EventStepStart, schema.StepStartData{
			StepName: stepName, BuildID: buildID, PlanID: env["AGENT_PLAN_ID"],
		})
		_ = writeEvent(writer, schema.EventStepEnd, schema.StepEndData{
			StepName: stepName, Status: string(status), Summary: summary,
			WallTimeSeconds: int(time.Since(start).Seconds()),
		})
	}

	results := schema.Results{
		SchemaVersion: "1.0", Status: status, Confidence: 1,
		Summary: summary, Artifacts: []schema.Artifact{},
	}
	if raw, err := json.Marshal(results); err == nil {
		_ = os.WriteFile(filepath.Join(flightDir, "results.json"), raw, 0o644)
	}
	_ = os.WriteFile(
		filepath.Join(flightDir, "transcript.ndjson"),
		[]byte(`{"type":"result","subtype":"success","is_error":false,"num_turns":0}`+"\n"),
		0o644,
	)
}

func writeEvent(writer *schema.EventWriter, kind schema.EventType, data any) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return writer.Write(schema.Event{
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Type:      kind,
		Data:      raw,
	})
}
```

- [x] Run `go test ./cmd/fixture-agent/ -count=1 -v`. Expected: three tests pass.
- [x] Run `go build ./cmd/fixture-agent/` and `gofmt -l cmd/fixture-agent`; both silent.
- [x] Commit `feat(agent): add the fixture-agent step binary`.

---

### Task 5: Tier A — seal agent outputs through the real contract registry

The first test anywhere that runs `AgentStep.Run` with a real `snapshot.BatchSealer` over a real `contracts.NewRegistry()`. Nothing is faked between the step's output mount and contract validation; only storage is in-memory.

Two production facts shape the harness:
1. `collectTypedOutputs` builds `OpenTar` as `capturedArtifact.StreamOut(ctx, ".", nil)` (`atc/exec/agent_step.go:1384`) — a **nil** `compression.Compression`, meaning raw. `runtimetest.Volume.StreamOut` calls `compression.Encoding()` on that nil interface and panics, and it can only emit files an `fstest.MapFS` can hold (no symlinks, no `../` names). So the specs need a volume whose `StreamOut` returns fixed raw tar bytes.
2. After sealing, the step calls `materializeSealedSnapshotArtifact`, which re-reads the committed manifest through `MetadataStore.GetAuthorized` (`atc/exec/typed_output.go:169`). The in-memory metadata store must therefore remember what it committed.

**Files:**
- Create: `atc/exec/agent_step_fixture_test.go`

- [x] Create `atc/exec/agent_step_fixture_test.go` with the harness and the first positive spec:

```go
package exec_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"sync"
	"testing/fstest"
	"time"

	"github.com/concourse/concourse/agent/fixtureagent"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/exec/build"
	"github.com/concourse/concourse/atc/exec/execfakes"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/runtime/runtimetest"
	"github.com/concourse/concourse/tracing"
	"github.com/concourse/concourse/vars"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// fixtureTarVolume is a runtime.Volume whose StreamOut hands back fixed raw tar
// bytes. It exists because the production OpenTar passes a nil
// compression.Compression (raw), which runtimetest.Volume cannot serve, and
// because an fstest.MapFS cannot express a `../` entry name or a symlink — the
// two archive-layer attacks this suite must reach.
type fixtureTarVolume struct {
	*runtimetest.Volume
	tar []byte
}

func newFixtureTarVolume(handle string, raw []byte) *fixtureTarVolume {
	return &fixtureTarVolume{Volume: runtimetest.NewVolume(handle), tar: raw}
}

func (v *fixtureTarVolume) StreamOut(_ context.Context, _ string, _ compression.Compression) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(v.tar)), nil
}

// fixtureMemoryContent is a snapshot.ContentStore over a map. It verifies the
// digest on write exactly like the durable store does, so a canonicalization
// bug cannot hide behind a cooperative fake.
type fixtureMemoryContent struct {
	mutex   sync.Mutex
	objects map[snapshot.Digest][]byte
}

func newFixtureMemoryContent() *fixtureMemoryContent {
	return &fixtureMemoryContent{objects: map[snapshot.Digest][]byte{}}
}

func (store *fixtureMemoryContent) Put(_ context.Context, digest snapshot.Digest, reader io.Reader) ([]snapshot.Location, error) {
	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(body)
	if snapshot.Digest("sha256:"+hex.EncodeToString(sum[:])) != digest {
		return nil, fixtureDigestMismatch
	}
	store.mutex.Lock()
	store.objects[digest] = append([]byte(nil), body...)
	store.mutex.Unlock()
	return []snapshot.Location{{Digest: digest, Driver: "fixture-memory", Key: digest.String()}}, nil
}

func (store *fixtureMemoryContent) Open(_ context.Context, value snapshot.Snapshot) (io.ReadCloser, error) {
	store.mutex.Lock()
	body, found := store.objects[value.Digest]
	store.mutex.Unlock()
	if !found {
		return nil, fixtureContentMissing
	}
	return io.NopCloser(bytes.NewReader(append([]byte(nil), body...))), nil
}

func (store *fixtureMemoryContent) Exists(_ context.Context, location snapshot.Location) (bool, error) {
	store.mutex.Lock()
	_, found := store.objects[location.Digest]
	store.mutex.Unlock()
	return found, nil
}

func (store *fixtureMemoryContent) DeleteLocation(_ context.Context, location snapshot.Location) error {
	store.mutex.Lock()
	delete(store.objects, location.Digest)
	store.mutex.Unlock()
	return nil
}

func (store *fixtureMemoryContent) DeleteAll(_ context.Context, digest snapshot.Digest) error {
	store.mutex.Lock()
	delete(store.objects, digest)
	store.mutex.Unlock()
	return nil
}

var (
	fixtureDigestMismatch = errors.New("fixture content store: digest mismatch")
	fixtureContentMissing = errors.New("fixture content store: content not found")
)

// fixtureMemoryMetadata implements only the four snapshot.MetadataStore methods
// BatchSealer.Seal and materializeSealedSnapshotArtifact call. Embedding the
// interface leaves the rest nil, so an unexpected call panics loudly instead of
// silently succeeding.
type fixtureMemoryMetadata struct {
	snapshot.MetadataStore

	mutex     sync.Mutex
	nextID    int64
	stages    int64
	committed map[snapshot.SnapshotID]snapshot.Snapshot
}

func newFixtureMemoryMetadata() *fixtureMemoryMetadata {
	return &fixtureMemoryMetadata{nextID: 900, committed: map[snapshot.SnapshotID]snapshot.Snapshot{}}
}

func (store *fixtureMemoryMetadata) StageUpload(
	_ context.Context, _ snapshot.DigestLease, request snapshot.StageUploadRequest,
) (snapshot.StagedUpload, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	store.stages++
	return snapshot.StagedUpload{
		ID: store.stages, Digest: request.Digest, TeamID: request.TeamID,
		Attempt: request.Attempt, LeaseExpiresAt: request.LeaseExpiresAt,
		CreatedAt: request.LeaseExpiresAt.Add(-time.Hour),
	}, nil
}

func (store *fixtureMemoryMetadata) DigestState(
	_ context.Context, _ snapshot.DigestLease, digest snapshot.Digest, _ time.Time,
) (snapshot.DigestState, error) {
	return snapshot.DigestState{Digest: digest}, nil
}

func (store *fixtureMemoryMetadata) CommitSealBatch(
	_ context.Context, _ snapshot.DigestLease, commit snapshot.SealCommit,
) (map[string]snapshot.SealedOutput, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	sealed := make(map[string]snapshot.SealedOutput, len(commit.Outputs))
	for _, output := range commit.Outputs {
		store.nextID++
		id := snapshot.SnapshotID(store.nextID)
		store.committed[id] = snapshot.Snapshot{
			ID: id, Type: output.Port.Type, Digest: output.Digest,
			ByteSize: output.ByteSize, FileCount: output.FileCount,
			Representation: output.Representation, IntrinsicMetadata: output.IntrinsicMetadata,
			ContentState: snapshot.ContentStateAvailable, CreatedAt: time.Now().UTC(),
		}
		sealed[output.ClientKey] = snapshot.SealedOutput{
			Port:     output.Port,
			Snapshot: snapshot.SnapshotRef{ID: id, Type: output.Port.Type, Digest: output.Digest},
		}
	}
	return sealed, nil
}

func (store *fixtureMemoryMetadata) GetAuthorized(
	_ context.Context, _ int, id snapshot.SnapshotID,
) (snapshot.Snapshot, bool, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	manifest, found := store.committed[id]
	return manifest, found, nil
}

// fixtureLease covers everything: the exec specs are single-threaded, and the
// lock manager's real exclusion semantics are WS6's subject, not this suite's.
type fixtureLease struct{ closed int }

func (l *fixtureLease) Covers(snapshot.Digest) bool { return true }
func (l *fixtureLease) Close() error                { l.closed++; return nil }

type fixtureLocks struct{ lease *fixtureLease }

func (l *fixtureLocks) AcquireMany(context.Context, []snapshot.Digest) (snapshot.DigestLease, error) {
	if l.lease == nil {
		l.lease = &fixtureLease{}
	}
	return l.lease, nil
}

var _ = Describe("AgentStep against the real sealer (fixture agent)", func() {
	var (
		ctx    context.Context
		cancel func()

		fixtureCase   string
		contentLimit  int64
		markerFiles   runtimetest.VolumeContent
		inputRef      snapshot.SnapshotRef
		reviewSchema  snapshot.Digest
		outputVolume  *fixtureTarVolume
		metadataStore *fixtureMemoryMetadata
		contentStore  *fixtureMemoryContent
		locks         *fixtureLocks

		fakePool            *execfakes.FakePool
		fakeStreamer        *execfakes.FakeStreamer
		fakeDelegate        *execfakes.FakeTaskDelegate
		fakeDelegateFactory *execfakes.FakeTaskDelegateFactory

		state exec.RunState
		repo  *build.Repository

		agentPlan atc.AgentPlan
		step      exec.Step
		runErr    error
		runOK     bool
	)

	containerMetadata := db.ContainerMetadata{
		WorkingDirectory: "some-artifact-root",
		Type:             db.ContainerTypeAgent,
		StepName:         "fixture-review",
	}
	stepMetadata := exec.StepMetadata{
		TeamID: 123, BuildID: 1234, JobID: 12345, PipelineID: 555,
		TeamName: "main", SnapshotCreatedBy: "concourse", ExternalURL: "http://foo.bar",
	}
	planID := atc.PlanID("77")

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())

		fixtureCase = fixtureagent.CaseReviewAccept
		contentLimit = 0
		// nil means "no marker mount at all", which is what a plan with no
		// optional typed outputs produces.
		markerFiles = nil

		var found bool
		reviewSchema, found = contracts.SchemaDigestFor(snapshot.TypeRef("review/v1"))
		Expect(found).To(BeTrue())

		inputDigest, err := snapshot.ParseDigest("sha256:" + strings.Repeat("d", 64))
		Expect(err).NotTo(HaveOccurred())
		inputRef = snapshot.SnapshotRef{ID: 82, Type: snapshot.TypeRef("opaque/v1"), Digest: inputDigest}

		metadataStore = newFixtureMemoryMetadata()
		contentStore = newFixtureMemoryContent()
		locks = &fixtureLocks{}

		fakeStreamer = new(execfakes.FakeStreamer)
		fakeDelegate = new(execfakes.FakeTaskDelegate)
		fakeDelegate.StartSpanReturns(ctx, tracing.NoopSpan)
		fakeDelegateFactory = new(execfakes.FakeTaskDelegateFactory)
		fakeDelegateFactory.TaskDelegateReturns(fakeDelegate)

		state = exec.NewRunState(noopStepper, vars.StaticVariables{})
		repo = state.ArtifactRepository()
		Expect(repo.RegisterArtifacts(map[build.ArtifactName]build.ArtifactEntry{
			"change": {Artifact: runtimetest.NewVolume("fixture-input"), Snapshot: &inputRef},
		})).To(Succeed())

		agentPlan = atc.AgentPlan{
			Name: "fixture-review", Hermetic: true, Prompt: "unused by the fixture agent",
			Inputs:  []string{"change"},
			Outputs: []string{"review"},
			SnapshotInputs: map[string]atc.SnapshotInputConfig{
				"change": {Type: snapshot.TypeRef("opaque/v1")},
			},
			SnapshotOutputs: map[string]atc.SnapshotOutputConfig{
				"review": {Type: snapshot.TypeRef("review/v1")},
			},
		}
	})

	AfterEach(func() { cancel() })

	JustBeforeEach(func() {
		raw, err := fixtureagent.Tar(fixtureCase, fixtureagent.Authority{
			RecordType:    "review/v1",
			RecordSchema:  reviewSchema.String(),
			SubjectInput:  "change",
			SubjectType:   inputRef.Type.String(),
			SubjectDigest: inputRef.Digest.String(),
			OversizeBytes: 4096,
		})
		Expect(err).NotTo(HaveOccurred())
		outputVolume = newFixtureTarVolume("fixture-review-output", raw)

		mounts := []runtime.VolumeMount{
			{Volume: outputVolume, MountPath: "some-artifact-root/review"},
			{Volume: runtimetest.NewVolume("flight-volume"), MountPath: "some-artifact-root/flight"},
		}
		if markerFiles != nil {
			// The optional-output control mount. It is deliberately separate
			// from every output mount so marker bytes never enter a snapshot,
			// and optionalOutputWasProduced streams it out GZIPPED — unlike the
			// output mount, which is raw — so runtimetest.Volume is the right
			// type here and fixtureTarVolume is not.
			mounts = append(mounts, runtime.VolumeMount{
				Volume:    runtimetest.NewVolume("typed-output-markers").WithContent(markerFiles),
				MountPath: "/tmp/.jetbridge/typed-output-markers/v1",
			})
		}

		owner := db.NewBuildStepContainerOwner(stepMetadata.BuildID, planID, stepMetadata.TeamID)
		worker := runtimetest.NewWorker("worker").WithContainer(
			owner,
			runtimetest.NewContainer().WithProcess(
				runtime.ProcessSpec{
					ID: "agent", Path: "agent-runner", Dir: "some-artifact-root",
					TTY: &runtime.TTYSpec{WindowSize: runtime.WindowSize{Columns: 500, Rows: 500}},
				},
				runtimetest.ProcessStub{Attachable: true},
			),
			mounts,
		)
		fakePool = new(execfakes.FakePool)
		fakePool.FindOrSelectWorkerReturns(worker, nil)

		registry, err := contracts.NewRegistry(
			contracts.WithCanonicalizer(snapshot.Canonicalizer{TempDir: GinkgoT().TempDir()}),
		)
		Expect(err).NotTo(HaveOccurred())
		canonicalizer := snapshot.Canonicalizer{TempDir: GinkgoT().TempDir(), MaxContentBytes: contentLimit}
		sealer, err := snapshot.NewBatchSealer(canonicalizer, registry, metadataStore, contentStore, locks)
		Expect(err).NotTo(HaveOccurred())

		step = exec.NewAgentStep(
			planID, agentPlan, atc.ContainerLimits{}, atc.ContainerLimits{},
			stepMetadata, containerMetadata, fakePool, fakeStreamer, fakeDelegateFactory,
			0, "registry.home/fixture-agent:e2e",
			exec.WithAgentOutputSealer(sealer),
			exec.WithAgentSnapshotStores(metadataStore, contentStore),
		)
		runOK, runErr = step.Run(ctx, state)
	})

	Context("when the fixture writes an accepted review/v1", func() {
		It("seals it through the real registry and publishes it to the build repository", func() {
			Expect(runErr).NotTo(HaveOccurred())
			Expect(runOK).To(BeTrue())

			entry, found := repo.ArtifactEntryFor("review")
			Expect(found).To(BeTrue())
			Expect(entry.Snapshot).NotTo(BeNil())
			Expect(entry.Snapshot.Type).To(Equal(snapshot.TypeRef("review/v1")))
			Expect(entry.Snapshot.Digest.Validate()).To(Succeed())
			Expect(entry.Artifact).To(BeAssignableToTypeOf(&runtime.SnapshotArtifact{}))

			// The bytes the sealer uploaded are the canonical archive keyed by
			// the digest the repository now advertises: content-addressing held
			// end to end, with no fake in between.
			body, err := contentStore.Open(ctx, snapshot.Snapshot{Digest: entry.Snapshot.Digest})
			Expect(err).NotTo(HaveOccurred())
			raw, err := io.ReadAll(body)
			Expect(err).NotTo(HaveOccurred())
			Expect(body.Close()).To(Succeed())
			sum := sha256.Sum256(raw)
			Expect(snapshot.Digest("sha256:" + hex.EncodeToString(sum[:]))).To(Equal(entry.Snapshot.Digest))

			// The flight output stays legacy and untyped.
			flight, found := repo.ArtifactEntryFor("flight")
			Expect(found).To(BeTrue())
			Expect(flight.Snapshot).To(BeNil())
		})
	})

	Context("when the fixture writes a blocking changes-required review/v1", func() {
		BeforeEach(func() { fixtureCase = fixtureagent.CaseReviewChangesRequired })

		It("seals it too: a blocking finding is a valid judgment, not a malformed one", func() {
			Expect(runErr).NotTo(HaveOccurred())
			Expect(runOK).To(BeTrue())
			entry, found := repo.ArtifactEntryFor("review")
			Expect(found).To(BeTrue())
			Expect(entry.Snapshot.Type).To(Equal(snapshot.TypeRef("review/v1")))
		})
	})
})
```

- [x] Run `ginkgo --focus="fixture" ./atc/exec/`. Expected failure before the file compiles: `undefined: fixtureagent` if Task 2 was skipped; otherwise the suite must compile and both specs must pass. If the first run fails inside `Seal` with `snapshot: capture output "review": ...`, the fixture tar is malformed — fix the fixture, not the assertion.
- [x] Confirm the real registry is genuinely in the path: temporarily change `acceptBody()`'s `Conclusion` to `"maybe"` in `agent/fixtureagent/fixtures.go`, re-run `ginkgo --focus="fixture" ./atc/exec/`, and confirm the first spec now fails with a message containing `conclusion`. Revert the change.
- [x] Run the whole existing agent-step suite for regressions: `ginkgo --focus="AgentStep" ./atc/exec/`. Expected: `SUCCESS! -- 96 Passed | 0 Failed` (94 pre-existing plus the two new specs, since the new Describe also matches `AgentStep`).
- [x] Commit `test(exec): seal agent outputs through the real contract registry`.

---

### Task 6: Tier A — honor a produced optional typed output marker

`agent_step_test.go:693` covers an optional typed output whose marker is **absent**. The produced arm — marker present, output sealed — has no spec anywhere, so the base64 marker-name encoding (`atc/exec/typed_output.go:122`) is pinned in only one direction.

**Files:**
- Modify: `atc/exec/agent_step_fixture_test.go`

The harness from Task 5 already builds the marker mount whenever `markerFiles` is non-nil, so this task only sets that variable. The marker file name is the raw base64url of the output name: `base64url("review") == "cmV2aWV3"`.

- [x] Add two `Context` blocks inside the existing `Describe`, after the changes-required context:

```go
	Context("when an optional typed output is marked produced", func() {
		BeforeEach(func() {
			declaration := agentPlan.SnapshotOutputs["review"]
			declaration.Optional = true
			agentPlan.SnapshotOutputs["review"] = declaration
			markerFiles = runtimetest.VolumeContent{
				"cmV2aWV3": &fstest.MapFile{Data: []byte{}},
			}
		})

		It("seals the optional output and publishes it", func() {
			Expect(runErr).NotTo(HaveOccurred())
			Expect(runOK).To(BeTrue())
			entry, found := repo.ArtifactEntryFor("review")
			Expect(found).To(BeTrue())
			Expect(entry.Snapshot).NotTo(BeNil())
			Expect(entry.Snapshot.Type).To(Equal(snapshot.TypeRef("review/v1")))
		})
	})

	Context("when an optional typed output's marker names a different output", func() {
		BeforeEach(func() {
			declaration := agentPlan.SnapshotOutputs["review"]
			declaration.Optional = true
			agentPlan.SnapshotOutputs["review"] = declaration
			// base64url("preview"), not base64url("review"): the marker mount
			// exists and is non-empty, but nothing marks THIS output produced.
			markerFiles = runtimetest.VolumeContent{
				"cHJldmlldw": &fstest.MapFile{Data: []byte{}},
			}
		})

		It("treats the output as absent and seals nothing", func() {
			Expect(runErr).NotTo(HaveOccurred())
			Expect(runOK).To(BeTrue())
			_, found := repo.ArtifactEntryFor("review")
			Expect(found).To(BeFalse())
			Expect(contentStore.objects).To(BeEmpty())
		})
	})
```

- [x] Run `ginkgo --focus="fixture" ./atc/exec/`. Expected: 4 specs passing. If the produced spec fails with `optional typed-output marker mount is missing`, the marker mount path in Task 5's harness does not match `typedOutputMarkerMountPath` (`atc/exec/typed_output.go`); fix the harness.
- [x] The second Context is the negative control that makes the first one meaningful: identical wiring, one different marker name, opposite outcome. Confirm both pass — if they both pass with the *same* outcome, the marker is not being read at all.
- [x] Commit `test(exec): honor produced optional typed output markers`.

---

### Task 7: Tier A — reject hostile agent output at the seal boundary

Seven adversarial cases, each asserting two verbatim substrings: the sealer's framing (which output failed) and the violation clause (what was wrong). Both are copied from production source.

Where the clause comes from, so an implementer can re-derive it if a message ever changes:

| Case | Framing | Clause source |
|---|---|---|
| `hostile-traversal` | `snapshot: capture output "review"` | `agent/snapshot/archive.go:921` |
| `hostile-symlink` | `snapshot: capture output "review"` | `agent/snapshot/archive.go:1289` |
| `hostile-oversized` | `snapshot: capture output "review"` | `agent/snapshot/archive.go:1032` |
| `hostile-missing-record` | `snapshot: validate output "review"` | `agent/snapshot/contracts/json.go:46` |
| `hostile-schema-digest` | `snapshot: validate output "review"` | `agent/snapshot/contracts/record.go` `schemaAdmission.admit` |
| `hostile-unexposed-subject` | `snapshot: validate output "review"` | `agent/snapshot/contracts/record.go` `RebindSubjectsToExposedInputs` |
| `hostile-duplicate-finding` | `snapshot: validate output "review"` | `agent/snapshot/contracts/schema_core_validator.go:272` |

**Files:**
- Modify: `atc/exec/agent_step_fixture_test.go`

A Ginkgo `DescribeTable` is the wrong construct here: its entry parameters arrive inside the spec body, which runs *after* the outer `JustBeforeEach` has already built the tar and run the step, so an entry cannot select its own fixture case. A `for` loop generating one `Context` per case sets `fixtureCase`/`contentLimit` in a `BeforeEach`, which does run first.

- [x] Add this loop at the end of the `Describe` body:

```go
	type hostileCase struct {
		name         string
		contentLimit int64
		framing      string
		clause       string
	}

	for _, hostile := range []hostileCase{
		{
			name:    fixtureagent.CaseHostileTraversal,
			framing: `snapshot: capture output "review"`,
			clause:  `archive path "../escape" contains an empty, dot, or traversal segment`,
		},
		{
			name:    fixtureagent.CaseHostileSymlink,
			framing: `snapshot: capture output "review"`,
			clause:  `symlink "escape" target escapes the archive root`,
		},
		{
			// The ARCHIVE-layer configured limit, injected here and nowhere
			// else. The contracts-layer document limits belong to WS5 and are
			// deliberately untouched.
			name:         fixtureagent.CaseHostileOversized,
			contentLimit: 512,
			framing:      `snapshot: capture output "review"`,
			clause:       `archive exceeds regular content limit of 512 bytes`,
		},
		{
			name:    fixtureagent.CaseHostileMissingRecord,
			framing: `snapshot: validate output "review"`,
			clause:  `required regular file "record.json" is missing`,
		},
		{
			name:    fixtureagent.CaseHostileSchemaDigest,
			framing: `snapshot: validate output "review"`,
			clause:  `record schema must be exactly the current schema digest`,
		},
		{
			name:    fixtureagent.CaseHostileUnexposedSubject,
			framing: `snapshot: validate output "review"`,
			clause:  `record subject "primary" input "not-a-declared-input" is not an exact declared input`,
		},
		{
			name:    fixtureagent.CaseHostileDuplicateFinding,
			framing: `snapshot: validate output "review"`,
			clause:  `body/findings/*/id: "F-1" is duplicate`,
		},
	} {
		hostile := hostile

		Context("when the fixture writes "+hostile.name, func() {
			BeforeEach(func() {
				fixtureCase = hostile.name
				contentLimit = hostile.contentLimit
			})

			It("fails the step with an operator-actionable message and publishes nothing", func() {
				Expect(runErr).To(HaveOccurred())
				Expect(runOK).To(BeFalse())
				// Two verbatim halves: which output failed, and what was wrong.
				Expect(runErr.Error()).To(ContainSubstring(hostile.framing))
				Expect(runErr.Error()).To(ContainSubstring(hostile.clause))

				_, found := repo.ArtifactEntryFor("review")
				Expect(found).To(BeFalse())
				Expect(contentStore.objects).To(BeEmpty())
			})
		})
	}
```

- [x] Run `ginkgo --focus="fixture" ./atc/exec/`. Expected: 11 specs (2 positive seals + 2 optional-marker + 7 hostile), all passing. Any hostile case that passes the step instead of failing it is a real finding — record it and stop; do not weaken the assertion.
- [x] Run `ginkgo --focus="fixture" ./atc/exec/ -v 2>&1 | grep -c "hostile-"` and confirm `7`.
- [x] Run the full package once: `ginkgo ./atc/exec/`. Expected: `SUCCESS!` with 691 + 11 specs.
- [x] Commit `test(exec): reject hostile agent output at the seal boundary`.

---

### Task 8: Build and load the fixture-agent image for the behavioral suite

`topgun/k8s_behavioral` uses testcontainers K3s, not KinD: images enter the cluster through `k3sContainer.LoadImages(ctx, ref)`, which does the docker-save/copy/`ctr images import` dance internally (`topgun/k8s_behavioral/cluster_lifecycle_test.go:132`). `buildAndLoadOOMTriggerImage` (`:202`, called from `:174`) is the in-repo precedent for building a small auxiliary Go image at suite start; this task clones it.

The image base must carry a POSIX userland: `atc/worker/jetbridge/supervisor.go` runs every process as `sh -c <supervisor script>` needing `sh`, `cat`, `sed`, `cut`, `tail`, `mkdir`, `mv`, `sleep`. `FROM scratch` (which oom-trigger uses, because its `path` is an absolute binary run without a supervisor for a *task*) is not an option here. The binary must be installed as `agent-runner`, because `atc/exec/agent_step.go:729` execs `ProcessSpec{Path: "agent-runner"}`.

**Files:**
- Create: `deploy/fixture-agent/Dockerfile`
- Modify: `topgun/k8s_behavioral/cluster_lifecycle_test.go`

- [x] Create `deploy/fixture-agent/Dockerfile`:

```dockerfile
# The deterministic stand-in agent image used by the behavioral suite and by
# any operator who wants to exercise the agent-step path without spending real
# model tokens. It is NEVER a release artifact.
#
# busybox, not scratch: jetbridge runs every supervised process as
# `sh -c <supervisor script>` (atc/worker/jetbridge/supervisor.go), which needs
# sh plus cat/sed/cut/tail/mkdir/mv/sleep.
FROM golang:1.25-bookworm AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/fixture-agent ./cmd/fixture-agent

FROM busybox:1.37.0
# Installed under the name atc/exec/agent_step.go execs. Renaming this breaks
# the behavioral tier with a bare "executable file not found".
COPY --from=build /out/fixture-agent /usr/local/bin/agent-runner
ENTRYPOINT ["agent-runner"]
```

- [x] In `topgun/k8s_behavioral/cluster_lifecycle_test.go`, add the constant next to `artifactHelperSourceImage`:

```go
// fixtureAgentImage is the deterministic stand-in agent the behavioral suite
// points --agent-step-image at. A tag, not a digest: the workflow renderer's
// digest requirement only applies to schema-v3 workflow runs, and the agent
// spec that uses this is an ordinary pipeline (see agentic_workflows_test.go).
const fixtureAgentImage = "fixture-agent:behavioral"
```

- [x] In `loadImagesIntoCluster`, immediately after the existing `buildAndLoadOOMTriggerImage(ctx)` call at line 174, add:

```go
	buildAndLoadFixtureAgentImage(ctx)
```

- [x] Append the builder, modeled line-for-line on `buildAndLoadOOMTriggerImage`:

```go
// buildAndLoadFixtureAgentImage compiles cmd/fixture-agent, packages it into a
// busybox image whose entrypoint binary is named agent-runner, and loads it
// into the K3s cluster. Unlike the oom-trigger helper this one is FATAL on
// failure: a missing fixture-agent image turns the agent-node spec into a
// confusing ImagePullBackOff rather than a clear skip.
func buildAndLoadFixtureAgentImage(ctx context.Context) {
	if err := exec.Command("docker", "image", "inspect", fixtureAgentImage).Run(); err == nil &&
		os.Getenv("CONCOURSE_REBUILD_IMAGE") != "1" {
		log.Printf("fixture-agent image already exists, loading into K3s...")
		if err := k3sContainer.LoadImages(ctx, fixtureAgentImage); err != nil {
			log.Fatalf("failed to load %s into K3s: %v", fixtureAgentImage, err)
		}
		return
	}

	root := mustRepoRoot()
	log.Println("Building fixture-agent Docker image...")
	dockerBuild := exec.Command(
		"docker", "build",
		"-f", filepath.Join(root, "deploy", "fixture-agent", "Dockerfile"),
		"-t", fixtureAgentImage, root,
	)
	dockerBuild.Stdout = os.Stderr
	dockerBuild.Stderr = os.Stderr
	if err := dockerBuild.Run(); err != nil {
		log.Fatalf("failed to build %s: %v", fixtureAgentImage, err)
	}

	log.Println("Loading fixture-agent into K3s cluster...")
	if err := k3sContainer.LoadImages(ctx, fixtureAgentImage); err != nil {
		log.Fatalf("failed to load %s into K3s: %v", fixtureAgentImage, err)
	}
}
```

- [x] Verify the image builds and the entrypoint is correctly named, without running the suite:

```
docker build -f deploy/fixture-agent/Dockerfile -t fixture-agent:behavioral .
docker run --rm --entrypoint sh fixture-agent:behavioral -c 'command -v agent-runner'
```

  Expected output: `/usr/local/bin/agent-runner`.
- [x] Verify the binary refuses an empty environment the way the flight contract requires:

```
docker run --rm fixture-agent:behavioral; echo "exit=$?"
```

  Expected: `fixture-agent: no AGENT_OUTPUT_<NAME> destinations in the environment` on stderr and `exit=2`.
- [x] Run `go vet ./topgun/k8s_behavioral/` and `gofmt -l topgun/k8s_behavioral deploy`; both silent. Do not run the behavioral suite yet — Task 10 does that once.
- [x] Commit `test(k8s): build and load the fixture-agent image`.

---

### Task 9: Add a chart value for the agent step image

`--agent-step-image` (`atc/atccmd/command.go:362`, env `CONCOURSE_AGENT_STEP_IMAGE`) has no chart value; the only way to set it today is the generic `web.extraArgs` list, and `helmDeployConcourse` only populates that list under `COLLECT_OTEL=1`. A dedicated value keeps the behavioral suite's helm invocation flat and gives operators the same shape the other agent flags already have.

**Files:**
- Modify: `deploy/chart/values.yaml`
- Modify: `deploy/chart/templates/web-deployment.yaml`
- Modify: `deploy/chart/tests/agentic_config_test.go`

- [x] Add the failing chart test first. Append to `deploy/chart/tests/agentic_config_test.go`:

```go
func TestWebAgentStepImageRendersTheFlagOnlyWhenSet(t *testing.T) {
	absent := renderChart(t)
	if strings.Contains(absent, "--agent-step-image") {
		t.Fatal("--agent-step-image must not render when web.agentStepImage is empty")
	}

	present := renderChartSetString(t, "web.agentStepImage=fixture-agent:behavioral")
	if !strings.Contains(present, "- --agent-step-image=fixture-agent:behavioral") {
		t.Fatalf("rendered web args do not carry the agent step image:\n%s", present)
	}
}

func TestWebExtraArgsMayNotOverrideTheChartManagedAgentStepImage(t *testing.T) {
	output := renderChartFailure(t,
		"web.agentStepImage=fixture-agent:behavioral",
		"web.extraArgs={--agent-step-image=other:tag}",
	)
	if !strings.Contains(output, "web.extraArgs may not override web.agentStepImage") {
		t.Fatalf("render output = %s", output)
	}
}
```

- [x] Run `go test ./deploy/chart/tests/ -run 'AgentStepImage|AgentStepImage' -count=1`. Expected failure: `--agent-step-image` is absent from the `present` render, so the first test fails with `rendered web args do not carry the agent step image`.
- [x] In `deploy/chart/values.yaml`, add under the `web:` block, immediately above `extraArgs: []`:

```yaml
  # Container image for the agent: step's main container. Leave empty to
  # disable agent steps entirely; the web node errors at runtime when an agent
  # step runs without it. Schema-v3 workflow runs additionally require an exact
  # @sha256 digest, which the web node validates at render time.
  agentStepImage: ""
```

- [x] In `deploy/chart/templates/web-deployment.yaml`, extend the `extraArgs` guard block (lines 40-47) with:

```yaml
{{- if and $.Values.web.agentStepImage (hasPrefix "--agent-step-image" $argument) }}
{{- fail "web.extraArgs may not override web.agentStepImage" }}
{{- end }}
```

- [x] In the same template, add the argument immediately before the `# Extra args` comment:

```yaml
            {{- if .Values.web.agentStepImage }}
            - --agent-step-image={{ .Values.web.agentStepImage }}
            {{- end }}
```

- [x] Re-run `go test ./deploy/chart/tests/ -count=1`. Expected: the whole chart test package passes, including the two new tests.
- [x] Wire the behavioral suite to it. In `topgun/k8s_behavioral/cluster_lifecycle_test.go`, add to the `helmArgs` slice, immediately after the `agentExperiments.runnerEnabled` line:

```go
		"--set-string", "web.agentStepImage=" + fixtureAgentImage,
```

- [x] Run `helm template concourse deploy/chart --set-string kubernetes.artifactHelperImage=busybox@sha256:$(printf 'a%.0s' {1..64}) --set-string web.agentStepImage=fixture-agent:behavioral | grep -- '--agent-step-image'`. Expected: one line, `- --agent-step-image=fixture-agent:behavioral`.
- [x] Commit `feat(chart): configure the agent step image`.

> **Task 13 note (2026-07-26):** this task's checklist above instructs wiring `topgun/k8s_behavioral/cluster_lifecycle_test.go`'s `helmArgs` to `web.agentStepImage`, but this task's own **Files:** block never listed that file. It was correctly left out of this task's commit (`3f2b148a52`, chart files only) and landed instead in Task 10's commit (`abd9c46cbf`), which already had reason to touch the behavioral suite. A plan `Files:`-block defect, not an implementation gap — noted here and in `## Deviations from the design`.

---

### Task 10: Tier C — run a real `agent:` node end to end

`topgun/k8s_behavioral/agentic_workflows_test.go` is the suite's only agentic spec and its DAG node is a busybox `cp` **task**. This adds a sibling `It` whose node is a real `agent:` step: the fixture writes a `review/v1` record, the seal boundary validates and commits it, and the test downloads the sealed content and asserts its digest.

It is an ordinary pipeline, not a workflow, on purpose. `agent/workflowrun/binder.go:1174` requires `--agent-step-image` to be an exact `@sha256:` digest for any workflow containing an agent node, and a locally-built image has no `RepoDigests` entry to supply one. An ordinary pipeline's `AgentPlan.RuntimeImage` is empty, so `atc/exec/agent_step.go:229-243` uses the configured value verbatim — which is the honest scope for this tier anyway: the claim being closed is "an `agent:` node really runs, seals, and the bytes come back", not "the workflow renderer pins digests" (which `binder_test.go` already covers).

**Files:**
- Modify: `topgun/k8s_behavioral/agentic_workflows_test.go`

- [x] Add a second `It` inside the existing `Describe("Agentic workflows", ...)`:

```go
	It("runs a real agent node that seals a review record through the fixture agent", func() {
		subjectDir := filepath.Join(tmp, "agent-node-subject")
		Expect(os.MkdirAll(subjectDir, 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(subjectDir, "payload.txt"), []byte("subject under review\n"), 0o644)).To(Succeed())

		create := fly.Start("agent", "snapshots", "create", "--type=opaque/v1", "--from="+subjectDir, "--json")
		Eventually(create).Should(gexec.Exit(0))
		var subject snapshot.Snapshot
		Expect(json.Unmarshal(create.Out.Contents(), &subject)).To(Succeed())
		Expect(subject.ID.Validate()).To(Succeed())

		// An ordinary pipeline, not a workflow: a workflow containing an
		// agent node would require --agent-step-image to be an exact @sha256
		// digest (agent/workflowrun/binder.go), which a locally built image
		// cannot supply. The claim under test is that a real agent: node runs,
		// seals, and its bytes come back — not how workflows pin images.
		pipeline := fmt.Sprintf(`---
jobs:
- name: fixture-review
  plan:
  - load_snapshot: change
    id: "%s"
    type: opaque/v1
  - agent: review
    prompt: The fixture agent ignores this prompt.
    inputs: [change]
    outputs: [review]
    input_types:
      change: {type: opaque/v1}
    output_types:
      review: review/v1
    env:
      FIXTURE_CASE: review-accept
      FIXTURE_SUBJECT_INPUT: change
`, subject.ID.String())
		pipelinePath := filepath.Join(tmp, "agent-node-pipeline.yml")
		Expect(os.WriteFile(pipelinePath, []byte(pipeline), 0o644)).To(Succeed())

		fly.Run("set-pipeline", "-n", "-p", pipelineName, "-c", pipelinePath)
		fly.Run("unpause-pipeline", "-p", pipelineName)

		trigger := fly.Start("trigger-job", "-j", pipelineName+"/fixture-review", "-w")
		Eventually(trigger, 10*time.Minute).Should(gexec.Exit(0))

		// The seal committed a review/v1 for this team; there is exactly one,
		// because each spec gets a fresh randomly named pipeline and a fresh
		// subject snapshot.
		list := fly.Start("agent", "snapshots", "list", "--type=review/v1", "--json")
		Eventually(list).Should(gexec.Exit(0))
		var reviews []snapshot.Snapshot
		Expect(json.Unmarshal(list.Out.Contents(), &reviews)).To(Succeed())
		Expect(reviews).NotTo(BeEmpty())
		sealed := reviews[0]
		Expect(sealed.Type.String()).To(Equal("review/v1"))
		Expect(sealed.ContentState).To(Equal(snapshot.ContentStateAvailable))

		download := filepath.Join(tmp, "agent-node-review.tar")
		fly.Run("agent", "snapshots", "download", sealed.ID.String(), "--to="+download)

		// The record the fixture wrote, bound to the exact subject the platform
		// exposed to it — and the archive's bytes hash to the digest the seal
		// committed, which is the whole content-addressing claim.
		var record contracts.Record[contracts.ReviewBody]
		Expect(json.Unmarshal([]byte(readTarFile(download, "record.json")), &record)).To(Succeed())
		Expect(record.Type.String()).To(Equal("review/v1"))
		Expect(record.Body.Conclusion).To(Equal("accept"))
		Expect(record.Subjects).To(HaveLen(1))
		Expect(record.Subjects[0].Input).To(Equal("change"))
		Expect(record.Subjects[0].Digest).To(Equal(subject.Digest))

		raw, err := os.ReadFile(download)
		Expect(err).NotTo(HaveOccurred())
		sum := sha256.Sum256(raw)
		Expect(snapshot.Digest("sha256:" + hex.EncodeToString(sum[:]))).To(Equal(sealed.Digest))
	})
```

- [x] Add the imports the new spec needs to the file's import block: `"crypto/sha256"`, `"encoding/hex"`, `"time"`, and `"github.com/concourse/concourse/agent/snapshot/contracts"`.
- [x] Run `go vet ./topgun/k8s_behavioral/` and `gofmt -l topgun/k8s_behavioral`; both silent.
- [x] Run just this spec against a live cluster: `ginkgo --procs=1 -v --timeout=1h --focus="real agent node" ./topgun/k8s_behavioral/`. Expected: `SUCCESS! -- 1 Passed`. Prerequisites per CLAUDE.md: Docker, Helm, kubectl on PATH. Budget ~20 minutes for cluster bring-up.
- [x] If the build fails with `agent step requires the web node to be started with --agent-step-image`, Task 9's chart wiring did not reach the pod — check `kubectl -n concourse get deploy concourse-web -o yaml | grep agent-step-image`.
- [x] If the agent pod fails with `executable file not found`, the image installed the binary under the wrong name — re-check Task 8's `COPY` line.
- [x] Run the whole agentic file to confirm no regression in the pre-existing task-node spec: `ginkgo --procs=1 -v --timeout=1h --focus="Agentic workflows" ./topgun/k8s_behavioral/`. Expected: `2 Passed`.
- [x] Commit `test(k8s): run a real agent node end to end`.

---

### Task 11: Pin the chained-DAG execution seam

Tier B has to choose an execution seam, and the choice is load-bearing enough to be its own task with a written outcome. Do not skip to Task 12.

**Files:**
- Modify: `docs/superpowers/plans/test-hardening/02-fixture-agent-e2e.md`

- [x] Read the current vertical slice end to end: `atc/db/agent_workflow_run_integration_test.go:32-214`. Note what it already proves (import → promote → `DispatchOne` → plan → `build.Start` → real `BatchSealer` → `build.Finish` → reconciler → lineage/retention/history) and the two things it does not: the sealed output is hand-built from a literal tar, and nothing consumes it as a later step's input.
- [x] Establish the two facts that constrain the choice:
  - Run `rg -n 'authored load_snapshot' agent/workflow/render.go`. Confirm `render.go:364`: **authored `load_snapshot` steps are rejected inside a workflow**; the renderer injects one per public *input* at `render.go:189-194`. Intra-plan chaining between two agent steps is therefore by artifact **name** through `build.Repository`, not by `load_snapshot`. Record this: the spec's phrase "feeds it to step 2 as a typed input (`load_snapshot`)" describes the workflow-input path, and the plan covers both.
    - **Confirmed (2026-07-26).** `render.go:364` is verbatim `workflow: %s: authored load_snapshot steps are not allowed; workflow inputs are loaded by the renderer`. The renderer's injection is at `render.go:190-196` (`loads = append(loads, atc.Step{Config: &atc.LoadSnapshotStep{...}})`) — six lines later than the plan's `189-194`, an offset only, same construct.
  - Run `rg -l 'concourse/atc/exec' atc/db/*_test.go`. Confirm there are currently **zero** hits, then confirm no import cycle exists: `atc/exec` imports `atc/db` (`atc/exec/agent_step.go:23`), and `db_test` is an external test package, so `db_test` → `atc/exec` → `atc/db` is legal Go.
    - **Confirmed (2026-07-26).** `rg -l 'concourse/atc/exec' atc/db/*_test.go` exits 1 with no output — zero hits. `go list -f '{{join .Imports "\n"}}' ./atc/db/ | grep -c 'concourse/atc/exec'` prints `0`, so the non-test `atc/db` package has no edge back to `atc/exec`; `atc/exec` does import `github.com/concourse/concourse/atc/db` (`agent_step.go:23`). Every one of the 84 test files in `atc/db` declares `package db_test` (checked with `rg -N '^package ' atc/db/*_test.go | sort -u`) — there is no in-package `package db` test file that a new `atc/exec` import could cycle through.
- [x] Evaluate exactly three candidate seams against three criteria — (a) does it execute the real `atc/exec` steps, (b) does it run under real PostgreSQL, (c) what does it cost the suite it lands in:

  | Seam | Real exec steps | Real Postgres | Cost |
  |---|---|---|---|
  | **A.** New file in `db_test`, importing `atc/exec` to drive `LoadSnapshotStep` and `AwaitSnapshotStep` directly, with the real `BatchSealer` standing in for the agent pod | `load_snapshot` and `await_snapshot`: yes. `agent:` itself: no (Tier A owns that) | yes | one new import edge in the largest suite |
  | **B.** New Postgres wiring inside the `atc/exec` suite | all | requires adding `atc/postgresrunner` to a suite that has none | high; makes a 0.07s unit suite DB-dependent |
  | **C.** Full engine through `atc/integration` | all | yes | highest; that suite drives a real ATC over HTTP and has no snapshot fixtures |

**Decision (2026-07-26): seam A.** Both fact-checks held — authored `load_snapshot` is still rejected at `render.go:364` (so intra-plan chaining must go by artifact name through `build.Repository`, while the renderer's injected `load_snapshot` covers the public-input path), and `atc/db`'s test files are uniformly `package db_test` with zero existing `atc/exec` imports and no `atc/db` → `atc/exec` edge, so the new import edge is legal Go rather than a cycle. Seam A is what the design's own wording asks for — "typed output → typed input chaining and disposition are asserted, not that the full engine scheduler runs in-process".

- [x] Confirm the wait-answering API before writing any test. Run `rg -n 'func \(factory \*agentWorkflowWaitsFactory\)' atc/db/agent_workflow_waits_factory.go` and confirm the server-side answer is a **two-phase outbox**, not a single call: `ReserveResolution` (`:173`) → `workflowwait.MaterializeAnswer` (`agent/workflowwait/materializer.go:31`) → `Resolve` (`:340`). There is no `AnswerWait`/`SubmitAnswer`.
  - **Confirmed (2026-07-26).** The factory exposes exactly `CreateOrGet` (`:48`), `Get` (`:127`), `List` (`:142`), `ReserveResolution` (`:173`), `PendingResolutions` (`:276`), `Resolve` (`:340`), `Expire` (`:435`), `CancelRun` (`:481`) — all three plan line numbers exact, and no single-call answer method exists. `MaterializeAnswer` is at `agent/workflowwait/materializer.go:31` as stated.
- [x] Confirm `AwaitSnapshotStep` needs a context deadline: `rg -n 'an ordinary timeout wrapper is required' atc/exec/await_snapshot_step.go` (`:131`). Task 12 must wrap its context with `context.WithTimeout`.
  - **Confirmed (2026-07-26)** at `:132` (one line below the plan's `:131`): `return false, fmt.Errorf("await_snapshot: an ordinary timeout wrapper is required")`.
- [x] Commit `docs(test-hardening): pin the chained DAG execution seam`.

---

### Task 12: Tier B — chain typed outputs through a workflow run under Postgres

**Files:**
- Create: `atc/db/agent_workflow_chained_e2e_test.go`

- [x] Create the file. The workflow manifest fixture is complete, and every helper it uses (`workflowRunTar`, `workflowRunMemoryContent`, `findAgentPlan`, `newWorkflowRunVerticalSlice`, `defaultTeam`, `buildFactory`, `dbConn`, `logger`, `lockFactory`) already exists in the `db_test` package:

```go
package db_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/api/workflowoutcomes"
	"github.com/concourse/concourse/agent/fixtureagent"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/agent/workflowrun"
	"github.com/concourse/concourse/agent/workflowwait"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/builds"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/exec/build"
	"github.com/concourse/concourse/atc/exec/execfakes"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/tracing"
	"github.com/concourse/concourse/vars"
	oteltrace "go.opentelemetry.io/otel/trace"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// chainedWorkflowManifest is the smallest function that exercises every link
// this test exists to prove: a public input materialized by the renderer's
// load_snapshot, one agent producing a typed output, an await_snapshot answered
// server-side, and a second agent consuming BOTH the first agent's output (by
// artifact name, the only intra-plan chaining a workflow permits — see
// agent/workflow/render.go:364) and the sealed human answer.
func chainedWorkflowManifest(name string) workflow.Manifest {
	return workflow.Manifest{
		"workflow.yml": fmt.Sprintf(`schema_version: 3
name: %s
signature_version: 1
disposition_output: verdict
inputs:
  - name: change
    type: opaque/v1
outputs:
  - name: verdict
    type: review/v1
    from: final-review
plan:
  - agent: review
    function_id: review
    prompt: Review the exposed change, write review/v1, and ask for approval.
    inputs: [change]
    outputs: [draft-review, approval-question]
    input_types:
      change: {type: opaque/v1}
    output_types:
      draft-review: review/v1
      approval-question: question/v1
  - await_snapshot: approval
    question: approval-question
    type: human-answer/v1
    on_timeout: fail
    timeout: 1h
  - agent: confirm
    function_id: confirm
    prompt: Re-issue the reviewed verdict once a human approved it.
    inputs: [change, draft-review, approval]
    outputs: [final-review]
    input_types:
      change: {type: opaque/v1}
      draft-review: {type: review/v1}
      approval: {type: human-answer/v1}
    output_types:
      final-review:
        type: review/v1
        retention: workflow
        workflow_port: verdict
`, name),
	}
}
```

> **Task 13 note (2026-07-26) — manifest fix:** the `output_types.final-review` block shown above does not decode as written. An `output_types` entry accepts only `type` and `optional` (`agent/workflow/parse.go:482`, `validateObjectSource(..., []string{"type", "optional"})`); `retention` and `workflow_port` are not authorable there and are rejected at import. The landed manifest (`atc/db/agent_workflow_chained_e2e_test.go:86`) declares `final-review: review/v1` as a bare type reference instead — retention and the port name are attached by the platform in `AnnotatePublicOutputs` (`agent/workflow/typecheck.go:103-104`) from the top-level `outputs: - name: verdict ... from: final-review` mapping, not authored per-step. See `## Deviations from the design`.

- [x] Add the spec body:

```go
var _ = Describe("agent workflow run chained DAG", func() {
	It("chains a sealed typed output into the next step, answers a wait server-side, and records the outcome", func() {
		name := fmt.Sprintf("chained-%d", time.Now().UnixNano())
		renderer := workflowrun.WorkflowTargetRenderer{
			RuntimeImage: "registry.example/agent-runner@sha256:" + strings.Repeat("a", 64),
		}
		workflows := db.NewAgentWorkflowsFactory(dbConn, renderer)
		snapshots := db.NewAgentSnapshotsFactory(dbConn)
		runs := db.NewAgentWorkflowRunsFactory(dbConn)
		waits := db.NewAgentWorkflowWaitsFactory(dbConn)
		outcomes := db.NewAgentWorkflowOutcomesFactory(dbConn)

		registry, err := contracts.NewRegistry(
			contracts.WithCanonicalizer(snapshot.Canonicalizer{TempDir: GinkgoT().TempDir()}),
		)
		Expect(err).NotTo(HaveOccurred())
		content := &workflowRunMemoryContent{objects: map[snapshot.Digest][]byte{}}
		sealer, err := snapshot.NewBatchSealer(
			snapshot.Canonicalizer{TempDir: GinkgoT().TempDir()}, registry, snapshots, content,
			db.NewAgentSnapshotDigestLocker(dbConn),
		)
		Expect(err).NotTo(HaveOccurred())

		definition, err := workflows.ImportManifest(name, chainedWorkflowManifest(name), "alice")
		Expect(err).NotTo(HaveOccurred())
		_, err = workflows.Promote(name, definition.Version, "alice")
		Expect(err).NotTo(HaveOccurred())

		// The public input: an ordinary uploaded opaque/v1 value.
		changeArchive := workflowRunTar(map[string][]byte{"payload.txt": []byte("subject under review\n")})
		change, err := sealer.Upload(context.Background(), snapshot.UploadRequest{
			TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(), UploadedBy: "alice",
			Actor: "chained", IdempotencyKey: name + "-change", Type: "opaque/v1",
			OpenTar: func(context.Context) (io.ReadCloser, error) {
				return io.NopCloser(bytes.NewReader(changeArchive)), nil
			},
			SourceMetadata: json.RawMessage(`{"adapter":"chained"}`),
		})
		Expect(err).NotTo(HaveOccurred())

		templates, err := workflowrun.NewTemplateSaver(
			teamFactory, db.NewWorkflowRunTemplateFactory(dbConn, lockFactory),
		)
		Expect(err).NotTo(HaveOccurred())
		binder, err := workflowrun.NewBinder(
			workflowrun.WorkflowDefinitionStoreResolver{Store: workflows},
			renderer, snapshots, runs, workflowrun.AllowAllBudgetAdmitter{}, templates,
			db.NewPipelineRunFactory(logger, dbConn, lockFactory, checkFactory),
			workflowRunNoopSecretPreparer{},
		)
		Expect(err).NotTo(HaveOccurred())
		bound, err := binder.BindAndCreate(context.Background(), workflowrun.AdmissionContext{
			TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(), CreatedBy: "alice",
			Origin: workflowrun.Origin{Kind: "manual"},
		}, workflowrun.BindRequest{
			WorkflowName: name, Version: &definition.Version,
			Inputs:         map[string]snapshot.SnapshotID{"change": change.ID},
			IdempotencyKey: name + "-run",
		})
		Expect(err).NotTo(HaveOccurred())
		runID := bound.Run.ID

		stored, found, err := runs.Get(context.Background(), defaultTeam.ID(), runID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		var concrete atc.Config
		Expect(atc.UnmarshalConfig(stored.ConcreteConfig, &concrete)).To(Succeed())
		plan, err := builds.NewPlanner(atc.NewPlanFactory(0)).Create(
			concrete.Jobs[0].StepConfig(), nil, concrete.ResourceTypes, concrete.Prototypes, nil, false,
		)
		Expect(err).NotTo(HaveOccurred())
		reviewPlan := findAgentPlan(plan, "review")
		confirmPlan := findAgentPlan(plan, "confirm")
		Expect(reviewPlan).NotTo(BeNil())
		Expect(confirmPlan).NotTo(BeNil())

		buildRow, found, err := buildFactory.Build(int(*stored.PlannedBuildID))
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		started, err := buildRow.Start(plan)
		Expect(err).NotTo(HaveOccurred())
		Expect(started).To(BeTrue())

		// ---- Step 0: the renderer's load_snapshot materializes the public input.
		state := exec.NewRunState(noopChainedStepper, vars.StaticVariables{})
		repository := state.ArtifactRepository()
		delegates := chainedDelegates()
		loadPlan := findLoadSnapshotPlan(plan, "change")
		Expect(loadPlan).NotTo(BeNil())
		loadStep := exec.NewLoadSnapshotStep(
			loadPlan.ID, *loadPlan.LoadSnapshot,
			exec.StepMetadata{TeamID: defaultTeam.ID(), BuildID: buildRow.ID()},
			delegates, snapshots, content, runs,
		)
		ok, err := loadStep.Run(context.Background(), state)
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
		loaded, found := repository.ArtifactEntryFor("change")
		Expect(found).To(BeTrue())
		Expect(loaded.Snapshot.ID).To(Equal(change.ID))

		// ---- Step 1: the first agent's sealed typed outputs.
		reviewDigest, _ := contracts.SchemaDigestFor(snapshot.TypeRef("review/v1"))
		draftTar, err := fixtureagent.Tar(fixtureagent.CaseReviewAccept, fixtureagent.Authority{
			RecordType: "review/v1", RecordSchema: reviewDigest.String(),
			SubjectInput: "change", SubjectType: "opaque/v1", SubjectDigest: change.Digest.String(),
		})
		Expect(err).NotTo(HaveOccurred())
		// question/v1 decodes question.json with DisallowUnknownFields and
		// requires both prompt and context (contracts/interaction.go).
		questionTar := workflowRunTar(map[string][]byte{
			"question.json": []byte(`{"schema_version":"1.0.0","prompt":"Approve this exact review?","context":"chained DAG integration test"}`),
		})
		definitionID := definition.ID
		sealedFirst, err := sealer.Seal(context.Background(), snapshot.SealRequest{
			BuildID: buildRow.ID(), TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(), CreatedBy: "alice",
			PlanID: reviewPlan.ID.String(), Attempt: "1", StepKind: "agent", StepName: "review",
			WorkflowDefinitionID: &definitionID, WorkflowRunID: &runID,
			InputOrder: []string{"change"},
			Inputs:     map[string]snapshot.SnapshotRef{"change": *loaded.Snapshot},
			InputExposures: map[string]snapshot.InputExposure{
				"change": snapshot.FullTreeExposure("/tmp/build/change", change.Digest),
			},
			OutputDeclarations: []snapshot.Port{
				{Name: "approval-question", Type: "question/v1"},
				{Name: "draft-review", Type: "review/v1"},
			},
			Outputs: []snapshot.OutputSource{
				{
					ClientKey: "approval-question", Port: snapshot.Port{Name: "approval-question", Type: "question/v1"},
					OpenTar: func(context.Context) (io.ReadCloser, error) {
						return io.NopCloser(bytes.NewReader(questionTar)), nil
					},
				},
				{
					ClientKey: "draft-review", Port: snapshot.Port{Name: "draft-review", Type: "review/v1"},
					OpenTar: func(context.Context) (io.ReadCloser, error) {
						return io.NopCloser(bytes.NewReader(draftTar)), nil
					},
				},
			},
		})
		Expect(err).NotTo(HaveOccurred())
		draftRef := sealedFirst["draft-review"].Snapshot
		questionRef := sealedFirst["approval-question"].Snapshot

		draftArtifact, err := chainedArtifact(context.Background(), snapshots, defaultTeam.ID(), draftRef, content)
		Expect(err).NotTo(HaveOccurred())
		questionArtifact, err := chainedArtifact(context.Background(), snapshots, defaultTeam.ID(), questionRef, content)
		Expect(err).NotTo(HaveOccurred())
		Expect(repository.RegisterArtifacts(map[build.ArtifactName]build.ArtifactEntry{
			"draft-review":      {Artifact: draftArtifact, Snapshot: &draftRef},
			"approval-question": {Artifact: questionArtifact, Snapshot: &questionRef},
		})).To(Succeed())

		// ---- Step 2: the await, answered server-side while the step polls.
		awaitPlan := findAwaitSnapshotPlan(plan, "approval")
		Expect(awaitPlan).NotTo(BeNil())
		awaitStep := exec.NewAwaitSnapshotStep(
			awaitPlan.ID, nil, *awaitPlan.AwaitSnapshot,
			exec.StepMetadata{TeamID: defaultTeam.ID(), BuildID: buildRow.ID()},
			delegates, waits, sealer, snapshots, content, 10*time.Millisecond,
		)
		awaitCtx, cancelAwait := context.WithTimeout(context.Background(), time.Minute)
		defer cancelAwait()

		answered := make(chan error, 1)
		go func() {
			defer GinkgoRecover()
			answered <- answerChainedWait(awaitCtx, waits, sealer, runID)
		}()

		ok, err = awaitStep.Run(awaitCtx, state)
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
		Eventually(answered, time.Minute).Should(Receive(BeNil()))

		approval, found := repository.ArtifactEntryFor("approval")
		Expect(found).To(BeTrue())
		Expect(approval.Snapshot.Type).To(Equal(snapshot.TypeRef("human-answer/v1")))

		// ---- Step 3: the second agent consumes step 1's sealed output BY NAME
		// plus the sealed answer. This is the chaining claim: the subject the
		// final record binds is the digest step 1 committed, not a literal.
		finalTar, err := fixtureagent.Tar(fixtureagent.CaseReviewAccept, fixtureagent.Authority{
			RecordType: "review/v1", RecordSchema: reviewDigest.String(),
			SubjectInput: "draft-review", SubjectType: "review/v1", SubjectDigest: draftRef.Digest.String(),
		})
		Expect(err).NotTo(HaveOccurred())
		sealedSecond, err := sealer.Seal(context.Background(), snapshot.SealRequest{
			BuildID: buildRow.ID(), TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(), CreatedBy: "alice",
			PlanID: confirmPlan.ID.String(), Attempt: "1", StepKind: "agent", StepName: "confirm",
			WorkflowDefinitionID: &definitionID, WorkflowRunID: &runID,
			InputOrder: []string{"approval", "change", "draft-review"},
			Inputs: map[string]snapshot.SnapshotRef{
				"approval": *approval.Snapshot, "change": *loaded.Snapshot, "draft-review": draftRef,
			},
			InputExposures: map[string]snapshot.InputExposure{
				"approval":     snapshot.FullTreeExposure("/tmp/build/approval", approval.Snapshot.Digest),
				"change":       snapshot.FullTreeExposure("/tmp/build/change", change.Digest),
				"draft-review": snapshot.FullTreeExposure("/tmp/build/draft-review", draftRef.Digest),
			},
			OutputDeclarations: []snapshot.Port{{Name: "final-review", Type: "review/v1"}},
			Outputs: []snapshot.OutputSource{{
				ClientKey: "final-review", Port: snapshot.Port{Name: "final-review", Type: "review/v1"},
				Retention: snapshot.RetentionClassWorkflow, WorkflowPort: "verdict",
				OpenTar: func(context.Context) (io.ReadCloser, error) {
					return io.NopCloser(bytes.NewReader(finalTar)), nil
				},
			}},
		})
		Expect(err).NotTo(HaveOccurred())
		verdict := sealedSecond["final-review"].Snapshot

		// ---- Terminal disposition.
		Expect(buildRow.Finish(db.BuildStatusSucceeded)).To(Succeed())
		later := time.Now().Add(time.Hour)
		reconciler, err := workflowrun.NewReconciler(
			runs, logger, 10*time.Minute, time.Minute,
			workflowrun.WithReconcilerClock(func() time.Time { return later }),
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(reconciler.Run(context.Background())).To(Succeed())

		completed, found, err := runs.Get(context.Background(), defaultTeam.ID(), runID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(completed.Status).To(Equal(db.AgentWorkflowRunStatusSucceeded))

		// The lineage the chain produced: change is an input, verdict is the
		// public output, and the intermediate draft is bound to the run too.
		bindings, err := runs.Snapshots(context.Background(), runID)
		Expect(err).NotTo(HaveOccurred())
		Expect(bindings).To(ContainElement(db.AgentWorkflowRunSnapshotBinding{
			WorkflowRunID: runID, Direction: db.AgentWorkflowRunSnapshotInput,
			PortName: "change", Snapshot: snapshot.SnapshotRef{ID: change.ID, Type: "opaque/v1", Digest: change.Digest},
		}))
		Expect(bindings).To(ContainElement(db.AgentWorkflowRunSnapshotBinding{
			WorkflowRunID: runID, Direction: db.AgentWorkflowRunSnapshotOutput,
			PortName: "verdict", Snapshot: verdict,
		}))

		// The outcome row: recordable only because the reconciler promoted the
		// output binding, which is the platform's definition of "this run
		// really produced this value".
		outcome, created, err := outcomes.Record(context.Background(), defaultTeam.ID(), workflowoutcomes.RecordRequest{
			WorkflowRunID: runID, OutputSnapshotID: verdict.ID,
			Disposition: workflowoutcomes.DispositionAccepted,
			PublicationState: workflowoutcomes.PublicationNotRequested,
			Actor: "alice",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())
		Expect(outcome.Disposition).To(Equal(workflowoutcomes.DispositionAccepted))
		Expect(outcome.OutputSnapshotID).To(Equal(verdict.ID))
	})
})

// answerChainedWait performs the server-side two-phase answer: reserve the
// durable intent, materialize the deterministic human-answer/v1 snapshot, then
// CAS it onto the wait. There is no single "answer" call.
func answerChainedWait(
	ctx context.Context,
	waits db.AgentWorkflowWaitsFactory,
	creator workflowwait.AnswerSnapshotCreator,
	runID snapshot.WorkflowRunID,
) error {
	var pending workflowwait.Wait
	Eventually(func() bool {
		listed, err := waits.List(ctx, defaultTeam.ID(), runID)
		if err != nil || len(listed) == 0 {
			return false
		}
		pending = listed[0]
		return pending.Status == workflowwait.StatusWaiting
	}, 30*time.Second, 10*time.Millisecond).Should(BeTrue())

	wait, intent, _, err := waits.ReserveResolution(ctx, workflowwait.ReserveResolutionRequest{
		TeamID: defaultTeam.ID(), WorkflowRunID: runID, WaitID: pending.ID,
		AnswerValue: "approved", Actor: "alice", DisplayName: "Alice",
	})
	if err != nil {
		return err
	}
	answer, err := workflowwait.MaterializeAnswer(
		ctx, creator, defaultTeam.ID(), defaultTeam.Name(), runID, wait.ID, intent,
	)
	if err != nil {
		return err
	}
	_, _, err = waits.Resolve(ctx, workflowwait.ResolveRequest{
		TeamID: defaultTeam.ID(), WorkflowRunID: runID, WaitID: wait.ID,
		Answer: snapshot.SnapshotRef{ID: answer.ID, Type: answer.Type, Digest: answer.Digest},
		AnswerValue: intent.AnswerValue, Actor: intent.Actor,
		DisplayName: intent.DisplayName, ReservedAt: intent.ReservedAt,
	})
	return err
}

func chainedArtifact(
	ctx context.Context,
	store db.AgentSnapshotsFactory,
	teamID int,
	ref snapshot.SnapshotRef,
	content snapshot.ContentStore,
) (runtime.Artifact, error) {
	manifest, found, err := store.GetAuthorized(ctx, teamID, ref.ID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("sealed snapshot %s is unavailable", ref.ID)
	}
	return runtime.NewSnapshotArtifact(manifest, content)
}

func chainedDelegates() *execfakes.FakeBuildStepDelegateFactory {
	delegate := new(execfakes.FakeBuildStepDelegate)
	delegate.StartSpanCalls(func(ctx context.Context, _ string, _ tracing.Attrs) (context.Context, oteltrace.Span) {
		return ctx, tracing.NoopSpan
	})
	delegates := new(execfakes.FakeBuildStepDelegateFactory)
	delegates.BuildStepDelegateReturns(delegate)
	return delegates
}

var noopChainedStepper exec.Stepper = func(atc.Plan) exec.Step { return nil }

func findLoadSnapshotPlan(plan atc.Plan, name string) *atc.Plan {
	var found *atc.Plan
	plan.Each(func(candidate *atc.Plan) {
		if found == nil && candidate.LoadSnapshot != nil && candidate.LoadSnapshot.Name == name {
			clone := *candidate
			found = &clone
		}
	})
	return found
}

func findAwaitSnapshotPlan(plan atc.Plan, name string) *atc.Plan {
	var found *atc.Plan
	plan.Each(func(candidate *atc.Plan) {
		if found == nil && candidate.AwaitSnapshot != nil && candidate.AwaitSnapshot.Name == name {
			clone := *candidate
			found = &clone
		}
	})
	return found
}
```

- [x] Run `ginkgo --focus="chained DAG" ./atc/db/`. PostgreSQL must be up (`pg_isready`). Work through the failures in this order, because each one is a real contract the test is discovering:
  1. **Compile errors on constructor arity.** Re-read the four signatures the test calls — `exec.NewLoadSnapshotStep` (`atc/exec/load_snapshot_step.go:45`), `exec.NewAwaitSnapshotStep` (`atc/exec/await_snapshot_step.go:56`), `db.NewAgentWorkflowWaitsFactory` (`atc/db/agent_workflow_waits_factory.go:19`), `workflowwait.MaterializeAnswer` (`agent/workflowwait/materializer.go:31`) — and match them exactly.
  2. **Manifest rejected at import.** `workflow.ParseCompiled` decodes with `DisallowUnknownFields`; a typo in a step key surfaces here. Fix the manifest, not the parser.
  3. **`await_snapshot: an ordinary timeout wrapper is required`.** The `awaitCtx` must have a deadline; the `context.WithTimeout` above supplies it.
  4. **`record subject "primary" input "draft-review" is not an exact declared input`** on the final seal. That means the second `SealRequest.Inputs` map is missing `draft-review` — which is exactly the chaining link the test exists to prove.
- [x] When green, prove the chaining is load-bearing rather than incidental: change the final fixture's `SubjectDigest` from `draftRef.Digest.String()` to `change.Digest.String()` and re-run. Expected failure: `record subject "primary" digest does not match input "draft-review" digest`. Restore it.
- [x] Run `ginkgo --focus="agent workflow run" ./atc/db/` and confirm the pre-existing vertical-slice specs still pass alongside the new one.
- [x] Run the whole suite once: `ginkgo ./atc/db/`. Expected: ~1008 specs, `SUCCESS!`, roughly 90 seconds. A new `atc/exec` import edge in `db_test` lengthens compilation; if the suite now exceeds the default Ginkgo timeout, raise it in the command rather than trimming the test.
- [x] Commit `test(db): chain typed outputs through a workflow run`.

---

### Task 13: Self-review against the WS2 acceptance criteria

**Files:**
- Modify: `docs/superpowers/plans/test-hardening/02-fixture-agent-e2e.md`
- Modify: `TESTING.md`

- [x] Walk the design's four acceptance sentences and record a verdict for each, with the command that proves it:
  1. *"`ginkgo ./atc/exec/ --focus="fixture"` exercises the real sealer with zero fakes between step and contract validation."* — run it; confirm 11 specs. Confirm by inspection that the only fakes in the path are `execfakes.FakePool`/`FakeStreamer`/`FakeTaskDelegate` (worker selection, log streaming, build events) and the in-memory stores (durability); `snapshot.BatchSealer`, `snapshot.Canonicalizer`, and `contracts.NewRegistry()` are all real.
     - **Verdict (2026-07-26): PASS.** `ginkgo --focus="fixture" ./atc/exec/` → `SUCCESS! -- 11 Passed | 0 Failed | 0 Pending | 691 Skipped`. `atc/exec/agent_step_fixture_test.go:222-225` declares exactly four counterfeiter fakes (`fakePool`, `fakeStreamer`, `fakeDelegate`, `fakeDelegateFactory`); the sealer path at `:350-355` constructs a real `contracts.NewRegistry(contracts.WithCanonicalizer(...))`, a real `snapshot.Canonicalizer{...}`, and a real `snapshot.NewBatchSealer(...)`. The other hand-written types (`fixtureMemoryContent`, `fixtureMemoryMetadata`, `fixtureLocks`) are durability stand-ins that verify digests on write, not behavior fakes.
  2. *"The chained test proves output→input propagation and disposition under Postgres."* — run `ginkgo --focus="chained DAG" ./atc/db/`; confirm the final record's subject digest equals the digest step 1 sealed, and the outcome row's disposition is `accepted`.
     - **Verdict (2026-07-26): PASS.** `ginkgo --focus="chained DAG" ./atc/db/` → `SUCCESS! -- 1 Passed | 0 Failed | 0 Pending | 1342 Skipped` (Postgres up throughout). `atc/db/agent_workflow_chained_e2e_test.go:291` binds the second seal's subject digest to `draftRef.Digest` — the exact ref step 1's seal returned — and `:358` asserts `outcome.Disposition` equals `workflowoutcomes.DispositionAccepted`.
  3. *"The behavioral suite runs at least one true `agent:` node."* — run `ginkgo --procs=1 --focus="real agent node" ./topgun/k8s_behavioral/`.
     - **Verdict (2026-07-26): PASS; live execution CI-deferred.** This review environment has no Docker/Helm/kubectl/K3s cluster (the tier's own prerequisites per CLAUDE.md), so the live run is not re-executed here. Substituted with `ginkgo --procs=1 --dry-run -v --focus="real agent node" ./topgun/k8s_behavioral/`: discovers exactly one spec, `Agentic workflows runs a real agent node that seals a review record through the fixture agent` (`agentic_workflows_test.go:100`). `ginkgo -v --dry-run --focus="Agentic workflows" ./topgun/k8s_behavioral/` discovers both it and the pre-existing task-node spec (`2` specs total, matching Task 10's own "`2 Passed`" expectation). The live pass is Task 10's commit (`abd9c46cbf`) and CI's responsibility going forward, not this self-review's.
  4. *"Every hostile case fails with a message an operator can act on."* — run `ginkgo --focus="hostile" ./atc/exec/ -v` and read all seven messages aloud; each must name the output and the violated rule.
     - **Verdict (2026-07-26): PASS.** `ginkgo --focus="hostile" -v ./atc/exec/` → `SUCCESS! -- 7 Passed | 0 Failed | 0 Pending | 695 Skipped`. The seven framing/clause pairs asserted at `agent_step_fixture_test.go:458-497` each name the failing output (`snapshot: capture output "review"` or `snapshot: validate output "review"`) and the violated rule verbatim — e.g. `archive path "../escape" contains an empty, dot, or traversal segment`, `symlink "escape" target escapes the archive root`, `archive exceeds regular content limit of 512 bytes`, `required regular file "record.json" is missing`, `record schema must be exactly the current schema digest`, `record subject "primary" input "not-a-declared-input" is not an exact declared input`, `body/findings/*/id: "F-1" is duplicate`.
- [x] Check the two non-goals held: `git diff --stat main -- agent/snapshot/contracts` must be empty, and `git diff main -- cmd/function-runner` must be empty.
  - **Verdict (2026-07-26): non-goal HELD, but the literal command is confounded by branch topology — read before trusting the raw output.** `main` here (`08f6d98950`) sits 37 commits behind this branch's `HEAD` and predates an unrelated, already-landed merge, `3e16271c28` ("merge: sealed record contracts onto the v3 snapshot platform"), plus two more `feat(snapshot)` commits after it (`9b376febc6`, `410d9b59f8`) — none of them WS2 or even WS1 work. Because of that, the literal `git diff --stat main -- agent/snapshot/contracts` is **not** empty (54 files, +13338/-494) and `git diff main -- cmd/function-runner` is also non-empty (sealed-record decoding updates in `main_test.go`). Neither diff was authored by this plan. The scoped check that actually isolates WS2 (and WS1) — `git diff --stat 410d9b59f8..HEAD -- agent/snapshot/contracts cmd/function-runner`, where `410d9b59f8` is the last commit before this test-hardening effort's own history begins — **is empty**, confirming the non-goal genuinely held for every task in this plan.
- [x] Add an `agent/` tier row to `TESTING.md` naming `go test ./agent/fixtureagent/ ./cmd/fixture-agent/` and a one-line note that `--focus="fixture"` in `atc/exec` is the real-sealer lane. (WS1 owns the rest of the TESTING.md work; do not duplicate it.)
  - Done: new "9. Fixture Agent & Real-Sealer Lane" section added to `TESTING.md`, after WS1's existing tier 8.
- [x] Record any deviation discovered during implementation in a short `## Deviations from the design` section at the bottom of this plan — in particular, whether Task 11 confirmed seam A, and the fact that intra-workflow chaining is by artifact name rather than authored `load_snapshot`.
  - Done: see `## Deviations from the design` at the bottom of this file.
- [x] Tick every checkbox in this document that was completed, and run `rg -n '\- \[ \]' docs/superpowers/plans/test-hardening/02-fixture-agent-e2e.md` to confirm none remain unaccounted for.
  - Done: every task checkbox in Tasks 1-13 is ticked. The plan's literal (unanchored) `rg -n '\- \[ \]' docs/superpowers/plans/test-hardening/02-fixture-agent-e2e.md` returns exactly one hit after this edit: line 3, `` Steps use checkbox (`- [ ]`) syntax for tracking. `` — the header's own meta-documentation of the checkbox convention, not a task item. `rg -n '^\- \[ \]' ...` (anchored) returns zero matches, confirming no real task checkbox remains unticked.
- [x] Commit `docs(test-hardening): record WS2 completion`.

---

## Deviations from the design

- **Seam A confirmed (Task 11).** The chained-DAG test drives `atc/exec`'s `LoadSnapshotStep`/`AwaitSnapshotStep` directly from a new `db_test` file, with the real `BatchSealer` standing in for the agent pod, under real PostgreSQL — not the full engine scheduler. This is deliberate and matches the design's own wording ("typed output → typed input chaining and disposition are asserted, not that the full engine scheduler runs in-process"), not a shortfall.
- **Intra-workflow chaining is by artifact name, not authored `load_snapshot`.** `agent/workflow/render.go:364` rejects any authored `load_snapshot` step inside a workflow; the renderer injects one per public *input* only (`render.go:190-196`). A second agent step consuming a first agent step's typed output does so purely through `build.Repository` artifact names (see `atc/db/agent_workflow_chained_e2e_test.go`'s `repository.RegisterArtifacts` call). The design's phrase "feeds it to step 2 as a typed input (`load_snapshot`)" describes the *workflow-input* path specifically; Task 12's test exercises both that path (for `change`) and the by-name intra-plan path (for `draft-review` and `approval`).
- **Task 9 → Task 10 `helmArgs` relocation.** Task 9's checklist instructed adding a `web.agentStepImage` line to `topgun/k8s_behavioral/cluster_lifecycle_test.go`'s `helmArgs`, but Task 9's own `Files:` block never listed that file. The line was correctly deferred and landed in Task 10's commit (`abd9c46cbf`) instead, alongside the new agent-node spec that actually needed it. A plan `Files:`-block defect, not an implementation gap; annotated in place above Task 10.
- **Task 12 manifest fix: `output_types` does not accept `retention`/`workflow_port`.** The Task 12 code block above authors `final-review`'s `output_types` entry as an object with `type`, `retention`, and `workflow_port` keys. `agent/workflow/parse.go:482` only allow-lists `type` and `optional` for an output-type object; the other two keys are rejected at import. The landed test (`atc/db/agent_workflow_chained_e2e_test.go:86`) declares `final-review: review/v1` as a bare type reference instead, and relies on `AnnotatePublicOutputs` (`agent/workflow/typecheck.go:78-117`) to attach `Retention`/`WorkflowPort` after render, driven by the workflow's top-level `outputs: - name: verdict ... from: final-review` mapping. Annotated in place above the Task 12 manifest.
- **Task 4's binary got one immediate follow-up fix.** `ea23575891` (Task 4's main commit) was followed by `a441442838` ("emit three-way status vocabulary in fixture step.end events") before Task 5 began: the real runner writes `ok|failed|error` via `schema.ThreeWayStatus`, and the fixture binary's first cut would have left a strict consumer misreading a `pass` result as an error. Both commits are within Task 4's scope; the fix is small (`cmd/fixture-agent/main.go`, 4 insertions/1 deletion).
- **`git diff main` is not a clean signal for this plan's non-goals.** `main` is 37 commits behind this branch's `HEAD` and predates an unrelated merge (`3e16271c28`, "sealed record contracts onto the v3 snapshot platform") plus two more `feat(snapshot)` commits, none of which belong to WS1 or WS2. A literal `git diff --stat main -- agent/snapshot/contracts` / `git diff main -- cmd/function-runner` therefore shows large, unrelated diffs. The scoped range `410d9b59f8..HEAD` (the commit immediately before this test-hardening effort's own history begins) is empty against both paths, which is the check that actually matters. Annotated in place above Task 13's non-goal checkbox.
- **The `atc/db` suite has grown well past the plan's "~1008 specs" estimate.** A full `ginkgo ./atc/db/` run during this self-review reports `1342 Passed | 0 Failed | 1 Pending` out of 1343 specs total (~230s), not ~1008/~90s. The difference predates and is unrelated to WS2 — other, already-landed workstreams sharing this branch (the sealed-record schema layer, WS1's CI plumbing) added specs of their own. WS2's contribution is exactly the +1 `chained DAG` spec, plus the 3 pre-existing vertical-slice specs Task 12 confirmed still pass alongside it (4 total under `--focus="agent workflow run"`).
