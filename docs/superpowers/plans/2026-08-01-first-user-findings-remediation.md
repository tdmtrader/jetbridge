# First-User Findings Remediation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the findings from `FIRST-USER-FINDINGS.md` that have an unambiguous fix — the deployed image missing `git`, failures that hide their reason, and CLI/doc gaps that cost a first user a pod launch to discover.

**Architecture:** Three independently shippable phases. Phase A restores the deployed runtime image to its declared contract. Phase B introduces one opt-in, provably-safe channel for returning contract-validation detail to API clients, then uses it. Phase C is CLI, chart, and documentation ergonomics. No migrations, no schema changes, no behavior change to a healthy deployment.

**Tech Stack:** Go 1.25, `agent/snapshot` + `agent/snapshot/contracts` (server-authoritative validators), `agent/api/*` (rata-routed HTTP handlers, hand-rolled `net/http` tests), `fly` (go-flags commands), Helm chart in `deploy/chart`, Concourse pipeline YAML in `deploy/concourse-pipeline.yml`.

---

## Scope: what this plan does and does not cover

Findings with an unambiguous fix, addressed here:

| Finding | Task |
|---|---|
| F7 deployed web image has no `git` | 1 |
| F6 error responses strip every actionable detail | 2, 3, 4 |
| F11 a failed node run hides its reason three hops away | 5 |
| F19 version numbers are racing state; scripts must parse prose | 6 |
| F22 no `fly agent nodes cancel` | 7 |
| F16 fail-closed hermetic egress with no warning | 8 |
| F12 runner passes a flag the deployed CLI does not have | 9 |
| F1, F2, F9, F17, F21, F27 guide/sample defects | 10 |

Findings deliberately **excluded**, because the fix requires a design decision rather than an implementation:

- **F3** (`repository-change/v1` cannot be created from the CLI) — the seal gate requires a bound base input; letting `snapshots create` bind inputs is a product decision about where changes may originate.
- **F8** (`repository/v1` needs a git repo vs. leak-safe `git archive` materialization) — a corpus/harness policy question, not a platform defect.
- **F13** (raw JSON event stream needs a human view) — a UI project already tracked by UX audits №4 and №5.
- **F23/F27 platform half** (warn the agent as its turn budget runs out) — changes the agent loop; needs a decision on mechanism (injected turn vs. system message) and threshold.
- **F26/F35** (reviewer finds the right code, misjudges the mechanism) — capability work: a code-intelligence sidecar, or splitting into an `impacted-surface` node feeding the review node. This is an experiment, not a fix.
- **F30/F34** (prompt changes need graded A/B) — motivates adding `TargetNode` to `agent/experiment`; that is its own spec.
- **F28** (cost on `show-run`) — needs a decision on the authoritative source (cost ledger vs. `agent_run_metrics`) and on whether a still-running run reports partial spend.
- **F10, F31, F33** — observations that need no code.

F5, F20 and F25 are already fixed in commit `72526ad7da`. **F20's fix reaches the cluster only when the agent-runner image is rebuilt** (`build-agent-runner-image` job, manual trigger) — Task 9 makes the same class of skew fail loudly rather than silently.

---

## File Structure

**Phase A — runtime image parity**
- Modify: `deploy/concourse-pipeline.yml` (inline runtime Dockerfile in `build-image`)
- Create: `deploy/runtime_image_parity_test.go` — drift guard between the inline Dockerfile and `Dockerfile.build`'s runtime stage

**Phase B — actionable validation errors**
- Create: `agent/snapshot/client_detail.go` — the opt-in safe-detail error type and its accessors
- Create: `agent/snapshot/client_detail_test.go`
- Modify: `agent/snapshot/archive.go` — mark archive path/header messages as client detail
- Modify: `agent/snapshot/contracts/json.go` — mark file-shape and decode messages
- Modify: `agent/snapshot/contracts/registry.go`, `workitem.go`, `logbundle.go`, `record.go` — mark document and record-envelope messages
- Modify: `agent/api/snapshots/types.go` — add `detail` to `ErrorResponse`
- Modify: `agent/api/snapshots/handler.go` — populate `detail` for client-caused error classes only
- Modify: `agent/api/snapshots/handler_test.go` — extend the existing no-leak table
- Modify: `fly/commands/agent_snapshots.go` — `decodeAgentSnapshotResponse` must carry `detail` through; it does not use the shared `decodeOrError`

**Phase C — CLI, chart, docs**
- Modify: `fly/commands/agent_workflow_runs.go` — failure-reason printing shared by node and workflow run detail
- Modify: `fly/commands/agent_nodes.go` — `--json` on import/release; `cancel` command
- Modify: `atc/routes.go`, `atc/api/handler.go`, `agent/api/noderuns/handler.go` — node-run cancel route
- Create: `deploy/chart/templates/NOTES.txt` — post-install warning for fail-closed hermetic egress
- Modify: `agent/runner/runner.go` — claude CLI flag preflight
- Modify: `docs/operations/reusable-node-definitions.md`, `docs/agentic/README.md`

---

# Phase A — the deployed image must match its declared contract

## Task 1: Put `git` back in the image the pipeline actually ships

`Dockerfile.build`'s runtime stage installs `git=1:2.34.1-1ubuntu1.17`. The `build-image` job in `deploy/concourse-pipeline.yml` builds `registry.home/jetbridge` from a *different*, inline Dockerfile that installs only `ca-certificates` and `dumb-init`. Consequences on the deployed cluster:

- `repository/v1` and `repository-change/v1` can never seal — their validators exec `git` (`agent/snapshot/contracts/repository.go:568`), so every seed carrying those types is unrunnable (F7).
- The ATC-owned direct-Git publisher, which Task 14 of the semantic-rebase deliberately pinned to the *image-owned* `/usr/bin/git`, has no executable to run.

**Files:**
- Modify: `deploy/concourse-pipeline.yml` (the `cat <<'DOCKERFILE'` heredoc inside `build-image`, around line 593)
- Create: `deploy/runtime_image_parity_test.go`

- [ ] **Step 1: Write the failing drift-guard test**

Create `deploy/runtime_image_parity_test.go`:

```go
package deploy_test

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The build-image job ships registry.home/jetbridge from a Dockerfile inlined
// in the pipeline, while Dockerfile.build declares the runtime image for
// everything else. They have drifted before: the inline copy omitted git,
// which silently made every repository/v1 seal and the ATC direct-Git
// publisher impossible on the deployed cluster while every test stayed green
// on hosts that happen to have git. This pins the runtime package set in both
// places to the same list.
var runtimePackages = []string{
	"ca-certificates",
	"dumb-init",
	"git=1:2.34.1-1ubuntu1.17",
}

func TestPipelineInlineRuntimeImageInstallsTheDeclaredPackages(t *testing.T) {
	pipeline := read(t, "concourse-pipeline.yml")
	inline := inlineDockerfile(t, pipeline)
	for _, pkg := range runtimePackages {
		if !strings.Contains(inline, pkg) {
			t.Errorf("pipeline inline runtime Dockerfile does not install %q:\n%s", pkg, inline)
		}
	}
}

func TestDockerfileBuildRuntimeImageStageInstallsTheDeclaredPackages(t *testing.T) {
	dockerfile := read(t, "../Dockerfile.build")
	stages := strings.Split(dockerfile, "\nFROM ")
	runtime := stages[len(stages)-1]
	for _, pkg := range runtimePackages {
		if !strings.Contains(runtime, pkg) {
			t.Errorf("Dockerfile.build runtime stage does not install %q:\n%s", pkg, runtime)
		}
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// inlineDockerfile extracts the heredoc the build-image job writes to
// /tmp/Dockerfile.
func inlineDockerfile(t *testing.T, pipeline string) string {
	t.Helper()
	block := regexp.MustCompile(`(?s)cat <<'DOCKERFILE' > /tmp/Dockerfile\n(.*?)\n\s*DOCKERFILE\n`)
	match := block.FindStringSubmatch(pipeline)
	if match == nil {
		t.Fatal("could not find the inline Dockerfile heredoc in concourse-pipeline.yml")
	}
	return match[1]
}
```

