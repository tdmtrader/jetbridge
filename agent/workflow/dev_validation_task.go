package workflow

import (
	"fmt"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
)

const (
	devValidationDefinitionToken = "__workflow_definition_id__"
	devValidationVersionToken    = "__workflow_version__"
)

func renderDevValidationSelector(step *atc.TaskStep, profiles []CompiledDevValidationProfile) error {
	if step.DevValidationProfile == "" {
		return nil
	}
	if step.DevValidationAuthority != nil || step.Config != nil || step.ConfigPath != "" || step.FunctionID != "" || step.Privileged || step.Hermetic || step.Limits != nil || step.Requests != nil || step.ImageArtifactName != "" || step.Timeout != "" || len(step.Params) != 0 || len(step.Vars) != 0 || len(step.Sidecars) != 0 || len(step.Tags) != 0 || len(step.InputMapping) != 0 || len(step.OutputMapping) != 0 {
		return fmt.Errorf("workflow: validation selector %q may not author task execution fields", step.Name)
	}
	var profile *CompiledDevValidationProfile
	for i := range profiles {
		if profiles[i].Name == step.DevValidationProfile {
			profile = &profiles[i]
			break
		}
	}
	if profile == nil {
		return fmt.Errorf("workflow: validation selector %q names unknown profile %q", step.Name, step.DevValidationProfile)
	}
	if len(step.SnapshotInputs) != 1+len(profile.BaseInputs) || len(step.SnapshotOutputs) != 1 {
		return fmt.Errorf("workflow: validation selector %q must declare exactly its candidate, bases, and validation output", step.Name)
	}
	candidate, ok := step.SnapshotInputs[profile.Candidate.Name]
	if !ok || candidate.Type != profile.Candidate.Type || candidate.Optional {
		return fmt.Errorf("workflow: validation selector %q has invalid candidate input", step.Name)
	}
	bases := make([]atc.DevValidationBaseInput, len(profile.BaseInputs))
	for i, base := range profile.BaseInputs {
		input, ok := step.SnapshotInputs[base.Name]
		if !ok || input.Type != base.Type || input.Optional {
			return fmt.Errorf("workflow: validation selector %q has invalid base %q", step.Name, base.Name)
		}
		bases[i] = atc.DevValidationBaseInput{Name: base.Name, Type: base.Type}
	}
	output, ok := step.SnapshotOutputs[atc.DevValidationOutput]
	if !ok || output.Type != snapshot.TypeRef("validation/v1") || output.Optional {
		return fmt.Errorf("workflow: validation selector %q must emit validation/v1 at %q", step.Name, atc.DevValidationOutput)
	}
	// Definition identity is not available while source is compiled. Leave it
	// unset here; RenderFunction binds the authenticated persisted definition
	// and only then materializes the executable task configuration.
	authority := atc.DevValidationAuthority{ProfileName: profile.Name, Profile: append([]byte(nil), profile.Profile...), ProfileDigest: profile.ProfileDigest, ProtectedConfig: append([]byte(nil), profile.ProtectedConfig...), ProtectedConfigDigest: profile.ProtectedConfigDigest, CapabilityImage: profile.CapabilityImage, CapabilityImageDigest: profile.CapabilityImageDigest, CandidateInput: profile.Candidate.Name, BaseInputs: bases}
	config := unboundDevValidationTaskConfig(authority, candidate.Type)
	step.DevValidationProfile = ""
	// Typed-flow analysis requires a stable producer identity. This is
	// compiler-produced, never source-authored task behavior.
	step.FunctionID = "dev-validation-" + profile.Name
	step.Hermetic = true
	step.Config = config
	step.DevValidationAuthority = &authority
	return nil
}

// unboundDevValidationTaskConfig is only used in a compiled definition before
// it has a durable definition identity. RenderFunction replaces these two
// renderer-owned tokens with the authenticated persisted values before an ATC
// plan is produced; it never uses a numeric placeholder.
func unboundDevValidationTaskConfig(authority atc.DevValidationAuthority, candidate snapshot.TypeRef) *atc.TaskConfig {
	args := atc.DevValidationStaticArgs(authority, candidate)
	for index := range args {
		switch args[index] {
		case "0":
			if index > 0 && args[index-1] == "--workflow-definition-id" {
				args[index] = devValidationDefinitionToken
			}
			if index > 0 && args[index-1] == "--workflow-version" {
				args[index] = devValidationVersionToken
			}
		}
	}
	inputs := []atc.TaskInputConfig{{Name: authority.CandidateInput}}
	for _, base := range authority.BaseInputs {
		inputs = append(inputs, atc.TaskInputConfig{Name: base.Name})
	}
	return &atc.TaskConfig{Platform: "linux", RootfsURI: authority.CapabilityImage, Run: atc.TaskRunConfig{Path: atc.DevValidationFunctionRunnerPath, Args: args}, Inputs: inputs, Outputs: []atc.TaskOutputConfig{{Name: atc.DevValidationOutput}}, ScratchPaths: []atc.TaskScratchConfig{{Path: atc.DevValidationWorkspacePath}}}
}
