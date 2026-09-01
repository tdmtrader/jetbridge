package steps

import (
	"encoding/base64"
	"fmt"
	"sort"
	"strings"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
)

type TaskConfigObservation struct{ Value string }

// TaskConfigBehaviorDefinitions covers the task YAML contract at its public
// constructor/Validate boundary. It intentionally observes decoded values and
// validation text rather than decoder internals.
func TaskConfigBehaviorDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[brine.Empty, TaskConfigObservation](
			"the production task config evaluates profile {string}",
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder) (TaskConfigObservation, error) {
				profile, ok := p.GetString(0)
				if !ok {
					return TaskConfigObservation{}, fmt.Errorf("expected task config profile")
				}
				value, err := observeTaskConfig(profile)
				return TaskConfigObservation{Value: value}, err
			},
		),
		CheckString[TaskConfigObservation]("the task config observation is {string}", "task config observation",
			func(in TaskConfigObservation) (string, error) { return in.Value, nil }),
		CheckContains[TaskConfigObservation]("the task config observation contains {string}", "task config observation",
			func(in TaskConfigObservation) (string, error) { return in.Value, nil }),
	}
}

func observeTaskConfig(profile string) (string, error) {
	atc.LoadBaseResourceTypeDefaults(map[string]atc.Source{})
	switch profile {
	case "decode/basic":
		config, err := atc.NewTaskConfig([]byte("platform: beos\ninputs: []\nrun: {path: a/file}\n"))
		if err != nil {
			return taskError(err), nil
		}
		return "platform=" + config.Platform + ";path=" + config.Run.Path, nil
	case "params/bool":
		return observeTaskParam("true")
	case "params/int":
		return observeTaskParam("1059262")
	case "params/large-int":
		return observeTaskParam("18446744073709551615")
	case "params/unquoted-scientific":
		return observeTaskParam("1.8446744e+19")
	case "params/quoted-scientific":
		return observeTaskParam(`"1.8446744e+19"`)
	case "params/float":
		return observeTaskParam("1059262.123123123")
	case "params/map":
		config, err := atc.NewTaskConfig([]byte("platform: beos\nparams:\n  testParam:\n    foo: bar\nrun: {path: a/file}\n"))
		if err != nil {
			return taskError(err), nil
		}
		return base64.RawStdEncoding.EncodeToString([]byte(config.Params["testParam"])), nil
	case "params/empty":
		return observeTaskParam("")
	case "params/numeric-environment":
		config, err := atc.NewTaskConfig([]byte("platform: beos\nparams: {FOO: 1}\nrun: {path: a/file}\n"))
		if err != nil {
			return taskError(err), nil
		}
		return "platform=" + config.Platform + ";FOO=" + config.Params["FOO"], nil
	case "decode/unknown-key":
		_, err := atc.NewTaskConfig([]byte("platform: beos\nintputs: []\nrun: {path: a/file}\n"))
		return taskError(err), nil
	case "decode/invalid-input-output":
		_, err := atc.NewTaskConfig([]byte("platform: beos\ninputs: ['a/b/c']\noutputs: ['a/b/c']\nrun: {path: a/file}\n"))
		return taskError(err), nil
	case "validate/missing-platform":
		config := validTaskConfig()
		config.Platform = ""
		return taskValidation(config), nil
	case "limits/both-with-unit":
		return observeTaskResources("container_limits: {cpu: 1024, memory: 1KB}")
	case "limits/both-no-unit":
		return observeTaskResources("container_limits: {cpu: 1024, memory: 209715200}")
	case "limits/memory-only":
		return observeTaskResources("container_limits: {memory: 1KB}")
	case "limits/cpu-only":
		return observeTaskResources("container_limits: {cpu: 355}")
	case "limits/invalid-memory":
		return observeTaskResources("container_limits: {cpu: 1024, memory: abc1000kb}")
	case "limits/invalid-cpu":
		return observeTaskResources("container_limits: {cpu: str1ng-cpu-l1mit, memory: 20MB}")
	case "inputs/valid":
		config := validTaskConfig()
		config.Inputs = []atc.TaskInputConfig{{Name: "concourse"}}
		return taskValidation(config), nil
	case "inputs/one-missing":
		config := validTaskConfig()
		config.Inputs = []atc.TaskInputConfig{{Name: "concourse"}, {}}
		return taskValidation(config), nil
	case "inputs/two-missing":
		config := validTaskConfig()
		config.Inputs = []atc.TaskInputConfig{{Name: "concourse"}, {}, {}}
		return missingNameObservation(config.Validate(), "input"), nil
	case "outputs/valid":
		config := validTaskConfig()
		config.Outputs = []atc.TaskOutputConfig{{Name: "concourse"}}
		return taskValidation(config), nil
	case "outputs/one-missing":
		config := validTaskConfig()
		config.Outputs = []atc.TaskOutputConfig{{Name: "concourse"}, {}}
		return taskValidation(config), nil
	case "outputs/two-missing":
		config := validTaskConfig()
		config.Outputs = []atc.TaskOutputConfig{{Name: "concourse"}, {}, {}}
		return missingNameObservation(config.Validate(), "output"), nil
	case "requests/both":
		return observeTaskResources("container_requests: {cpu: 512, memory: 1GB}")
	case "requests/memory-only":
		return observeTaskResources("container_requests: {memory: 256MB}")
	case "requests/with-limits":
		return observeTaskResources("container_limits: {cpu: 2048, memory: 4GB}\ncontainer_requests: {cpu: 512, memory: 1GB}")
	case "requests/without-limits":
		return observeTaskResources("container_requests: {cpu: 256, memory: 512MB}")
	case "scratch/two":
		config, err := atc.NewTaskConfig([]byte("platform: linux\nscratch_paths:\n  - path: /scratch/buildkit\n  - path: /tmp/workspace\nrun: {path: /bin/sh}\n"))
		if err != nil {
			return taskError(err), nil
		}
		return fmt.Sprintf("scratch=%s,%s", config.ScratchPaths[0].Path, config.ScratchPaths[1].Path), nil
	case "scratch/empty":
		config, err := atc.NewTaskConfig([]byte("platform: linux\nscratch_paths: []\nrun: {path: /bin/sh}\n"))
		if err != nil {
			return taskError(err), nil
		}
		return fmt.Sprintf("scratch-count=%d", len(config.ScratchPaths)), nil
	case "scratch/with-cache":
		config, err := atc.NewTaskConfig([]byte("platform: linux\ncaches:\n  - path: /tmp/cache\nscratch_paths:\n  - path: /scratch/work\nrun: {path: /bin/sh}\n"))
		if err != nil {
			return taskError(err), nil
		}
		return fmt.Sprintf("cache=%s;scratch=%s", config.Caches[0].Path, config.ScratchPaths[0].Path), nil
	case "validate/missing-run":
		config := validTaskConfig()
		config.Run.Path = ""
		return taskValidation(config), nil
	case "image/nil":
		return observeNilImageDefaults(), nil
	case "image/no-defaults":
		return observeImageDefaults(nil, nil), nil
	case "image/base-defaults":
		return observeImageDefaults(map[string]atc.Source{"docker": {"some-key": "some-value"}}, nil), nil
	case "image/custom-type-defaults":
		return observeImageDefaults(nil, atc.ResourceTypes{{Name: "docker", Defaults: atc.Source{"some-key": "some-value"}}}), nil
	default:
		return "", fmt.Errorf("unknown task config profile %q", profile)
	}
}

