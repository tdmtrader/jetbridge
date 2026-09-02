package steps

import (
	"fmt"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/gc"
)

type BuildLogRetentionObservation struct{ Value string }

func BuildLogRetentionDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[brine.Empty, BuildLogRetentionObservation](
			"the production build log retention profile {string} is calculated",
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder) (BuildLogRetentionObservation, error) {
				profile, ok := p.GetString(0)
				if !ok {
					return BuildLogRetentionObservation{}, fmt.Errorf("expected build log retention profile")
				}
				value, err := observeBuildLogRetention(profile)
				return BuildLogRetentionObservation{Value: value}, err
			},
		),
		CheckString[BuildLogRetentionObservation]("the build log retention is {string}", "build log retention",
			func(in BuildLogRetentionObservation) (string, error) { return in.Value, nil }),
	}
}

func observeBuildLogRetention(profile string) (string, error) {
	type input struct {
		defaults [4]uint64
		job      [3]int
	}
	profiles := map[string]input{
		"nothing":                  {},
		"job-only":                 {job: [3]int{3, 2, 1}},
		"defaults":                 {defaults: [4]uint64{5, 0, 4, 0}},
		"job-over-defaults":        {defaults: [4]uint64{5, 0, 4, 0}, job: [3]int{6, 3, 0}},
		"max-lower":                {defaults: [4]uint64{5, 6, 5, 6}, job: [3]int{10, 9, 0}},
		"max-only":                 {defaults: [4]uint64{0, 4, 0, 3}},
		"mixed-max":                {defaults: [4]uint64{2, 4, 3, 2}, job: [3]int{5, 5, 8}},
		"min-equals-builds":        {defaults: [4]uint64{2, 10, 3, 0}, job: [3]int{5, 0, 5}},
		"min-greater-than-builds":  {defaults: [4]uint64{2, 10, 3, 0}, job: [3]int{5, 0, 8}},
		"only-max-builds-with-job": {defaults: [4]uint64{0, 7, 0, 0}, job: [3]int{5, 7, 0}},
		"only-max-days-with-job":   {defaults: [4]uint64{0, 0, 0, 7}, job: [3]int{7, 5, 0}},
	}
	in, ok := profiles[profile]
	if !ok {
		return "", fmt.Errorf("unknown build log retention profile %q", profile)
	}
	calculator := gc.NewBuildLogRetentionCalculator(in.defaults[0], in.defaults[1], in.defaults[2], in.defaults[3])
	job := atc.JobConfig{BuildLogRetention: &atc.BuildLogRetention{
		Builds: in.job[0], Days: in.job[1], MinimumSucceededBuilds: in.job[2],
	}}
	retention := calculator.BuildLogsToRetain(job)
	return fmt.Sprintf("builds=%d;days=%d;min=%d", retention.Builds, retention.Days, retention.MinimumSucceededBuilds), nil
}
