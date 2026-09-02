package steps

import (
	"fmt"
	"net/url"
	"reflect"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/exec"
)

type StepMetadataObservation struct {
	Profile string
	Failure string
}

func StepMetadataStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[brine.Empty, StepMetadataObservation](
			"the production step metadata profile {string} is exercised",
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder) (StepMetadataObservation, error) {
				profile, err := paramAt("the production step metadata profile {string} is exercised", p, 0)
				if err != nil {
					return StepMetadataObservation{}, err
				}
				return StepMetadataObservation{Profile: profile, Failure: observeStepMetadata(profile)}, nil
			},
		),
		brine.DefineCheck[StepMetadataObservation](
			"the step metadata observation exactly matches {string}",
			func(in StepMetadataObservation, p brine.Params, _ *brine.Recorder) error {
				profile, err := paramAt("the step metadata observation exactly matches {string}", p, 0)
				if err != nil {
					return err
				}
				if in.Profile != profile {
					return fmt.Errorf("profile got %q, want %q", in.Profile, profile)
				}
				if in.Failure != "" {
					return fmt.Errorf("%s: %s", profile, in.Failure)
				}
				return nil
			},
		),
	}
}

func observeStepMetadata(profile string) string {
	full := exec.StepMetadata{
		BuildID: 1, BuildName: "42", TeamID: 2222, TeamName: "some-team",
		JobID: 3333, JobName: "some-job-name", PipelineID: 4444,
		PipelineName: "some-pipeline-name", ExternalURL: "https://www.example.com", CreatedBy: "someone",
	}
	switch profile {
	case "full-env":
		want := []string{
			"BUILD_ID=1", "BUILD_NAME=42", "BUILD_TEAM_ID=2222", "BUILD_TEAM_NAME=some-team",
			"BUILD_JOB_ID=3333", "BUILD_JOB_NAME=some-job-name", "BUILD_PIPELINE_ID=4444",
			"BUILD_PIPELINE_NAME=some-pipeline-name", "ATC_EXTERNAL_URL=https://www.example.com",
			"BUILD_URL=https://www.example.com/teams/some-team/pipelines/some-pipeline-name/jobs/some-job-name/builds/42",
			"BUILD_URL_SHORT=https://www.example.com/builds/1", "BUILD_CREATED_BY=someone",
		}
		return unorderedStringsFailure(full.Env(), want)
	case "instance-vars":
		full.PipelineInstanceVars = map[string]any{"branch": "main", "env": "prod", "num": 9000}
		full.InstanceVarsQuery = url.Values{
			"vars.branch": []string{`"main"`}, "vars.env": []string{`"prod"`}, "vars.num": []string{"9000"},
		}
		got := full.Env()
		for _, want := range []string{
			"BUILD_ID=1", "BUILD_NAME=42", `BUILD_PIPELINE_INSTANCE_VARS={"branch":"main","env":"prod","num":9000}`,
			"BUILD_URL=https://www.example.com/teams/some-team/pipelines/some-pipeline-name/jobs/some-job-name/builds/42?vars.branch=%22main%22&vars.env=%22prod%22&vars.num=9000",
			"BUILD_URL_SHORT=https://www.example.com/builds/1",
		} {
			if !containsExactString(got, want) {
				return fmt.Sprintf("environment %v lacks %q", got, want)
			}
		}
		return ""
	case "sparse-env":
		got := exec.StepMetadata{BuildID: 1}.Env()
		if !reflect.DeepEqual(got, []string{"BUILD_ID=1"}) {
			return fmt.Sprintf("Env got %v", got)
		}
		return ""
	case "full-task-env":
		got := exec.StepMetadata{BuildID: 42, BuildName: "3", TeamName: "main", JobName: "build-and-test", PipelineName: "concourse-self", ExternalURL: "https://concourse.home"}.TaskEnv()
		want := []string{"BUILD_ID=42", "BUILD_NAME=3", "BUILD_TEAM_NAME=main", "BUILD_JOB_NAME=build-and-test", "BUILD_PIPELINE_NAME=concourse-self", "ATC_EXTERNAL_URL=https://concourse.home"}
		return unorderedStringsFailure(got, want)
	case "empty-task-env":
		got := exec.StepMetadata{}.TaskEnv()
		if len(got) != 0 {
			return fmt.Sprintf("TaskEnv got %v, want empty", got)
		}
		return ""
	default:
		return fmt.Sprintf("unknown step metadata profile %q", profile)
	}
}

func containsExactString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func unorderedStringsFailure(got, want []string) string {
	if len(got) != len(want) {
		return fmt.Sprintf("environment got %v, want %v", got, want)
	}
	for _, value := range want {
		if !containsExactString(got, value) {
			return fmt.Sprintf("environment %v lacks %q", got, value)
		}
	}
	return ""
}
