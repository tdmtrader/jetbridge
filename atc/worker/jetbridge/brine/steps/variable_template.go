package steps

import (
	"fmt"
	"strings"

	"github.com/brine-dev/brine-go/pkg/brine"
	concoursevars "github.com/concourse/concourse/vars"
)

type VariableTemplateObservation struct{ Value string }

// VariableTemplateDefinitions exercises the pipeline variable interpolator
// with concrete StaticVariables/NamedVariables. The only source case omitted
// is the one whose sole purpose is propagating an error from FakeVariables.
func VariableTemplateDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[brine.Empty, VariableTemplateObservation](
			"the production variable template evaluates profile {string}",
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder) (VariableTemplateObservation, error) {
				profile, ok := p.GetString(0)
				if !ok {
					return VariableTemplateObservation{}, fmt.Errorf("expected variable template profile")
				}
				value, err := observeVariableTemplate(profile)
				return VariableTemplateObservation{Value: value}, err
			},
		),
		CheckString[VariableTemplateObservation]("the template observation is {string}", "template observation",
			func(in VariableTemplateObservation) (string, error) { return in.Value, nil }),
		brine.DefineCheck[VariableTemplateObservation](
			"the template observation contains {string}",
			func(in VariableTemplateObservation, p brine.Params, _ *brine.Recorder) error {
				expected, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected template diagnostic")
				}
				for _, fragment := range strings.Split(expected, " ;; ") {
					if !strings.Contains(in.Value, fragment) {
						return fmt.Errorf("template observation %q does not contain %q", in.Value, fragment)
					}
				}
				return nil
			},
		),
	}
}