- [ ] **Step 2: Run the test to verify the inline copy fails and Dockerfile.build passes**

Run: `go test ./deploy/ -run RuntimeImage -v`
Expected: `TestPipelineInlineRuntimeImageInstallsTheDeclaredPackages` FAILS with `does not install "git=1:2.34.1-1ubuntu1.17"`; `TestDockerfileBuildRuntimeImageStageInstallsTheDeclaredPackages` PASSES.

- [ ] **Step 3: Add git to the pipeline's inline Dockerfile**

In `deploy/concourse-pipeline.yml`, inside the `build-image` job's heredoc, replace:

```
          FROM ubuntu:22.04
          RUN apt-get update && \
              apt-get install -y ca-certificates dumb-init && \
              rm -rf /var/lib/apt/lists/*
```

with:

```
          FROM ubuntu:22.04
          # Keep this package set identical to Dockerfile.build's runtime stage;
          # deploy/runtime_image_parity_test.go pins them together. git is not
          # optional: the repository/v1 and repository-change/v1 validators exec
          # it inside ATC, and the direct-Git publisher is pinned to the
          # image-owned /usr/bin/git.
          RUN apt-get update && \
              apt-get install -y --no-install-recommends \
                ca-certificates \
                dumb-init \
                git=1:2.34.1-1ubuntu1.17 \
              && rm -rf /var/lib/apt/lists/*
```

- [ ] **Step 4: Run the test to verify both pass**

Run: `go test ./deploy/ -run RuntimeImage -v`
Expected: PASS (both tests).

- [ ] **Step 5: Commit**

```bash
git add deploy/concourse-pipeline.yml deploy/runtime_image_parity_test.go
git commit -m "fix(ci): install git in the image the build-image job actually ships

Dockerfile.build's runtime stage installs a pinned git; the inline Dockerfile
the build-image job builds registry.home/jetbridge from installed only
ca-certificates and dumb-init. On the deployed cluster that makes every
repository/v1 seal impossible and leaves the ATC direct-Git publisher with no
executable, while tests stay green on hosts that happen to have git.

Pins the runtime package set in both places to one list and guards the drift."
```

- [ ] **Step 6: Note the deploy requirement**

This fix is inert until `build-image` runs and the web deployment picks up the new image. Add a line to `FIRST-USER-FINDINGS.md` under F7 recording the commit and that a rebuild is required. Verify after rollout with:

```bash
kubectl --context theborg -n cicd exec deploy/concourse-web -c concourse-web -- git --version
```

Expected: `git version 2.34.1`.

---

# Phase B — a failure should say what is wrong with the bytes you sent

`writeSnapshotError` maps every failure onto a fixed string. `fly` therefore reports `validation_failed: snapshot does not satisfy its declared type` while the actual cause (`work-item.json: adapter is required`) exists only in the web pod's log. That is deliberate — an existing test asserts a wrapped dependency error containing `secret /tmp/storage-node` never reaches the client — so the fix is **not** to echo the error chain. Instead, validators that build a message purely from the caller's own submitted bytes mark it as safe, and the handler returns only marked text.

## Task 2: The client-detail mechanism

**Files:**
- Create: `agent/snapshot/client_detail.go`
- Create: `agent/snapshot/client_detail_test.go`

- [ ] **Step 1: Write the failing test**

Create `agent/snapshot/client_detail_test.go`:

```go
package snapshot_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
)

func TestClientDetailIsFoundThroughWrappingAndJoining(t *testing.T) {
	marked := snapshot.ClientDetailf("archive path %q has a trailing separator", ".claude/")
	wrapped := errors.Join(
		snapshot.ErrInvalidArchive,
		fmt.Errorf("snapshot: capture upload: %w", marked),
	)

	detail, ok := snapshot.ClientDetail(wrapped)
	if !ok {
		t.Fatal("client detail was not found through the wrapped, joined chain")
	}
	if detail != `archive path ".claude/" has a trailing separator` {
		t.Fatalf("detail = %q", detail)
	}
	if !errors.Is(wrapped, snapshot.ErrInvalidArchive) {
		t.Fatal("marking broke the error class")
	}
}

func TestUnmarkedErrorsCarryNoClientDetail(t *testing.T) {
	unmarked := errors.Join(snapshot.ErrValidation, errors.New("secret /tmp/storage-node"))
	if detail, ok := snapshot.ClientDetail(unmarked); ok {
		t.Fatalf("unmarked error exposed detail %q", detail)
	}
}

// The outermost mark wins: a caller that adds context closer to the boundary
// is describing the same failure in more useful terms.
func TestOutermostClientDetailWins(t *testing.T) {
	inner := snapshot.ClientDetailf("adapter is required")
	outer := snapshot.WrapClientDetailf(inner, "work-item.json: %s", "adapter is required")
	detail, ok := snapshot.ClientDetail(outer)
	if !ok || detail != "work-item.json: adapter is required" {
		t.Fatalf("detail = %q ok = %v", detail, ok)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./agent/snapshot/ -run ClientDetail -v`
Expected: FAIL to compile — `undefined: snapshot.ClientDetailf`.

- [ ] **Step 3: Implement the mechanism**

Create `agent/snapshot/client_detail.go`:

```go
package snapshot

import (
	"errors"
	"fmt"
)

// clientDetailError carries a message that is safe to return to the API
// caller.
//
// Safe means one thing precisely: every byte of the message is derived from
// the caller's OWN submitted content plus fixed strings this repository
// wrote. Host paths, scratch directories, storage-node identities, database
// state, and wrapped dependency errors are none of those, and must never be
// marked.
//
// The channel is opt-in because the default has to be silence: the snapshot
// API maps failures onto fixed strings on purpose, and one blanket "just
// return the error" would undo that for every future error path at once.
// Marking is therefore a decision made at the site that FORMATS the message,
// where the safety argument is checkable, and nowhere else.
type clientDetailError struct {
	detail string
	err    error
}

func (e *clientDetailError) Error() string { return e.detail }
func (e *clientDetailError) Unwrap() error { return e.err }

// ClientDetailf creates a new error whose message is safe to disclose.
func ClientDetailf(format string, args ...any) error {
	return &clientDetailError{detail: fmt.Sprintf(format, args...)}
}

// WrapClientDetailf marks err with a safe message while preserving err for
// errors.Is/As. Use it when the underlying error must keep travelling but the
// disclosable phrasing is decided here.
func WrapClientDetailf(err error, format string, args ...any) error {
	if err == nil {
		return nil
	}
	return &clientDetailError{detail: fmt.Sprintf(format, args...), err: err}
}

// ClientDetail returns the outermost disclosable message in err's tree.
//
// errors.As walks both wrapped and joined errors, so a mark survives the
// sealer's errors.Join(category, fmt.Errorf("...: %w", err)) composition
// without every intermediate layer having to know about it.
func ClientDetail(err error) (string, bool) {
	var detail *clientDetailError
	if errors.As(err, &detail) {
		return detail.detail, true
	}
	return "", false
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./agent/snapshot/ -run ClientDetail -v`
Expected: PASS (3 tests).

- [ ] **Step 5: Commit**

```bash
git add agent/snapshot/client_detail.go agent/snapshot/client_detail_test.go
git commit -m "feat(snapshot): add an opt-in channel for disclosable validation detail

The snapshot API maps every failure onto a fixed string, so a caller cannot
tell 'your work-item.json is missing a field' from 'the storage node is
unreachable'. Blanket-echoing the error chain would leak host paths, so
marking is opt-in and happens where the message is formatted and its safety is
checkable."
```

