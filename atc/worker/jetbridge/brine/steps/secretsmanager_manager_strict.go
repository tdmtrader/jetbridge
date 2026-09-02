package steps

import (
	"fmt"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/creds/secretsmanager"
	"github.com/jessevdk/go-flags"
)

type SecretsManagerObservation struct {
	Profile string
	Failure string
}

func SecretsManagerStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[brine.Empty, SecretsManagerObservation](
			"the production Secrets Manager configuration profile {string} is exercised",
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder) (SecretsManagerObservation, error) {
				profile, err := paramAt("the production Secrets Manager configuration profile {string} is exercised", p, 0)
				if err != nil {
					return SecretsManagerObservation{}, err
				}
				manager := secretsmanager.Manager{}
				want := false
				switch profile {
				case "empty-unconfigured":
				case "region-configured":
					manager.AwsRegion = "test-region"
					want = true
				default:
					return SecretsManagerObservation{}, fmt.Errorf("unknown Secrets Manager profile %q", profile)
				}
				if _, err := flags.ParseArgs(&manager, []string{}); err != nil {
					return SecretsManagerObservation{}, fmt.Errorf("parse production flags: %w", err)
				}
				failure := ""
				if got := manager.IsConfigured(); got != want {
					failure = fmt.Sprintf("IsConfigured got %t, want %t", got, want)
				}
				return SecretsManagerObservation{Profile: profile, Failure: failure}, nil
			},
		),
		brine.DefineCheck[SecretsManagerObservation](
			"the Secrets Manager configuration observation exactly matches {string}",
			func(in SecretsManagerObservation, p brine.Params, _ *brine.Recorder) error {
				profile, err := paramAt("the Secrets Manager configuration observation exactly matches {string}", p, 0)
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
