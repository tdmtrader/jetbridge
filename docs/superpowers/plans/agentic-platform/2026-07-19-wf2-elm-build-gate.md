# WF-2 Elm-Capable Agent Loop + elm-build Gate Implementation Plan

> **SUPERSEDED — historical implementation record (not executable).** The approved [Agentic Workflows as Functions over Snapshots design](../../specs/2026-07-21-agentic-workflows-as-functions-design.md) and [Agentic Functions program plan](../2026-07-21-agentic-functions-program.md) are authoritative. This Elm-capable agent loop + elm-build gate proposal targeted the ticket-centric dispatch flow; the elm-build gate concept remains relevant to the live workflow pipeline.

> For agentic workers: REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox syntax.

## Goal

Give the agent-runner image an Elm 0.19.1 toolchain and add an `elm-build` harvest gate that compiles `web/elm` and fails the push whenever `web/elm/**` changed without `web/public/elm.min.js` being regenerated in the same diff — killing the deployed-stale-bundle failure mode.

## Architecture

The harvest terminal step (`agent/harvest`) runs a fixed set of gates against the in-pod workspace checkout between the clean-tree check and the push-by-sha. Today the gate vocabulary is `build|test|lint` (fixed command map in `gates.go`, validated at workflow-import in `agent/workflow/parse.go`). This plan adds a fourth gate name, `elm-build`, whose executor is diff-aware: it reads the `base..HEAD` changed-path set (already computable in-pod from the workspace git repo via `agent/harvest/workspace.go:ChangedPaths`), only applies when `web/elm/**` is present, runs `elm make --optimize` to a throwaway output to prove the source compiles, and DIFF-CHECKs that `web/public/elm.min.js` was also changed. To run `elm make` in-pod, the runner image (`deploy/agent-runner/Dockerfile`) gains the Elm 0.19.1 binary and a pre-warmed `ELM_HOME` package cache on its existing `node:20-bookworm-slim` base.

## Tech Stack

- Go 1.25 (agent module) — harvest gate engine, workflow validator.
- Elm 0.19.1 compiler binary (`elm make --optimize`) — added to the agent-runner image.
- `node:20-bookworm-slim` — existing runner base image (already carries node/npm for `@anthropic-ai/claude-code`).
- Go stdlib `os/exec`, `context`, `path/filepath` for the in-pod elm invocation; the existing `agent/schema` flight-event writer for `gate.start`/`gate.result` events.

## File Structure

| File | Create/Modify | Responsibility |
|------|---------------|----------------|
| `agent/harvest/gates.go` | Modify | Thread `baseSHA` through `RunGates`/`runGate`; add `runElmBuildGate` + `elmCompile` + `elmCLI` + elm path constants. |
| `agent/harvest/runner.go` | Modify | Pass `facts.BaseSHA` into the `RunGates` call. |
| `agent/harvest/gates_test.go` | Modify | Update the 9 existing `RunGates(...)` call sites for the new `baseSHA` parameter. |
| `agent/harvest/gates_elm_test.go` | Create | Table + focused tests for the `elm-build` gate (not-applicable, stale-bundle fail, compile fail, missing-base error, happy path) using a real git fixture and a stubbed elm CLI. |
| `agent/workflow/parse.go` | Modify | Add `elm-build` to `validGates` and update the gate-name error message. |
| `agent/workflow/config.go` | Modify | Update the `Gate.Gate` field comment to include `elm-build`. |
| `agent/workflow/validate_test.go` | Modify | Add an accept-test proving `elm-build` imports clean. |
| `deploy/agent-runner/Dockerfile` | Modify | Install Elm 0.19.1 and pre-warm `ELM_HOME` on the runner image. |

---

## Task 1 — Thread `baseSHA` through the gate engine (refactor, existing coverage stays green)

The `elm-build` gate needs the pushed diff (`base..HEAD`) to decide applicability and to diff-check the bundle. The runner already resolves the merge-base into `facts.BaseSHA`; thread it into `RunGates`/`runGate` so the gate executor can read it. Non-elm gates ignore it, so this is a pure signature refactor guarded by the existing gate suite.

**Files**
- Modify: `agent/harvest/gates.go:57` (`RunGates`), `agent/harvest/gates.go:69` (`runGate`)
- Modify: `agent/harvest/runner.go:124` (call site)
- Modify (Test): `agent/harvest/gates_test.go` (9 call sites: lines 49, 84, 109, 122, 138, 151, 213, 240, 264)

**Steps**

- [ ] Update the 9 existing test call sites first (they document the new signature). In `agent/harvest/gates_test.go`, replace every occurrence of `harvest.RunGates(policy, workspace, nil)` with `harvest.RunGates(policy, workspace, "", nil)`. Exact replacement (applies to all 9 identical lines):

  ```go
  	outcomes, err := harvest.RunGates(policy, workspace, "", nil)
  ```

