package runtime_test

import (
	"context"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/broker"
	brokerruntime "github.com/concourse/concourse/agent/broker/runtime"
	"github.com/concourse/concourse/agent/broker/workspace"
)

func TestRunnerRefusesToRecaptureLiveWorkspaceDuringReviewRun(t *testing.T) {
	root := t.TempDir()
	scratch := t.TempDir()
	runner, err := brokerruntime.NewRunner(brokerruntime.RunnerConfig{
		WorkspaceRoot: root, ScratchRoot: scratch,
		OutputSchemas: map[broker.Tool]string{
			broker.ToolRequestReview: scratch + "/review.schema.json",
			broker.ToolConsultAgent:  scratch + "/consult.schema.json",
		},
		CaptureLimits: workspace.Limits{
			MaxPatchBytes: 1024, MaxEntries: 100, StabilityAttempts: 1,
		},
		Probe: inertProbe{},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(context.Background(), broker.RunRequest{Tool: broker.ToolRequestReview})
	if err == nil || !strings.Contains(err.Error(), "authoritative workspace capture") {
		t.Fatalf("Run() error = %v", err)
	}
}

type inertProbe struct{}

func (inertProbe) LookPath(string) (string, error) { return "", nil }
func (inertProbe) Output(context.Context, string, []string, []string) ([]byte, error) {
	return nil, nil
}
