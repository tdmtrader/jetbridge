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
		vars.StaticVariables{
			"run":    identity.Number,
			"run_id": identity.ID,
		},
		vars.StaticVariables(runParamsForInterpolation(normalizedParams)),
	}).Resolve(false)
	if err != nil {
		return RunMaterialization{}, err
	}

	var materialized Config
	if err := UnmarshalConfig(resolvedPayload, &materialized); err != nil {
		return RunMaterialization{}, err
	}
	materialized.Template = false
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
