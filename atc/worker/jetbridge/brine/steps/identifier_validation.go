package steps

import (
	"fmt"
	"strings"

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
		CheckString[IdentifierObservation]("the identifier result is {string}", "identifier result", func(in IdentifierObservation) (string, error) {
			return in.Value, nil
		}),
	}
}

func observeIdentifier(profile string) (string, error) {
	tests := map[string]struct {
		identifier string
		context    []string
		kind       string
		message    string
	}{
		"letter":            {"something", nil, "clean", ""},
		"multilingual":      {"ひらがな", nil, "clean", ""},
		"underscore":        {"some_thing", nil, "clean", ""},
		"number":            {"1something", nil, "clean", ""},
		"number-underscore": {"1_min", nil, "clean", ""},
		"hyphen":            {"-something", nil, "warning", "must start with a lowercase letter or a number"},
		"period":            {".something", nil, "warning", "must start with a lowercase letter or a number"},
		"uppercase-start":   {"Something", nil, "warning", "must start with a lowercase letter or a number"},
		"space":             {"some thing", nil, "warning", "illegal character ' '"},
		"uppercase-inner":   {"someThing", nil, "warning", "illegal character 'T'"},
		"empty":             {"", nil, "error", "identifier cannot be an empty string"},
		"across-task":       {"((.:name))", []string{".across", ".task(running-((.:name)))"}, "clean", ""},
		"across-pipeline":   {"running-((.:name))", []string{".across", ".set_pipeline(((.:name)))"}, "clean", ""},
		"warning-context":   {"_something", []string{"pipeline"}, "warning", "'_something' is not a valid identifier"},
	}
	test, ok := tests[profile]
	if !ok {
		return "", fmt.Errorf("unknown identifier profile %q", profile)
	}
	warning, err := atc.ValidateIdentifier(test.identifier, test.context...)
	switch test.kind {
	case "clean":
		if warning != nil || err != nil {
			return "", fmt.Errorf("expected clean identifier, got warning=%v error=%v", warning, err)
		}
	case "warning":
		if warning == nil || !strings.Contains(warning.Message, test.message) {
			return "", fmt.Errorf("expected warning containing %q, got %#v", test.message, warning)
		}
	case "error":
		if warning != nil || err == nil || !strings.Contains(err.Error(), test.message) {
			return "", fmt.Errorf("expected error containing %q, got warning=%v error=%v", test.message, warning, err)
		}
	}
	return test.kind, nil
}
