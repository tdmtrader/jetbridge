package steps

import (
	"fmt"

	"github.com/brine-dev/brine-go/pkg/brine"
	flyversion "github.com/concourse/concourse/fly/version"
)

type FlyVersionStrictObservation struct {
	Profile string
	Failure string
}

func FlyVersionStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[brine.Empty, FlyVersionStrictObservation](
			"the production Fly version profile {string} is exercised",
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder) (FlyVersionStrictObservation, error) {
				profile, err := paramAt("the production Fly version profile {string} is exercised", p, 0)
				if err != nil {
					return FlyVersionStrictObservation{}, err
				}

				failure := ""
				switch profile {
				case "stable", "pre-release", "post-release":
					input := map[string]string{
						"stable":       "1.2.3",
						"pre-release":  "1.2.3-dev.2",
						"post-release": "1.2.3+bonus_feature.1",
					}[profile]
					major, minor, patch, err := flyversion.GetSemver(input)
					if err != nil || major != 1 || minor != 2 || patch != 3 {
						failure = fmt.Sprintf("GetSemver(%q) = %d.%d.%d, err=%v; want 1.2.3 with no error", input, major, minor, patch, err)
					}
				case "invalid":
					_, _, _, emptyErr := flyversion.GetSemver("")
					_, _, _, shortErr := flyversion.GetSemver("1.2")
					if emptyErr == nil || shortErr == nil {
						failure = fmt.Sprintf("GetSemver invalid errors: empty=%v short=%v; want both non-nil", emptyErr, shortErr)
					}
				case "development-detection":
					checks := []struct {
						version string
						want    bool
					}{
						{version: "1.2.3", want: false},
						{version: "0.0.0-dev", want: true},
						{version: "0.0.0-devolve", want: false},
						{version: "0.0.0-not-dev", want: false},
						{version: "0.0.0+not+dev", want: false},
						{version: "0.0.0-dev.1", want: true},
						{version: "0.0.0-abc+dev", want: true},
						{version: "0.0.0-abc+dev.1", want: true},
						{version: "0.0.0-dev+dev", want: true},
						{version: "0.0.0-abc+dev.1", want: true},
					}
					for _, check := range checks {
						if got := flyversion.IsDev(check.version); got != check.want {
							failure = fmt.Sprintf("IsDev(%q) = %t, want %t", check.version, got, check.want)
							break
						}
					}
				default:
					return FlyVersionStrictObservation{}, fmt.Errorf("unknown Fly version profile %q", profile)
				}

				return FlyVersionStrictObservation{Profile: profile, Failure: failure}, nil
			},
		),
		brine.DefineCheck[FlyVersionStrictObservation](
			"the Fly version observation exactly matches {string}",
			func(in FlyVersionStrictObservation, p brine.Params, _ *brine.Recorder) error {
				profile, err := paramAt("the Fly version observation exactly matches {string}", p, 0)
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
