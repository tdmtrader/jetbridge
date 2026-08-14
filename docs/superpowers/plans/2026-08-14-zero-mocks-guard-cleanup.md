# Mock-Free Architecture Guard and Cleanup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make zero interaction mocks a permanent repository invariant, remove Counterfeiter tooling and stale generation/documentation, and prove the complete unit/runtime criteria.

**Architecture:** A root AST-based architecture test enumerates every module Go file reported by `go list`, including ignored/build-tagged files, and rejects framework imports, generated mock packages/directives, spy-shaped declarations, client action inspection, and non-watch reactors. The guard has no enumerated allowlist; approved state/protocol/lifecycle fixtures use domain APIs that do not match mock signatures.

**Tech Stack:** Go 1.25 standard library (`go/ast`, `go/parser`, `go/token`, `os/exec`), Go modules, Ginkgo v2, Make.

**Spec:** `docs/superpowers/specs/2026-08-14-zero-mocks-design.md`

## Global Constraints

- Execute every multi-command shell block with fail-fast semantics; stop on the first non-zero status even when a snippet does not repeat `set -e`.
- Run this plan only after the lock, engine/exec, scheduler, API access, supporting-fixture, and Kubernetes-runtime plans are green.
- Enumerate source via `go list -e -json ./...`; ordinary `rg` is not evidence because `atc/.ignore` hides `fake_*.go`.
- Scan `GoFiles`, `CgoFiles`, `TestGoFiles`, `XTestGoFiles`, and `IgnoredGoFiles`, so tools and build-tagged/live tests cannot hide mocks.
- Prove at least 50 packages and 500 Go files were scanned; these are lower-bound vacuum guards, not exact repository counts.
- Do not ban words such as `fake`, `recording`, `stub`, or `mock` in names. Approved protocol/state models legitimately use them.
- Explicitly allow client-go object/watch models and `PrependWatchReactor`; reject `.Actions()`, `PrependReactor`, and `AddReactor`.
- Leave the two untracked review documents untouched. Approved specs/plans are historical design records and may name removed tooling; stale contributor/source documentation may not.

---

### Task 1: Add the Failing AST Architecture Guard

**Files:**
- Create: `mock_architecture_test.go`
- Read: `architecture_test.go`

**Interfaces:**
- Consumes: streamed `go list -e -json ./...` package metadata and Go syntax trees.
- Produces: `TestNoInteractionMocks(t *testing.T)` with non-vacuous source enumeration and deterministic violations reported as `path:line: reason`.

- [ ] **Step 1: Implement module-file enumeration**

Create `mock_architecture_test.go` in package `concourse` with:

```go
package concourse

import (
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

type mockGuardPackage struct {
	ImportPath     string
	Name           string
	Dir            string
	Incomplete     bool
	Error          *mockGuardPackageError
	DepsErrors     []*mockGuardPackageError
	GoFiles        []string
	CgoFiles       []string
	TestGoFiles    []string
	XTestGoFiles   []string
	IgnoredGoFiles []string
	InvalidGoFiles []string
}

type mockGuardPackageError struct {
	Err string
}

func moduleGoFiles(t *testing.T) ([]mockGuardPackage, []string) {
	t.Helper()
	cmd := exec.Command("go", "list", "-e", "-json", "./...")
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			t.Fatalf("go list failed: %v\n%s", err, exitErr.Stderr)
		}
		t.Fatalf("go list failed: %v", err)
	}

	var packages []mockGuardPackage
	fileSet := map[string]struct{}{}
	decoder := json.NewDecoder(strings.NewReader(string(out)))
	for {
		var pkg mockGuardPackage
		err := decoder.Decode(&pkg)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("decode go list output: %v", err)
		}
		if pkg.Error != nil {
			t.Fatalf("go list reported an error for %s: %s", pkg.ImportPath, pkg.Error.Err)
		}
		if pkg.Incomplete {
			t.Fatalf("go list reported incomplete package %s", pkg.ImportPath)
		}
		if len(pkg.DepsErrors) > 0 {
			t.Fatalf("go list reported dependency errors for %s: %s", pkg.ImportPath, pkg.DepsErrors[0].Err)
		}
		if len(pkg.InvalidGoFiles) > 0 {
			t.Fatalf("go list reported invalid Go files for %s: %v", pkg.ImportPath, pkg.InvalidGoFiles)
		}
		packages = append(packages, pkg)
		for _, group := range [][]string{pkg.GoFiles, pkg.CgoFiles, pkg.TestGoFiles, pkg.XTestGoFiles, pkg.IgnoredGoFiles} {
			for _, name := range group {
				fileSet[filepath.Join(pkg.Dir, name)] = struct{}{}
			}
		}
	}

	if len(packages) < 50 {
		t.Fatalf("go list returned only %d packages; guard would be vacuous", len(packages))
	}
	if len(fileSet) < 500 {
		t.Fatalf("go list returned only %d Go files; guard would be vacuous", len(fileSet))
	}

	files := make([]string, 0, len(fileSet))
	for path := range fileSet {
		files = append(files, path)
	}
	sort.Strings(files)
	return packages, files
}
```

