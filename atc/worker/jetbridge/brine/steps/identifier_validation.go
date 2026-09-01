package steps

import (
	"fmt"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
)

type IdentifierObservation struct{ Value string }

func IdentifierValidationDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[brine.Empty, IdentifierObservation](
			"the production identifier validator handles profile {string}",
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder) (IdentifierObservation, error) {
				profile, _ := p.GetString(0)
				value, err := observeIdentifier(profile)
				return IdentifierObservation{Value: value}, err
			},
		),
		CheckString[IdentifierObservation]("the exact identifier result is {string}", "exact identifier result", func(in IdentifierObservation) (string, error) {
			return in.Value, nil
		}),
	}
}

func observeIdentifier(profile string) (string, error) {
	tests := map[string]struct {
		identifier string
		context    []string
	}{
		"letter":            {"something", nil},
		"multilingual":      {"ひらがな", nil},
		"underscore":        {"some_thing", nil},
		"number":            {"1something", nil},
		"number-underscore": {"1_min", nil},
		"hyphen":            {"-something", nil},
		"period":            {".something", nil},
		"uppercase-start":   {"Something", nil},
		"space":             {"some thing", nil},
		"uppercase-inner":   {"someThing", nil},
		"empty":             {"", nil},
		"across-task":       {"((.:name))", []string{".across", ".task(running-((.:name)))"}},
		"across-pipeline":   {"running-((.:name))", []string{".across", ".set_pipeline(((.:name)))"}},
		"warning-context":   {"_something", []string{"pipeline"}},
	}
	test, ok := tests[profile]
	if !ok {
		return "", fmt.Errorf("unknown identifier profile %q", profile)
	}
	warning, err := atc.ValidateIdentifier(test.identifier, test.context...)
	warningValue := "<nil>"
	if warning != nil {
		warningValue = warning.Type + ":" + warning.Message
	}
	errorValue := "<nil>"
	if err != nil {
		errorValue = err.Error()
	}
	return fmt.Sprintf("warning=%s;error=%s", warningValue, errorValue), nil
}
