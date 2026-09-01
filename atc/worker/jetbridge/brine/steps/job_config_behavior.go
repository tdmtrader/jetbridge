package steps

import (
	"fmt"
	"sort"
	"strings"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
)

type JobConfigObservation struct{ Value string }

func JobConfigBehaviorDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[brine.Empty, JobConfigObservation](
			"the production job config handles profile {string}",
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder) (JobConfigObservation, error) {
				profile, _ := p.GetString(0)
				value, err := observeJobConfig(profile)
				return JobConfigObservation{Value: value}, err
			},
		),
		CheckString[JobConfigObservation]("the job config result is {string}", "job config result", func(in JobConfigObservation) (string, error) {
			return in.Value, nil
		}),
	}
}

func observeJobConfig(profile string) (string, error) {
	switch profile {
	case "max-raw":
		return fmt.Sprintf("%d", (atc.JobConfig{RawMaxInFlight: 42}).MaxInFlight()), nil
	case "max-serial":
		values := []int{
			(atc.JobConfig{Serial: true}).MaxInFlight(),
			(atc.JobConfig{Serial: true, SerialGroups: []string{"one"}}).MaxInFlight(),
			(atc.JobConfig{SerialGroups: []string{"one"}}).MaxInFlight(),
		}
		return fmt.Sprintf("%d,%d,%d", values[0], values[1], values[2]), nil
	case "max-serial-overrides":
		values := []int{
			(atc.JobConfig{Serial: true, RawMaxInFlight: 3}).MaxInFlight(),
			(atc.JobConfig{Serial: true, SerialGroups: []string{"one"}, RawMaxInFlight: 3}).MaxInFlight(),
			(atc.JobConfig{SerialGroups: []string{"one"}, RawMaxInFlight: 3}).MaxInFlight(),
		}
		return fmt.Sprintf("%d,%d,%d", values[0], values[1], values[2]), nil
	case "max-default":
		return fmt.Sprintf("%d", (atc.JobConfig{}).MaxInFlight()), nil
	}

	if strings.HasPrefix(profile, "input-") {
		config, err := inputJobConfig(strings.TrimPrefix(profile, "input-"))
		if err != nil {
			return "", err
		}
		inputs := config.Inputs()
		items := make([]string, 0, len(inputs))
		for _, input := range inputs {
			metadata := make([]string, 0, 3)
			if len(input.Passed) > 0 {
				metadata = append(metadata, "passed:"+strings.Join(input.Passed, "+"))
			}
			metadata = append(metadata, fmt.Sprintf("trigger:%t", input.Trigger))
			if input.Version != nil && input.Version.Every {
				metadata = append(metadata, "version:every")
			}
			items = append(items, fmt.Sprintf("%s=%s[%s]", input.Name, input.Resource, strings.Join(metadata, ",")))
		}
		sort.Strings(items)
		return strings.Join(items, ","), nil
	}

	if strings.HasPrefix(profile, "output-") {
		config, err := outputJobConfig(strings.TrimPrefix(profile, "output-"))
		if err != nil {
			return "", err
		}
		outputs := config.Outputs()
		items := make([]string, 0, len(outputs))
		for _, output := range outputs {
			items = append(items, output.Name+"="+output.Resource)
		}
		sort.Strings(items)
		return strings.Join(items, ","), nil
	}

	return "", fmt.Errorf("unknown job config profile %q", profile)
}

func inputJobConfig(profile string) (atc.JobConfig, error) {
	get := func(name string) atc.Step { return atc.Step{Config: &atc.GetStep{Name: name}} }
	config := atc.JobConfig{}
	switch profile {
	case "empty":
		config.PlanSequence = []atc.Step{}
	case "serial":
		config.PlanSequence = []atc.Step{
			{Config: &atc.GetStep{Name: "some-get-plan", Passed: []string{"a", "b"}, Trigger: true}},
			get("some-other-get-plan"),
		}
	case "version":
		config.PlanSequence = []atc.Step{{Config: &atc.GetStep{Name: "a", Version: &atc.VersionConfig{Every: true}}}}
	case "resource":
		config.PlanSequence = []atc.Step{{Config: &atc.GetStep{Name: "some-get-plan", Resource: "some-get-resource"}}}
	case "parallel":
		config.PlanSequence = []atc.Step{{Config: &atc.InParallelStep{Config: atc.InParallelConfig{Steps: []atc.Step{
			get("a"),
			{Config: &atc.PutStep{Name: "ignored"}},
			{Config: &atc.GetStep{Name: "b", Resource: "some-resource", Passed: []string{"x"}}},
			{Config: &atc.GetStep{Name: "c", Trigger: true}},
		}}}}}
	case "no-gets":
		config.PlanSequence = []atc.Step{{Config: &atc.PutStep{Name: "put"}}}
	case "ensure", "success", "failure", "abort", "error":
		config.PlanSequence = []atc.Step{get("a")}
		setJobHook(&config, profile, get("b"))
	default:
		return config, fmt.Errorf("unknown input profile %q", profile)
	}
	return config, nil
}

func outputJobConfig(profile string) (atc.JobConfig, error) {
	put := func(name string) atc.Step { return atc.Step{Config: &atc.PutStep{Name: name}} }
	config := atc.JobConfig{}
	switch profile {
	case "empty":
		config.PlanSequence = []atc.Step{}
	case "simple":
		config.PlanSequence = []atc.Step{{Config: &atc.PutStep{Name: "some-name", Resource: "some-resource"}}}
	case "no-puts":
		config.PlanSequence = []atc.Step{{Config: &atc.GetStep{Name: "get"}}}
	case "ensure", "success", "failure", "abort", "error":
		config.PlanSequence = []atc.Step{put("a")}
		setJobHook(&config, profile, put("b"))
	default:
		return config, fmt.Errorf("unknown output profile %q", profile)
	}
	return config, nil
}

func setJobHook(config *atc.JobConfig, name string, step atc.Step) {
	switch name {
	case "ensure":
		config.Ensure = &step
	case "success":
		config.OnSuccess = &step
	case "failure":
		config.OnFailure = &step
	case "abort":
		config.OnAbort = &step
	case "error":
		config.OnError = &step
	}
}
