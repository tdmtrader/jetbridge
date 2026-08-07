package workflow

import (
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
)

func TestAuthoritativeValidationFlowRejectsOrdinaryAndStaleBindings(t *testing.T) {
	publish := func() atc.Step {
		return atc.Step{Config: &atc.PublishSnapshotStep{Name: "publish", Publisher: publisher.GitPublisher, Input: "change", InputType: repositoryChangeV1, Validation: "validation", Destination: "git.example/acme/widget", Mode: publisher.ModeBranch, Parameters: map[string]string{"target_branch": "main"}, ApprovalPolicyVersion: "engineering/v1"}}
	}
	overwrite := func(name string) atc.Step {
		return atc.Step{Config: &atc.AgentStep{Name: name, FunctionID: name, Prompt: "replace", Outputs: []string{name}, SnapshotOutputs: map[string]atc.SnapshotOutputConfig{name: {Type: repositoryChangeV1}}}}
	}
	ordinaryValidation := atc.Step{Config: &atc.AgentStep{Name: "ordinary-validation", FunctionID: "ordinary-validation", Prompt: "forge", Inputs: []string{"change"}, Outputs: []string{"validation"}, SnapshotInputs: map[string]atc.SnapshotInputConfig{"change": {Type: repositoryChangeV1}}, SnapshotOutputs: map[string]atc.SnapshotOutputConfig{"validation": {Type: snapshot.TypeRef("validation/v1")}}}}
	withBase := func() atc.Step {
		authority := &atc.DevValidationAuthority{ProfileName: "exact", CandidateInput: "change", BaseInputs: []atc.DevValidationBaseInput{{Name: "base", Type: snapshot.TypeRef("repository/v1")}}}
		return atc.Step{Config: &atc.TaskStep{Name: "validate", FunctionID: "validate", Config: &atc.TaskConfig{Inputs: []atc.TaskInputConfig{{Name: "change"}, {Name: "base"}}, Outputs: []atc.TaskOutputConfig{{Name: "validation"}}}, SnapshotInputs: map[string]atc.SnapshotInputConfig{"change": {Type: repositoryChangeV1}, "base": {Type: snapshot.TypeRef("repository/v1")}}, SnapshotOutputs: map[string]atc.SnapshotOutputConfig{"validation": {Type: snapshot.TypeRef("validation/v1")}}, DevValidationAuthority: authority}}
	}

	base := []snapshot.Port{{Name: "change", Type: repositoryChangeV1}, {Name: "base", Type: snapshot.TypeRef("repository/v1")}}
	tests := []struct {
		name string
		plan []atc.Step
		want string
	}{
		{"exact authoritative validation passes", []atc.Step{withBase(), publish()}, ""},
		{"ordinary agent validation is rejected", []atc.Step{ordinaryValidation, publish()}, "not authoritative dev validation"},
		{"candidate overwritten after validation is rejected", []atc.Step{withBase(), overwrite("change"), publish()}, "does not dominate the current candidate"},
		{"base overwritten after validation is rejected", []atc.Step{withBase(), atc.Step{Config: &atc.AgentStep{Name: "replace-base", FunctionID: "replace-base", Prompt: "replace", Outputs: []string{"base"}, SnapshotOutputs: map[string]atc.SnapshotOutputConfig{"base": {Type: snapshot.TypeRef("repository/v1")}}}}, publish()}, "does not bind current base"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			function := &FunctionConfig{SignatureVersion: 1, Inputs: base, Plan: test.plan, DevValidationProfiles: validationProfiles(), DevValidationProvenanceHash: validationProvenance()}
			err := TypeCheckFunction(function)
			if test.want == "" {
				if err != nil {
					t.Fatalf("TypeCheckFunction: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestRequireValidationMapsHumanReviewRepositoryNames(t *testing.T) {
	candidate := snapshotBinding{
		typ:       repositoryChangeV1,
		presence:  snapshotGuaranteed,
		typed:     true,
		writePath: "candidate-producer",
	}
	base := snapshotBinding{
		typ:       snapshot.TypeRef("repository/v1"),
		presence:  snapshotGuaranteed,
		typed:     true,
		writePath: "base-producer",
	}
	validation := snapshotBinding{
		typ:       snapshot.TypeRef("validation/v1"),
		presence:  snapshotGuaranteed,
		typed:     true,
		writePath: "validation-producer",
		validation: &validationBindingProvenance{
			candidate: "candidate-producer",
			bases:     map[string]string{"base": "base-producer"},
			profile:   "exact",
		},
	}
	checker := snapshotFlowChecker{devValidationProfiles: map[string]CompiledDevValidationProfile{
		"exact": {Name: "exact"},
	}}
	inputs := map[string]atc.SnapshotInputConfig{
		"physical-candidate":  {Type: repositoryChangeV1},
		"physical-base":       {Type: snapshot.TypeRef("repository/v1")},
		"physical-validation": {Type: snapshot.TypeRef("validation/v1")},
	}
	entry := snapshotEnvironment{
		"physical-candidate":  candidate,
		"physical-base":       base,
		"physical-validation": validation,
	}
	mapping := map[string]string{
		"candidate":  "physical-candidate",
		"base":       "physical-base",
		"validation": "physical-validation",
	}

	if err := checker.requireValidation(entry, "validation", "candidate", inputs, mapping, "mapped-review"); err != nil {
		t.Fatalf("mapped validation flow: %v", err)
	}
}

func TestTypeCheckMappedHumanReviewValidation(t *testing.T) {
	authority := &atc.DevValidationAuthority{
		ProfileName:    "exact",
		CandidateInput: "candidate",
		BaseInputs: []atc.DevValidationBaseInput{{
			Name: "base",
			Type: snapshot.TypeRef("repository/v1"),
		}},
	}
	validation := atc.Step{Config: &atc.TaskStep{
		Name:       "validate",
		FunctionID: "validate",
		Config: &atc.TaskConfig{
			Inputs:  []atc.TaskInputConfig{{Name: "candidate"}, {Name: "base"}},
			Outputs: []atc.TaskOutputConfig{{Name: "validation"}},
		},
		SnapshotInputs: map[string]atc.SnapshotInputConfig{
			"candidate": {Type: repositoryChangeV1},
			"base":      {Type: snapshot.TypeRef("repository/v1")},
		},
		SnapshotOutputs: map[string]atc.SnapshotOutputConfig{
			"validation": {Type: snapshot.TypeRef("validation/v1")},
		},
		DevValidationAuthority: authority,
	}}
	review := atc.Step{Config: &atc.AgentStep{
		Name:       "review",
		FunctionID: "review",
		Prompt:     "review",
		Inputs:     []string{"change", "repository", "gate"},
		Outputs:    []string{"question"},
		InputMapping: map[string]string{
			"change":     "candidate",
			"repository": "base",
			"gate":       "validation",
		},
		SnapshotInputs: map[string]atc.SnapshotInputConfig{
			"change":     {Type: repositoryChangeV1},
			"repository": {Type: snapshot.TypeRef("repository/v1")},
			"gate":       {Type: snapshot.TypeRef("validation/v1")},
		},
		SnapshotOutputs: map[string]atc.SnapshotOutputConfig{
			"question": {Type: snapshot.TypeRef("question/v1")},
		},
		Validation: "gate",
	}}
	function := &FunctionConfig{
		SignatureVersion: 1,
		Inputs: []snapshot.Port{
			{Name: "candidate", Type: repositoryChangeV1},
			{Name: "base", Type: snapshot.TypeRef("repository/v1")},
		},
		Plan:                        []atc.Step{validation, review},
		DevValidationProfiles:       validationProfiles(),
		DevValidationProvenanceHash: validationProvenance(),
	}

	if err := TypeCheckFunction(function); err != nil {
		t.Fatalf("mapped human-review TypeCheckFunction: %v", err)
	}
}