- [ ] Run the suite, expected FAIL (compile error — `RunGates` still takes 3 args):

  ```bash
  cd /Users/tdmtrader/concourse/concourse && go test ./agent/harvest/ -run TestRunGates 2>&1 | tail -5
  ```

  Expected: `too many arguments in call to harvest.RunGates` (build failure).

- [ ] Change the `RunGates` signature in `agent/harvest/gates.go`. Replace the current function head and its `runGate` call:

  ```go
  func RunGates(policy GatePolicy, workspaceDir, baseSHA string, events *schema.EventWriter) ([]GateOutcome, error) {
  	outcomes := make([]GateOutcome, 0, len(policy.Gates))
  	for _, gate := range policy.Gates {
  		outcome := runGate(gate, workspaceDir, baseSHA, events)
  		outcomes = append(outcomes, outcome)
  		if outcome.Status != "ok" {
  			break
  		}
  	}
  	return outcomes, nil
  }
  ```

- [ ] Change the `runGate` signature in `agent/harvest/gates.go` to accept `baseSHA` (the body is otherwise unchanged in this task — the `elm-build` branch lands in Task 4). Replace only the function head line:

  ```go
  func runGate(gate Gate, workspaceDir, baseSHA string, events *schema.EventWriter) GateOutcome {
  ```

- [ ] Update the runner call site. In `agent/harvest/runner.go:124`, replace:

  ```go
  		outcomes, gatesErr := RunGates(cfg.GatePolicy, workspaceDir, facts.BaseSHA, rec.eventWriter())
  ```

- [ ] Run the suite, expected PASS (all existing gate behavior preserved; `baseSHA` is unused by `build|test|lint`):

  ```bash
  cd /Users/tdmtrader/concourse/concourse && go test ./agent/harvest/ -run TestRunGates 2>&1 | tail -5
  ```

  Expected: `ok  	github.com/concourse/concourse/agent/harvest`.

- [ ] Commit:

  ```bash
  cd /Users/tdmtrader/concourse/concourse && git add agent/harvest/gates.go agent/harvest/runner.go agent/harvest/gates_test.go && git commit -m "refactor(harvest): thread baseSHA through RunGates for diff-aware gates"
  ```

---

## Task 2 — Accept `elm-build` in the workflow validator

Add `elm-build` to the import-time gate vocabulary so a workflow YAML declaring the gate parses clean (today `parse.go` rejects any gate outside `build|test|lint`).

**Files**
- Modify: `agent/workflow/parse.go:44` (`validGates`), `agent/workflow/parse.go:258` (error message)
- Modify: `agent/workflow/config.go:113` (comment)
- Modify (Test): `agent/workflow/validate_test.go`

**Steps**

- [ ] Write the failing accept-test. Add to `agent/workflow/validate_test.go` (after `TestValidateAcceptsMinimalDefinition`, ~line 98):

  ```go
  // TestValidateAcceptsElmBuildGate (WF-2): the elm-build gate name imports
  // clean — it is the stale-bundle guard added to the fixed gate vocabulary.
  func TestValidateAcceptsElmBuildGate(t *testing.T) {
  	yaml := mutate(t, "- gate: lint", "- gate: elm-build")
  	if _, err := workflow.Parse([]byte(yaml)); err != nil {
  		t.Fatalf("elm-build must be a valid gate name: %v", err)
  	}
  }
  ```

- [ ] Run it, expected FAIL:

  ```bash
  cd /Users/tdmtrader/concourse/concourse && go test ./agent/workflow/ -run TestValidateAcceptsElmBuildGate 2>&1 | tail -8
  ```

  Expected: a failure containing `gate must be build|test|lint, got "elm-build"`.

- [ ] Add `elm-build` to `validGates` in `agent/workflow/parse.go:44`. Replace:

  ```go
  var validGates = map[string]bool{"build": true, "test": true, "lint": true, "elm-build": true}
  ```

- [ ] Update the gate-name error message in `agent/workflow/parse.go:258`. Replace:

  ```go
  			return fmt.Errorf("workflow: gate_policy.gates[%d]: gate must be build|test|lint|elm-build, got %q", i, g.Gate)
  ```

- [ ] Update the `Gate.Gate` field comment in `agent/workflow/config.go:113`. Replace:

  ```go
  	Gate    string `yaml:"gate" json:"gate"`   // build | test | lint | elm-build
  ```

- [ ] Run the accept-test plus the existing rejection suite, expected PASS (the `"bad gate name"` case uses `vibes`, still invalid):

  ```bash
  cd /Users/tdmtrader/concourse/concourse && go test ./agent/workflow/ -run 'TestValidate' 2>&1 | tail -5
  ```

  Expected: `ok  	github.com/concourse/concourse/agent/workflow`.

- [ ] Commit:

  ```bash
  cd /Users/tdmtrader/concourse/concourse && git add agent/workflow/parse.go agent/workflow/config.go agent/workflow/validate_test.go && git commit -m "feat(workflow): accept elm-build in the gate vocabulary"
  ```

