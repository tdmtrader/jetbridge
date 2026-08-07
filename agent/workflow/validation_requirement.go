package workflow

import (
	"context"
	"fmt"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
	"github.com/concourse/concourse/atc"
)

// RequirePassingValidationAuthority is the read-side rev3 gate. Seal-time
// provenance is rechecked here because a stored record may be old, malformed,
// or belong to a different exact candidate/base binding.
func RequirePassingValidationAuthority(ctx context.Context, candidate snapshot.SnapshotRef, record contracts.Record[contracts.ValidationBody], authority snapshot.ValidationAttestationAuthority) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if candidate.Type != snapshot.TypeRef("repository-change/v1") || candidate.Validate() != nil {
		return fmt.Errorf("validation requirement: invalid candidate")
	}
	if err := authority.Validate(); err != nil {
		return fmt.Errorf("validation requirement: authority: %w", err)
	}
	if authority.Candidate != candidate {
		return fmt.Errorf("validation requirement: candidate does not match authority")
	}
	revision, found := contracts.SchemaRevisionFor(snapshot.TypeRef("validation/v1"), record.Schema)
	if !found || revision != 3 {
		return fmt.Errorf("validation requirement: validation record must use schema revision 3")
	}
	if err := record.RevalidateSealed(snapshot.TypeRef("validation/v1")); err != nil {
		return fmt.Errorf("validation requirement: sealed record: %w", err)
	}
	if err := record.Body.Validate(record.Subjects); err != nil {
		return fmt.Errorf("validation requirement: record body: %w", err)
	}
	primary := contracts.Subject{}
	for _, subject := range record.Subjects {
		if subject.Role == contracts.SubjectRolePrimary {
			primary = subject
			break
		}
	}
	if primary.Input != authority.CandidateInput || primary.Type != candidate.Type || primary.Digest != candidate.Digest {
		return fmt.Errorf("validation requirement: primary subject does not match server authority")
	}
	if record.Body.Conclusion != "passed" {
		return fmt.Errorf("validation requirement: conclusion must be passed")
	}
	attestation := record.Body.Attestation
	if attestation.CandidateDigest != candidate.Digest || attestation.ProfileDigest != authority.ProfileDigest || attestation.ProtectedConfigDigest != authority.ProtectedConfigDigest || attestation.CapabilityImage != authority.CapabilityImage || attestation.CapabilityImageDigest != authority.CapabilityImageDigest || attestation.WorkflowDefinitionID != authority.WorkflowDefinitionID || attestation.WorkflowVersion != authority.WorkflowVersion || attestation.Toolchain != authority.Toolchain {
		return fmt.Errorf("validation requirement: attestation does not match server authority")
	}
	if len(attestation.BaseInputs) != len(authority.BaseInputs) {
		return fmt.Errorf("validation requirement: base inputs do not match server authority")
	}
	for index, base := range authority.BaseInputs {
		got := attestation.BaseInputs[index]
		if got.Input != base.Input || got.Type != base.Ref.Type || got.Digest != base.Ref.Digest {
			return fmt.Errorf("validation requirement: base inputs do not match server authority")
		}
	}
	return nil
}

// validation requirements deliberately exist only at the three governed
// consumer seams. Source supplies a validation artifact name; rendering binds
// it to the exact authoritative task that produced it.
func rejectAuthoredValidationRequirements(function *FunctionConfig) error {
	if function == nil {
		return fmt.Errorf("workflow: function is required")
	}
	return walkFunctionSteps(function.Plan, func(step atc.Step, path string, _ bool) error {
		switch config := step.Config.(type) {
		case *atc.AgentStep:
			if config.ReviewValidation != nil {
				return fmt.Errorf("workflow: %s: review validation requirement is renderer-owned", path)
			}
		case *atc.AwaitSnapshotStep:
			if config.MergeApprovalValidation != nil {
				return fmt.Errorf("workflow: %s: merge approval validation requirement is renderer-owned", path)
			}
		case *atc.PublishSnapshotStep:
			if config.PublishValidation != nil {
				return fmt.Errorf("workflow: %s: publish validation requirement is renderer-owned", path)
			}
		}
		return nil
	})
}

func renderValidationRequirements(function *FunctionConfig) error {
	if function == nil {
		return fmt.Errorf("workflow: function is required")
	}
	producers := map[string]*atc.DevValidationAuthority{}
	if err := walkFunctionSteps(function.Plan, func(step atc.Step, path string, _ bool) error {
		task, ok := step.Config.(*atc.TaskStep)
		if !ok || task.DevValidationAuthority == nil {
			return nil
		}
		if _, duplicate := producers[atc.DevValidationOutput]; duplicate {
			return fmt.Errorf("workflow: %s: duplicate authoritative validation output", path)
		}
		producers[atc.DevValidationOutput] = task.DevValidationAuthority.Clone()
		return nil
	}); err != nil {
		return err
	}
	return walkFunctionSteps(function.Plan, func(step atc.Step, path string, _ bool) error {
		bind := func(candidate, validation string, mappings map[string]string) (*atc.DevValidationAuthority, error) {
			authority, found := producers[validation]
			if !found {
				return nil, fmt.Errorf("workflow: %s: validation %q is not a renderer-owned dev validation output", path, validation)
			}
			authority = mappedDevValidationAuthority(authority, mappings)
			if authority.CandidateInput != candidate {
				return nil, fmt.Errorf("workflow: %s: validation %q does not bind current candidate %q", path, validation, candidate)
			}
			return authority, nil
		}
		switch config := step.Config.(type) {
		case *atc.AgentStep:
			candidate, governed, err := humanReviewCandidate(config)
			if err != nil {
				return err
			}
			if !governed || config.Validation == "" {
				return nil
			}
			candidate = mappedAgentValidationName(candidate, config.InputMapping)
			validation := mappedAgentValidationName(config.Validation, config.InputMapping)
			authority, err := bind(candidate, config.Validation, config.InputMapping)
			if err != nil {
				return err
			}
			config.ReviewValidation = &atc.ReviewValidationRequirement{Candidate: candidate, Validation: validation, Authority: authority}
		case *atc.AwaitSnapshotStep:
			if config.MergeApproval != nil && config.Validation != "" {
				authority, err := bind(config.MergeApproval.Input, config.Validation, nil)
				if err != nil {
					return err
				}
				config.MergeApprovalValidation = &atc.MergeApprovalValidationRequirement{Candidate: config.MergeApproval.Input, Validation: config.Validation, Authority: authority}
			}
		case *atc.PublishSnapshotStep:
			if config.InputType.String() != "repository-change/v1" || config.Validation == "" {
				return nil
			}
			authority, err := bind(config.Input, config.Validation, nil)
			if err != nil {
				return err
			}
			config.PublishValidation = &atc.PublishValidationRequirement{Candidate: config.Input, Validation: config.Validation, Authority: authority}
		}
		return nil
	})
}

func mappedAgentValidationName(name string, mappings map[string]string) string {
	if mapped, found := mappings[name]; found {
		return mapped
	}
	return name
}

func mappedDevValidationAuthority(authority *atc.DevValidationAuthority, mappings map[string]string) *atc.DevValidationAuthority {
	cloned := authority.Clone()
	if cloned == nil {
		return nil
	}
	cloned.CandidateInput = mappedAgentValidationName(cloned.CandidateInput, mappings)
	for index := range cloned.BaseInputs {
		cloned.BaseInputs[index].Name = mappedAgentValidationName(cloned.BaseInputs[index].Name, mappings)
	}
	return cloned
}