- [ ] **Step 2: Implement declaration, import, directive, and selector checks**

Add:

```go
var forbiddenMockImports = []string{
	"github.com/maxbrunsfeld/counterfeiter",
	"github.com/golang/mock",
	"go.uber.org/mock",
	"github.com/stretchr/testify/mock",
	"github.com/vektra/mockery",
	"github.com/matryer/moq",
	"github.com/tedsuo/ifrit/fake_runner_v2",
}

var forbiddenMockSuffixes = []string{
	"callcount", "argsforcall", "returns", "returnsoncall", "invocations", "calls",
}

func forbiddenMockName(name string) bool {
	name = strings.ToLower(name)
	for _, suffix := range forbiddenMockSuffixes {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

func isGoTestEntry(decl *ast.FuncDecl) bool {
	if decl.Recv != nil {
		return false
	}
	name := decl.Name.Name
	for _, prefix := range []string{"Test", "Benchmark", "Fuzz", "Example"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func TestNoInteractionMocks(t *testing.T) {
	packages, files := moduleGoFiles(t)
	fset := token.NewFileSet()
	var violations []string

	for _, pkg := range packages {
		lastSlash := strings.LastIndex(pkg.ImportPath, "/")
		importComponent := pkg.ImportPath[lastSlash+1:]
		mockPackage := false
		for _, component := range []string{pkg.Name, importComponent} {
			component = strings.ToLower(component)
			if strings.HasSuffix(component, "fakes") || strings.HasSuffix(component, "mocks") {
				mockPackage = true
			}
		}
		if mockPackage {
			violations = append(violations, pkg.ImportPath+": mock package")
		}
	}

	for _, path := range files {
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			t.Errorf("parse %s: %v", path, err)
			continue
		}
		report := func(pos token.Pos, reason string) {
			violations = append(violations, fmt.Sprintf("%s: %s", fset.Position(pos), reason))
		}

		for _, group := range file.Comments {
			text := strings.ToLower(group.Text())
			generatorDirective := strings.Contains(text, "go:generate") &&
				(strings.Contains(text, "counterfeiter") || strings.Contains(text, "mockgen") ||
					strings.Contains(text, "mockery") || strings.Contains(text, "moq"))
			if strings.Contains(text, "counterfeiter:generate") || generatorDirective ||
				(strings.Contains(text, "code generated by") &&
					(strings.Contains(text, "counterfeiter") || strings.Contains(text, "mockgen") ||
						strings.Contains(text, "mockery") || strings.Contains(text, "moq"))) {
				report(group.Pos(), "generated-mock directive or header")
			}
		}

		for _, spec := range file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				report(spec.Pos(), "invalid import path")
				continue
			}
			for _, prefix := range forbiddenMockImports {
				if importPath == prefix || strings.HasPrefix(importPath, prefix+"/") {
					report(spec.Pos(), "mocking framework import "+importPath)
				}
			}
		}

		ast.Inspect(file, func(node ast.Node) bool {
			switch value := node.(type) {
			case *ast.FuncDecl:
				if !isGoTestEntry(value) && forbiddenMockName(value.Name.Name) {
					report(value.Name.Pos(), "spy-shaped function "+value.Name.Name)
				}
			case *ast.StructType:
				for _, field := range value.Fields.List {
					_, isFunc := field.Type.(*ast.FuncType)
					for _, name := range field.Names {
						lowerName := strings.ToLower(name.Name)
						if forbiddenMockName(name.Name) || (isFunc && (strings.HasPrefix(lowerName, "stub") || strings.HasSuffix(lowerName, "stub"))) {
							report(name.Pos(), "spy-shaped field "+name.Name)
						}
					}
				}
			case *ast.CallExpr:
				selector, ok := value.Fun.(*ast.SelectorExpr)
				if !ok {
					break
				}
				switch selector.Sel.Name {
				case "Actions", "PrependReactor", "AddReactor":
					report(selector.Sel.Pos(), "interaction inspection or non-watch reactor "+selector.Sel.Name)
				}
			}
			return true
		})
	}

	sort.Strings(violations)
	for _, violation := range violations {
		t.Error(violation)
	}
}
```

The source file must not contain a comment with a forbidden generated directive/header; the string literals above are intentionally safe because the guard inspects comment nodes, not arbitrary text. Standard Go `Test`/`Benchmark`/`Fuzz`/`Example` entry-point names are exempt from suffix checks so a behavioral test such as `TestPeerSelector_DeterministicAcrossCalls` is not mistaken for a callable spy API; their bodies and declarations are still scanned normally.