---

## Task 3 — `elm-build` gate: not-applicable when `web/elm` is untouched

First slice of the executor. When the pushed diff contains no `web/elm/**` path, the gate is a no-op PASS (it only guards Elm changes). This slice adds the git-fixture test helper and the applicability branch; the compile/diff-check land in Task 4.

**Files**
- Modify: `agent/harvest/gates.go` (add `runElmBuildGate` skeleton + path constants + `elm-build` dispatch in `runGate`; add `os`/`path/filepath` imports)
- Create (Test): `agent/harvest/gates_elm_test.go`

**Steps**

- [ ] Create the test file `agent/harvest/gates_elm_test.go` with the git-fixture helper and the not-applicable case:

  ```go
  package harvest_test

  import (
  	"os"
  	"os/exec"
  	"path/filepath"
  	"testing"

  	"github.com/concourse/concourse/agent/harvest"
  )

  // gitFixture builds a two-commit repo: baseFiles are committed first
  // (returned baseSHA points at that commit), then headFiles are written and
  // committed on top so ChangedPaths(dir, baseSHA) == the headFiles set.
  func gitFixture(t *testing.T, baseFiles, headFiles map[string]string) (dir, baseSHA string) {
  	t.Helper()
  	dir = t.TempDir()
  	run := func(args ...string) {
  		cmd := exec.Command("git", args...)
  		cmd.Dir = dir
  		cmd.Env = append(os.Environ(),
  			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_AUTHOR_DATE=2026-01-01T00:00:00Z",
  			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z")
  		if out, err := cmd.CombinedOutput(); err != nil {
  			t.Fatalf("git %v: %v: %s", args, err, out)
  		}
  	}
  	writeAll := func(files map[string]string) {
  		for name, content := range files {
  			full := filepath.Join(dir, name)
  			if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
  				t.Fatal(err)
  			}
  			if err := os.WriteFile(full, []byte(content), 0644); err != nil {
  				t.Fatal(err)
  			}
  		}
  	}
  	run("init", "-q")
  	writeAll(baseFiles)
  	run("add", "-A")
  	run("commit", "-q", "-m", "base")
  	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
  	if err != nil {
  		t.Fatal(err)
  	}
  	baseSHA = string(out[:len(out)-1]) // strip trailing newline
  	writeAll(headFiles)
  	run("add", "-A")
  	run("commit", "-q", "-m", "head")
  	return dir, baseSHA
  }

  func TestElmGateNotApplicableWhenElmUntouched(t *testing.T) {
  	dir, base := gitFixture(t,
  		map[string]string{"README.md": "hi\n"},
  		map[string]string{"README.md": "hi there\n"}, // no web/elm change
  	)
  	policy := harvest.GatePolicy{Gates: []harvest.Gate{{Gate: "elm-build", Scope: "full"}}}

  	outcomes, err := harvest.RunGates(policy, dir, base, nil)
  	if err != nil {
  		t.Fatalf("RunGates: %v", err)
  	}
  	if len(outcomes) != 1 || outcomes[0].Status != "ok" {
  		t.Fatalf("outcomes = %+v, want a single ok gate (elm untouched => not applicable)", outcomes)
  	}
  	if !contains(outcomes[0].Detail, "not applicable") {
  		t.Errorf("detail = %q, want it to say the gate was not applicable", outcomes[0].Detail)
  	}
  }
  ```

  (`contains` already exists in `gates_test.go`, same `harvest_test` package.)

- [ ] Run it, expected FAIL (unknown gate — `elm-build` not yet in the executor):

  ```bash
  cd /Users/tdmtrader/concourse/concourse && go test ./agent/harvest/ -run TestElmGateNotApplicable 2>&1 | tail -8
  ```

  Expected: failure — `outcomes[0].Status` is `error` (`unknown gate "elm-build"`) rather than `ok`.

- [ ] Add `os` and `path/filepath` to the `agent/harvest/gates.go` import block (it currently imports `bytes`, `context`, `encoding/json`, `errors`, `fmt`, `os/exec`, `strings`, `time`, `schema`). New import block:

  ```go
  import (
  	"bytes"
  	"context"
  	"encoding/json"
  	"errors"
  	"fmt"
  	"os"
  	"os/exec"
  	"path/filepath"
  	"strings"
  	"time"

  	schema "github.com/concourse/concourse/agent/schema"
  )
  ```

- [ ] Add the `elm-build` dispatch to `runGate`. In `agent/harvest/gates.go`, insert the branch immediately after the `if gate.Scope != "full"` block and before the `args, ok := gateCommands[gate.Gate]` lookup:

  ```go
  	if gate.Gate == "elm-build" {
  		return runElmBuildGate(gate, workspaceDir, baseSHA, events)
  	}
  ```

