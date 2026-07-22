// Package gates implements the deterministic repository gate function used by
// version-3 workflows and by the legacy harvest compatibility composition.
package gates

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/snapshot/contracts"
)

const defaultTimeout = 30 * time.Minute

const resultsFileName = "gate-results.json"

type Policy struct {
	Gates         []Gate `json:"gates,omitempty"`
	OnGateFailure string `json:"on_gate_failure,omitempty"`
}

type Gate struct {
	Name    string `json:"gate"`
	Scope   string `json:"scope"`
	Focus   string `json:"focus,omitempty"`
	Timeout string `json:"timeout,omitempty"`
	Retries int    `json:"retries,omitempty"`
}

type Command struct {
	Path string   `json:"path"`
	Args []string `json:"args"`
}

func (command Command) Clone() Command {
	command.Args = append([]string(nil), command.Args...)
	return command
}

var commandCatalog = map[string]Command{
	"build": {Path: "go", Args: []string{"build", "./..."}},
	"test":  {Path: "go", Args: []string{"test", "./..."}},
	"lint":  {Path: "go", Args: []string{"vet", "./..."}},
}

func DeterministicCommands() map[string]Command {
	copy := make(map[string]Command, len(commandCatalog))
	for name, command := range commandCatalog {
		copy[name] = command.Clone()
	}
	return copy
}

type Execution struct {
	ExitCode int
	Output   string
}

type Executor interface {
	Execute(context.Context, string, Command) (Execution, error)
}

type AttemptEvent struct {
	Gate    Gate
	Attempt int
}

type Observer interface {
	AttemptStarted(context.Context, AttemptEvent)
	AttemptFinished(context.Context, AttemptEvent, contracts.GateOutcome)
}

type Runner struct {
	executor Executor
	observer Observer
	now      func() time.Time
}

func NewRunner(executor Executor) *Runner {
	if executor == nil {
		executor = commandExecutor{}
	}
	return &Runner{executor: executor, now: time.Now}
}

func (runner *Runner) WithObserver(observer Observer) *Runner {
	copy := *runner
	copy.observer = observer
	return &copy
}

func (runner *Runner) Run(ctx context.Context, workspace string, policy Policy) (contracts.GateResultsDocument, error) {
	if ctx == nil {
		return contracts.GateResultsDocument{}, fmt.Errorf("gates: context is required")
	}
	if err := ctx.Err(); err != nil {
		return contracts.GateResultsDocument{}, err
	}
	if strings.TrimSpace(workspace) == "" {
		return contracts.GateResultsDocument{}, fmt.Errorf("gates: workspace is required")
	}
	if len(policy.Gates) == 0 {
		return contracts.GateResultsDocument{}, fmt.Errorf("gates: at least one gate is required")
	}
	seen := make(map[string]struct{}, len(policy.Gates))
	for index, gate := range policy.Gates {
		if strings.TrimSpace(gate.Name) == "" {
			return contracts.GateResultsDocument{}, fmt.Errorf("gates: gate %d name is required", index)
		}
		if _, found := seen[gate.Name]; found {
			return contracts.GateResultsDocument{}, fmt.Errorf("gates: duplicate gate %q", gate.Name)
		}
		seen[gate.Name] = struct{}{}
	}

	document := contracts.GateResultsDocument{SchemaVersion: "1.0.0"}
	for _, gate := range policy.Gates {
		outcome, err := runner.runGate(ctx, workspace, gate)
		if err != nil {
			return contracts.GateResultsDocument{}, err
		}
		document.Gates = append(document.Gates, outcome)
		if outcome.Status != "ok" {
			break
		}
	}
	if err := document.Validate(); err != nil {
		return contracts.GateResultsDocument{}, fmt.Errorf("gates: produced invalid gate-results/v1: %w", err)
	}
	return document, nil
}

