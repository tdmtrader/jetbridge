package schema_test

import (
	"encoding/json"
	"strings"
	"testing"

	schema "github.com/concourse/concourse/agent/schema"
)

func TestRunMetricsRoundTrip(t *testing.T) {
	// round-trips the ingest payload shape
	runID := schema.WorkflowRunID(71)
	rm := schema.RunMetrics{
		WorkflowRunID: &runID, FunctionID: "review",
		BuildID: 123, PlanID: "5f2a", StepName: "implement",
		Status: schema.RunStatusOK, Summary: "did the thing", Model: "claude-sonnet-4-5",
		Usage: schema.Usage{InputTokens: 100, OutputTokens: 50, CacheReadInputTokens: 10, CacheCreationInputTokens: 5},
		Turns: 9, WallTimeSeconds: 60, CostUSD: 0.42,
		Results:        json.RawMessage(`{"schema_version":"1.0","status":"pass"}`),
		EventsArtifact: "vol-abc123",
		EventCounts:    map[string]int{"tool.call": 4},
	}
	data, err := json.Marshal(rm)
	requireNoErr(t, err)
	var back schema.RunMetrics
	requireNoErr(t, json.Unmarshal(data, &back))
	requireEqual(t, back, rm)
	requireContains(t, string(data), `"cache_read_input_tokens":10`)
	// the 64-bit run id rides the wire as a quoted string (JS precision safety)
	requireContains(t, string(data), `"workflow_run_id":"71"`)
	requireContains(t, string(data), `"function_id":"review"`)
}

func TestDeriveOutcome(t *testing.T) {
	cases := []struct {
		name        string
		buildStatus string
		status      string
		summary     string
		results     string
		want        string
	}{
		// 1. a terminally-bad build is final — even over parked, or an
		// abort-while-parked run pulses "waiting on you" forever
		{"failed build masks an ok step", "failed", "ok", "looked fine", `{"status":"pass"}`, schema.RunOutcomeFailed},
		{"errored build wins over parked", "errored", "parked", "", "", schema.RunOutcomeErrored},
		{"aborted build wins over parked", "aborted", "parked", "", "", schema.RunOutcomeAborted},
		// 2. otherwise parked beats everything, including a succeeded build
		{"parked under a started build", "started", "parked", "", "", schema.RunOutcomeParked},
		{"parked under a succeeded build", "succeeded", "parked", "", "", schema.RunOutcomeParked},
		{"parked with no build status", "", "parked", "", "", schema.RunOutcomeParked},
		// 3. otherwise a step-reported failure is never masked
		{"step error under a started build", "started", "error", "", "", schema.RunOutcomeErrored},
		{"step error under a succeeded build", "succeeded", "error", "", "", schema.RunOutcomeErrored},
		{"step failed under a succeeded build", "succeeded", "failed", "", "", schema.RunOutcomeFailed},
		{"step failed with no build status", "", "failed", "", "", schema.RunOutcomeFailed},
		// 4. only then does the build speak: green needs a delivered result
		{"succeeded with a results payload", "succeeded", "ok", "", `{"status":"pass"}`, schema.RunOutcomeOK},
		{"succeeded with a summary only", "succeeded", "ok", "did the thing", "", schema.RunOutcomeOK},
		{"succeeded with nothing delivered", "succeeded", "ok", "", "", schema.RunOutcomeNoOutput},
		{"started renders running", "started", "ok", "", "", schema.RunOutcomeRunning},
		{"pending renders running", "pending", "ok", "", "", schema.RunOutcomeRunning},
		// 5. no/unknown build status: fall back to the step's own word
		{"no build status, ok step", "", "ok", "", "", schema.RunOutcomeOK},
		{"unknown build status, ok step", "mystery", "ok", "", "", schema.RunOutcomeOK},
		{"nothing derivable", "", "bogus", "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rm := schema.RunMetrics{BuildStatus: c.buildStatus, Status: c.status, Summary: c.summary}
			if c.results != "" {
				rm.Results = json.RawMessage(c.results)
			}
			if got := rm.DeriveOutcome(); got != c.want {
				t.Fatalf("DeriveOutcome(build=%q step=%q) = %q, want %q", c.buildStatus, c.status, got, c.want)
			}
		})
	}
}

func TestOutcomeOnTheWire(t *testing.T) {
	rm := schema.RunMetrics{BuildID: 1, PlanID: "p", StepName: "s", Status: schema.RunStatusOK}
	data, err := json.Marshal(rm)
	requireNoErr(t, err)
	if got := string(data); strings.Contains(got, `"outcome"`) {
		t.Fatalf("empty outcome must be omitted from the wire, got %s", got)
	}

	rm.BuildStatus = "failed"
	rm.Outcome = rm.DeriveOutcome()
	data, err = json.Marshal(rm)
	requireNoErr(t, err)
	requireContains(t, string(data), `"outcome":"failed"`)
}
