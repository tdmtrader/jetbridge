package harvest

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

// GateOutcome is the per-gate result of RunGates (§6.3, v0.5 slice).
type GateOutcome struct {
	Gate            string  `json:"gate"`
	Scope           string  `json:"scope"`
	Status          string  `json:"status"` // ok | failed | error
	Attempt         int     `json:"attempt"`
	Flaky           bool    `json:"flaky,omitempty"`
	DurationSeconds float64 `json:"duration_seconds"`
	Detail          string  `json:"detail,omitempty"`
}

// gateCommands is the v0.5 FIXED command map — the interim executor
// until dev-mcp owns per-repo commands (full harvest-step workstream,
// wave 3). Every command runs with cwd=workspaceDir.
var gateCommands = map[string][]string{
	"build": {"go", "build", "./..."},
	"test":  {"go", "test", "./..."},
	"lint":  {"go", "vet", "./..."},
}

const defaultGateTimeout = 30 * time.Minute

// RunGates executes policy.Gates in order against workspaceDir using
// the v0.5 fixed command map. It stops at the first gate whose final
// status is not "ok" — a gate behind a blocked gate can never unblock
// the push, so there is nothing to gain by running it.
//
// v0.5 can only enforce scope "full": any other scope errors that gate
// (dev-mcp, wave 3, is the only executor able to resolve affected
// components) rather than silently running the full suite in its
// place. An unrecognized gate name is likewise a tooling fault, not a
// failure.
//
// Retries follow the §6.3 flake stance: 0-2 failed-only re-runs
// (errors are never retried); a gate that passes on a retry is
// recorded ok with Flaky:true and Attempt:N — flakiness is surfaced,
// never hidden.
//
// events (nil-tolerant: nil = no flight dir) receives live gate.start /
// gate.result events per attempt (§6.3: flakiness surfaced); emission
// failures never break gate control flow.
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

func runGate(gate Gate, workspaceDir, baseSHA string, events *schema.EventWriter) GateOutcome {
	if gate.Scope != "full" {
		return GateOutcome{
			Gate: gate.Gate, Scope: gate.Scope, Status: "error", Attempt: 1,
			Detail: fmt.Sprintf("scope %q is not enforceable by the v0.5 in-pod gate engine — dev-mcp remains the wave-3 executor for affected/affected_then_full scopes", gate.Scope),
		}
	}

	if gate.Gate == "elm-build" {
		return runElmBuildGate(gate, workspaceDir, baseSHA, events)
	}

	args, ok := gateCommands[gate.Gate]
	if !ok {
		return GateOutcome{
			Gate: gate.Gate, Scope: gate.Scope, Status: "error", Attempt: 1,
			Detail: fmt.Sprintf("unknown gate %q — the v0.5 command map is build|test|lint", gate.Gate),
		}
	}

	timeout := defaultGateTimeout
	if gate.Timeout != "" {
		d, err := time.ParseDuration(gate.Timeout)
		if err != nil {
			return GateOutcome{
				Gate: gate.Gate, Scope: gate.Scope, Status: "error", Attempt: 1,
				Detail: fmt.Sprintf("invalid timeout %q: %v", gate.Timeout, err),
			}
		}
		timeout = d
	}

	maxAttempts := gate.Retries + 1
	if maxAttempts < 1 {
		maxAttempts = 1
	}

	var last GateOutcome
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		emitEvent(events, schema.EventGateStart, schema.GateStartData{
			Gate: gate.Gate, Scope: gate.Scope,
		})
		start := time.Now()
		status, detail := execGate(args, workspaceDir, timeout)
		last = GateOutcome{
			Gate: gate.Gate, Scope: gate.Scope, Status: status,
			Attempt: attempt, DurationSeconds: time.Since(start).Seconds(), Detail: detail,
		}
		if status == "ok" && attempt > 1 {
			last.Flaky = true
		}
		// Every attempt gets a result event (§6.3: flakiness surfaced).
		emitEvent(events, schema.EventGateResult, schema.GateResultData{
			Gate: gate.Gate, Scope: gate.Scope, Status: status,
			DurationSeconds: last.DurationSeconds,
			Summary:         truncate(detail, 4096),
			Attempt:         attempt, Flaky: last.Flaky,
		})
		if status == "ok" {
			return last
		}
		if status == "error" {
			// Tooling faults are never retried (§6.3 flake stance).
			return last
		}
		// status == "failed": failed-only retries continue the loop.
	}
	return last
}

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

// emitEvent writes one event to a nil-tolerant writer; marshal or write
// failures are ignored — the recorder must never break control flow.
func emitEvent(events *schema.EventWriter, t schema.EventType, payload any) {
	if events == nil {
		return
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_ = events.Write(schema.Event{Type: t, Data: data})
}

// truncate caps s at n bytes.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// execGate runs one gate command to completion (or timeout), returning
// "ok" on a clean exit, "failed" on a normal non-zero exit (the gate
// ran and found something), and "error" for anything else (timeout,
// couldn't start, killed) — a tooling fault rather than a gate finding.
func execGate(args []string, workspaceDir string, timeout time.Duration) (status, detail string) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = workspaceDir
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	output := strings.TrimSpace(buf.String())

	if err == nil {
		return "ok", output
	}
	if ctx.Err() == context.DeadlineExceeded {
		return "error", fmt.Sprintf("gate timed out after %s", timeout)
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return "failed", output
	}
	return "error", err.Error()
}