## Task 3: Mark the messages that describe the caller's own bytes

**Files:**
- Modify: `agent/snapshot/archive.go:917-941` (`validateArchivePath`)
- Modify: `agent/snapshot/contracts/json.go:16-34, 59-78` (`decodeStrictDocument`, `readRegularFile`)
- Modify: `agent/snapshot/contracts/record.go:288-297` (`admitRecordForSeal`)
- Modify: `agent/snapshot/contracts/workitem.go:54-65`, `agent/snapshot/contracts/logbundle.go:96-102`
- Test: `agent/snapshot/contracts/client_detail_test.go` (create)

- [ ] **Step 1: Write the failing test**

Create `agent/snapshot/contracts/client_detail_test.go`:

```go
package contracts_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

// Each of these is a mistake a first user makes with `fly agent snapshots
// create`, and each must come back with a message naming the actual problem.
func TestContractFailuresCarryClientDetail(t *testing.T) {
	tests := map[string]struct {
		typeRef string
		files   map[string]string
		want    string
	}{
		"missing declared document": {
			typeRef: "work-item/v1",
			files:   map[string]string{"task.md": "# not the declared document"},
			want:    `required regular file "work-item.json" is missing`,
		},
		"document fails its own rules": {
			typeRef: "work-item/v1",
			files: map[string]string{"work-item.json": `{
				"schema_version":"1.0.0","adapter":"","external_id":"x","revision":"1",
				"captured_at":"2026-08-01T12:00:00Z","title":"t","body":"b"}`},
			want: "adapter is required",
		},
		// The marked detail names the document that failed to decode, not the
		// decoder's own message — see the note under Step 3 on why the
		// dependency's text stays out of the disclosable channel.
		"unknown field": {
			typeRef: "work-item/v1",
			files: map[string]string{"work-item.json": `{
				"schema_version":"1.0.0","adapter":"a","external_id":"x","revision":"1",
				"captured_at":"2026-08-01T12:00:00Z","title":"t","body":"b","extra":1}`},
			want: "decode work-item.json",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			registry, err := contracts.NewRegistry()
			if err != nil {
				t.Fatal(err)
			}
			ref, err := snapshot.ParseTypeRef(test.typeRef)
			if err != nil {
				t.Fatal(err)
			}
			validator, err := registry.Lookup(ref)
			if err != nil {
				t.Fatal(err)
			}

			dir := t.TempDir()
			for name, contents := range test.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()

			validationContext, err := snapshot.NewValidationContext(nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			_, validationErr := validator.AdmitForSeal(context.Background(), root, validationContext)
			if validationErr == nil {
				t.Fatal("expected validation to fail")
			}
			detail, ok := snapshot.ClientDetail(validationErr)
			if !ok {
				t.Fatalf("no client detail on %v", validationErr)
			}
			if !strings.Contains(detail, test.want) {
				t.Fatalf("detail = %q, want it to contain %q", detail, test.want)
			}
			if strings.Contains(detail, dir) {
				t.Fatalf("detail leaked the host scratch path: %q", detail)
			}
		})
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./agent/snapshot/contracts/ -run ClientDetail -v`
Expected: FAIL with `no client detail on snapshot contracts: required regular file "work-item.json" is missing`.

- [ ] **Step 3: Mark the file-shape and decode messages**

In `agent/snapshot/contracts/json.go`, in `readRegularFile`, replace the three fully-constructed messages:

```go
	info, err := root.Lstat(name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, snapshot.WrapClientDetailf(err, "snapshot contracts: required regular file %q is missing", name)
		}
		return nil, fmt.Errorf("snapshot contracts: inspect %q: %w", name, err)
	}
	if !info.Mode().IsRegular() {
		return nil, snapshot.ClientDetailf("snapshot contracts: %q must be a regular file", name)
	}
	if info.Size() > limit {
		return nil, snapshot.ClientDetailf("snapshot contracts: %q exceeds size limit of %d bytes", name, limit)
	}
```

Leave `inspect %q: %w` and `open %q: %w` unmarked — they wrap an OS error whose text this repository does not control.

In `decodeStrictDocument`, mark the decode failures — their text is composed by `encoding/json` from the caller's own document:

```go
	if err := decoder.Decode(target); err != nil {
		return snapshot.WrapClientDetailf(err, "snapshot contracts: decode %s", name)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return snapshot.ClientDetailf("snapshot contracts: %s contains trailing JSON", name)
		}
		return snapshot.WrapClientDetailf(err, "snapshot contracts: decode trailing data in %s", name)
	}
```

**Do not restate the wrapped error's text in the format string.** `clientDetailError.Error()` composes `detail + ": " + err.Error()`, so the underlying message already reaches the log through the error chain; `ClientDetail` deliberately returns only the marked half. Writing `: %v` with `err` would push an arbitrary dependency's message into the *disclosable* channel, which is exactly what the safety rule forbids.

This means the decode detail a caller sees is `snapshot contracts: decode work-item.json` without the encoding/json specifics. If the test written in Step 1 asserts on a substring of the json decoder's own message (the "unknown field" case expects `extra`), that expectation is now wrong — the marked detail no longer contains it. Change that case's `want` to `decode work-item.json`, and if you want the decoder's own text disclosed, that is a separate decision requiring its own safety argument: raise it rather than smuggling it through `%v`.

`json.go` does not currently import the snapshot package — add
`"github.com/concourse/concourse/agent/snapshot"` to its import block. (There
is no cycle: `contracts` already imports `snapshot` in `registry.go` and
`workitem.go`.) `fmt` stays in use for the messages left unmarked.

- [ ] **Step 4: Mark the document and record-envelope rule failures**

These three differ from Step 3's decode sites: the wrapped error is a message
**this repository authored** from the caller's own values, and it *is* the
explanation the user needs (`adapter is required`). So its text must appear in
the disclosed detail — which means formatting it in with `%v`.

Use `ClientDetailf`, not `WrapClientDetailf`, at these three sites. Reason:
`clientDetailError.Error()` composes `detail + ": " + err.Error()`, so wrapping
an error whose text you have already formatted into the detail prints it twice
in the log. Reproducing the message in the detail loses nothing, because the
detail becomes a superset of the wrapped error's text.

**Before doing this, verify the assumption it rests on:** nothing may depend on
`errors.Is`/`errors.As` reaching through these three returns, because
`ClientDetailf` does not preserve a chain. Check with:

```bash
grep -rn "errors.Is\|errors.As" agent/snapshot/ atc/ --include="*.go" | grep -i "admitforseal\|workitem\|logbundle"
grep -rn "AdmitForSeal(" --include="*.go" agent/ atc/ | grep -v _test
```

If any caller matches a sentinel through these paths, report NEEDS_CONTEXT
instead of proceeding — do not silently drop a chain someone depends on.

In `agent/snapshot/contracts/workitem.go`, in `workItemValidator.Validate`:

```go
	if err := document.Validate(); err != nil {
		return snapshot.ValidationResult{}, snapshot.ClientDetailf("snapshot contracts: work-item.json: %v", err)
	}
```

In `agent/snapshot/contracts/logbundle.go`, in `logBundleValidator.Validate`:

```go
		if err := metadata.Validate(); err != nil {
			return snapshot.ValidationResult{}, snapshot.ClientDetailf("snapshot contracts: metadata.json: %v", err)
		}
```

and the bundle-shape rule in the same function:

```go
	if logFiles == 0 {
		return snapshot.ValidationResult{}, snapshot.ClientDetailf("snapshot contracts: log bundle must contain at least one log regular file")
	}
```

In `agent/snapshot/contracts/record.go`, in `admitRecordForSeal`:

```go
	if err := record.AdmitForSeal(expected, declarations); err != nil {
		return Record[T]{}, snapshot.ClientDetailf("snapshot contracts: record.json: %v", err)
	}
