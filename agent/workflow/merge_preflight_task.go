package workflow

import (
	"fmt"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
)

// renderMergePreflightSelector makes function_id: merge-preflight a selector,
// never an authorable task implementation. Its exact runtime image and durable
// workflow identity are bound later by the trusted target renderer.
func renderMergePreflightSelector(step *atc.TaskStep) error {
	if step.FunctionID != atc.MergePreflightFunctionID {
		return nil
	}
	if step.MergePreflightAuthority != nil || step.DevValidationAuthority != nil || step.Config != nil || step.ConfigPath != "" || step.Privileged || step.Hermetic || step.Limits != nil || step.Requests != nil || step.ImageArtifactName != "" || step.Timeout != "" || len(step.Params) != 0 || len(step.Vars) != 0 || len(step.Sidecars) != 0 || len(step.Tags) != 0 || len(step.InputMapping) != 0 || len(step.OutputMapping) != 0 {
		return fmt.Errorf("workflow: merge preflight selector %q may not author task execution fields", step.Name)
	}
	if len(step.SnapshotInputs) != 3 || len(step.SnapshotOutputs) != 1 {
		return fmt.Errorf("workflow: merge preflight selector %q must declare exactly base, candidate, target, and merge-report", step.Name)
	}
	for name, typ := range map[string]snapshot.TypeRef{"base": "repository/v1", "candidate": "repository-change/v1", "target": "repository/v1"} {
		input, ok := step.SnapshotInputs[name]
		if !ok || input.Optional || input.Type != typ {
			return fmt.Errorf("workflow: merge preflight selector %q has invalid %s input", step.Name, name)
		}
	}
	output, ok := step.SnapshotOutputs[atc.MergePreflightOutput]
	if !ok || output.Optional || output.Type != snapshot.TypeRef("validation/v1") {
		return fmt.Errorf("workflow: merge preflight selector %q must emit validation/v1 at %q", step.Name, atc.MergePreflightOutput)
	}
	// A syntactically pinned placeholder keeps the compiled durable model
	// serializable. WorkflowTargetRenderer overwrites it with trusted runtime
	// configuration before any executable plan exists.
	a := atc.MergePreflightAuthority{ProfileDigest: snapshot.Digest(atc.MergePreflightPolicyDigest), ProtectedConfigDigest: snapshot.Digest(atc.MergePreflightConfigDigest), CapabilityImage: "registry.invalid/agent-runner@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", CapabilityImageDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", CandidateInput: "candidate", BaseInput: "base", TargetInput: "target"}
	step.Hermetic = true
	step.MergePreflightAuthority = &a
	// RenderFunction replaces this incomplete authority with runtime image and
	// authenticated definition identity before an executable plan is emitted.
	step.Config = &atc.TaskConfig{Platform: "linux", Run: atc.TaskRunConfig{Path: atc.MergePreflightRunnerPath}, Inputs: []atc.TaskInputConfig{{Name: "candidate"}, {Name: "base"}, {Name: "target"}}, Outputs: []atc.TaskOutputConfig{{Name: atc.MergePreflightOutput}}}
	return nil
}
