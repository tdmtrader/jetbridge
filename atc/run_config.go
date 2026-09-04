package atc

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/concourse/concourse/vars"
)

type RunIdentity struct {
	Number int
	ID     int
}

type RunMaterialization struct {
	Config             Config
	CanonicalJSON      []byte
	EntryJobNames      []string
	ExpectedJobNames   map[string]bool
	PolicyKeyByJobName map[string]string
}

func MaterializeRunConfig(config Config, identity RunIdentity, params RunParams) (RunMaterialization, error) {
	normalizedParams, err := ValidateRunParams(config.Params, params)
	if err != nil {
		return RunMaterialization{}, err
	}

	payload, err := json.Marshal(config)
	if err != nil {
		return RunMaterialization{}, err
	}

	resolvedPayload, err := vars.NewTemplateResolver(payload, []vars.Variables{
		runScopedVariables{vars.StaticVariables{
			"run":    identity.Number,
			"run_id": identity.ID,
		}},
		runScopedVariables{vars.StaticVariables(runParamsForInterpolation(normalizedParams))},
	}).Resolve(false)
	if err != nil {
		return RunMaterialization{}, err
	}

	var materialized Config
	if err := UnmarshalConfig(resolvedPayload, &materialized); err != nil {
		return RunMaterialization{}, err
	}
	// A materialized run config is a payload, not a template: it declares no
	// parameters of its own and carries no retention policy. Both belong to the
	// template row and are only ever read from there — effectiveConfig rebuilds
	// the template config from the template pipeline, every retention predicate
	// in pipeline_run_reclaim.go joins pipeline_runs back to its template, and
	// present.Pipeline exposes a parameter schema only when the pipeline is a
	// template. Leaving them here makes the payload's stored config, its
	// canonical JSON and its config hash all claim a schema the pipeline does
	// not have, so `fly get-pipeline` on a run emits a config that
	// configvalidate.ValidateTemplateDeclaration refuses.
	materialized.Template = false
	materialized.Params = nil
	materialized.RunRetention = nil
	clearUnpassedTriggers(materialized.Jobs)

	policyKeys, err := policyKeysByMaterializedJobName(config.Jobs, materialized.Jobs)
	if err != nil {
		return RunMaterialization{}, err
	}
	canonicalJSON, err := json.Marshal(materialized)
	if err != nil {
		return RunMaterialization{}, err
	}

	entries := entryJobNames(materialized.Jobs)
	return RunMaterialization{
		Config:             materialized,
		CanonicalJSON:      canonicalJSON,
		EntryJobNames:      entries,
		ExpectedJobNames:   expectedJobNames(materialized.Jobs, entries),
		PolicyKeyByJobName: policyKeys,
	}, nil
}

// runScopedVariables resolves only whole-word references. A dotted reference
// such as ((db.password)) or ((run.foo)) is an ordinary Concourse variable even
// when its first segment names a declared parameter or a reserved run value --
// template_placeholders.go and vars.NewExactReferenceExclusion both document
// that materialization leaves it entirely alone -- so it must be reported as
// absent for a runtime var source to resolve, rather than traversed into a
// scalar and rejected with a field error.
type runScopedVariables struct {
	vars.StaticVariables
}

func (v runScopedVariables) Get(ref vars.Reference) (any, bool, error) {
	if len(ref.Fields) > 0 {
		return nil, false, nil
	}
	return v.StaticVariables.Get(ref)
}

func runParamsForInterpolation(params RunParams) RunParams {
	interpolationParams := make(RunParams, len(params))
	for name, value := range params {
		if number, ok := value.(float64); ok {
			interpolationParams[name] = json.Number(strconv.FormatFloat(number, 'f', -1, 64))
			continue
		}
		interpolationParams[name] = value
	}
	return interpolationParams
}

func clearUnpassedTriggers(jobs JobConfigs) {
	for _, job := range jobs {
		_ = job.StepConfig().Visit(StepRecursor{
			OnGet: func(step *GetStep) error {
				if len(step.Passed) == 0 {
					step.Trigger = false
				}
				return nil
			},
		})
	}
}

func policyKeysByMaterializedJobName(sourceJobs, materializedJobs JobConfigs) (map[string]string, error) {
	if len(sourceJobs) != len(materializedJobs) {
		return nil, fmt.Errorf("materialized job count does not match template")
	}

	keys := make(map[string]string, len(materializedJobs))
	seenNames := make(map[string]struct{}, len(materializedJobs))
	for index, job := range materializedJobs {
		if _, found := seenNames[job.Name]; found {
			return nil, fmt.Errorf("duplicate job name %s", job.Name)
		}
		seenNames[job.Name] = struct{}{}
		if job.Name != sourceJobs[index].Name {
			keys[job.Name] = sourceJobs[index].Name
		}
	}
	return keys, nil
}

func entryJobNames(jobs JobConfigs) []string {
	entries := []string{}
	for _, job := range jobs {
		entry := true
		for _, input := range job.Inputs() {
			if len(input.Passed) > 0 {
				entry = false
				break
			}
		}
		if entry {
			entries = append(entries, job.Name)
		}
	}
	return entries
}

func expectedJobNames(jobs JobConfigs, entries []string) map[string]bool {
	expected := make(map[string]bool, len(entries))
	for _, entry := range entries {
		expected[entry] = true
	}

	for changed := true; changed; {
		changed = false
		for _, job := range jobs {
			if expected[job.Name] || !jobHasTriggeredReachableInput(job, expected) {
				continue
			}
			expected[job.Name] = true
			changed = true
		}
	}

	return expected
}

func jobHasTriggeredReachableInput(job JobConfig, expected map[string]bool) bool {
	hasTriggeredPassedInput := false
	for _, input := range job.Inputs() {
		if len(input.Passed) == 0 {
			continue
		}
		if input.Trigger {
			hasTriggeredPassedInput = true
		}

		for _, passedJobName := range input.Passed {
			if !expected[passedJobName] {
				return false
			}
		}
	}

	return hasTriggeredPassedInput
}