- [ ] Add the path constants and the `runElmBuildGate` function to `agent/harvest/gates.go` (append after `runGate`). This slice implements applicability + the missing-base guard; `elmCompile` lands in Task 4 but is referenced here, so include the whole function now:

  ```go
  // elm-build gate paths are fixed in v0.5, exactly like gateCommands: the
  // concourse web app builds src/Main.elm from web/elm into the committed
  // web/public/elm.min.js bundle (see package.json "build-elm"). dev-mcp
  // (wave 3) will resolve these per-repo; until then they are hard-wired.
  const (
  	elmSourcePrefix = "web/elm/"
  	elmBundlePath   = "web/public/elm.min.js"
  	elmSourceDir    = "web/elm"
  	elmMainModule   = "src/Main.elm"
  )

  // runElmBuildGate is the stale-bundle guard (WF-2). It applies ONLY when
  // web/elm/** is in the pushed diff (base..HEAD). When it applies it (1)
  // compiles web/elm with `elm make --optimize` to a throwaway output — a
  // source that no longer compiles is a real "failed" finding — and (2)
  // DIFF-CHECKS that web/public/elm.min.js was regenerated in the same diff.
  // A changed web/elm with an unchanged bundle FAILS: that is exactly the
  // deployed-stale-bundle failure mode this gate exists to kill.
  //
  // Retries are meaningless here (a stale bundle does not un-stale on a
  // re-run, a compile error is deterministic), so the gate always reports
  // Attempt:1 and never sets Flaky.
  func runElmBuildGate(gate Gate, workspaceDir, baseSHA string, events *schema.EventWriter) GateOutcome {
  	emitEvent(events, schema.EventGateStart, schema.GateStartData{Gate: gate.Gate, Scope: gate.Scope})
  	start := time.Now()

  	finish := func(status, detail string) GateOutcome {
  		o := GateOutcome{
  			Gate: gate.Gate, Scope: gate.Scope, Status: status, Attempt: 1,
  			DurationSeconds: time.Since(start).Seconds(), Detail: detail,
  		}
  		emitEvent(events, schema.EventGateResult, schema.GateResultData{
  			Gate: gate.Gate, Scope: gate.Scope, Status: status,
  			DurationSeconds: o.DurationSeconds, Summary: truncate(detail, 4096), Attempt: 1,
  		})
  		return o
  	}

  	// The diff-check is the load-bearing half; without a merge-base we cannot
  	// prove the invariant, so we fail closed rather than wave the push through.
  	if baseSHA == "" {
  		return finish("error", "elm-build gate: no merge-base resolved for the workspace — cannot diff-check the elm bundle (fail-closed; ensure origin/<target_branch> is fetched)")
  	}
  	changed, err := ChangedPaths(workspaceDir, baseSHA)
  	if err != nil {
  		return finish("error", "elm-build gate: "+err.Error())
  	}
  	elmChanged, bundleChanged := false, false
  	for _, p := range changed {
  		if strings.HasPrefix(p, elmSourcePrefix) {
  			elmChanged = true
  		}
  		if p == elmBundlePath {
  			bundleChanged = true
  		}
  	}
  	if !elmChanged {
  		return finish("ok", "elm-build gate: no web/elm/** changes in the diff — not applicable")
  	}

  	// web/elm changed: the source must still compile...
  	timeout := defaultGateTimeout
  	if gate.Timeout != "" {
  		d, perr := time.ParseDuration(gate.Timeout)
  		if perr != nil {
  			return finish("error", fmt.Sprintf("elm-build gate: invalid timeout %q: %v", gate.Timeout, perr))
  		}
  		timeout = d
  	}
  	if status, detail := elmCompile(workspaceDir, timeout); status != "ok" {
  		return finish(status, detail)
  	}

  	// ...and the committed bundle must have been regenerated in the same diff.
  	if !bundleChanged {
  		return finish("failed", "elm-build gate: web/elm/** changed but "+elmBundlePath+" was not regenerated in this diff — run `npm run build-elm` and commit the rebuilt bundle (stale-bundle guard)")
  	}
  	return finish("ok", "elm-build gate: web/elm compiled and "+elmBundlePath+" was regenerated")
  }

  // elmCompile runs `elm make --optimize` on the workspace's web/elm to a
  // throwaway output (never web/public, so the post-gate workspace stays
  // byte-identical to the pushed HEAD — same discipline as the judge). It
  // returns "ok" on a clean compile, "failed" on a normal non-zero exit (a
  // real compile error), and "error" for a toolchain fault (missing binary,
  // timeout).
  func elmCompile(workspaceDir string, timeout time.Duration) (status, detail string) {
  	outDir, err := os.MkdirTemp("", "elm-gate")
  	if err != nil {
  		return "error", "elm-build gate: mkdtemp: " + err.Error()
  	}
  	defer os.RemoveAll(outDir)

  	ctx, cancel := context.WithTimeout(context.Background(), timeout)
  	defer cancel()
  	cmd := exec.CommandContext(ctx, elmCLI(), "make", "--optimize",
  		"--output", filepath.Join(outDir, "elm.js"), elmMainModule)
  	cmd.Dir = filepath.Join(workspaceDir, elmSourceDir)
  	var buf bytes.Buffer
  	cmd.Stdout = &buf
  	cmd.Stderr = &buf
  	runErr := cmd.Run()
  	out := strings.TrimSpace(buf.String())
  	if runErr == nil {
  		return "ok", out
  	}
  	if ctx.Err() == context.DeadlineExceeded {
  		return "error", fmt.Sprintf("elm-build gate: elm make timed out after %s", timeout)
  	}
  	var exitErr *exec.ExitError
  	if errors.As(runErr, &exitErr) {
  		return "failed", "elm-build gate: elm make --optimize failed:\n" + out
  	}
  	return "error", "elm-build gate: could not run elm: " + runErr.Error()
  }

  // elmCLI is the elm binary, overridable via HARVEST_ELM_CLI (test seam,
  // mirrors HARVEST_JUDGE_CLI); "" defaults to "elm" on PATH.
  func elmCLI() string {
  	if c := os.Getenv("HARVEST_ELM_CLI"); c != "" {
  		return c
  	}
  	return "elm"
  }
  ```

