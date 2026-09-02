package steps

import (
	"fmt"
	"reflect"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/creds"
	"github.com/concourse/concourse/vars"
)

type CredsValueObservation struct {
	Profile string
	Failure string
}

func CredsValuesStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[brine.Empty, CredsValueObservation](
			"the production credential value profile {string} is exercised",
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder) (CredsValueObservation, error) {
				profile, err := paramAt("the production credential value profile {string} is exercised", p, 0)
				if err != nil {
					return CredsValueObservation{}, err
				}
				return CredsValueObservation{Profile: profile, Failure: observeCredsValue(profile)}, nil
			},
		),
		brine.DefineCheck[CredsValueObservation](
			"the credential value observation exactly matches {string}",
			func(in CredsValueObservation, p brine.Params, _ *brine.Recorder) error {
				profile, err := paramAt("the credential value observation exactly matches {string}", p, 0)
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

func observeCredsValue(profile string) string {
	switch profile {
	case "string-interpolate":
		got, err := creds.NewString(vars.StaticVariables{"token": "super-secret"}, "((token))").Evaluate()
		if err != nil || got != "super-secret" {
			return fmt.Sprintf("got %q, error %v", got, err)
		}
	case "string-error":
		got, err := creds.NewString(vars.StaticVariables{}, "((missing-var))").Evaluate()
		if err == nil || got != "((missing-var))" {
			return fmt.Sprintf("got %q, error %v", got, err)
		}
	case "string-plain":
		got, err := creds.NewString(vars.StaticVariables{}, "plain-value").Evaluate()
		if err != nil || got != "plain-value" {
			return fmt.Sprintf("got %q, error %v", got, err)
		}
	case "list-whole":
		got, err := creds.NewList(vars.StaticVariables{"list": []string{"foo", "bar"}}, "((list))").Evaluate()
		if err != nil || !reflect.DeepEqual(got, []any{"foo", "bar"}) {
			return fmt.Sprintf("got %v, error %v", got, err)
		}
	case "list-element":
		got, err := creds.NewList(vars.StaticVariables{"element": "blah"}, []any{"abc", "((element))"}).Evaluate()
		if err != nil || !reflect.DeepEqual(got, []any{"abc", "blah"}) {
			return fmt.Sprintf("got %v, error %v", got, err)
		}
	case "source":
		got, err := creds.NewSource(vars.StaticVariables{"some-param": "lol"}, atc.Source{"some": map[string]any{"source-key": "((some-param))"}}).Evaluate()
		want := atc.Source{"some": map[string]any{"source-key": "lol"}}
		if err != nil || !reflect.DeepEqual(got, want) {
			return fmt.Sprintf("got %v, error %v", got, err)
		}
	default:
		return fmt.Sprintf("unknown credential value profile %q", profile)
	}
	return ""
}
