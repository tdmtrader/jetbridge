package steps

import (
	"fmt"

	"github.com/brine-dev/brine-go/pkg/brine"
)

type APIBuildsRemainingObservation struct {
	Profile string
	Failure string
}

func APIBuildsRemainingStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, APIBuildsRemainingObservation](
			"the strict remaining builds API executes profile {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, rec *brine.Recorder, resources brine.Resources) (APIBuildsRemainingObservation, error) {
				profile, err := paramAt("the strict remaining builds API executes profile {string}", p, 0)
				if err != nil {
					return APIBuildsRemainingObservation{}, err
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return APIBuildsRemainingObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				boundary, err := newStrictBuildBoundaryForProfile(database, rec, profile)
				if err != nil {
					return APIBuildsRemainingObservation{}, err
				}
				return APIBuildsRemainingObservation{Profile: profile, Failure: boundary.observe(profile)}, nil
			},
		),
		brine.DefineCheck[APIBuildsRemainingObservation](
			"the strict remaining builds API observation exactly matches profile {string}",
			func(in APIBuildsRemainingObservation, p brine.Params, _ *brine.Recorder) error {
				profile, err := paramAt("the strict remaining builds API observation exactly matches profile {string}", p, 0)
				if err != nil {
					return err
				}
				if profile != in.Profile {
					return fmt.Errorf("builds API profile got %q, want %q", in.Profile, profile)
				}
				if in.Failure != "" {
					return fmt.Errorf("%s: %s", profile, in.Failure)
				}
				return nil
			},
		),
	}
}
