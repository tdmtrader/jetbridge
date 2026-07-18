package harvest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
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
func RunGates(policy GatePolicy, workspaceDir string) ([]GateOutcome, error) {
	outcomes := make([]GateOutcome, 0, len(policy.Gates))
	for _, gate := range policy.Gates {
		outcome := runGate(gate, workspaceDir)
		outcomes = append(outcomes, outcome)
		if outcome.Status != "ok" {
			break
		}
	}
	return outcomes, nil
}

func runGate(gate Gate, workspaceDir string) GateOutcome {
	if gate.Scope != "full" {
		return GateOutcome{
			Gate: gate.Gate, Scope: gate.Scope, Status: "error", Attempt: 1,
			Detail: fmt.Sprintf("scope %q is not enforceable by the v0.5 in-pod gate engine — dev-mcp remains the wave-3 executor for affected/affected_then_full scopes", gate.Scope),
		}
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
		start := time.Now()
		status, detail := execGate(args, workspaceDir, timeout)
		last = GateOutcome{
			Gate: gate.Gate, Scope: gate.Scope, Status: status,
			Attempt: attempt, DurationSeconds: time.Since(start).Seconds(), Detail: detail,
		}
		if status == "ok" {
			if attempt > 1 {
				last.Flaky = true
			}
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