- [ ] Run the not-applicable test, expected PASS (the gate returns early on `!elmChanged`, so `elmCompile`/elm is never invoked):

  ```bash
  cd /Users/tdmtrader/concourse/concourse && go test ./agent/harvest/ -run TestElmGateNotApplicable 2>&1 | tail -5
  ```

  Expected: `ok  	github.com/concourse/concourse/agent/harvest`.

- [ ] Commit:

  ```bash
  cd /Users/tdmtrader/concourse/concourse && git add agent/harvest/gates.go agent/harvest/gates_elm_test.go && git commit -m "feat(harvest): elm-build gate applicability + executor scaffold"
  ```

---

## Task 4 — `elm-build` gate: stale-bundle fail, compile fail, missing-base error, happy path

Cover the load-bearing behaviors with a stubbed elm CLI (so the suite runs without Elm installed — the real binary is exercised only in the image smoke test, Task 6). The stub is a tiny shell script whose exit code the test controls.

**Files**
- Modify (Test): `agent/harvest/gates_elm_test.go`

**Steps**

- [ ] Add the stub-elm helper and the four behavior tests to `agent/harvest/gates_elm_test.go`:

  ```go
  // stubElm writes an executable that exits with the given code, and points
  // HARVEST_ELM_CLI at it so runElmBuildGate's compile step is deterministic
  // without a real Elm toolchain (mirrors the judge's HARVEST_JUDGE_CLI seam).
  func stubElm(t *testing.T, exitCode int) {
  	t.Helper()
  	dir := t.TempDir()
  	path := filepath.Join(dir, "elm-stub")
  	script := "#!/bin/sh\necho 'stub elm'\nexit " + itoa(exitCode) + "\n"
  	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
  		t.Fatal(err)
  	}
  	t.Setenv("HARVEST_ELM_CLI", path)
  }

  func itoa(n int) string {
  	if n == 0 {
  		return "0"
  	}
  	return string(rune('0' + n)) // single-digit exit codes only
  }

  // elmChange is the minimal web/elm edit that trips the gate's applicability.
  func elmChange() (base, head map[string]string, withBundle map[string]string) {
  	base = map[string]string{
  		"web/elm/src/Main.elm":     "module Main exposing (..)\nx = 1\n",
  		"web/public/elm.min.js":    "old\n",
  	}
  	head = map[string]string{
  		"web/elm/src/Main.elm": "module Main exposing (..)\nx = 2\n", // elm changed, bundle NOT
  	}
  	withBundle = map[string]string{
  		"web/elm/src/Main.elm":  "module Main exposing (..)\nx = 2\n",
  		"web/public/elm.min.js": "new\n", // elm AND bundle changed
  	}
  	return
  }

  func TestElmGateFailsWhenBundleNotRegenerated(t *testing.T) {
  	stubElm(t, 0) // compiles fine
  	base, head, _ := elmChange()
  	dir, baseSHA := gitFixture(t, base, head)
  	policy := harvest.GatePolicy{Gates: []harvest.Gate{{Gate: "elm-build", Scope: "full"}}}

  	outcomes, err := harvest.RunGates(policy, dir, baseSHA, nil)
  	if err != nil {
  		t.Fatalf("RunGates: %v", err)
  	}
  	if len(outcomes) != 1 || outcomes[0].Status != "failed" {
  		t.Fatalf("outcomes = %+v, want failed (elm changed, bundle stale)", outcomes)
  	}
  	if !contains(outcomes[0].Detail, "stale-bundle guard") {
  		t.Errorf("detail = %q, want it to name the stale-bundle guard", outcomes[0].Detail)
  	}
  }

  func TestElmGatePassesWhenBundleRegenerated(t *testing.T) {
  	stubElm(t, 0)
  	base, _, withBundle := elmChange()
  	dir, baseSHA := gitFixture(t, base, withBundle)
  	policy := harvest.GatePolicy{Gates: []harvest.Gate{{Gate: "elm-build", Scope: "full"}}}

  	outcomes, err := harvest.RunGates(policy, dir, baseSHA, nil)
  	if err != nil {
  		t.Fatalf("RunGates: %v", err)
  	}
  	if len(outcomes) != 1 || outcomes[0].Status != "ok" {
  		t.Fatalf("outcomes = %+v, want ok (elm changed AND bundle regenerated)", outcomes)
  	}
  }

  func TestElmGateFailsWhenSourceDoesNotCompile(t *testing.T) {
  	stubElm(t, 1) // compile error
  	base, _, withBundle := elmChange()
  	dir, baseSHA := gitFixture(t, base, withBundle) // bundle present, but elm make fails
  	policy := harvest.GatePolicy{Gates: []harvest.Gate{{Gate: "elm-build", Scope: "full"}}}

  	outcomes, err := harvest.RunGates(policy, dir, baseSHA, nil)
  	if err != nil {
  		t.Fatalf("RunGates: %v", err)
  	}
  	if len(outcomes) != 1 || outcomes[0].Status != "failed" {
  		t.Fatalf("outcomes = %+v, want failed (compile error)", outcomes)
  	}
  	if !contains(outcomes[0].Detail, "elm make --optimize failed") {
  		t.Errorf("detail = %q, want it to name the compile failure", outcomes[0].Detail)
  	}
  }

  func TestElmGateErrorsWithoutMergeBase(t *testing.T) {
  	stubElm(t, 0)
  	base, _, withBundle := elmChange()
  	dir, _ := gitFixture(t, base, withBundle)
  	policy := harvest.GatePolicy{Gates: []harvest.Gate{{Gate: "elm-build", Scope: "full"}}}

  	outcomes, err := harvest.RunGates(policy, dir, "", nil) // empty base => fail-closed
  	if err != nil {
  		t.Fatalf("RunGates: %v", err)
  	}
  	if len(outcomes) != 1 || outcomes[0].Status != "error" {
  		t.Fatalf("outcomes = %+v, want error (no merge-base, fail-closed)", outcomes)
  	}
  	if !contains(outcomes[0].Detail, "fail-closed") {
  		t.Errorf("detail = %q, want it to say fail-closed", outcomes[0].Detail)
  	}
  }

  func TestElmGateErrorsWhenElmBinaryMissing(t *testing.T) {
  	t.Setenv("HARVEST_ELM_CLI", "/nonexistent/elm-binary")
  	base, _, withBundle := elmChange()
  	dir, baseSHA := gitFixture(t, base, withBundle)
  	policy := harvest.GatePolicy{Gates: []harvest.Gate{{Gate: "elm-build", Scope: "full"}}}

  	outcomes, err := harvest.RunGates(policy, dir, baseSHA, nil)
  	if err != nil {
  		t.Fatalf("RunGates: %v", err)
  	}
  	if len(outcomes) != 1 || outcomes[0].Status != "error" {
  		t.Fatalf("outcomes = %+v, want error (elm toolchain missing is a tooling fault, not a failure)", outcomes)
  	}
  	if !contains(outcomes[0].Detail, "could not run elm") {
  		t.Errorf("detail = %q, want it to name the missing toolchain", outcomes[0].Detail)
  	}
  }

  // TestElmGateRequiresFullScope pins that elm-build obeys the same scope
  // guard as every other v0.5 gate: only "full" is enforceable in-pod.
  func TestElmGateRequiresFullScope(t *testing.T) {
  	base, _, withBundle := elmChange()
  	dir, baseSHA := gitFixture(t, base, withBundle)
  	policy := harvest.GatePolicy{Gates: []harvest.Gate{{Gate: "elm-build", Scope: "affected"}}}

  	outcomes, err := harvest.RunGates(policy, dir, baseSHA, nil)
  	if err != nil {
  		t.Fatalf("RunGates: %v", err)
  	}
  	if len(outcomes) != 1 || outcomes[0].Status != "error" {
  		t.Fatalf("outcomes = %+v, want error (non-full scope is unenforceable)", outcomes)
  	}
  }
  ```