func observeTaskParam(yamlValue string) (string, error) {
	value := yamlValue
	if value == "" {
		value = ""
	}
	data := fmt.Sprintf("platform: beos\nparams:\n  testParam: %s\nrun: {path: a/file}\n", value)
	config, err := atc.NewTaskConfig([]byte(data))
	if err != nil {
		return taskError(err), nil
	}
	return config.Params["testParam"], nil
}

func observeTaskResources(fragment string) (string, error) {
	config, err := atc.NewTaskConfig([]byte("platform: beos\n" + fragment + "\nrun: {path: a/file}\n"))
	if err != nil {
		return taskError(err), nil
	}
	return "limits=" + containerLimitsObservation(config.Limits) + ";requests=" + containerLimitsObservation(config.Requests), nil
}

func containerLimitsObservation(limits *atc.ContainerLimits) string {
	if limits == nil {
		return "nil"
	}
	cpu, memory := "nil", "nil"
	if limits.CPU != nil {
		cpu = fmt.Sprint(*limits.CPU)
	}
	if limits.Memory != nil {
		memory = fmt.Sprint(*limits.Memory)
	}
	return "cpu:" + cpu + ",memory:" + memory
}

func validTaskConfig() atc.TaskConfig {
	return atc.TaskConfig{Platform: "linux", Run: atc.TaskRunConfig{Path: "reboot"}}
}

func taskValidation(config atc.TaskConfig) string {
	if err := config.Validate(); err != nil {
		return taskError(err)
	}
	return "valid"
}

func taskError(err error) string {
	if err == nil {
		return "no-error"
	}
	return "error:" + err.Error()
}

func missingNameObservation(err error, kind string) string {
	if err == nil {
		return "no-error"
	}
	message := err.Error()
	positions := make([]string, 0, 2)
	for _, position := range []string{"1", "2"} {
		if strings.Contains(message, kind+" in position "+position+" is missing a name") {
			positions = append(positions, position)
		}
	}
	if len(positions) != 2 {
		return taskError(err)
	}
	return "missing-" + kind + "-names=" + strings.Join(positions, ",")
}

func observeImageDefaults(base map[string]atc.Source, custom atc.ResourceTypes) string {
	if base != nil {
		atc.LoadBaseResourceTypeDefaults(base)
		defer atc.LoadBaseResourceTypeDefaults(map[string]atc.Source{})
	}
	image := &atc.ImageResource{Type: "docker", Source: atc.Source{"a": "b", "evaluated-value": "((task-variable-name))"}}
	image.ApplySourceDefaults(custom)
	keys := make([]string, 0, len(image.Source))
	for key := range image.Source {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+fmt.Sprint(image.Source[key]))
	}
	return strings.Join(parts, ";")
}

func observeNilImageDefaults() (observation string) {
	defer func() {
		if recovered := recover(); recovered != nil {
			observation = fmt.Sprintf("panic:%v", recovered)
		}
	}()
	var image *atc.ImageResource
	image.ApplySourceDefaults(nil)
	return "nil"
}
