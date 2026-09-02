package steps

import (
	"fmt"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/wrappa"
)

type ConcurrentPolicyObservation struct{ Value string }

func ConcurrentRequestPolicyStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[brine.Empty, ConcurrentPolicyObservation](
			"the production concurrent request policy evaluates profile {string}",
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder) (ConcurrentPolicyObservation, error) {
				profile, ok := p.GetString(0)
				if !ok {
					return ConcurrentPolicyObservation{}, fmt.Errorf("expected concurrent request policy profile")
				}
				value, err := observeConcurrentRequestPolicy(profile)
				return ConcurrentPolicyObservation{Value: value}, err
			},
		),
		CheckString[ConcurrentPolicyObservation](
			"the concurrent request policy observation is {string}",
			"concurrent request policy observation",
			func(in ConcurrentPolicyObservation) (string, error) { return in.Value, nil },
		),
		CheckContains[ConcurrentPolicyObservation](
			"the concurrent request policy observation contains {string}",
			"concurrent request policy observation",
			func(in ConcurrentPolicyObservation) (string, error) { return in.Value, nil },
		),
	}
}

func observeConcurrentRequestPolicy(profile string) (string, error) {
	switch profile {
	case "unmarshal-supported":
		var route wrappa.LimitedRoute
		if err := route.UnmarshalFlag(atc.ListAllJobs); err != nil {
			return "error:" + err.Error(), nil
		}
		return string(route), nil
	case "unmarshal-unsupported":
		var route wrappa.LimitedRoute
		err := route.UnmarshalFlag(atc.CreateJobBuild)
		if err == nil {
			return "no-error", nil
		}
		return err.Error(), nil
	case "limited-found":
		policy := wrappa.NewConcurrentRequestPolicy(map[wrappa.LimitedRoute]int{
			wrappa.LimitedRoute(atc.CreateJobBuild): 0,
		})
		_, found := policy.HandlerPool(atc.CreateJobBuild)
		return fmt.Sprintf("found=%t", found), nil
	case "unlimited-not-found":
		policy := wrappa.NewConcurrentRequestPolicy(map[wrappa.LimitedRoute]int{
			wrappa.LimitedRoute(atc.CreateJobBuild): 0,
		})
		_, found := policy.HandlerPool(atc.ListAllPipelines)
		return fmt.Sprintf("found=%t", found), nil
	case "shared-pool":
		policy := wrappa.NewConcurrentRequestPolicy(map[wrappa.LimitedRoute]int{
			wrappa.LimitedRoute(atc.CreateJobBuild): 1,
		})
		first, _ := policy.HandlerPool(atc.CreateJobBuild)
		if first == nil {
			return "first-pool-missing", nil
		}
		if !first.TryAcquire() {
			return "first-acquire=false", nil
		}
		second, _ := policy.HandlerPool(atc.CreateJobBuild)
		if second == nil {
			return "second-pool-missing", nil
		}
		return fmt.Sprintf("second-acquire=%t", second.TryAcquire()), nil
	default:
		return "", fmt.Errorf("unknown concurrent request policy profile %q", profile)
	}
}