- [ ] Run the full elm-gate suite, expected PASS:

  ```bash
  cd /Users/tdmtrader/concourse/concourse && go test ./agent/harvest/ -run TestElmGate 2>&1 | tail -5
  ```

  Expected: `ok  	github.com/concourse/concourse/agent/harvest`.

- [ ] Run the whole harvest package to confirm no regressions in the existing gate/runner/judge suites:

  ```bash
  cd /Users/tdmtrader/concourse/concourse && go test ./agent/harvest/ 2>&1 | tail -5
  ```

  Expected: `ok  	github.com/concourse/concourse/agent/harvest`.

- [ ] Commit:

  ```bash
  cd /Users/tdmtrader/concourse/concourse && git add agent/harvest/gates_elm_test.go && git commit -m "test(harvest): elm-build gate stale-bundle/compile/fail-closed coverage"
  ```

---

## Task 5 — Add the Elm 0.19.1 toolchain to the agent-runner image

Install the Elm compiler on the runner image and pre-warm its package cache so in-pod `elm make` works offline. The final stage is `node:20-bookworm-slim`, so node/npm are already present; only the `elm` binary and a seeded `ELM_HOME` are new.

**Files**
- Modify: `deploy/agent-runner/Dockerfile`

**Steps**

- [ ] Read the current file to confirm the exact lines being extended:

  ```bash
  cd /Users/tdmtrader/concourse/concourse && cat deploy/agent-runner/Dockerfile
  ```

  Expected head: `FROM golang:1.25-bookworm AS build` … `FROM node:20-bookworm-slim` … `RUN npm install -g @anthropic-ai/claude-code@2.0.1 …`.