```

Every message reachable from `Record.AdmitForSeal` — envelope shape, subject rebinding, entity-id ordering, and each body's `Validate` — is composed from the record's own JSON values and fixed strings, which is exactly the safety rule.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./agent/snapshot/contracts/ ./agent/snapshot/ -count=1`
Expected: PASS. The new `TestContractFailuresCarryClientDetail` passes and no existing contract test regresses (marking preserves both the message text and the wrapped error).

- [ ] **Step 6: Commit**

```bash
git add agent/snapshot/contracts/ agent/snapshot/
git commit -m "feat(contracts): mark contract failures that describe the caller's own bytes

A missing work-item.json, a document that fails its own rules, an unknown
field, a malformed record envelope: each message is built from the submitted
content and fixed strings, so each is safe to hand back. Messages that wrap an
OS error stay unmarked."
```

## Task 4: Return the detail from the snapshot API

**Files:**
- Modify: `agent/api/snapshots/types.go:17-20`
- Modify: `agent/api/snapshots/handler.go:928-951` (`writeSnapshotError`), and `writeError`
- Test: `agent/api/snapshots/handler_test.go:497-529`

- [ ] **Step 1: Write the failing test**

In `agent/api/snapshots/handler_test.go`, extend the error-mapping table. Replace the `tests` slice and its loop body with:

```go
	tests := []struct {
		name   string
		err    error
		status int
		code   string
		detail string
	}{
		{name: "invalid archive", err: snapshot.ErrInvalidArchive, status: 400, code: "invalid_archive"},
		{name: "limit", err: snapshot.ErrLimitExceeded, status: 413, code: "limit_exceeded"},
		{name: "unsupported type", err: snapshot.ErrUnsupportedType, status: 400, code: "invalid_type"},
		{name: "semantic validation", err: snapshot.ErrValidation, status: 422, code: "validation_failed"},
		{name: "conflict", err: snapshot.ErrConflict, status: 409, code: "conflict"},
		{name: "unavailable", err: snapshot.ErrContentUnavailable, status: 503, code: "content_unavailable"},
		{name: "unexpected", err: errors.New("platform"), status: 500, code: "internal_error"},
		{
			name:   "validation with client detail",
			err:    errors.Join(snapshot.ErrValidation, snapshot.ClientDetailf("work-item.json: adapter is required")),
			status: 422, code: "validation_failed",
			detail: "work-item.json: adapter is required",
		},
		{
			name:   "archive with client detail",
			err:    errors.Join(snapshot.ErrInvalidArchive, snapshot.ClientDetailf(`archive path ".claude/" has a trailing separator`)),
			status: 400, code: "invalid_archive",
			detail: `archive path ".claude/" has a trailing separator`,
		},
		{
			// An internal fault must stay opaque even if some inner layer
			// marked something: the class decides disclosure, not the mark.
			name:   "internal fault ignores any mark",
			err:    errors.Join(errors.New("platform"), snapshot.ClientDetailf("storage node 7 is down")),
			status: 500, code: "internal_error",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newHandlerHarness(t)
			harness.creator.upload = func(context.Context, snapshot.UploadRequest) (snapshot.Snapshot, error) {
				return snapshot.Snapshot{}, errors.Join(test.err, errors.New("secret /tmp/storage-node"))
			}
			request := httptest.NewRequest(http.MethodPost, "/snapshots?type=opaque%2Fv1", strings.NewReader("tar"))
			request.Header.Set("Content-Type", "application/x-tar")
			recorder := httptest.NewRecorder()
			harness.factory.Create(harness.team).ServeHTTP(recorder, request)
			response := decodeError(t, recorder)
			if recorder.Code != test.status || response.Error != test.code {
				t.Fatalf("status/error = %d/%q, want %d/%q", recorder.Code, response.Error, test.status, test.code)
			}
			if response.Detail != test.detail {
				t.Fatalf("detail = %q, want %q", response.Detail, test.detail)
			}
			if strings.Contains(recorder.Body.String(), "secret") || strings.Contains(recorder.Body.String(), "/tmp") {
				t.Fatalf("response leaked dependency error: %s", recorder.Body.String())
			}
		})
	}
```

The unchanged no-leak assertion is the point: the joined `secret /tmp/storage-node` is unmarked, so it still never appears.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./agent/api/snapshots/ -run ErrorMapping -v`
(If the enclosing test has a different name, find it with `grep -n "invalid archive" agent/api/snapshots/handler_test.go` and run that test.)
Expected: FAIL to compile — `response.Detail undefined`.

- [ ] **Step 3: Add the field**

In `agent/api/snapshots/types.go`:

```go
type ErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
	// Detail is present only for failures caused by the caller's own request
	// and only when a validator explicitly marked the text as disclosable.
	// Internal faults never carry it.
	Detail string `json:"detail,omitempty"`
}
```

- [ ] **Step 4: Populate it for client-caused classes only**

In `agent/api/snapshots/handler.go`, add a detail-aware writer and use it for the three classes whose cause is the caller's own submission:

```go
func (factory *HandlerFactory) writeSnapshotError(w http.ResponseWriter, err error) {
	factory.logger.Error("snapshot-request-failed", err)
	var maxBytesError *http.MaxBytesError
	switch {
	case errors.As(err, &maxBytesError), errors.Is(err, snapshot.ErrLimitExceeded):
		writeError(w, http.StatusRequestEntityTooLarge, "limit_exceeded", "snapshot archive exceeds the configured limit")
	case errors.Is(err, snapshot.ErrInvalidArchive):
		writeDetailedError(w, http.StatusBadRequest, "invalid_archive", "snapshot archive is invalid", err)
	case errors.Is(err, snapshot.ErrUnsupportedType):
		writeError(w, http.StatusBadRequest, "invalid_type", "snapshot type is unsupported")
	case errors.Is(err, snapshot.ErrValidation):
		writeDetailedError(w, http.StatusUnprocessableEntity, "validation_failed", "snapshot does not satisfy its declared type", err)
	case errors.Is(err, snapshot.ErrConflict):
		writeError(w, http.StatusConflict, "conflict", "snapshot request conflicts with immutable state")
	case errors.Is(err, snapshot.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "snapshot was not found")
	case errors.Is(err, snapshot.ErrExpired):
		writeError(w, http.StatusConflict, "conflict", "snapshot content has expired")
	case errors.Is(err, snapshot.ErrContentUnavailable):
		writeError(w, http.StatusServiceUnavailable, "content_unavailable", "snapshot content is unavailable")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "snapshot service failed")
	}
}

// writeDetailedError adds the disclosable detail when the failing validator
// marked one. The error CLASS decides whether detail may travel at all, so a
// mark that somehow reaches an internal fault cannot escape through here.
//
// The detail is truncated because a mark carries caller-supplied values and
// nothing bounds them: an archive-path rejection can quote an entry name up
// to MaxSnapshotPathBytes, so an unbounded copy would let a caller choose the
// size of the error body they get back.
func writeDetailedError(w http.ResponseWriter, status int, code, message string, err error) {
	response := ErrorResponse{Error: code, Message: message}
	if detail, ok := snapshot.ClientDetail(err); ok {
		response.Detail = truncateDetail(detail)
	}
	writeJSON(w, status, response)
}

const maxErrorDetailBytes = 512

func truncateDetail(detail string) string {
	if len(detail) <= maxErrorDetailBytes {
		return detail
	}
	return detail[:maxErrorDetailBytes] + "…"
}
```

Add a test case covering the bound: a marked error whose detail exceeds
`maxErrorDetailBytes` must come back truncated, and the response must stay
valid JSON. Truncating on a byte boundary can split a multi-byte rune; decide
deliberately whether to trim back to a rune boundary (`utf8.DecodeLastRuneInString`)
and test whichever you choose — an invalid UTF-8 sequence in a JSON string is
replaced by `encoding/json`, so this is a quality question, not a safety one.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./agent/api/snapshots/ -count=1`
Expected: PASS, including the pre-existing no-leak assertions.

