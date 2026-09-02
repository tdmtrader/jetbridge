package steps

import (
	"fmt"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
)

type ATCJobVisibility struct {
	Public bool
	Err    error
}

func ATCJobPublicStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[brine.Empty, ATCJobVisibility](
			"a concrete ATC config checks the {string} job",
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder) (ATCJobVisibility, error) {
				profile, ok := p.GetString(0)
				if !ok {
					return ATCJobVisibility{}, fmt.Errorf("expected a job profile")
				}

				config := atc.Config{Jobs: atc.JobConfigs{
					{Name: "public-job", Public: true},
					{Name: "private-job", Public: false},
				}}
				jobName := profile
				if profile == "missing" {
					jobName = "does-not-exist"
				}
				public, err := config.JobIsPublic(jobName)
				return ATCJobVisibility{Public: public, Err: err}, nil
			},
		),
		brine.DefineCheck[ATCJobVisibility](
			"the job visibility is {string}",
			func(in ATCJobVisibility, p brine.Params, _ *brine.Recorder) error {
				want, ok := p.GetString(0)
				if !ok || (want != "public" && want != "private") {
					return fmt.Errorf("expected public or private")
				}
				if in.Public != (want == "public") {
					return fmt.Errorf("expected %s visibility, got public=%t", want, in.Public)
				}
				return nil
			},
		),
		CheckThat[ATCJobVisibility](
			"the job lookup succeeds",
			func(in ATCJobVisibility) error {
				if in.Err != nil {
					return fmt.Errorf("expected lookup to succeed: %w", in.Err)
				}
				return nil
			},
		),
		CheckThat[ATCJobVisibility](
			"the job lookup fails",
			func(in ATCJobVisibility) error {
				if in.Err == nil {
					return fmt.Errorf("expected lookup to fail")
				}
				return nil
			},
		),
	}
}
