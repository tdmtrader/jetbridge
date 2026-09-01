package steps

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/configvalidate"

	_ "github.com/concourse/concourse/atc/creds/dummy"
)

type ConfigValidationOutcome struct {
	Warnings []string
	Errors   []string
}

// ConfigValidationDefinitions drives the same production validator used when
// a pipeline is configured. Profiles are named after operator-visible invalid
// configurations; no parser, validator, warning, or error is replaced.
func ConfigValidationDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[brine.Empty, ConfigValidationOutcome](
			"the production pipeline validator checks profile {string}",
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder) (ConfigValidationOutcome, error) {
				profile, ok := p.GetString(0)
				if !ok {
					return ConfigValidationOutcome{}, fmt.Errorf("expected validation profile")
				}
				config, err := configValidationProfile(profile)
				if err != nil {
					return ConfigValidationOutcome{}, err
				}
				warnings, errors := configvalidate.Validate(config)
				out := ConfigValidationOutcome{Errors: errors}
				for _, warning := range warnings {
					out.Warnings = append(out.Warnings, warning.Message)
				}
				return out, nil
			},
		),
		brine.DefineCheck[ConfigValidationOutcome](
			"validation returns {int} warnings and {int} errors",
			func(in ConfigValidationOutcome, p brine.Params, _ *brine.Recorder) error {
				warnings, ok := p.GetInt(0)
				if !ok {
					return fmt.Errorf("expected warning count")
				}
				errors, ok := p.GetInt(1)
				if !ok {
					return fmt.Errorf("expected error count")
				}
				if len(in.Warnings) != warnings || len(in.Errors) != errors {
					return fmt.Errorf("validation returned %d warnings and %d errors; warnings=%q errors=%q", len(in.Warnings), len(in.Errors), in.Warnings, in.Errors)
				}
				return nil
			},
		),
		brine.DefineCheck[ConfigValidationOutcome](
			"the validation diagnostics contain {string}",
			func(in ConfigValidationOutcome, p brine.Params, _ *brine.Recorder) error {
				expected, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected diagnostic fragments")
				}
				if expected == "none" {
					return nil
				}
				all := strings.Join(append(append([]string{}, in.Warnings...), in.Errors...), "\n")
				for _, fragment := range strings.Split(expected, " ;; ") {
					if !strings.Contains(all, fragment) {
						return fmt.Errorf("validation diagnostics %q do not contain %q", all, fragment)
					}
				}
				return nil
			},
		),
	}
}

