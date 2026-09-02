package steps

import (
	"fmt"

	"github.com/brine-dev/brine-go/pkg/brine"
)

func APIJobsRemainingStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, JobStrictObservation](
			"the strict remaining jobs API executes profile {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, rec *brine.Recorder, resources brine.Resources) (JobStrictObservation, error) {
				profile, err := paramAt("the strict remaining jobs API executes profile {string}", p, 0)
				if err != nil {
					return JobStrictObservation{}, err
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return JobStrictObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				boundary, err := newStrictJobBoundary(database, rec)
				if err != nil {
					return JobStrictObservation{}, err
				}
				return boundary.observe(profile)
			},
		),
		brine.DefineCheck[JobStrictObservation](
			"the strict remaining jobs API observation exactly matches profile {string}",
			func(in JobStrictObservation, p brine.Params, _ *brine.Recorder) error {
				profile, err := paramAt("the strict remaining jobs API observation exactly matches profile {string}", p, 0)
				if err != nil {
					return err
				}
				if in.Profile != profile {
					return fmt.Errorf("jobs API profile got %q, want %q", in.Profile, profile)
				}
				want := 401
				if len(profile) >= len("forbidden") && profile[len(profile)-len("forbidden"):] == "forbidden" {
					want = 403
				}
				if in.Status != want {
					return fmt.Errorf("jobs API status got %d, want %d (body %q)", in.Status, want, string(in.Body))
				}
				return nil
			},
		),
	}
}
