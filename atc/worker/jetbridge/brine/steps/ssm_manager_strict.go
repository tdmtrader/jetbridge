package steps

import (
	"fmt"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/creds/ssm"
	"github.com/jessevdk/go-flags"
)

type SSMManagerStrictObservation struct {
	Profile string
	Failure string
}

func SSMManagerStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[brine.Empty, SSMManagerStrictObservation](
			"the production SSM manager configuration profile {string} is exercised",
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder) (SSMManagerStrictObservation, error) {
				profile, err := paramAt("the production SSM manager configuration profile {string} is exercised", p, 0)
				if err != nil {
					return SSMManagerStrictObservation{}, err
				}

				manager := ssm.SsmManager{}
				want := false
				switch profile {
				case "empty-unconfigured":
				case "region-configured":
					manager.AwsRegion = "test-region"
					want = true
				default:
					return SSMManagerStrictObservation{}, fmt.Errorf("unknown SSM manager profile %q", profile)
				}

				if _, err := flags.ParseArgs(&manager, []string{}); err != nil {
					return SSMManagerStrictObservation{}, fmt.Errorf("parse production flags: %w", err)
				}

				failure := ""
				if got := manager.IsConfigured(); got != want {
					failure = fmt.Sprintf("IsConfigured got %t, want %t", got, want)
				}

				return SSMManagerStrictObservation{Profile: profile, Failure: failure}, nil
			},
		),
		brine.DefineCheck[SSMManagerStrictObservation](
			"the SSM manager configuration observation exactly matches {string}",
			func(in SSMManagerStrictObservation, p brine.Params, _ *brine.Recorder) error {
				profile, err := paramAt("the SSM manager configuration observation exactly matches {string}", p, 0)
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
