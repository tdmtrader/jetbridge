package steps

import (
	"fmt"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/creds/conjur"
	"github.com/jessevdk/go-flags"
)

type ConjurManagerStrictObservation struct {
	Configured bool
	ParseError string
}

func ConjurManagerStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[brine.Empty, ConjurManagerStrictObservation](
			"production go-flags parses the Conjur manager profile {string}",
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder) (ConjurManagerStrictObservation, error) {
				profile, ok := p.GetString(0)
				if !ok {
					return ConjurManagerStrictObservation{}, fmt.Errorf("Conjur manager profile is not a string")
				}
				manager := conjur.Manager{}
				switch profile {
				case "empty":
				case "appliance-url":
					manager.ConjurApplianceUrl = "http://conjur-test"
				default:
					return ConjurManagerStrictObservation{}, fmt.Errorf("unknown Conjur manager profile %q", profile)
				}
				_, err := flags.ParseArgs(&manager, []string{})
				observation := ConjurManagerStrictObservation{Configured: manager.IsConfigured()}
				if err != nil {
					observation.ParseError = err.Error()
				}
				return observation, nil
			},
		),
		brine.DefineCheck[ConjurManagerStrictObservation](
			"the Conjur manager is configured {string} without a parse error",
			func(in ConjurManagerStrictObservation, p brine.Params, _ *brine.Recorder) error {
				wantText, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("configured expectation is not a string")
				}
				var want bool
				switch wantText {
				case "true":
					want = true
				case "false":
					want = false
				default:
					return fmt.Errorf("configured expectation %q is not true or false", wantText)
				}
				if in.ParseError != "" {
					return fmt.Errorf("Conjur go-flags parse error: %s", in.ParseError)
				}
				if in.Configured != want {
					return fmt.Errorf("Conjur configured result got %t, want %t", in.Configured, want)
				}
				return nil
			},
		),
	}
}
