package commands

import (
	"testing"

	agentschema "github.com/concourse/concourse/agent/schema"
)

// runLabel renders the durable identity of a recent run: the workflow run and
// the function that produced the step, or a plain "CI" for an unbound CI
// invocation. A metric without workflow identity is never joined to a ticket.
func TestRunLabel(t *testing.T) {
	runID := agentschema.WorkflowRunID(71)
	for _, tc := range []struct {
		name string
		rm   agentschema.RunMetrics
		want string
	}{
		{"unbound CI invocation", agentschema.RunMetrics{}, "CI"},
		{"workflow run with function", agentschema.RunMetrics{WorkflowRunID: &runID, FunctionID: "review"}, "#71 review"},
		{"workflow run without function", agentschema.RunMetrics{WorkflowRunID: &runID}, "#71"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := runLabel(tc.rm); got != tc.want {
				t.Fatalf("runLabel = %q, want %q", got, tc.want)
			}
		})
	}
}