- [ ] **Step 3: Run the guard and verify it finds cleanup work**

Run: `go test . -run TestNoInteractionMocks -count=1`

Expected: FAIL on remaining generation/tool source references or any interaction helper missed by preceding plans. Every reported source violation must be migrated before Task 2; do not add an allowlist.

- [ ] **Step 4: Fix any semantic misses at their owning behavioral boundary**

For each reported declaration/selector, classify it using the approved definition. Interaction helpers are migrated to state/protocol/lifecycle outcomes and their owning package test is rerun. Approved models are renamed only when they accidentally expose a generic mock-shaped API; their semantics remain.

After each owning package checkpoint passes, stage the exact source/test files for that semantic correction and commit it (or commit one cohesive set of misses from the same package) before rerunning the root guard. Do not leave these edits unstaged for Step 5: that step deliberately stages only the architecture guard. Before proceeding, `git status --short` may show the two preserved untracked review documents and `?? mock_architecture_test.go`, but no other implementation change.

- [ ] **Step 5: Commit the guard after all semantic violations except tooling are gone**

Run: `git diff --check`

```bash
git add mock_architecture_test.go
git commit -m "test: prohibit interaction mocks"
```

### Task 2: Remove Generation Tooling and Stale Source Documentation

**Files:**
- Delete: `tools.go`
- Delete: `atc/.ignore`
- Modify: `go.mod`
- Modify: `go.sum`
- Modify: `CONTRIBUTING.md:423-430`
- Modify: `fly/commands/validate_pipeline.go:53`
- Modify: `atc/engine/engine.go:23`
- Modify: `atc/exec/build/repository.go:10`
- Modify any source still matched: remaining former generation sites.

**Interfaces:**
- Consumes: no remaining generated fakes/directives.
- Produces: no Counterfeiter dependency, tool entry, generation entry point, ignore rule, or stale contributor/source guidance.

- [ ] **Step 1: Delete orphan generation entry points**

Remove every `go:generate` line that invokes the removed generator from engine, exec/build, and former fake-owning packages. Remove stale source comments that describe a former generated fake; rewrite the validate-pipeline comment to describe the current real signing-key fixture only.

- [ ] **Step 2: Replace contributor guidance with the durable boundary**

Delete the Counterfeiter section in `CONTRIBUTING.md` and add:

```markdown
### Test doubles

Tests should prefer public HTTP/protocol behavior, persisted database state,
and deterministic in-memory boundary models. Do not add generated or
interaction mocks, call-count/argument assertions, or mocking frameworks.
Controlled clocks, protocol servers, client-go object/watch state, and small
channel-gated lifecycle functions are acceptable when asserted through
observable outcomes.
```

- [ ] **Step 3: Delete tooling and ignore rule, then tidy the module**

Delete `tools.go` and `atc/.ignore`. Run: `go mod tidy`

Expected: `github.com/maxbrunsfeld/counterfeiter/v6` and dependencies retained solely by it disappear from `go.mod`/`go.sum`; production dependencies remain.

- [ ] **Step 4: Verify tracked source/tooling references**

Run:

```bash
set -e
if git grep -n -i 'counterfeiter' -- '*.go' ':(exclude)mock_architecture_test.go' go.mod go.sum tools.go CONTRIBUTING.md; then false; else test $? -eq 1; fi
if git grep -n -E 'counterfeiter:generate|Code generated by (counterfeiter|MockGen|mockery)' -- '*.go' ':(exclude)mock_architecture_test.go'; then false; else test $? -eq 1; fi
fake_dirs=$(find atc -type d -name '*fakes' -print)
find_status=$?
test "$find_status" -eq 0
test -z "$fake_dirs"
```

Expected: no source/tool/dependency/contributor matches and no fake package directories. Approved design specs/plans are excluded because they are the historical decision record.

- [ ] **Step 5: Run the architecture guard to green**

Run: `go test . -run TestNoInteractionMocks -count=1`

Expected: PASS after scanning at least 50 packages and 500 Go files.

- [ ] **Step 6: Commit cleanup**

```bash
git add CONTRIBUTING.md go.mod go.sum fly/commands/validate_pipeline.go atc/engine/engine.go atc/exec/build/repository.go
git add -u tools.go atc/.ignore
git commit -m "build: remove counterfeiter tooling"
```

### Task 3: Perform the Final Semantic Audit

**Files:**
- Modify only files reported by the searches and rejected by the approved boundary.
- Preserve approved protocol/state/lifecycle fixtures identified in the spec.

**Interfaces:**
- Consumes: the AST guard and the full post-migration source tree.
- Produces: evidence that no mock-shaped API escaped the structured checks and retained fixtures are outcome-based.