func observeVariableTemplate(profile string) (string, error) {
	var (
		template  string
		variables concoursevars.Variables
		opts      concoursevars.EvaluateOpts
	)
	switch profile {
	case "simple":
		template, variables = "((key))", concoursevars.StaticVariables{"key": "foo"}
	case "leading-slash":
		template, variables = "((/key/foo))", concoursevars.StaticVariables{"/key/foo": "foo"}
	case "multiple":
		template, variables = "((key)): ((value))", concoursevars.StaticVariables{"key": "foo", "value": "bar"}
	case "boolean":
		template, variables = "otherstuff: ((boule))", concoursevars.StaticVariables{"boule": true}
	case "typed-values":
		template = "name1: ((name1))\nname2: ((name2))\nname3: ((name3))\nname4: ((name4))\nname5: ((name5))\nname6: ((name6))\n1234: value\n"
		variables = concoursevars.StaticVariables{
			"name1": 1, "name2": "nil", "name3": true, "name4": "", "name5": nil,
			"name6": map[string]any{"key": map[string]any{"key2": []string{"value1", "value2"}}},
		}
	case "missing-required":
		template = "\n((key4))_array:\n- ((key_in_array))\n((key)): ((key2))\n((key3)): 2\ndup-key: ((key3))\n"
		variables, opts = concoursevars.StaticVariables{"key3": "foo"}, concoursevars.EvaluateOpts{ExpectAllKeys: true}
	case "missing-named":
		template = "((var1:key1)): ((var2:key1))"
		variables = concoursevars.NamedVariables{"var1": concoursevars.StaticVariables{"key3": "fuzz"}, "var2": concoursevars.StaticVariables{"key4": "blah"}}
		opts.ExpectAllKeys = true
	case "missing-tolerated":
		template, variables = "((key)): ((key2))\n((key3)): 2", concoursevars.StaticVariables{"key3": "foo"}
	case "unused-required":
		template, variables = "((key2))", concoursevars.StaticVariables{"key1": "1", "key2": "2", "key3": "3"}
		opts.ExpectAllVarsUsed = true
	case "unused-named":
		template = "((key2))"
		variables = concoursevars.NamedVariables{"var1": concoursevars.StaticVariables{"key1": "fuzz"}, "var2": concoursevars.StaticVariables{"key1": "blah"}}
		opts.ExpectAllVarsUsed = true
	case "unused-tolerated":
		template, variables = "((key)): ((key2))", concoursevars.StaticVariables{"key3": "foo"}
	case "missing-and-unused":
		template, variables = "((key2))", concoursevars.StaticVariables{"key1": "1", "key3": "3"}
		opts = concoursevars.EvaluateOpts{ExpectAllKeys: true, ExpectAllVarsUsed: true}
	case "number-template":
		template, variables = "1234", concoursevars.StaticVariables{"key": "not key"}
	case "nil-key":
		template, variables = "((key)): value", concoursevars.StaticVariables{"key": nil}
	case "unicode":
		template, variables = "((Ω))", concoursevars.StaticVariables{"Ω": "☃"}
	case "dash-underscore":
		template, variables = "((with-a-dash)): ((with_an_underscore))", concoursevars.StaticVariables{"with-a-dash": "dash", "with_an_underscore": "underscore"}
	case "quoted-dot-colon":
		template = `bar: ((foo:"with.dot:colon".buzz))`
		variables = concoursevars.NamedVariables{"foo": concoursevars.StaticVariables{"with.dot:colon": map[string]any{"buzz": "fuzz"}}}
	case "quoted-colon":
		template, variables = `bar: (("with:colon"))`, concoursevars.StaticVariables{"with:colon": "foo"}
	case "quoted-dot-subkey":
		template = `bar: ((secret-name."secret.field"))`
		variables = concoursevars.StaticVariables{"secret-name": map[string]any{"secret.field": "topsekrit"}}
	case "middle-one":
		template, variables = "url: https://((ip))", concoursevars.StaticVariables{"ip": "10.0.0.0"}
	case "middle-many":
		template, variables = "uri: nats://nats:((password))@((ip)):4222", concoursevars.StaticVariables{"password": "secret", "ip": "10.0.0.0"}
	case "at-in-name":
		template, variables = `(("foo/bar/me.com-test@me.com/password"))`, concoursevars.StaticVariables{"foo/bar/me.com-test@me.com/password": "secret"}
	case "middle-string-int":
		template, variables = "address: ((ip)):((port))", concoursevars.StaticVariables{"port": 4222, "ip": "10.0.0.0"}
	case "middle-unsupported-float":
		template, variables = "address: ((definition)):((eulers_number))", concoursevars.StaticVariables{"eulers_number": 2.717, "definition": "natural_log"}
	case "middle-repeated":
		template, variables = "acct_and_password: ((user)):((user))", concoursevars.StaticVariables{"user": "nats"}
	case "middle-of-key":
		template, variables = "((iaas))_cpi: props", concoursevars.StaticVariables{"iaas": "aws"}
	case "same-value-twice":
		template, variables = "((key)): ((key))", concoursevars.StaticVariables{"key": "foo"}
	case "multiline-value":
		template, variables = "((key))", concoursevars.StaticVariables{"key": "this\nhas\nmany\nlines"}
	case "operation-unspecified":
		template, variables = "((key))", concoursevars.StaticVariables{"key": "val"}
	case "invalid-expression-tolerated":
		template, variables = "(()", concoursevars.StaticVariables{}
	case "named-source":
		template, variables = "abc: ((dummy:key))", concoursevars.NamedVariables{"dummy": concoursevars.StaticVariables{"key": "val"}}
	case "subkey":
		template, variables = "((key.subkey))", concoursevars.StaticVariables{"key": map[any]any{"subkey": "e"}}
	case "subkey-variable-missing":
		template, variables = "((key.subkey_not_found))", concoursevars.StaticVariables{}
		opts.ExpectAllKeys = true
	case "subkey-field-missing":
		template, variables = "((key.subkey_not_found))", concoursevars.StaticVariables{"key": map[any]any{"subkey": "e"}}
		opts.ExpectAllKeys = true
	default:
		return "", fmt.Errorf("unknown variable template profile %q", profile)
	}

	result, err := concoursevars.NewTemplate([]byte(template)).Evaluate(variables, opts)
	if err != nil {
		return "error:" + normalizeTemplateText(err.Error()), nil
	}
	if profile == "typed-values" {
		expected := "1234: value\nname1: 1\nname2: nil\nname3: true\nname4: \"\"\nname5: null\nname6:\n  key:\n    key2:\n    - value1\n    - value2\n"
		if string(result) != expected {
			return "typed-yaml-mismatch:" + normalizeTemplateText(string(result)), nil
		}
		return "typed-yaml-preserved", nil
	}
	if profile == "multiline-value" {
		expected := "|-\n  this\n  has\n  many\n  lines\n"
		if string(result) != expected {
			return "multiline-yaml-mismatch:" + normalizeTemplateText(string(result)), nil
		}
		return "multiline-yaml-preserved", nil
	}
	return normalizeTemplateText(string(result)), nil
}

func normalizeTemplateText(value string) string {
	return strings.ReplaceAll(value, "\n", `\n`)
}