func configValidationProfile(profile string) (atc.Config, error) {
	config := baseValidationConfig()
	if strings.HasPrefix(profile, "job-hook/") {
		return configValidationHookProfile(config, profile)
	}
	if strings.HasPrefix(profile, "nested/") {
		return configValidationNestedProfile(config, profile)
	}
	if strings.HasPrefix(profile, "cross-job/") {
		return configValidationCrossJobProfile(config, profile)
	}
	if strings.HasPrefix(profile, "cycle/") {
		return configValidationCycleProfile(profile)
	}
	switch profile {
	case "valid":
	case "identifier/group":
		config.Groups = append(config.Groups, atc.GroupConfig{Name: "_some-group", Jobs: []string{"some-job"}})
	case "identifier/resource":
		config.Resources = append(config.Resources, atc.ResourceConfig{Name: "_some-resource", Type: "some-type"})
	case "identifier/resource-type":
		config.ResourceTypes = append(config.ResourceTypes, atc.ResourceType{Name: "_some-resource-type", Type: "some-type"})
	case "identifier/prototype":
		config.Prototypes = append(config.Prototypes, atc.Prototype{Name: "_some-prototype", Type: "some-type"})
	case "identifier/var-source":
		config.VarSources = append(config.VarSources, atc.VarSourceConfig{Name: "_some-var-source", Type: "dummy", Config: ""})
	case "identifier/job":
		config.Jobs = append(config.Jobs, atc.JobConfig{Name: "_some-job"})
	case "identifier/steps":
		config.Jobs[0].PlanSequence = append(config.Jobs[0].PlanSequence,
			atc.Step{Config: &atc.GetStep{Name: "_get-step"}},
			atc.Step{Config: &atc.TaskStep{Name: "_task-step"}},
			atc.Step{Config: &atc.PutStep{Name: "_put-step"}},
			atc.Step{Config: &atc.RunStep{Message: "_run-step"}},
			atc.Step{Config: &atc.SetPipelineStep{Name: "_set-pipeline-step"}},
			atc.Step{Config: &atc.LoadVarStep{Name: "_load-var-step"}},
		)
	case "group/unknown-resource":
		config.Groups = append(config.Groups, atc.GroupConfig{Name: "bogus", Resources: []string{"bogus-resource"}})
	case "group/unknown-job-glob":
		config.Groups = append(config.Groups, atc.GroupConfig{Name: "bogus", Jobs: []string{"bogus-*"}})
	case "group/jobs-excluded":
		config.Jobs = append(config.Jobs, atc.JobConfig{Name: "stand-alone-job"}, atc.JobConfig{Name: "other-stand-alone-job"})
	case "group/duplicate-twice":
		config.Groups = append(config.Groups, atc.GroupConfig{Name: "some-group"})
	case "group/duplicate-four-times":
		config.Groups = append(config.Groups, atc.GroupConfig{Name: "some-group"}, atc.GroupConfig{Name: "some-group"}, atc.GroupConfig{Name: "some-group"})
	case "group/invalid-glob":
		config.Groups = append(config.Groups, atc.GroupConfig{Name: "a-group", Jobs: []string{"some-bad-glob-[0-9"}})
	case "var-source/unknown-type":
		config.VarSources = append(config.VarSources, atc.VarSourceConfig{Name: "some", Type: "some", Config: ""})
	case "var-source/invalid-config":
		config.VarSources = append(config.VarSources, atc.VarSourceConfig{Name: "some", Type: "dummy", Config: ""})
	case "var-source/duplicate":
		config.VarSources = append(config.VarSources,
			atc.VarSourceConfig{Name: "some", Type: "dummy", Config: dummyVarSourceConfig("v2")},
			atc.VarSourceConfig{Name: "some", Type: "dummy", Config: dummyVarSourceConfig("v2")},
		)
	case "var-source/unresolved":
		config.VarSources = append(config.VarSources,
			atc.VarSourceConfig{Name: "s1", Type: "dummy", Config: dummyVarSourceConfig("v")},
			atc.VarSourceConfig{Name: "s2", Type: "dummy", Config: dummyVarSourceConfig("((s1:k))")},
			atc.VarSourceConfig{Name: "s3", Type: "dummy", Config: dummyVarSourceConfig("((none:k))")},
		)
	case "var-source/circular":
		config.VarSources = append(config.VarSources,
			atc.VarSourceConfig{Name: "s1", Type: "dummy", Config: dummyVarSourceConfig("((s3:v))")},
			atc.VarSourceConfig{Name: "s2", Type: "dummy", Config: dummyVarSourceConfig("((s1:k))")},
			atc.VarSourceConfig{Name: "s3", Type: "dummy", Config: dummyVarSourceConfig("((s2:k))")},
		)
	case "resource/no-name":
		config.Resources = append(config.Resources, atc.ResourceConfig{})
	case "resource/no-type":
		config.Resources = append(config.Resources, atc.ResourceConfig{Name: "bogus-resource"})
	case "resource/no-name-or-type":
		config.Resources = append(config.Resources, atc.ResourceConfig{})
	case "resource/duplicate":
		config.Resources = append(config.Resources, config.Resources[0])
	case "resource/unused-and-aliased":
		config = atc.Config{
			Resources: atc.ResourceConfigs{
				{Name: "unused-resource", Type: "some-type"},
				{Name: "get-alias", Type: "some-type"},
				{Name: "put-alias", Type: "some-type"},
				{Name: "some-resource", Type: "some-type"},
			},
			Jobs: atc.JobConfigs{{Name: "some-job", PlanSequence: []atc.Step{
				{Config: &atc.GetStep{Name: "get-alias", Resource: "some-resource"}},
				{Config: &atc.PutStep{Name: "put-alias", Resource: "some-resource"}},
			}}},
		}
	case "resource-type/no-name":
		config.ResourceTypes = append(config.ResourceTypes, atc.ResourceType{})
	case "resource-type/no-type":
		config.ResourceTypes = append(config.ResourceTypes, atc.ResourceType{Name: "bogus-resource-type"})
	case "resource-type/no-name-or-type":
		config.ResourceTypes = append(config.ResourceTypes, atc.ResourceType{})
	case "resource-type/image-only":
		config.ResourceTypes = append(config.ResourceTypes, atc.ResourceType{Name: "image-ref-type", Image: "my-registry/custom-resource:latest"})
	case "resource-type/image-and-type":
		config.ResourceTypes = append(config.ResourceTypes, atc.ResourceType{Name: "conflicting-type", Type: "registry-image", Image: "my-registry/custom-resource:latest"})
	case "resource-type/duplicate":
		config.ResourceTypes = append(config.ResourceTypes, config.ResourceTypes[0])
	case "prototype/no-name":
		config.Prototypes = append(config.Prototypes, atc.Prototype{})
	case "prototype/no-type":
		config.Prototypes = append(config.Prototypes, atc.Prototype{Name: "bogus-prototype"})
	case "prototype/no-name-or-type":
		config.Prototypes = append(config.Prototypes, atc.Prototype{})
	case "prototype/duplicate":
		config.Prototypes = append(config.Prototypes, config.Prototypes[0])
	case "prototype/name-conflicts-with-resource-type":
		config.Prototypes = append(config.Prototypes, atc.Prototype{Name: "some-resource-type", Type: "some-type"})
	case "display/http":
		config.Display = &atc.DisplayConfig{BackgroundImage: "http://example.com/image.jpg"}
	case "display/relative":
		config.Display = &atc.DisplayConfig{BackgroundImage: "public/images/image.jpg"}
	case "display/unsupported-scheme":
		config.Display = &atc.DisplayConfig{BackgroundImage: "data:image/png;base64, iVBORw0KGgoA"}
	case "display/invalid-url":
		config.Display = &atc.DisplayConfig{BackgroundImage: "://example.com"}
	case "pipeline/no-jobs":
		config = atc.Config{}
	case "job/no-name":
		config.Groups = nil
		config.Jobs = append(config.Jobs, atc.JobConfig{})
	case "job/appended-negative-build-logs":
		config.Groups = nil
		config.Jobs = append(config.Jobs, atc.JobConfig{Name: "some-other-job", BuildLogsToRetain: -1})
	case "job/duplicate-inputs":
		config.Groups = nil
		config.Jobs = append(config.Jobs, atc.JobConfig{Name: "some-other-job", PlanSequence: []atc.Step{
			{Config: &atc.GetStep{Name: "some-resource"}},
			{Config: &atc.GetStep{Name: "some-resource"}},
			{Config: &atc.GetStep{Name: "some-resource"}},
		}})
	case "job/duplicate-input-names":
		config.Groups = nil
		config.Jobs = append(config.Jobs, atc.JobConfig{Name: "some-other-job", PlanSequence: []atc.Step{
			{Config: &atc.GetStep{Name: "some-resource", Resource: "a"}},
			{Config: &atc.GetStep{Name: "some-resource", Resource: "b"}},
			{Config: &atc.GetStep{Name: "some-resource", Resource: "c"}},
		}})
	case "job/same-resource-different-names":
		config.Groups = nil
		config.Jobs = append(config.Jobs, atc.JobConfig{Name: "some-other-job", PlanSequence: []atc.Step{
			{Config: &atc.GetStep{Name: "a", Resource: "some-resource"}},
			{Config: &atc.GetStep{Name: "b", Resource: "some-resource"}},
		}})
	case "job/duplicate-name":
		config.Jobs = append(config.Jobs, config.Jobs...)
	case "job/both-retention-fields":
		config.Jobs[0].BuildLogRetention = &atc.BuildLogRetention{Builds: 1, Days: 1}
		config.Jobs[0].BuildLogsToRetain = 1
	case "job/negative-build-logs":
		config.Jobs[0].BuildLogsToRetain = -1
	case "job/deprecated-build-logs":
		config.Jobs[0].BuildLogsToRetain = 1
	case "job/negative-retention":
		config.Jobs[0].BuildLogRetention = &atc.BuildLogRetention{Builds: -1, Days: -1}
	case "plan/task-missing-config-and-name":
		config.Groups = nil
		config.Jobs = append(config.Jobs, atc.JobConfig{Name: "some-other-job", PlanSequence: []atc.Step{{Config: &atc.TaskStep{}}}})
	case "plan/task-file-and-config":
		config.Groups = nil
		config.Jobs = append(config.Jobs, atc.JobConfig{Name: "some-other-job", PlanSequence: []atc.Step{{Config: &atc.TaskStep{
			Name: "lol", ConfigPath: "task.yml", Config: &atc.TaskConfig{Params: atc.TaskEnv{"param1": "value1"}},
		}}}})
	case "plan/task-invalid-inline":
		config.Groups = nil
		config.Jobs = append(config.Jobs, atc.JobConfig{Name: "some-other-job", PlanSequence: []atc.Step{{Config: &atc.TaskStep{
			Name: "some-resource", Config: &atc.TaskConfig{Params: atc.TaskEnv{"param1": "value1"}},
		}}}})
	case "plan/task-hermetic":
		config.Groups = nil
		config.Jobs = append(config.Jobs, atc.JobConfig{Name: "some-other-job", PlanSequence: []atc.Step{{Config: &atc.TaskStep{
			Name: "some-resource", Hermetic: true, Config: &atc.TaskConfig{Params: atc.TaskEnv{"param1": "value1"}},
		}}}})
	case "plan/sidecar-missing-name":
		config = appendValidationTask(config, &atc.TaskStep{Name: "sidecar-task", ConfigPath: "task.yml", Sidecars: []atc.SidecarSource{{Config: &atc.SidecarConfig{Image: "redis:7"}}}})
	case "plan/sidecar-missing-image":
		config = appendValidationTask(config, &atc.TaskStep{Name: "sidecar-task", ConfigPath: "task.yml", Sidecars: []atc.SidecarSource{{Config: &atc.SidecarConfig{Name: "redis"}}}})
	case "plan/sidecar-reserved-name":
		config = appendValidationTask(config, &atc.TaskStep{Name: "sidecar-task", ConfigPath: "task.yml", Sidecars: []atc.SidecarSource{{Config: &atc.SidecarConfig{Name: "main", Image: "redis:7"}}}})
	case "plan/sidecar-valid":
		config = appendValidationTask(config, &atc.TaskStep{Name: "sidecar-task", ConfigPath: "task.yml", Sidecars: []atc.SidecarSource{{Config: &atc.SidecarConfig{Name: "redis", Image: "redis:7"}}}})
	case "plan/skip-download-registry-image":
		config = appendValidationGet(config, atc.ResourceConfig{Name: "my-image", Type: "registry-image", Source: atc.Source{"repository": "golang"}}, atc.GetStep{Name: "my-image", SkipDownload: true})
	case "plan/skip-download-image-field":
		config.Groups = nil
		config.ResourceTypes = append(config.ResourceTypes, atc.ResourceType{Name: "custom-image", Image: "my-org/custom-image-resource:latest"})
		config.Resources = append(config.Resources, atc.ResourceConfig{Name: "app-image", Type: "custom-image", Source: atc.Source{"repository": "my-org/app"}})
		config.Jobs = append(config.Jobs, atc.JobConfig{Name: "some-other-job", PlanSequence: []atc.Step{{Config: &atc.GetStep{Name: "app-image", SkipDownload: true}}}})
	case "plan/skip-download-non-image":
		config = appendValidationGet(config, atc.ResourceConfig{Name: "my-repo", Type: "git", Source: atc.Source{"uri": "https://example.com/repo.git"}}, atc.GetStep{Name: "my-repo", SkipDownload: true})
	case "plan/normal-download-any-type":
		config = appendValidationGet(config, atc.ResourceConfig{Name: "my-repo", Type: "git", Source: atc.Source{"uri": "https://example.com/repo.git"}}, atc.GetStep{Name: "my-repo"})
	case "plan/put-existing":
		config = appendValidationStep(config, &atc.PutStep{Name: "some-resource"})
	case "plan/get-missing":
		config = appendValidationStep(config, &atc.GetStep{Name: "some-nonexistent-resource"})
	case "plan/put-missing":
		config = appendValidationStep(config, &atc.PutStep{Name: "some-nonexistent-resource"})
	case "plan/run-missing-prototype":
		config = appendValidationStep(config, &atc.RunStep{Message: "some-message", Type: "some-nonexistent-prototype"})
	case "plan/get-custom-existing":
		config = appendValidationStep(config, &atc.GetStep{Name: "custom-name", Resource: "some-resource"})
	case "plan/get-custom-missing":
		config = appendValidationStep(config, &atc.GetStep{Name: "custom-name", Resource: "some-missing-resource"})
	case "plan/put-custom-existing":
		config = appendValidationStep(config, &atc.PutStep{Name: "custom-name", Resource: "some-resource"})
	case "plan/put-custom-missing":
		config = appendValidationStep(config, &atc.PutStep{Name: "custom-name", Resource: "some-missing-resource"})
	case "plan/invalid-timeout":
		config = appendValidationStep(config, &atc.TimeoutStep{Step: &atc.GetStep{Name: "some-resource"}, Duration: "nope"})
	case "plan/non-positive-retry":
		config = appendValidationStep(config, &atc.RetryStep{Step: &atc.PutStep{Name: "some-resource"}, Attempts: 0})
	case "plan/set-pipeline-empty":
		config = appendValidationStep(config, &atc.SetPipelineStep{})
	case "passed/bogus-job":
		config = appendValidationStep(config, &atc.GetStep{Name: "lol", Passed: []string{"bogus-job"}})
	case "passed/unmatched-glob":
		config = appendValidationStep(config, &atc.GetStep{Name: "lol", Passed: []string{"bogus-*"}})
	case "passed/valid-output":
		config.Groups = nil
		config.Jobs[0].PlanSequence = append(config.Jobs[0].PlanSequence, atc.Step{Config: &atc.PutStep{Name: "custom-name", Resource: "some-resource"}})
		config.Jobs = append(config.Jobs, atc.JobConfig{Name: "some-other-job", PlanSequence: []atc.Step{{Config: &atc.GetStep{Name: "some-resource", Passed: []string{"some-job"}}}}})
	case "passed/valid-input":
		config = appendValidationStep(config, &atc.GetStep{Name: "some-resource", Passed: []string{"some-job"}})
	case "passed/valid-glob":
		config = appendValidationStep(config, &atc.GetStep{Name: "some-resource", Passed: []string{"some-j*"}})
	case "passed/custom-name":
		config = appendValidationStep(config, &atc.GetStep{Name: "custom-name", Resource: "some-resource", Passed: []string{"some-job"}})
	case "passed/job-does-not-use-resource":
		config = appendValidationStep(config, &atc.GetStep{Name: "some-resource", Passed: []string{"some-empty-job"}})
	case "load-var/empty":
		config = appendValidationStep(config, &atc.LoadVarStep{})
	case "load-var/duplicate":
		config.Groups = nil
		config.Jobs = append(config.Jobs, atc.JobConfig{Name: "some-other-job", PlanSequence: []atc.Step{
			{Config: &atc.LoadVarStep{Name: "a-var", File: "file1"}},
			{Config: &atc.LoadVarStep{Name: "a-var", File: "file1"}},
		}})
	case "plan/unknown-field":
		config.Groups = nil
		config.Jobs = append(config.Jobs, atc.JobConfig{Name: "some-other-job", PlanSequence: []atc.Step{{
			Config: &atc.TaskStep{Name: "task", ConfigPath: "some-file"}, UnknownFields: map[string]*json.RawMessage{"bogus": nil},
		}}})
	case "across/valid":
		config = appendValidationStep(config, &atc.AcrossStep{Step: &atc.PutStep{Name: "some-resource"}, Vars: []atc.AcrossVarConfig{
			{Var: "var1", Values: []any{"v1", "v2"}},
			{Var: "var2", MaxInFlight: &atc.MaxInFlightConfig{Limit: 2}, Values: []any{"v1", "v2"}},
			{Var: "var3", MaxInFlight: &atc.MaxInFlightConfig{All: true}, Values: []any{"v1", "v2"}},
		}})
	case "across/no-vars":
		config = appendValidationStep(config, &atc.AcrossStep{Step: &atc.PutStep{Name: "some-resource"}})
	case "across/repeated-var":
		config = appendValidationStep(config, &atc.AcrossStep{Step: &atc.PutStep{Name: "some-resource"}, Vars: []atc.AcrossVarConfig{{Var: "var1"}, {Var: "var1"}}})
	case "across/shadows-parent":
		config.Groups = nil
		config.Jobs = append(config.Jobs, atc.JobConfig{Name: "some-other-job", PlanSequence: []atc.Step{
			{Config: &atc.LoadVarStep{Name: "var1", File: "unused"}},
			{Config: &atc.AcrossStep{Step: &atc.PutStep{Name: "some-resource"}, Vars: []atc.AcrossVarConfig{{Var: "var1"}}}},
		}})
	case "across/substep-shadows-parent":
		config.Groups = nil
		config.Jobs = append(config.Jobs, atc.JobConfig{Name: "some-other-job", PlanSequence: []atc.Step{
			{Config: &atc.LoadVarStep{Name: "a", File: "unused"}},
			{Config: &atc.AcrossStep{Step: &atc.LoadVarStep{Name: "a", File: "unused"}, Vars: []atc.AcrossVarConfig{{Var: "b"}}}},
		}})
	case "across/non-positive-limit":
		config = appendValidationStep(config, &atc.AcrossStep{Step: &atc.PutStep{Name: "some-resource"}, Vars: []atc.AcrossVarConfig{{Var: "var", MaxInFlight: &atc.MaxInFlightConfig{Limit: 0}}}})
	default:
		return atc.Config{}, fmt.Errorf("unknown config validation profile %q", profile)
	}
	return config, nil
}

