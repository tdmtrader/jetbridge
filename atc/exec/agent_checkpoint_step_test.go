package exec

import (
	"testing"
	"time"

	"github.com/concourse/concourse/agent/checkpoint"
	"github.com/concourse/concourse/agent/provider"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
)

func TestAgentCheckpointIdentityLeavesLegacyUnchangedButRejectsMalformedV3Candidate(t *testing.T) {
	runID := snapshot.WorkflowRunID(7)
	step := &AgentStep{
		planID: "plan-1",
		plan: atc.AgentPlan{
			Hermetic:     true,
			FunctionID:   "review",
			RuntimeImage: "registry.example/agent@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Model:        "claude-opus",
		},
		metadata: StepMetadata{BuildID: 42, WorkflowRunID: &runID},
		checkpointCapture: &AgentCheckpointStepConfig{
			Factory:         AgentCheckpointExecutionFactoryFunc(func(checkpoint.Identity) (AgentCheckpointController, error) { return nil, nil }),
			Provider:        "anthropic",
			Adapter:         provider.Identity{Name: "claude-cli", Version: "legacy-stream-json"},
			ElapsedInterval: time.Minute,
			MaxArchiveBytes: 1024,
		},
	}

	if _, enabled, err := step.checkpointIdentity(step.plan.RuntimeImage, "/work"); err != nil || !enabled {
		t.Fatalf("pinned v3 candidate = enabled %t, err %v", enabled, err)
	}
	legacy := *step
	legacy.checkpointCapture = nil
	legacy.plan.RuntimeImage = ""
	legacy.plan.FunctionID = ""
	legacy.metadata.WorkflowRunID = nil
	if _, enabled, err := legacy.checkpointIdentity("registry.example/agent:latest", "relative"); err != nil || enabled {
		t.Fatalf("legacy step changed: enabled %t, err %v", enabled, err)
	}

	for name, mutate := range map[string]func(*AgentStep){
		"missing function":   func(step *AgentStep) { step.plan.FunctionID = "" },
		"missing image pin":  func(step *AgentStep) { step.plan.RuntimeImage = "" },
		"tagged image":       func(step *AgentStep) { step.plan.RuntimeImage = "registry.example/agent:latest" },
		"missing model":      func(step *AgentStep) { step.plan.Model = "" },
		"relative workspace": func(step *AgentStep) {},
		"invalid policy":     func(step *AgentStep) { step.checkpointCapture.MaxArchiveBytes = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := *step
			candidate.plan = step.plan
			candidate.checkpointCapture = &AgentCheckpointStepConfig{
				Factory:         step.checkpointCapture.Factory,
				Provider:        step.checkpointCapture.Provider,
				Adapter:         step.checkpointCapture.Adapter,
				ElapsedInterval: step.checkpointCapture.ElapsedInterval,
				MaxArchiveBytes: step.checkpointCapture.MaxArchiveBytes,
			}
			mutate(&candidate)
			workdir := "/work"
			if name == "relative workspace" {
				workdir = "work"
			}
			if _, _, err := candidate.checkpointIdentity(candidate.plan.RuntimeImage, workdir); err == nil {
				t.Fatal("malformed authenticated v3 candidate enabled or silently fell back")
			}
		})
	}
}