- [ ] **Step 6: Verify the whole path by hand against a live target**

```bash
go build -o /tmp/fly ./fly
mkdir -p /tmp/bad-work-item && printf '# not the declared document\n' > /tmp/bad-work-item/task.md
/tmp/fly -t home agent snapshots create --type work-item/v1 --from /tmp/bad-work-item
```

Expected (after the server side is deployed): the error line now names the
missing `work-item.json`.

**Correction (found during execution):** an earlier draft of this plan claimed
`fly` needed no change because `decodeOrError` prints the response body
verbatim. That is true of `decodeOrError` — and irrelevant, because the
snapshot commands never call it. `fly agent snapshots create` goes through
`decodeAgentSnapshotResponse` (`fly/commands/agent_snapshots.go:500`), which
decodes the error body into a local struct carrying only `error` and
`message`, so `encoding/json` silently drops `detail`. Surfacing it therefore
requires a CLI change too; without it Tasks 2-4 are invisible to the person
they exist for.

- [ ] **Step 7: Commit**

```bash
git add agent/api/snapshots/
git commit -m "feat(snapshots api): return disclosable validation detail to the caller

A first user could not get past any create mistake without kubectl access to
the web pod's log. Failures caused by the caller's own submission now carry the
marked contract message; internal faults stay opaque, and the existing
no-leak assertions are unchanged."
```

---

# Phase C — ergonomics: CLI, chart, docs

## Task 5: `show-run` tells you why the run failed

`fly agent nodes show-run` reports `status: failed` and nothing else; the reason is only in the build event stream, reachable via a `planned_build_id` the guide never mentions.

**Files:**
- Modify: `fly/commands/agent_workflow_runs.go:450-469`
- Test: `fly/commands/agent_workflow_runs_test.go` (create if absent)

- [ ] **Step 1: Write the failing test**

Create or extend `fly/commands/agent_workflow_runs_test.go`:

```go
package commands

import (
	"strings"
	"testing"

	"github.com/concourse/concourse/atc/event"
)

func TestFailureReasonPrefersTheLastErrorEvent(t *testing.T) {
	events := []event.Error{
		{Message: "an earlier, superseded failure"},
		{Message: `snapshot: validate output "review": required regular file "record.json" is missing`},
	}
	reason := failureReasonFromErrorEvents(events)
	if reason != `snapshot: validate output "review": required regular file "record.json" is missing` {
		t.Fatalf("reason = %q", reason)
	}
}

func TestFailureReasonIsEmptyWithoutErrorEvents(t *testing.T) {
	if reason := failureReasonFromErrorEvents(nil); reason != "" {
		t.Fatalf("reason = %q, want empty", reason)
	}
}

func TestFailureReasonIsTrimmedToOneReadableLine(t *testing.T) {
	reason := failureReasonFromErrorEvents([]event.Error{{Message: "  line one\nline two  \n"}})
	if strings.Contains(reason, "\n") || !strings.HasPrefix(reason, "line one") {
		t.Fatalf("reason = %q", reason)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./fly/commands/ -run FailureReason -v`
Expected: FAIL to compile — `undefined: failureReasonFromErrorEvents`.

- [ ] **Step 3: Implement the reason extraction and print it**

In `fly/commands/agent_workflow_runs.go`, add:

```go
// failureReasonFromErrorEvents picks the message a human needs out of a
// build's error events: the last one, on one line.
//
// The last error is the one that terminated the run; earlier errors are
// usually a retried or superseded step. Multi-line messages are collapsed
// because this prints inside a field list, and the full text remains
// available through `fly watch`.
func failureReasonFromErrorEvents(errorEvents []event.Error) string {
	for index := len(errorEvents) - 1; index >= 0; index-- {
		message := strings.TrimSpace(errorEvents[index].Message)
		if message == "" {
			continue
		}
		if newline := strings.IndexByte(message, '\n'); newline >= 0 {
			message = strings.TrimSpace(message[:newline])
		}
		return message
	}
	return ""
}

// runFailureReason reads the terminal error out of the run's planned build.
// Every failure here is non-fatal: this is a diagnostic nicety layered on top
// of a successful show-run, and it must never turn a readable answer into an
// error.
func runFailureReason(target rc.Target, run workflowrunsapi.RunSummary) string {
	if run.PlannedBuildID == nil {
		return ""
	}
	// Only the terminal-unsuccessful states have a reason worth fetching.
	// Naming them positively means a status added later does not silently
	// start pulling event streams for healthy runs.
	switch run.Status {
	case db.AgentWorkflowRunStatusFailed, db.AgentWorkflowRunStatusErrored, db.AgentWorkflowRunStatusAborted:
	default:
		return ""
	}
	source, err := target.Client().BuildEvents(fmt.Sprintf("%d", *run.PlannedBuildID))
	if err != nil {
		return ""
	}
	defer source.Close()
	var errorEvents []event.Error
	for {
		streamEvent, err := source.NextEvent()
		if err != nil {
			break
		}
		if errorEvent, ok := streamEvent.(event.Error); ok {
			errorEvents = append(errorEvents, errorEvent)
		}
	}
	return failureReasonFromErrorEvents(errorEvents)
}
```

Then change `printAgentWorkflowRunDetail` to take the target and print the reason:

```go
func printAgentWorkflowRunDetail(target rc.Target, detail workflowrunsapi.RunDetail, jsonOutput bool) error {
	if jsonOutput {
		return displayhelpers.JsonPrint(detail)
	}
	if err := printAgentWorkflowRun(detail.RunSummary); err != nil {
		return err
	}
	fmt.Printf("inputs: %d\noutputs: %d\n", len(detail.Inputs), len(detail.Outputs))
	if reason := runFailureReason(target, detail.RunSummary); reason != "" {
		fmt.Printf("failure: %s\n", reason)
		if detail.PlannedBuildID != nil {
			fmt.Printf("full log: fly -t %s watch -b %d\n", Fly.Target, *detail.PlannedBuildID)
		}
	}
	return nil
}
```

Add the imports this needs: `"strings"`, `"github.com/concourse/concourse/atc/event"`, `"github.com/concourse/concourse/atc/db"`, and `"github.com/concourse/concourse/fly/rc"` if not already present.

- [ ] **Step 4: Update both call sites**

In `fly/commands/agent_nodes.go:436`, change:

```go
	return printAgentWorkflowRunDetail(detail, command.Json)
```

to:

```go
	return printAgentWorkflowRunDetail(target, detail, command.Json)
```

Find the workflow-side call with `grep -n "printAgentWorkflowRunDetail(" fly/commands/*.go` and pass its already-loaded `target` the same way.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./fly/commands/ -count=1 && go build ./fly/...`
Expected: PASS and a clean build.

- [ ] **Step 6: Commit**

```bash
git add fly/commands/
git commit -m "feat(fly): show why an agent run failed, not just that it did

