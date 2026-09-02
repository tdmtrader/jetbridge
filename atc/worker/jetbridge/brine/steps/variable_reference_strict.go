package steps

import (
	"fmt"
	"strings"

	"github.com/brine-dev/brine-go/pkg/brine"
	concoursevars "github.com/concourse/concourse/vars"
)

type VariableReferenceObservation struct{ Value string }

func VariableReferenceDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[brine.Empty, VariableReferenceObservation](
			"the production variable reference profile {string} is evaluated",
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder) (VariableReferenceObservation, error) {
				profile, ok := p.GetString(0)
				if !ok {
					return VariableReferenceObservation{}, fmt.Errorf("expected variable reference profile")
				}
				value, err := observeVariableReference(profile)
				return VariableReferenceObservation{Value: value}, err
			},
		),
		CheckString[VariableReferenceObservation]("the variable reference observation is {string}", "variable reference observation",
			func(in VariableReferenceObservation) (string, error) { return in.Value, nil }),
	}
}

func observeVariableReference(profile string) (string, error) {
	stringRefs := map[string]concoursevars.Reference{
		"string-path":          {Path: "hello"},
		"string-fields":        {Path: "hello", Fields: []string{"a", "b"}},
		"string-special-all":   {Path: "hello.world", Fields: []string{"a.b", "foo:bar"}},
		"string-special-mixed": {Path: "hello.world", Fields: []string{"a", "foo:bar", "other field", "another/field"}},
		"string-source":        {Source: "source", Path: "hello"},
	}
	if ref, ok := stringRefs[profile]; ok {
		got := ref.String()
		expected := map[string]string{
			"string-special-all":   `"hello.world"."a.b"."foo:bar"`,
			"string-special-mixed": `"hello.world".a."foo:bar"."other field"."another/field"`,
		}
		if want, special := expected[profile]; special {
			if got != want {
				return profile + "-mismatch", nil
			}
			return profile + "-preserved", nil
		}
		return got, nil
	}

	rawByProfile := map[string]string{
		"parse-path":                  "hello",
		"parse-fields":                "hello.a.b",
		"parse-special-all":           `"hello.world"."a.b"."foo:bar"`,
		"parse-special-mixed":         `"hello.world".a."foo:bar"`,
		"parse-source":                "source:hello",
		"parse-colon-no-source":       `"my:path"."field.1"."field.2"`,
		"parse-quoted-source-error":   `"some-source":path`,
		"parse-empty-field":           `vault:.field`,
		"parse-empty-quoted-field":    `vault:"".field`,
		"parse-empty-path":            `vault:`,
		"parse-trim-unquoted":         `hello .world `,
		"parse-preserve-quoted-space": `" hello "."world "`,
	}
	raw, ok := rawByProfile[profile]
	if !ok {
		return "", fmt.Errorf("unknown variable reference profile %q", profile)
	}
	ref, err := concoursevars.ParseReference(raw)
	if err != nil {
		expectedErrors := map[string]string{
			"parse-quoted-source-error": `invalid var '"some-source":path': source must not be quoted`,
			"parse-empty-quoted-field":  `invalid var 'vault:"".field': empty field`,
		}
		if want, quoted := expectedErrors[profile]; quoted {
			if err.Error() != want {
				return profile + "-mismatch", nil
			}
			return profile + "-preserved", nil
		}
		return "error:" + err.Error(), nil
	}
	if profile == "parse-preserve-quoted-space" {
		if ref.Source != "" || ref.Path != " hello " || strings.Join(ref.Fields, ",") != "world " {
			return profile + "-mismatch", nil
		}
		return profile + "-preserved", nil
	}
	return fmt.Sprintf("source=%s;path=%s;fields=%s", ref.Source, ref.Path, strings.Join(ref.Fields, ",")), nil
}
