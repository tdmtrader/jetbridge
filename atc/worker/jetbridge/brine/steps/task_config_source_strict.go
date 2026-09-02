package steps

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"code.cloudfoundry.org/lager/v3"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/exec/build"
	"github.com/concourse/concourse/vars"
)

type TaskConfigSourceObservation struct{ Value string }

func TaskConfigSourceStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[brine.Empty, TaskConfigSourceObservation](
			"the production task config source evaluates profile {string}",
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder) (TaskConfigSourceObservation, error) {
				profile, ok := p.GetString(0)
				if !ok {
					return TaskConfigSourceObservation{}, fmt.Errorf("expected task config source profile")
				}
				value, err := observeTaskConfigSource(profile)
				return TaskConfigSourceObservation{Value: value}, err
			},
		),
		CheckString[TaskConfigSourceObservation]("the task config source observation is {string}", "task config source observation", func(in TaskConfigSourceObservation) (string, error) {
			return in.Value, nil
		}),
	}
}

func observeTaskConfigSource(profile string) (string, error) {
	ctx := context.Background()
	logger := lager.NewLogger("brine-task-config-source")
	repo := build.NewRepository()
	base := strictSourceTaskConfig()
	fetch := func(source exec.TaskConfigSource) (atc.TaskConfig, error) {
		return source.FetchConfig(ctx, logger, repo)
	}

	switch profile {
	case "static/nil":
		got, err := fetch(exec.StaticConfigSource{})
		return fmt.Sprintf("error=%s;zero=%t", errorState(err), reflect.DeepEqual(got, atc.TaskConfig{})), nil
	case "params/no-override-config":
		source := &exec.OverrideParamsConfigSource{ConfigSource: exec.StaticConfigSource{Config: &base}}
		got, err := fetch(source)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("same=%t", reflect.DeepEqual(got, base)), nil
	case "params/override-values", "params/override-warning":
		source := &exec.OverrideParamsConfigSource{
			ConfigSource: exec.StaticConfigSource{Config: &base},
			Params:       atc.TaskEnv{"PARAM": "B", "EXTRA_PARAM": "C"},
		}
		got, err := fetch(source)
		if err != nil {
			return "", err
		}
		if profile == "params/override-warning" {
			if len(source.Warnings()) == 1 && strings.Contains(source.Warnings()[0], "EXTRA_PARAM was defined in pipeline but missing from task file") {
				return "warnings=EXTRA_PARAM-missing", nil
			}
			return fmt.Sprintf("warnings=%q", source.Warnings()), nil
		}
		keys := make([]string, 0, len(got.Params))
		for key := range got.Params {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			parts = append(parts, key+":"+got.Params[key])
		}
		return "params=" + strings.Join(parts, ","), nil
	case "limits/new-success", "limits/new-values":
		without := strictSourceTaskConfig()
		without.Limits = nil
		override := atc.ContainerLimits{CPU: strictCPU(2048), Memory: strictMemory(209715200)}
		source := &exec.OverrideContainerLimitsSource{ConfigSource: exec.StaticConfigSource{Config: &without}, Limits: &override}
		got, err := fetch(source)
		if profile == "limits/new-success" {
			return "error=" + errorState(err), nil
		}
		if err != nil {
			return "", err
		}
		return "limits=" + limitsString(got.Limits), nil
	case "limits/existing-values":
		override := atc.ContainerLimits{CPU: strictCPU(2048), Memory: strictMemory(209715200)}
		got, err := fetch(&exec.OverrideContainerLimitsSource{ConfigSource: exec.StaticConfigSource{Config: &base}, Limits: &override})
		if err != nil {
			return "", err
		}
		return "limits=" + limitsString(got.Limits), nil
	case "requests/new-values":
		override := atc.ContainerLimits{CPU: strictCPU(512), Memory: strictMemory(1073741824)}
		got, err := fetch(&exec.OverrideContainerLimitsSource{ConfigSource: exec.StaticConfigSource{Config: &base}, Requests: &override})
		if err != nil {
			return "", err
		}
		return "limits=" + limitsString(got.Limits) + ";requests=" + limitsString(got.Requests), nil
	case "limits-and-requests-values":
		limits := atc.ContainerLimits{CPU: strictCPU(2048), Memory: strictMemory(209715200)}
		requests := atc.ContainerLimits{CPU: strictCPU(256)}
		got, err := fetch(&exec.OverrideContainerLimitsSource{ConfigSource: exec.StaticConfigSource{Config: &base}, Limits: &limits, Requests: &requests})
		if err != nil {
			return "", err
		}
		return "limits=" + limitsString(got.Limits) + ";requests=" + limitsString(got.Requests), nil
	case "requests-empty-values":
		without := strictSourceTaskConfig()
		without.Limits = nil
		requests := atc.ContainerLimits{Memory: strictMemory(536870912)}
		got, err := fetch(&exec.OverrideContainerLimitsSource{ConfigSource: exec.StaticConfigSource{Config: &without}, Requests: &requests})
		if err != nil {
			return "", err
		}
		return "limits=" + limitsString(got.Limits) + ";requests=" + limitsString(got.Requests), nil
	case "validating/valid":
		got, err := fetch(exec.ValidatingConfigSource{ConfigSource: exec.StaticConfigSource{Config: &base}})
		return fmt.Sprintf("error=%s;same=%t", errorState(err), reflect.DeepEqual(got, base)), nil
	case "validating/invalid":
		invalid := atc.TaskConfig{RootfsURI: "some-image", Params: atc.TaskEnv{"PARAM": "A"}, Run: atc.TaskRunConfig{Args: []string{"bananapants"}}}
		_, err := fetch(exec.ValidatingConfigSource{ConfigSource: exec.StaticConfigSource{Config: &invalid}})
		if err == nil {
			return "error=nil", nil
		}
		return "error=validation", nil
	case "interpolate/all-success", "interpolate/all-values":
		template := strictTemplateTaskConfig()
		got, err := fetch(exec.InterpolateTemplateConfigSource{ConfigSource: exec.StaticConfigSource{Config: &template}, Vars: []vars.Variables{vars.StaticVariables{"task-variable-name": "task-variable-value"}}, ExpectAllKeys: true})
		if profile == "interpolate/all-success" {
			return "error=" + errorState(err), nil
		}
		if err != nil {
			return "", err
		}
		return interpolationString(got), nil
	case "interpolate/missing-success", "interpolate/missing-values":
		template := strictTemplateTaskConfig()
		got, err := fetch(exec.InterpolateTemplateConfigSource{ConfigSource: exec.StaticConfigSource{Config: &template}, Vars: []vars.Variables{vars.StaticVariables{}}, ExpectAllKeys: false})
		if profile == "interpolate/missing-success" {
			return "error=" + errorState(err), nil
		}
		if err != nil {
			return "", err
		}
		return interpolationString(got), nil
	case "defaults/base", "defaults/custom":
		defer atc.LoadBaseResourceTypeDefaults(map[string]atc.Source{})
		var types atc.ResourceTypes
		if profile == "defaults/base" {
			atc.LoadBaseResourceTypeDefaults(map[string]atc.Source{"docker": {"some-key": "some-value"}})
		}
		if profile == "defaults/custom" {
			types = atc.ResourceTypes{{Name: "docker", Defaults: atc.Source{"some-key": "some-value"}}}
		}
		template := strictTemplateTaskConfig()
		got, err := fetch(exec.BaseResourceTypeDefaultsApplySource{ConfigSource: exec.StaticConfigSource{Config: &template}, ResourceTypes: types})
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("error=nil;default=%v", got.ImageResource.Source["some-key"]), nil
	default:
		return "", fmt.Errorf("unknown task config source profile %q", profile)
	}
}