show-run reported a bare status while the reason lived in the build event
stream behind a planned_build_id the guide never mentions. Prints the
terminating error event plus the exact watch command for the full log."
```

## Task 6: `--json` on node import and release

Automation must read the version the server actually allocated rather than
predict it — today that means scraping a prose line.

**Corrected during execution.** An earlier draft of this plan said versions
come from "one team-global integer sequence". The sequence is in fact **per
node name**, shared across actors:
`atc/db/agent_nodes_factory.go:106-110` allocates
`COALESCE(MAX(version),0)+1 … WHERE definition_kind='node' AND name=$1` under a
per-name advisory lock. The concurrency hazard is real — two people iterating
on `code-review` — and there is a second failure mode that is not concurrency
at all: import is **content-hash idempotent** (`:79-84`), so re-importing
unchanged content returns the existing version without bumping. A script
predicting N+1 is wrong even single-threaded.

**Files:**
- Modify: `fly/commands/agent_nodes.go:164-214` (import), `:216-250` (release)

- [ ] **Step 1: Write the failing test**

Append to the existing `fly/commands/agent_nodes_test.go`, reusing the `nodeTarget` / `nodeResponse` harness already in that file:

```go
// A script must read the version the server allocated rather than predict it:
// the sequence is shared across the team and a concurrent import takes the
// number you expected. Scraping the prose line was previously the only way.
func TestAgentNodesImportPrintsTheAllocatedVersionAsJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, workflow.NodeFileName), []byte(agentNodeSource), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "prompts"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prompts", "review.md"), []byte("review it"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := nodeTarget(t, func(*http.Request) (*http.Response, error) {
		return nodeResponse(http.StatusOK, `{"name":"code-review","version":7,"content_hash":"abcdef012345"}`), nil
	})

	output := captureStdout(t, func() error { return importNodeDir(target, dir, true) })

	var decoded struct {
		Name    string `json:"name"`
		Version int    `json:"version"`
	}
	if err := json.Unmarshal([]byte(output), &decoded); err != nil {
		t.Fatalf("output is not JSON: %q", output)
	}
	if decoded.Name != "code-review" || decoded.Version != 7 {
		t.Fatalf("decoded = %+v", decoded)
	}
}

// captureStdout collects what fn prints. The commands print with fmt/JsonPrint
// rather than taking a writer, so the pipe is the seam available here.
func captureStdout(t *testing.T, fn func() error) string {
	t.Helper()
	original := os.Stdout
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = write
	runErr := fn()
	os.Stdout = original
	if err := write.Close(); err != nil {
		t.Fatal(err)
	}
	collected, err := io.ReadAll(read)
	if err != nil {
		t.Fatal(err)
	}
	if runErr != nil {
		t.Fatal(runErr)
	}
	return string(collected)
}
```

`encoding/json` must be in that file's import block; `io`, `os`, `net/http`, `path/filepath` and `workflow` already are.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./fly/commands/ -run AgentNodesImportPrintsTheAllocatedVersion -v`
Expected: FAIL to compile — `too many arguments in call to importNodeDir`.

- [ ] **Step 3: Add the flag to import**

In `fly/commands/agent_nodes.go`:

```go
type NodesImportCommand struct {
	Args struct {
		Path string `positional-arg-name:"PATH" required:"true" description:"Node source directory"`
	} `positional-args:"yes"`
	Json bool `long:"json" description:"Print the imported node record as JSON"`
}

func (command *NodesImportCommand) Execute([]string) error {
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}
	return importNodeDir(target, command.Args.Path, command.Json)
}
```

and at the end of `importNodeDir`, replace the `fmt.Printf` with:

```go
func importNodeDir(target rc.Target, dir string, jsonOutput bool) error {
	// ... unchanged body through decodeOrError ...
	if jsonOutput {
		return displayhelpers.JsonPrint(node)
	}
	_, err = fmt.Printf("imported %s version %d (hash %.12s)\n", node.Name, node.Version, node.ContentHash)
	return err
}
```

The signature change breaks existing callers. Fix them all:

```bash
grep -rn "importNodeDir(" fly/commands/
```

`fly/commands/agent_nodes_test.go` calls `importNodeDir(target, dir)` in at
least two existing tests — pass `false` there, since those tests assert the
request payload rather than the output form.

- [ ] **Step 4: Add the flag to release**

Give `NodesReleaseCommand` the same `Json bool` field, and print the decoded release record with `displayhelpers.JsonPrint` when set, keeping the existing prose line otherwise.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./fly/commands/ -count=1 && go build ./fly/...`
Expected: PASS.

- [ ] **Step 6: Verify by hand**

```bash
go build -o /tmp/fly ./fly
/tmp/fly -t home agent nodes import bench/nodes/code-review --json
```

Expected: a JSON object whose `version` is the allocated integer, usable as
`VERSION=$(... --json | python3 -c 'import sys,json;print(json.load(sys.stdin)["version"])')`.

- [ ] **Step 7: Commit**

```bash
git add fly/commands/
git commit -m "feat(fly): add --json to agent nodes import and release

