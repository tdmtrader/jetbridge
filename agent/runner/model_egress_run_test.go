package runner_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	runner "github.com/concourse/concourse/agent/runner"
	schema "github.com/concourse/concourse/agent/schema"
)

// An unreachable model endpoint must end the step immediately as a platform
// error, before the CLI is invoked at all. Previously the CLI was started and
// hung ~5 minutes on its own timeout, reporting nothing about the network.
func TestRunAbortsBeforeTheModelWhenEgressIsBlocked(t *testing.T) {
	restore := runner.SetModelEgressPreflight(func(context.Context, string) error {
		return errors.New("model endpoint api.anthropic.com:443 cannot be reached (i/o timeout). " +
			"In a hermetic pod this is egress being denied: the chart's networkPolicy.hermeticEgressTo must allow TCP to it")
	})
	defer restore()

	dir := t.TempDir()
	flight := filepath.Join(dir, "flight")
	if err := os.MkdirAll(flight, 0o755); err != nil {
		t.Fatal(err)
	}
	// A stub that would report success if it ever ran, so a passing result
	// here would prove the preflight was skipped rather than honored.
	claude := writeStubClaude(t, dir,
		`{"type":"result","subtype":"success","result":"\"done\"","model":"m1","cost_usd":0.1,"num_turns":1,"is_error":false}`)

	exit, err := runner.Run(context.Background(), runner.Config{
		Prompt:     "do it",
		Model:      "m1",
		MaxTurns:   1,
		FlightDir:  flight,
		WorkDir:    dir,
		StepName:   "blocked",
		ClaudePath: claude,
	})
	if err != nil {
		t.Fatalf("run returned a runner error: %v", err)
	}
	if exit != 2 {
		t.Fatalf("exit = %d, want 2 (platform error)", exit)
	}

	raw, err := os.ReadFile(filepath.Join(flight, "results.json"))
	if err != nil {
		t.Fatal(err)
	}
	var results schema.Results
	if err := json.Unmarshal(raw, &results); err != nil {
		t.Fatal(err)
	}
	if results.Status != schema.StatusError {
		t.Fatalf("status = %s, want %s", results.Status, schema.StatusError)
	}
	for _, want := range []string{"cannot be reached", "hermeticEgressTo"} {
		if !strings.Contains(results.Summary, want) {
			t.Errorf("summary lacks %q: %s", want, results.Summary)
		}
	}
}