func strictSourceTaskConfig() atc.TaskConfig {
	return atc.TaskConfig{
		Platform: "some-platform", RootfsURI: "some-image",
		Params: atc.TaskEnv{"PARAM": "A", "ORIG_PARAM": "D"},
		Limits: &atc.ContainerLimits{CPU: strictCPU(1024), Memory: strictMemory(209715200)},
		Run:    atc.TaskRunConfig{Path: "echo", Args: []string{"bananapants"}},
	}
}

func strictTemplateTaskConfig() atc.TaskConfig {
	return atc.TaskConfig{
		Platform: "some-platform", RootfsURI: "some-image",
		ImageResource: &atc.ImageResource{Type: "docker", Source: atc.Source{"a": "b", "evaluated-value": "((task-variable-name))"}, Params: atc.Params{"some": "params", "evaluated-value": "((task-variable-name))"}, Version: atc.Version{"some": "version"}},
		Params:        atc.TaskEnv{"key1": "key1-((task-variable-name))", "key2": "key2-((task-variable-name))"},
		Run:           atc.TaskRunConfig{Path: "ls", Args: []string{"-al", "((task-variable-name))"}, Dir: "some/dir", User: "some-user"},
		Inputs:        []atc.TaskInputConfig{{Name: "some-input", Path: "some-path"}},
	}
}

func strictCPU(value uint64) *atc.CPULimit       { v := atc.CPULimit(value); return &v }
func strictMemory(value uint64) *atc.MemoryLimit { v := atc.MemoryLimit(value); return &v }
func errorState(err error) string {
	if err == nil {
		return "nil"
	}
	return "non-nil"
}

func limitsString(limits *atc.ContainerLimits) string {
	if limits == nil {
		return "nil"
	}
	cpu, memory := "nil", "nil"
	if limits.CPU != nil {
		cpu = fmt.Sprintf("%d", *limits.CPU)
	}
	if limits.Memory != nil {
		memory = fmt.Sprintf("%d", *limits.Memory)
	}
	return "cpu:" + cpu + ",memory:" + memory
}

func interpolationString(config atc.TaskConfig) string {
	return fmt.Sprintf("args=%s;params=%s,%s;source=%v", strings.Join(config.Run.Args, ","), config.Params["key1"], config.Params["key2"], config.ImageResource.Source["evaluated-value"])
}
