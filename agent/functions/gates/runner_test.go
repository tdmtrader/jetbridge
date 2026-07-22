package gates

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/snapshot/contracts"
)

type scriptedExecutor struct {
	results []Execution
	errors  []error
	calls   []Command
}

func (executor *scriptedExecutor) Execute(_ context.Context, _ string, command Command) (Execution, error) {
	executor.calls = append(executor.calls, command.Clone())
	index := len(executor.calls) - 1
	var result Execution
	if index < len(executor.results) {
		result = executor.results[index]
	}
	if index < len(executor.errors) {
		return result, executor.errors[index]
	}
	return result, nil
}

func TestRunnerProducesGateResultsAndStopsAtFailure(t *testing.T) {
	executor := &scriptedExecutor{results: []Execution{
		{ExitCode: 0, Output: "built"},
		{ExitCode: 1, Output: "tests failed"},
	}}
	runner := NewRunner(executor)
	document, err := runner.Run(context.Background(), "/workspace", Policy{Gates: []Gate{
		{Name: "build", Scope: "full"},
		{Name: "test", Scope: "full"},
		{Name: "lint", Scope: "full"},
	}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if err := document.Validate(); err != nil {
		t.Fatalf("gate-results/v1: %v", err)
	}
	if len(document.Gates) != 2 || len(executor.calls) != 2 {
		t.Fatalf("document/calls = %+v/%+v, want build and test only", document.Gates, executor.calls)
	}
	if document.Gates[0].Status != "ok" || document.Gates[1].Status != "failed" {
		t.Fatalf("statuses = %+v", document.Gates)
	}
	want := []Command{
		{Path: "go", Args: []string{"build", "./..."}},
		{Path: "go", Args: []string{"test", "./..."}},
	}
	if !reflect.DeepEqual(executor.calls, want) {
		t.Fatalf("commands = %#v, want %#v", executor.calls, want)
	}
}

func TestRunnerRetriesFailuresButNeverToolingErrors(t *testing.T) {
	t.Run("pass on retry is explicitly flaky", func(t *testing.T) {
		executor := &scriptedExecutor{results: []Execution{{ExitCode: 1}, {ExitCode: 0}}}
		document, err := NewRunner(executor).Run(context.Background(), "/workspace", Policy{Gates: []Gate{{Name: "test", Scope: "full", Retries: 2}}})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		outcome := document.Gates[0]
		if outcome.Status != "ok" || outcome.Attempt != 2 || !outcome.Flaky || len(executor.calls) != 2 {
			t.Fatalf("outcome/calls = %+v/%d", outcome, len(executor.calls))
		}
	})

	t.Run("executor error is one error attempt", func(t *testing.T) {
		executor := &scriptedExecutor{errors: []error{errors.New("cannot start")}}
		document, err := NewRunner(executor).Run(context.Background(), "/workspace", Policy{Gates: []Gate{{Name: "test", Scope: "full", Retries: 2}}})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		outcome := document.Gates[0]
		if outcome.Status != "error" || outcome.Attempt != 1 || len(executor.calls) != 1 || !strings.Contains(outcome.Detail, "cannot start") {
			t.Fatalf("outcome/calls = %+v/%d", outcome, len(executor.calls))
		}
	})
}

func TestRunnerFailsClosedForUnsupportedOrMalformedDeclarations(t *testing.T) {
	tests := []struct {
		name string
		gate Gate
		want string
	}{
		{name: "unknown", gate: Gate{Name: "deploy", Scope: "full"}, want: "unknown gate"},
		{name: "live scope", gate: Gate{Name: "test", Scope: "affected"}, want: "dev-mcp"},
		{name: "timeout", gate: Gate{Name: "test", Scope: "full", Timeout: "later"}, want: "invalid timeout"},
		{name: "negative retries", gate: Gate{Name: "test", Scope: "full", Retries: -1}, want: "retries"},
		{name: "too many retries", gate: Gate{Name: "test", Scope: "full", Retries: 3}, want: "retries"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			executor := &scriptedExecutor{}
			document, err := NewRunner(executor).Run(context.Background(), "/workspace", Policy{Gates: []Gate{test.gate}})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if len(document.Gates) != 1 || document.Gates[0].Status != "error" || document.Gates[0].Attempt != 1 ||
				!strings.Contains(document.Gates[0].Detail, test.want) || len(executor.calls) != 0 {
				t.Fatalf("document/calls = %+v/%v, want tooling error containing %q", document, executor.calls, test.want)
			}
		})
	}
}

func TestRunnerUsesBoundedPerGateContextAndHonorsCancellation(t *testing.T) {
	executor := executorFunc(func(ctx context.Context, _ string, _ Command) (Execution, error) {
		<-ctx.Done()
		return Execution{}, ctx.Err()
	})
	started := time.Now()
	document, err := NewRunner(executor).Run(context.Background(), "/workspace", Policy{Gates: []Gate{{Name: "test", Scope: "full", Timeout: "5ms"}}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if time.Since(started) > time.Second || document.Gates[0].Status != "error" || !strings.Contains(document.Gates[0].Detail, "timed out") {
		t.Fatalf("document/elapsed = %+v/%s", document, time.Since(started))
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = NewRunner(&scriptedExecutor{}).Run(canceled, "/workspace", Policy{Gates: []Gate{{Name: "test", Scope: "full"}}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled error = %v", err)
	}
}

func TestDeterministicCommandsReturnsAnIsolatedCopy(t *testing.T) {
	commands := DeterministicCommands()
	commands["test"].Args[0] = "mutated"
	delete(commands, "build")
	again := DeterministicCommands()
	if again["test"].Args[0] != "test" || again["build"].Path != "go" {
		t.Fatalf("command catalog was mutable: %#v", again)
	}
}

func TestWriteResultsEmitsStrictSnapshotDocument(t *testing.T) {
	document := contracts.GateResultsDocument{
		SchemaVersion: "1.0.0",
		Gates:         []contracts.GateOutcome{{Gate: "test", Scope: "full", Status: "ok", Attempt: 1}},
	}
	root := t.TempDir()
	if err := WriteResults(context.Background(), root, document); err != nil {
		t.Fatalf("WriteResults: %v", err)
	}
	payload, err := os.ReadFile(filepath.Join(root, "gate-results.json"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["schema_version"] != "1.0.0" {
		t.Fatalf("payload = %s", payload)
	}
}

type executorFunc func(context.Context, string, Command) (Execution, error)

func (function executorFunc) Execute(ctx context.Context, workspace string, command Command) (Execution, error) {
	return function(ctx, workspace, command)
}