- [ ] **Step 1: Run tracked-file searches that ignore `atc/.ignore` semantics**

Run:

```bash
set -e
if git grep -n -E 'CallCount|ArgsForCall|ReturnsOnCall|Invocations|RunStub|RunReturns|[A-Za-z0-9_]+Stub[[:space:]]+func' -- '*.go' ':(exclude)mock_architecture_test.go'; then false; else test $? -eq 1; fi
if git grep -n -E '\.Actions\(\)|fake_runner_v2' -- '*.go' ':(exclude)mock_architecture_test.go'; then false; else test $? -eq 1; fi
if git grep -n -E '^[[:space:]]*[A-Za-z0-9_]*(Calls|CallCount)[[:space:]]+' -- '*.go' ':(exclude)mock_architecture_test.go'; then false; else test $? -eq 1; fi
if git grep -n -E '^func \([^)]*\) [A-Za-z0-9_]*Calls\(' -- '*.go' ':(exclude)mock_architecture_test.go'; then false; else test $? -eq 1; fi
if git grep -n -E '\.(PrependReactor|AddReactor)\(' -- '*.go' ':(exclude)mock_architecture_test.go'; then false; else test $? -eq 1; fi
```

Expected: no interaction matches. The guard file is excluded because it necessarily contains the forbidden policy literals as string constants. `PrependWatchReactor` may remain in watch lifecycle tests and is not matched.

- [ ] **Step 2: Review retained fixture classes by behavior**

Confirm all of the following still use domain outcomes and no generic spy API:

- OPA, AWS, OCI, S3/GCS, and artifact-daemon HTTP servers expose requests/responses;
- client-go exposes stored objects and watch events;
- fake clocks expose time-driven outcomes;
- `runtimetest` and pod runtime expose containers, processes, volumes, files, and artifacts;
- lifecycle gates expose named readiness/start/release/completion channels;
- value models expose values, not collaborator histories;
- production `fakeConnectionTracker` remains a disabled/null implementation, not a configurable test double.

- [ ] **Step 3: Run the guard after semantic review**

Run: `go test . -run TestNoInteractionMocks -count=1`

Expected: PASS with no allowlist additions.

- [ ] **Step 4: Commit only if the audit found misses**

For each miss, rerun its package suite, stage exactly its files, and commit with subject `test: remove remaining interaction fixture`. If no miss exists, create no commit.

### Task 4: Full Verification and Runtime Evidence

**Files:**
- Read: `CLAUDE.md`
- Create locally only: `/private/tmp/jetbridge-zero-mocks-final/`

**Interfaces:**
- Consumes: every completed zero-mocks plan.
- Produces: package, unit-tier, build/vet, runtime, and CI evidence required by the approved spec.

Before the first checkpoint, run `mkdir -p /private/tmp/jetbridge-zero-mocks-final`; every timing artifact in this task is written there, outside the repository.

- [ ] **Step 1: Run affected suites serially**

Run each separately:

```bash
ginkgo ./atc/db/lock
ginkgo ./atc/engine
ginkgo ./atc/exec
ginkgo ./atc/scheduler
ginkgo ./atc/api/accessor
ginkgo ./atc/api
ginkgo ./atc/lidar
ginkgo ./atc/component
ginkgo ./atc/worker/jetbridge
go test ./vars -count=1
go test ./cmd -count=1
go test ./cmd/artifact-daemon -count=1
go test . -run TestNoInteractionMocks -count=1
```

Expected: PASS.

- [ ] **Step 2: Run repository build checks**

Run:

```bash
go vet ./...
go build ./...
git diff --check
```

Expected: PASS.

- [ ] **Step 3: Run the complete supported unit tier**

Run: `make test-unit`

Expected: PASS through Ginkgo's parallel-process isolation. Do not replace this with `go test ./...`.

- [ ] **Step 4: Re-measure affected suite wall times**

Run every timed command in the plan-set baseline serially. Save the `real` values under the same package keys in `/private/tmp/jetbridge-zero-mocks-final/times.txt` and compare them with the baseline.

Expected: no unexplained material regression. Any increase is traced to a specific fixture/suite before merge.

- [ ] **Step 5: Run CI-only behavioral gates**

In CI, run the repository's documented live/K3s gates for busybox supervisor execution, exec streaming, pod volume/mount validity, worker behavior, and artifact flow.

Expected: PASS; these are not locally viable on macOS.

- [ ] **Step 6: Verify worktree scope and completion**

Run:

```bash
git status --short
git log --oneline --decorate -20
```

Expected: no unintended changes; `AUDIT-2026-08-11.md` and `FAKES-REVIEW-2026-08-11.md` remain untracked and untouched. The zero-mocks project is then stable enough to begin the separate 80% coverage design.
