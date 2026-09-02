package steps

import (
	"fmt"
	"strings"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/policy/opa"
)

type OPAResultObservation struct{ Value string }

func OPAResultDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[brine.Empty, OPAResultObservation](
			"the production OPA result profile {string} is parsed",
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder) (OPAResultObservation, error) {
				profile, ok := p.GetString(0)
				if !ok {
					return OPAResultObservation{}, fmt.Errorf("expected OPA result profile")
				}
				value, err := observeOPAResult(profile)
				return OPAResultObservation{Value: value}, err
			},
		),
		CheckString[OPAResultObservation]("the OPA result observation is {string}", "OPA result observation",
			func(in OPAResultObservation) (string, error) { return in.Value, nil }),
	}
}

func observeOPAResult(profile string) (string, error) {
	type input struct {
		body   string
		config opa.OpaConfig
	}
	profiles := map[string]input{
		"allowed-missing":     {`{"some":"value"}`, opa.OpaConfig{ResultAllowedKey: "a.b"}},
		"allowed-not-bool":    {`{"a":{"b":"ok"}}`, opa.OpaConfig{ResultAllowedKey: "a.b"}},
		"allowed-too-shallow": {`{"a":{"b":true}}`, opa.OpaConfig{ResultAllowedKey: "a"}},
		"allowed-too-deep":    {`{"a":{"b":true}}`, opa.OpaConfig{ResultAllowedKey: "a.b.c"}},
		"allowed":             {`{"a":{"b":true}}`, opa.OpaConfig{ResultAllowedKey: "a.b"}},
		"denied":              {`{"a":{"b":false}}`, opa.OpaConfig{ResultAllowedKey: "a.b"}},
		"explicit-block":      {`{"a":{"b":true,"c":true}}`, opa.OpaConfig{ResultAllowedKey: "a.b", ResultShouldBlockKey: "a.c"}},
		"messages":            {`{"a":{"b":true,"c":true,"d":["e","f"]}}`, opa.OpaConfig{ResultAllowedKey: "a.b", ResultShouldBlockKey: "a.c", ResultMessagesKey: "a.d"}},
	}
	in, ok := profiles[profile]
	if !ok {
		return "", fmt.Errorf("unknown OPA result profile %q", profile)
	}
	result, err := opa.ParseOpaResult([]byte(in.body), in.config)
	if err != nil {
		return "error:" + err.Error(), nil
	}
	return fmt.Sprintf("allowed=%t;block=%t;messages=%s", result.Allowed(), result.ShouldBlock(), strings.Join(result.Messages(), ",")), nil
}