func (runner *Runner) runGate(ctx context.Context, workspace string, gate Gate) (contracts.GateOutcome, error) {
	toolingError := func(detail string) contracts.GateOutcome {
		return contracts.GateOutcome{
			Gate: gate.Name, Scope: gate.Scope, Status: "error", Attempt: 1,
			Detail: boundedDetail(detail),
		}
	}
	if gate.Scope != "full" {
		return toolingError(fmt.Sprintf("scope %q is not enforceable by the deterministic gate function; dev-mcp must resolve affected scopes", gate.Scope)), nil
	}
	command, found := commandCatalog[gate.Name]
	if !found {
		return toolingError(fmt.Sprintf("unknown gate %q; deterministic gates are build, test, and lint", gate.Name)), nil
	}
	if gate.Retries < 0 || gate.Retries > 2 {
		return toolingError(fmt.Sprintf("retries must be within 0-2, got %d", gate.Retries)), nil
	}
	timeout := defaultTimeout
	if gate.Timeout != "" {
		parsed, err := time.ParseDuration(gate.Timeout)
		if err != nil || parsed <= 0 {
			return toolingError(fmt.Sprintf("invalid timeout %q", gate.Timeout)), nil
		}
		timeout = parsed
	}

	var last contracts.GateOutcome
	for attempt := 1; attempt <= gate.Retries+1; attempt++ {
		if err := ctx.Err(); err != nil {
			return contracts.GateOutcome{}, err
		}
		event := AttemptEvent{Gate: gate, Attempt: attempt}
		if runner.observer != nil {
			runner.observer.AttemptStarted(ctx, event)
		}
		started := runner.now()
		attemptCtx, cancel := context.WithTimeout(ctx, timeout)
		execution, executeErr := runner.executor.Execute(attemptCtx, workspace, command.Clone())
		attemptErr := attemptCtx.Err()
		cancel()
		if err := ctx.Err(); err != nil {
			return contracts.GateOutcome{}, err
		}

		last = contracts.GateOutcome{
			Gate: gate.Name, Scope: gate.Scope, Attempt: attempt,
			DurationSeconds: nonnegativeDurationSeconds(runner.now().Sub(started)),
		}
		switch {
		case errors.Is(attemptErr, context.DeadlineExceeded):
			last.Status = "error"
			last.Detail = fmt.Sprintf("gate timed out after %s", timeout)
		case executeErr != nil:
			last.Status = "error"
			last.Detail = executeErr.Error()
		case execution.ExitCode == 0:
			last.Status = "ok"
			last.Detail = execution.Output
		case execution.ExitCode > 0:
			last.Status = "failed"
			last.Detail = execution.Output
		default:
			last.Status = "error"
			last.Detail = fmt.Sprintf("gate executor returned invalid exit code %d", execution.ExitCode)
		}
		last.Detail = boundedDetail(last.Detail)
		last.Flaky = last.Status == "ok" && attempt > 1
		if runner.observer != nil {
			runner.observer.AttemptFinished(ctx, event, last)
		}
		if last.Status == "ok" || last.Status == "error" {
			return last, nil
		}
	}
	return last, nil
}

func nonnegativeDurationSeconds(duration time.Duration) float64 {
	if duration < 0 {
		return 0
	}
	return duration.Seconds()
}

func boundedDetail(detail string) string {
	detail = strings.TrimSpace(detail)
	if len(detail) > 64*1024 {
		return detail[:64*1024]
	}
	return detail
}

// WriteResults materializes a validated gate-results/v1 value at the fixed
// output path expected by the snapshot contract.
func WriteResults(ctx context.Context, outputRoot string, document contracts.GateResultsDocument) error {
	if ctx == nil {
		return fmt.Errorf("gates: context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(outputRoot) == "" {
		return fmt.Errorf("gates: output root is required")
	}
	if err := document.Validate(); err != nil {
		return fmt.Errorf("gates: invalid gate-results/v1: %w", err)
	}
	payload, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return fmt.Errorf("gates: marshal gate-results/v1: %w", err)
	}
	payload = append(payload, '\n')
	root, err := os.OpenRoot(outputRoot)
	if err != nil {
		return fmt.Errorf("gates: open output root: %w", err)
	}
	writeErr := root.WriteFile(resultsFileName, payload, 0600)
	closeErr := root.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return fmt.Errorf("gates: write %s: %w", resultsFileName, err)
	}
	return nil
}

type commandExecutor struct{}

func (commandExecutor) Execute(ctx context.Context, workspace string, command Command) (Execution, error) {
	process := exec.CommandContext(ctx, command.Path, command.Args...)
	process.Dir = workspace
	var output bytes.Buffer
	process.Stdout = &output
	process.Stderr = &output
	err := process.Run()
	result := Execution{Output: strings.TrimSpace(output.String())}
	if err == nil {
		return result, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		result.ExitCode = exitError.ExitCode()
		return result, nil
	}
	return result, err
}