func dummyVarSourceConfig(value string) map[string]any {
	return map[string]any{"vars": map[string]any{"k": value}}
}

func appendValidationTask(config atc.Config, task *atc.TaskStep) atc.Config {
	return appendValidationStep(config, task)
}

func appendValidationGet(config atc.Config, resource atc.ResourceConfig, get atc.GetStep) atc.Config {
	config.Resources = append(config.Resources, resource)
	return appendValidationStep(config, &get)
}

func appendValidationStep(config atc.Config, step atc.StepConfig) atc.Config {
	config.Groups = nil
	config.Jobs = append(config.Jobs, atc.JobConfig{
		Name: "some-other-job", PlanSequence: []atc.Step{{Config: step}},
	})
	return config
}

func configValidationHookProfile(config atc.Config, profile string) (atc.Config, error) {
	parts := strings.Split(profile, "/")
	if len(parts) != 3 {
		return atc.Config{}, fmt.Errorf("invalid job hook profile %q", profile)
	}
	hook, existence := parts[1], parts[2]
	resource := "some-resource"
	if existence == "missing" {
		resource = "some-nonexistent-resource"
	} else if existence != "existing" {
		return atc.Config{}, fmt.Errorf("invalid hook existence %q", existence)
	}
	step := &atc.Step{Config: &atc.GetStep{Name: resource}}
	job := atc.JobConfig{Name: "some-other-job"}
	switch hook {
	case "success":
		job.OnSuccess = step
	case "failure":
		job.OnFailure = step
	case "error":
		job.OnError = step
	case "abort":
		job.OnAbort = step
	case "ensure":
		job.Ensure = step
	default:
		return atc.Config{}, fmt.Errorf("invalid job hook %q", hook)
	}
	config.Groups = nil
	config.Jobs = append(config.Jobs, job)
	return config, nil
}

