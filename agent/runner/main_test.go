package runner_test

import (
	"context"
	"os"
	"testing"

	runner "github.com/concourse/concourse/agent/runner"
)

// TestMain disables the pre-model egress probe for the whole suite.
//
// Run performs a real DNS lookup and TCP dial against the model endpoint
// before invoking the CLI. That is correct in a pod and wrong in a unit test:
// left enabled, every test that drives Run would pass or fail on whether the
// machine running it has outbound network. Tests that care about the probe
// install their own stub via runner.SetModelEgressPreflight.
func TestMain(m *testing.M) {
	restore := runner.SetModelEgressPreflight(func(context.Context, string) error { return nil })
	code := m.Run()
	restore()
	os.Exit(code)
}