Node versions come from one shared sequence, so a script must read the
allocated version instead of predicting it. Scraping the prose line was the
only way to do that."
```

## Task 7: `fly agent nodes cancel`

Workflow runs have a cancel route. Node runs are the same durable runs and have none, so killing one means fishing `planned_build_id` out of `show-run --json` and dropping to `fly abort-build`.

**Files:**
- Modify: `atc/routes.go:174-176, 386-388`
- Modify: `agent/api/noderuns/handler.go:30-46, 165-189`
- Modify: `atc/api/handler.go:433-435`
- Modify: `fly/commands/agent_nodes.go:30-40`
- Test: `agent/api/noderuns/handler_test.go`, `agent/api/noderuns/route_registration_test.go`

- [ ] **Step 1: Write the failing route test**

In `agent/api/noderuns/route_registration_test.go`, add the new route to whatever assertion enumerates registered routes, and in `agent/api/noderuns/handler_test.go` add:

```go
func TestCancelCancelsOnlyThisTeamsNodeRun(t *testing.T) {
	harness := newHandlerHarness(t)
	harness.runs.get = func(context.Context, int, workflow.DefinitionKind, snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
		return db.AgentWorkflowRun{
			ID: 7, TeamID: harness.team.ID, TeamName: harness.team.Name,
			DefinitionKind: workflow.DefinitionKindNode, WorkflowName: "code-review",
			Status: db.AgentWorkflowRunStatusRunning,
		}, true, nil
	}
	canceled := false
	harness.canceler.cancel = func(_ context.Context, teamID int, runID snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
		canceled = true
		if teamID != harness.team.ID {
			t.Fatalf("cancel used team %d", teamID)
		}
		return db.AgentWorkflowRun{ID: runID, Status: db.AgentWorkflowRunStatusCanceling}, true, nil
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/agent/nodes/code-review/runs/7/cancel", nil)
	recorder := httptest.NewRecorder()
	harness.serve(recorder, request)

	if recorder.Code != http.StatusAccepted || !canceled {
		t.Fatalf("status = %d canceled = %v body = %s", recorder.Code, canceled, recorder.Body.String())
	}
}

func TestCancelRefusesAWorkflowRunAddressedAsANode(t *testing.T) {
	harness := newHandlerHarness(t)
	harness.runs.get = func(context.Context, int, workflow.DefinitionKind, snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
		return db.AgentWorkflowRun{}, false, nil
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/agent/nodes/code-review/runs/7/cancel", nil)
	recorder := httptest.NewRecorder()
	harness.serve(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", recorder.Code)
	}
}
```

Extend the existing test harness in that file with a `canceler` fake implementing `workflowrunsapi.Canceler`, following the shape of the fakes already there.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./agent/api/noderuns/ -run Cancel -v`
Expected: FAIL — the route is unregistered, so the request 404s before reaching a canceler, and `harness.canceler` does not exist yet.

- [ ] **Step 3: Add the route**

In `atc/routes.go`, beside the other node-run route names:

```go
	CancelAgentNodeRun                         = "CancelAgentNodeRun"
```

and in the routes table beside the other node-run paths:

```go
	{Path: "/api/v1/agent/nodes/:node_name/runs/:workflow_run_id/cancel", Method: "POST", Name: CancelAgentNodeRun},
```

- [ ] **Step 4: Implement the handler**

In `agent/api/noderuns/handler.go`, add `Canceler workflowrunsapi.Canceler` to `Config` and a `canceler` field to `Handler`, require it in `NewHandler`'s dependency check alongside the others, and add:

```go
// Cancel terminates one node run. The kind check is the point: node and
// workflow runs share the durable run table and the ID space, and a node
// route must never reach a workflow run.
func (handler *Handler) Cancel(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) || !requireNoBody(w, r) {
		return
	}
	name, _, runID, ok := parseRoute(w, r, true, false)
	if !ok {
		return
	}
	run, found, err := handler.runs.GetKind(r.Context(), handler.team.ID, workflow.DefinitionKindNode, runID)
	if err != nil {
		writeInternalError(w)
		return
	}
	if !found || run.ID != runID || run.DefinitionKind != workflow.DefinitionKindNode ||
		run.TeamID != handler.team.ID || run.TeamName != handler.team.Name || run.WorkflowName != name {
		workflowrunsapi.WriteNotFound(w)
		return
	}
	canceled, found, err := handler.canceler.Cancel(r.Context(), handler.team.ID, runID)
	if errors.Is(err, workflowrunsapi.ErrCancelConflict) {
		workflowrunsapi.WriteError(w, http.StatusConflict, "conflict", "node run cannot be canceled in its current state")
		return
	}
	if err != nil {
		writeInternalError(w)
		return
	}
	if !found {
		workflowrunsapi.WriteNotFound(w)
		return
	}
	workflowrunsapi.WriteJSON(w, http.StatusAccepted, map[string]string{
		"workflow_run_id": runID.String(),
		"status":          string(canceled.Status),
	})
}
```

In `atc/api/handler.go`, register it beside the other node-run handlers:

```go
		atc.CancelAgentNodeRun:                         http.HandlerFunc(nodeRunHandlers.Cancel),
```

and pass the same canceler the workflow-run handlers already receive into `noderunsapi.Config`.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./agent/api/noderuns/ ./atc/api/ -count=1`
Expected: PASS.

- [ ] **Step 6: Add the fly command**

In `fly/commands/agent_nodes.go`, add to the `NodesCommand` struct:

```go
	Cancel    NodesCancelCommand    `command:"cancel" description:"Cancel one reusable node run"`
```

and:

```go
type NodesCancelCommand struct {
	Args struct {
		Name  string `positional-arg-name:"NAME" required:"true" description:"Node definition name"`
		RunID string `positional-arg-name:"RUN-ID" required:"true" description:"Durable node run ID"`
	} `positional-args:"yes"`
}

func (command *NodesCancelCommand) Execute([]string) error {
	runID, err := snapshot.ParseWorkflowRunID(command.Args.RunID)
	if err != nil {
		return fmt.Errorf("agent node run: %w", err)
	}
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}
	response, err := agentAPIRequest(target, http.MethodPost, nodeRunPath(command.Args.Name, runID)+"/cancel", nil)
	if err != nil {
		return err
	}
	var result struct {
		WorkflowRunID string `json:"workflow_run_id"`
		Status        string `json:"status"`
	}
	if err := decodeOrError(response, &result); err != nil {
		return err
	}
	_, err = fmt.Printf("node run %s: %s\n", result.WorkflowRunID, result.Status)
	return err
}
```

- [ ] **Step 7: Verify the CLI builds and the command is wired**

Run: `go build -o /tmp/fly ./fly && /tmp/fly agent nodes cancel --help`
Expected: usage text naming `NAME` and `RUN-ID`.

- [ ] **Step 8: Commit**

```bash
git add atc/routes.go atc/api/handler.go agent/api/noderuns/ fly/commands/
git commit -m "feat(agent): cancel a node run without dropping to abort-build

Workflow runs could be canceled through their own surface; node runs, which
are the same durable runs, could not. Adds the route, the kind-checked
handler, and fly agent nodes cancel."
```

## Task 8: Warn when hermetic egress is fail-closed

`networkPolicy.hermeticEgressTo` defaults to `[]`, which is correct and documented. But on a deployment where nobody set it, the first agent run hangs for five minutes and dies with `Request timed out`, and nothing anywhere says why.

**Files:**
- Create: `deploy/chart/templates/NOTES.txt`
- Test: manual `helm template` / `helm lint`

- [ ] **Step 1: Write the NOTES template**

Create `deploy/chart/templates/NOTES.txt`:

```
{{ .Chart.Name }} {{ .Chart.Version }} installed into namespace {{ .Release.Namespace }}.

{{- if and .Values.agentSnapshots.enabled (not .Values.networkPolicy.hermeticEgressTo) }}

WARNING: agentSnapshots is enabled and networkPolicy.hermeticEgressTo is empty.

  Hermetic task and agent pods run under a deny-all egress policy, so an
  agent: node cannot reach a model endpoint. Its first request will hang until
  the client times out and the run will fail with "Request timed out" — the
  cause is not reported anywhere else.

  If this deployment runs agent: nodes, set networkPolicy.hermeticEgressTo to
  complete NetworkPolicy egress rules for your model endpoint or egress proxy
  (and DNS, if the endpoint is resolved by name). See values.yaml for an
  example. A deployment that runs only deterministic task: functions can
  leave it empty.
{{- end }}
```

- [ ] **Step 2: Verify the warning renders when it should**

Run:

```bash
helm template deploy/chart --set agentSnapshots.enabled=true --set artifactDaemon.enabled=true --set artifactDaemon.tls.enabled=true --set artifactDaemon.hangar.bucket=b --set kubernetes.artifactHelperImage=registry.example/helper@sha256:1111111111111111111111111111111111111111111111111111111111111111 --notes 2>&1 | tail -20
```

Expected: the WARNING block appears.

- [ ] **Step 3: Verify it stays silent when egress is configured**

Run the same command with the additional flag:

```bash
--set networkPolicy.hermeticEgressTo[0].to[0].ipBlock.cidr=0.0.0.0/0
```

Expected: no WARNING block.

- [ ] **Step 4: Lint the chart**

Run: `helm lint deploy/chart`
Expected: PASS with only the pre-existing informational image-value and icon messages.

- [ ] **Step 5: Commit**

```bash
git add deploy/chart/templates/NOTES.txt
git commit -m "feat(chart): warn when hermetic egress is fail-closed but agents are enabled

Empty hermeticEgressTo is the correct default and the correct setting for a
task-only deployment, but on one that runs agent: nodes it produces a
five-minute hang and a bare 'Request timed out' with no other signal."
```

## Task 9: Fail fast when the runner's CLI cannot take the flags the runner passes

The runner passes `--max-budget-usd` whenever a step declares a positive budget slice. The deployed agent-runner image's CLI predates that flag, so the run died with `error: unknown option '--max-budget-usd'` after a full pod launch. Runner-image staleness is a recurring incident class in this deployment, and the same shape hides the already-fixed `initialize` handshake (F20) until the image is rebuilt.

**Files:**
- Modify: `agent/runner/runner.go` (near the arg assembly at `:485-501`)
- Test: `agent/runner/runner_test.go`

- [ ] **Step 1: Write the failing test**

Add to `agent/runner/runner_test.go`:

```go
func TestUnsupportedBudgetFlagIsReportedAsImageSkew(t *testing.T) {
	help := "Usage: claude [options]\n  --max-turns <n>\n  --model <m>\n"
	err := verifyCLISupportsFlags(help, []string{"--max-turns", "--max-budget-usd"})
	if err == nil {
		t.Fatal("missing flag was accepted")
	}
	if !strings.Contains(err.Error(), "--max-budget-usd") || !strings.Contains(err.Error(), "rebuild") {
		t.Fatalf("error does not name the flag and the remedy: %v", err)
	}
}

func TestSupportedFlagsPassVerification(t *testing.T) {
	help := "Usage: claude [options]\n  --max-turns <n>\n  --max-budget-usd <usd>\n"
	if err := verifyCLISupportsFlags(help, []string{"--max-turns", "--max-budget-usd"}); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./agent/runner/ -run BudgetFlag -v`
Expected: FAIL to compile — `undefined: verifyCLISupportsFlags`.

- [ ] **Step 3: Implement the check**

In `agent/runner/runner.go`:

```go
// verifyCLISupportsFlags reports a flag the runner intends to pass that the
// CLI in this image does not accept.
//
// The failure it prevents is image skew: agent-runner is built from this
// repository but the claude CLI inside it is pinned separately, so a runner
// change can start passing a flag the deployed image has never heard of. The
// raw failure is one line of CLI stderr after a full pod launch and a budget
// round trip, and it looks nothing like "your image is old".
func verifyCLISupportsFlags(help string, flags []string) error {
	for _, flag := range flags {
		if !strings.Contains(help, flag) {
			return fmt.Errorf(
				"the claude CLI in this agent-runner image does not support %s; "+
					"rebuild the agent-runner image from this commit (build-agent-runner-image) "+
					"or remove the setting that requires the flag", flag)
		}
	}
	return nil
}
```

Call it once, immediately before exec'ing claude, only for the flags that are conditional on step configuration:

```go
	if cfg.BudgetSliceUSD > 0 {
		helpOutput, helpErr := exec.CommandContext(ctx, claudePath, "--help").CombinedOutput()
		if helpErr == nil {
			if err := verifyCLISupportsFlags(string(helpOutput), []string{"--max-budget-usd"}); err != nil {
				return 2, err
			}
		}
	}
```

Place this beside the existing `args` assembly, using the same `claudePath`/`ctx` values that block already has. A `--help` failure is deliberately ignored: this check exists to improve a diagnosis, and must never be the thing that fails a healthy run.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./agent/runner/ -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/runner/
git commit -m "feat(runner): name image skew instead of surfacing a raw CLI flag error

agent-runner is built from this repository but pins the claude CLI
separately, so a runner change can pass a flag the deployed image lacks. That
cost a full pod launch and produced one line of CLI stderr that says nothing
about the image being stale."
```

## Task 10: Correct the guides

Every example a first user copies from the guides fails on a current deployment, and the platform's own sample node contradicted the contract it produces.

**Files:**
- Modify: `docs/operations/reusable-node-definitions.md:30-56, 88-93`
- Modify: `docs/agentic/README.md:290-300`

- [ ] **Step 1: Fix the node-authoring example (F17, F12, F9)**

In `docs/operations/reusable-node-definitions.md`, in the schema-1 source example, replace `model: claude-sonnet` with `model: sonnet` and add, immediately after the example:

```markdown
`model` is passed to the agent runtime verbatim and is frozen into the node
version, so an invalid value is permanent for that version and fails only once
a pod is running. Use a value the deployment's agent runtime accepts — the
runtime CLI's aliases (`sonnet`, `opus`, `haiku`) are the safe choice.
`claude-sonnet` is not a model identifier and returns a 404 from the API.

`budget_slice_usd` requires an agent runtime that supports a per-run budget
cap. Omit it when the deployment's agent-runner image predates that support;
with a non-zero deployment daily cap, every agent leaf needs a positive slice,
so the two settings must be rolled out together.

Parameters are supplied to the step as **environment variables**, not
interpolated into the prompt text. A prompt that writes `${MINIMUM_SEVERITY}`
gets that literal string; write "read the `MINIMUM_SEVERITY` environment
variable" instead.
```

- [ ] **Step 2: Document the per-type content contract (F1)**

In `docs/agentic/README.md`, immediately after the `fly agent snapshots create` example, add:

```markdown
### What a typed snapshot directory must contain

`--type` selects a server-authoritative validator, and the directory must
already satisfy it. The common types:

| Type | Required content |
|---|---|
| `opaque/v1` | any files; no structure required |
| `repository/v1` | a real git repository — the validator runs `git` against `HEAD`; an exported tree with no `.git` is rejected |
| `work-item/v1` | `work-item.json`: `schema_version` exactly `"1.0.0"`, plus non-empty `adapter`, `external_id`, `revision`, `captured_at` (RFC 3339), `title`, `body` |
| `log-bundle/v1` | at least one regular log file; an optional `metadata.json` with `schema_version`, `captured_at` (RFC 3339), `source` |
| `upgrade-request/v1` | `upgrade-request.json` |
| record types (`review/v1`, `diagnosis/v1`, `validation/v1`, `repository-change/v1`, `measurements/v1`) | `record.json` in the common envelope, plus optional `content/` |

`repository-change/v1` cannot be created from the CLI: sealing it verifies the
change against its base `repository/v1`, which must be a bound declared input.
Produce it from a workflow or node, not from `snapshots create`.

A validation failure returns the contract message in the response's `detail`
field, naming the exact file or rule that failed.
```

- [ ] **Step 3: Document what a node prompt should and should not say (F21, F27, F2)**

In `docs/operations/reusable-node-definitions.md`, add a new section after "Author a node package":

```markdown
## Writing the step prompt

The runtime prepends its own instructions to your prompt describing the
output mechanism that is active for this step — the managed output-builder
tools when the builder is enabled, or the resolved record-authority values
when it is not. **Do not hardcode either mechanism in the node prompt.** A
prompt that names environment variables the builder path does not set makes
the agent write empty values into its record, which fails sealing after the
step has spent its whole budget. Say "use the platform-provided output
mechanism described above" and spend the prompt on what a good result is.

Two rules earn their place in almost any agent node prompt:

- **Instruct an early provisional write, then refinement.** Record writing is
  idempotent — the last successful write wins — so an agent that writes its
  best current answer early and rewrites it later cannot lose everything to
  the turn cap. Without this, an exploring agent reliably spends its whole
  budget and produces nothing.
- **State the contract's own vocabulary.** Severities are `observation`,
  `low`, `medium`, `high`, `critical`; `high` and `critical` must be blocking;
  an `accept` conclusion cannot carry a blocking finding; entity lists must be
  sorted by `id`. A prompt that invents a different vocabulary produces
  records that fail validation.
```

- [ ] **Step 4: Verify the documented facts against the code**

Run:

```bash
grep -n "severity must be one of" agent/snapshot/contracts/review.go
grep -n "schema_version must be exactly" agent/snapshot/contracts/workitem.go
grep -n "log bundle must contain at least one" agent/snapshot/contracts/logbundle.go
```

Expected: each grep matches, and the documented vocabulary matches the code exactly.

- [ ] **Step 5: Commit**

```bash
git add docs/
git commit -m "docs(agentic): correct the examples a first user copies

Every example failed on a current deployment: claude-sonnet is not a model,
budget_slice_usd needs runtime support the deployed image lacks, and
parameters are environment variables rather than prompt interpolation. Adds
the per-type snapshot content contract, which previously existed only in Go
source, and the prompt rules the node dogfood established."
```

---

## Final verification

- [ ] **Run every touched suite**

```bash
go test ./deploy/ ./agent/snapshot/... ./agent/api/... ./agent/runner/ ./fly/commands/ -count=1
helm lint deploy/chart
```

Expected: all PASS; `helm lint` reports only the pre-existing informational messages.

- [ ] **Run the repository acceptance suite once**

```bash
make test-unit
```

Expected: PASS. PostgreSQL must be running (`pg_isready`).

- [ ] **Update the findings record**

In `FIRST-USER-FINDINGS.md`, mark F1, F2, F6, F7, F9, F11, F12, F16, F17, F19, F21, F22 and F27 as addressed, each with the commit that closed it, and leave the excluded findings listed above as open with their stated reason.

- [ ] **Deploy-gated items**

Two fixes are inert until images are rebuilt. Record both in the findings file rather than claiming them complete:

1. Task 1 needs `build-image` plus a web rollout. Verify with
   `kubectl --context theborg -n cicd exec deploy/concourse-web -c concourse-web -- git --version`.
2. F20's already-committed MCP `initialize` fix, and Task 9's preflight, need
   `build-agent-runner-image` plus a `CONCOURSE_AGENT_STEP_IMAGE` bump in
   home-infra. Verify by re-running a node and confirming the session-init
   event lists `output-builder` with `status: "connected"` rather than
   `"failed"`.