func configValidationNestedProfile(config atc.Config, profile string) (atc.Config, error) {
	kind := strings.TrimPrefix(profile, "nested/")
	body := &atc.GetStep{Name: "some-resource"}
	hook := atc.Step{Config: &atc.PutStep{Name: "custom-name", Resource: "some-missing-resource"}}
	var step atc.StepConfig
	switch kind {
	case "abort":
		step = &atc.OnAbortStep{Step: body, Hook: hook}
	case "error":
		step = &atc.OnErrorStep{Step: body, Hook: hook}
	case "ensure":
		step = &atc.EnsureStep{Step: body, Hook: hook}
	case "success":
		step = &atc.OnSuccessStep{Step: body, Hook: hook}
	case "failure":
		step = &atc.OnFailureStep{Step: body, Hook: hook}
	case "try":
		step = &atc.TryStep{Step: hook}
	default:
		return atc.Config{}, fmt.Errorf("invalid nested profile %q", profile)
	}
	return appendValidationStep(config, step), nil
}

func configValidationCrossJobProfile(config atc.Config, profile string) (atc.Config, error) {
	kind := strings.TrimPrefix(profile, "cross-job/")
	var producer atc.StepConfig
	switch kind {
	case "hook-put":
		producer = &atc.OnSuccessStep{Step: &atc.TaskStep{Name: "job-one", ConfigPath: "job-one-config-path"}, Hook: atc.Step{Config: &atc.PutStep{Name: "some-resource"}}}
	case "hook-get":
		producer = &atc.OnSuccessStep{Step: &atc.TaskStep{Name: "job-one", ConfigPath: "job-one-config-path"}, Hook: atc.Step{Config: &atc.GetStep{Name: "some-resource"}}}
	case "try-put":
		producer = &atc.TryStep{Step: atc.Step{Config: &atc.PutStep{Name: "some-resource"}}}
	case "try-get":
		producer = &atc.TryStep{Step: atc.Step{Config: &atc.GetStep{Name: "some-resource"}}}
	default:
		return atc.Config{}, fmt.Errorf("invalid cross-job profile %q", profile)
	}
	config.Groups = nil
	config.Jobs = append(config.Jobs,
		atc.JobConfig{Name: "job-one", PlanSequence: []atc.Step{{Config: producer}}},
		atc.JobConfig{Name: "job-two", PlanSequence: []atc.Step{{Config: &atc.GetStep{Name: "some-resource", Passed: []string{"job-one"}}}}},
	)
	return config, nil
}