- [ ] Add an Elm install layer and an `ELM_HOME` pre-warm to `deploy/agent-runner/Dockerfile`. Insert the following AFTER the existing `RUN npm install -g @anthropic-ai/claude-code@2.0.1 …` block and BEFORE the `COPY --from=build /out/agent-runner …` line. (Placing elm on its own layer keeps the claude-code layer cache stable across elm bumps.)

  ```dockerfile
  # Elm 0.19.1 toolchain for the WF-2 elm-build harvest gate (`elm make
  # --optimize` on web/elm). The gate only shells out to `elm`; the committed
  # bundle is diff-checked, so uglify-js is intentionally NOT installed here.
  # elm ships as a single gzipped linux-x64 binary (~6 MiB gz, ~30 MiB on disk).
  ENV ELM_HOME=/usr/local/share/elm
  RUN curl -fsSL -o /tmp/elm.gz \
        https://github.com/elm/compiler/releases/download/0.19.1/binary-for-linux-64-bit.gz \
      && gunzip /tmp/elm.gz \
      && chmod +x /tmp/elm \
      && mv /tmp/elm /usr/local/bin/elm \
      && elm --version

  # Pre-warm ELM_HOME so in-pod `elm make` needs no network: build web/elm once
  # at image-build time (this also fails the build loudly if elm can't resolve
  # the repo's dependencies). Only web/elm is copied in — the source tree is not
  # otherwise part of the runtime image.
  COPY web/elm /tmp/elm-prewarm/web/elm
  RUN cd /tmp/elm-prewarm/web/elm \
      && elm make --optimize --output /dev/null src/Main.elm \
      && rm -rf /tmp/elm-prewarm
  ```

- [ ] Sanity-check the Dockerfile parses (no local docker on this machine per project notes; run this where a Docker daemon is available — theborg or CI). Build and smoke-test:

  ```bash
  # On a host with Docker (e.g. theborg): from the repo root
  docker build -f deploy/agent-runner/Dockerfile -t agent-runner:wf2-smoke .
  docker run --rm --network none agent-runner:wf2-smoke elm --version
  ```

  Expected: the build completes (the pre-warm `elm make` succeeds) and the offline `elm --version` prints `0.19.1` — proving the binary and the seeded `ELM_HOME` both work without network.

- [ ] Commit:

  ```bash
  cd /Users/tdmtrader/concourse/concourse && git add deploy/agent-runner/Dockerfile && git commit -m "build(agent-runner): add Elm 0.19.1 toolchain + pre-warmed ELM_HOME"
  ```

---

## Task 6 — Post-merge: rebuild the runner image and bump home-infra

This is an operational task, not a code change — it follows the A0-1-style rebuild pattern the runner image already uses (the claude-code pin comment in the Dockerfile references checking a recent agent-review build log). The new gate does nothing until a runner image containing `elm` is deployed.

**Files**
- None in this repo (external: the runner image registry and the home-infra image reference).

**Steps**

- [ ] After Tasks 1-5 are merged to `jetbridge`, rebuild and push the agent-runner image on a Docker host (theborg), tagging per the existing runner-image scheme used by the platform (confirm the current tag from a recent agent-review build before pushing):

  ```bash
  # On theborg, from a checkout at the merged commit:
  docker build -f deploy/agent-runner/Dockerfile -t <registry>/agent-runner:<new-tag> .
  docker push <registry>/agent-runner:<new-tag>
  ```

- [ ] Bump the agent-runner image reference in home-infra to `<new-tag>` and let ArgoCD roll it out (same flow as prior runner-image bumps: image-bump commit → Argo sync).

- [ ] Verify a dispatched ticket whose workflow declares an `elm-build` gate and touches `web/elm` now runs the gate: check the harvest step's `results.json` metadata `gates` array contains an `elm-build` entry. A dispatch that changes `web/elm` without the bundle must land in `needs_review` with the stale-bundle detail; a matching bundle change must pass.

- [ ] No commit in this repo. Record the rollout in the release/dogfood notes.

---

## Self-Review

**Spec coverage**
- Elm toolchain added to `deploy/agent-runner/Dockerfile` (node20 base already present; elm 0.19.1 binary + pre-warmed `ELM_HOME`) — Task 5. ✅
- `elm-build` added to the harvest gate vocabulary: executor in `agent/harvest/gates.go` (Tasks 3-4) and import-time validator in `agent/workflow/parse.go` (Task 2). ✅
- Gate builds `web/elm` with `elm make --optimize` (`elmCompile`) — Task 3. ✅
- Gate DIFF-CHECKs `web/public/elm.min.js` regeneration; changed `web/elm` + unchanged bundle FAILS (`TestElmGateFailsWhenBundleNotRegenerated`) — Task 4. ✅
- Gate applies ONLY when `web/elm/**` is in the diff (`TestElmGateNotApplicableWhenElmUntouched`) — Task 3. ✅
- Diff computed in-pod from the workspace git repo via `ChangedPaths(base..HEAD)` (NOT the web-node `gitcheck` mirror, which is the outcome watcher's system) — grounded, base threaded from `runner.go:facts.BaseSHA`. ✅
- Image-size increase (~30 MiB uncompressed elm binary + seeded package cache; no uglify-js) and post-merge rebuild/home-infra bump — noted in Task 5 comment and Task 6. ✅

**Placeholder scan** — no TODO/TBD/"similar to Task N"/"add error handling" left; every code step shows real code; commands include expected output.

**Type consistency** — `RunGates(policy GatePolicy, workspaceDir, baseSHA string, events *schema.EventWriter)` updated at its one non-test caller (`runner.go`) and all 9 test call sites; `GateOutcome`/`GateStartData`/`GateResultData` fields match `agent/harvest/gates.go` and `agent/schema/event_payloads.go` (`Gate`,`Scope`,`Status`,`DurationSeconds`,`Summary`,`Attempt`); `ChangedPaths`, `defaultGateTimeout`, `emitEvent`, `truncate` are all existing symbols in the `harvest` package. `elmCLI`/`HARVEST_ELM_CLI` mirror the existing `HARVEST_JUDGE_CLI` seam. No migration and no new API route are introduced (WF-2 touches only the Dockerfile, the gate engine, and the gate validator), so the six-touchpoint route pattern and the migration-slot coordination do not apply.

**No contended edits** — `agent/dispatch/render.go`'s refusal switch / `RenderAgentStep` literal is NOT touched. No migration number is claimed.

## Open Decisions

1. **Diff-presence check vs byte-for-byte bundle reproduction.** The gate proves the bundle is *present in the diff*, not that it was rebuilt from the exact committed source. A stronger check would rebuild `elm.min.js` (elm make + uglify-js, matching `package.json` "build-elm") and byte-compare against the committed file, catching a bundle regenerated from an *earlier* elm state. **Recommendation:** ship the diff-presence check (matches the WF-2 brief's "was regenerated" language, keeps the image small — no uglify-js — and avoids uglify's cross-version non-determinism). Revisit byte-compare only if partial-staleness bites in practice. Owner: platform/web.

2. **Fail-closed when no merge-base resolves.** `elmCompile` can run without a base, but the diff-check (the load-bearing half) cannot, so the gate currently `error`s (blocks the push) when `baseSHA == ""`. The alternative is to skip the diff-check and pass on compile alone. **Recommendation:** keep fail-closed — a silent pass reintroduces exactly the stale-bundle failure mode the gate exists to kill; in practice the harvest runner resolves the base for any repo with `origin/<target_branch>` fetched. Owner: platform.

3. **Repo-specific paths hard-wired in the gate.** `web/elm/`, `web/public/elm.min.js`, and `src/Main.elm` are hard-coded, consistent with the existing v0.5 fixed command map (`go build ./...`), and are correct only for the concourse repo. **Recommendation:** accept the hard-wiring for v0.5 and generalize via dev-mcp per-repo build metadata in wave 3 (same deferral the `build|test|lint` command map already carries). Owner: platform (dev-mcp workstream).

4. **`ELM_HOME` pre-warm requires network at image-build time.** The Docker `RUN elm make` layer fetches the Elm package registry during the build. **Recommendation:** keep it — it makes in-pod gate runs offline-capable and turns a dependency-resolution failure into a loud image-build failure rather than a silent in-pod gate error. If the build host has no egress to `package.elm-lang.org`, drop the pre-warm layer and instead ensure the harvest pod has egress (a strictly worse posture). Owner: infra.
