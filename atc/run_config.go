package atc

import (
	"encoding/json"

	"github.com/concourse/concourse/vars"
)

// MaterializeRunConfig produces the concrete config for a pipeline-run
// instance: ((param)) references are resolved from the validated params and
// two reserved vars (which take precedence over params) — ((run)), the
// per-template run NUMBER (also the instance var), and ((run_id)), the
// globally-unique pipeline_runs.id allocated before materialization
// (shared-contracts §7.1 item 9; F30 2026-07-09: run numbers reset per
// template, so cross-template consumers such as §8.1 AGENT_PIPELINE_RUN_ID
// must interpolate ((run_id))). Reactive
// semantics are stripped: get steps WITHOUT passed: constraints have
// trigger: true rewritten to false (external resource versions never
// trigger a run-instance build). Gets WITH passed: keep their trigger flag
// — the scheduler only auto-creates builds for trigger: true inputs, so
// this is what lets downstream jobs flow through passed: chains as normal
// (spec §3). Unresolved ((vars)) are left intact for runtime var sources,
// matching the set_pipeline step (Resolve(false)).
func MaterializeRunConfig(template Config, runNumber int, runID int, params map[string]any) (Config, error) {
	payload, err := json.Marshal(template)
	if err != nil {
		return Config{}, err
	}

	staticVars := []vars.Variables{
		vars.StaticVariables{"run": runNumber, "run_id": runID},
		vars.StaticVariables(params),
	}

	resolved, err := vars.NewTemplateResolver(payload, staticVars).Resolve(false)
	if err != nil {
		return Config{}, err
	}

	var config Config
	err = UnmarshalConfig(resolved, &config)
	if err != nil {
		return Config{}, err
	}

	for i := range config.Jobs {
		err = config.Jobs[i].StepConfig().Visit(StepRecursor{
			OnGet: func(step *GetStep) error {
				// suppress external-version triggering only; passed:
				// chains keep their trigger flag so downstream jobs flow
				if len(step.Passed) == 0 {
					step.Trigger = false
				}
				return nil
			},
		})
		if err != nil {
			return Config{}, err
		}
	}

	return config, nil
}

// EntryJobs returns the names of jobs with no passed: constraints on any
// input — the jobs auto-triggered when a run is created.
func (c Config) EntryJobs() []string {
	var names []string
	for _, job := range c.Jobs {
		entry := true
		for _, input := range job.Inputs() {
			if len(input.Passed) > 0 {
				entry = false
				break
			}
		}
		if entry {
			names = append(names, job.Name)
		}
	}
	return names
}