func configValidationCycleProfile(profile string) (atc.Config, error) {
	resource := atc.ResourceConfigs{{Name: "some-resource", Type: "some-type"}}
	job := func(name string, passed ...string) atc.JobConfig {
		return atc.JobConfig{Name: name, PlanSequence: []atc.Step{{Config: &atc.GetStep{
			Name: "some-input", Resource: "some-resource", Passed: passed,
		}}}}
	}
	config := atc.Config{Resources: resource}
	switch profile {
	case "cycle/self":
		config.Jobs = atc.JobConfigs{job("some-job-1", "some-job-1")}
	case "cycle/multiple-jobs":
		config.Jobs = atc.JobConfigs{
			job("some-job-1", "some-job-2"), job("some-job-2", "some-job-3"),
			job("some-job-3", "some-job-4"), job("some-job-4", "some-job-2"),
		}
	case "cycle/glob":
		config.Jobs = atc.JobConfigs{
			job("some-job-1", "some-job-2"), job("some-job-2", "some-job-3"),
			job("some-job-3", "some-job-4"), job("some-job-4", "some-job-*"),
		}
	case "cycle/multiple-passes-acyclic":
		config.Jobs = atc.JobConfigs{
			job("some-job-1", "some-job-2"), job("some-job-2", "some-job-3", "some-job-4"),
			job("some-job-3"), job("some-job-4", "some-job-3"),
		}
	case "cycle/none":
		config = baseValidationConfig()
	default:
		return atc.Config{}, fmt.Errorf("invalid cycle profile %q", profile)
	}
	return config, nil
}

func baseValidationConfig() atc.Config {
	return atc.Config{
		Groups: atc.GroupConfigs{
			{Name: "some-group", Jobs: []string{"some-job"}, Resources: []string{"some-resource"}},
			{Name: "some-other-group", Jobs: []string{"some-empty-*"}},
		},
		Resources:     atc.ResourceConfigs{{Name: "some-resource", Type: "some-type", Source: atc.Source{"source-config": "some-value"}}},
		ResourceTypes: atc.ResourceTypes{{Name: "some-resource-type", Type: "some-type", Source: atc.Source{"source-config": "some-value"}}},
		Prototypes:    atc.Prototypes{{Name: "some-prototype", Type: "some-type", Source: atc.Source{"source-config": "some-value"}}},
		Jobs: atc.JobConfigs{
			{Name: "some-job", PlanSequence: []atc.Step{{Config: &atc.GetStep{Name: "some-input", Resource: "some-resource"}}}},
			{Name: "some-empty-job"},
		},
	}
}
