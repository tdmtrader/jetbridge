package steps

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
)

type ConfigDiffObservation struct{ Value string }

func ConfigDiffBehaviorDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[brine.Empty, ConfigDiffObservation](
			"the production config diff evaluates profile {string}",
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder) (ConfigDiffObservation, error) {
				profile, ok := p.GetString(0)
				if !ok {
					return ConfigDiffObservation{}, fmt.Errorf("expected config diff profile")
				}
				value, err := observeConfigDiff(profile)
				return ConfigDiffObservation{Value: value}, err
			},
		),
		CheckString[ConfigDiffObservation]("the config diff observation is {string}", "config diff observation",
			func(in ConfigDiffObservation) (string, error) { return in.Value, nil }),
		brine.DefineCheck[ConfigDiffObservation](
			"the config diff observation contains {string}",
			func(in ConfigDiffObservation, p brine.Params, _ *brine.Recorder) error {
				expected, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected config diff fragments")
				}
				for _, fragment := range strings.Split(expected, " ;; ") {
					if !strings.Contains(in.Value, fragment) {
						return fmt.Errorf("config diff %q does not contain %q", in.Value, fragment)
					}
				}
				return nil
			},
		),
	}
}

func observeConfigDiff(profile string) (string, error) {
	job := configDiffJob("some-resource", true)
	display := &atc.DisplayConfig{BackgroundImage: "some-background.jpg"}
	var oldConfig, newConfig atc.Config
	switch profile {
	case "jobs/none":
	case "jobs/added":
		newConfig.Jobs = []atc.JobConfig{job}
	case "jobs/removed":
		oldConfig.Jobs = []atc.JobConfig{job}
	case "jobs/unchanged":
		oldConfig.Jobs, newConfig.Jobs = []atc.JobConfig{job}, []atc.JobConfig{job}
	case "jobs/remove-default-field":
		oldConfig.Jobs = []atc.JobConfig{job}
		newConfig.Jobs = []atc.JobConfig{configDiffJob("some-resource", false)}
	case "jobs/replace-field":
		oldConfig.Jobs = []atc.JobConfig{job}
		newConfig.Jobs = []atc.JobConfig{configDiffJob("some-other-resource", true)}
	case "display/none":
	case "display/added":
		newConfig.Display = display
	case "display/removed":
		oldConfig.Display = display
	case "display/unchanged":
		oldConfig.Display, newConfig.Display = display, display
	case "display/replaced":
		oldConfig.Display = display
		newConfig.Display = &atc.DisplayConfig{BackgroundImage: "some-other-background.jpg"}
	default:
		return "", fmt.Errorf("unknown config diff profile %q", profile)
	}
	var output bytes.Buffer
	changed := oldConfig.Diff(&output, newConfig)
	plain := regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(output.String(), "")
	return fmt.Sprintf("changed=%t;output=%s", changed, strings.Join(strings.Fields(plain), " ")), nil
}

func configDiffJob(resource string, trigger bool) atc.JobConfig {
	return atc.JobConfig{
		Name: "some-job",
		PlanSequence: []atc.Step{{Config: &atc.GetStep{
			Name: "some-name", Resource: resource, Trigger: trigger,
		}}},
	}
}
