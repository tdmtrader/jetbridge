package steps

import (
	"fmt"
	"net/url"
	"reflect"
	"strings"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
)

type PipelineRefStrictObservation struct {
	Profile string
	Value   any
	Err     error
}

func PipelineRefStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[brine.Empty, PipelineRefStrictObservation](
			"the strict production pipeline reference handles profile {string}",
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder) (PipelineRefStrictObservation, error) {
				profile, _ := p.GetString(0)
				value, err := observePipelineRefStrict(profile)
				return PipelineRefStrictObservation{Profile: profile, Value: value, Err: err}, nil
			},
		),
		brine.DefineMap[PipelineRefStrictObservation, PipelineRefStrictObservation](
			"the strict pipeline reference matches the original expectation",
			func(observation PipelineRefStrictObservation, _ brine.Params, _ *brine.Recorder) (PipelineRefStrictObservation, error) {
				want, errSubstring, err := expectedPipelineRefStrict(observation.Profile)
				if err != nil {
					return observation, err
				}
				if errSubstring != "" {
					if observation.Err == nil || !strings.Contains(observation.Err.Error(), errSubstring) {
						return observation, fmt.Errorf("strict pipeline reference profile %q: expected error containing %q, got %v", observation.Profile, errSubstring, observation.Err)
					}
					return observation, nil
				}
				if observation.Err != nil {
					return observation, fmt.Errorf("strict pipeline reference profile %q: unexpected error: %w", observation.Profile, observation.Err)
				}
				if !reflect.DeepEqual(observation.Value, want) {
					return observation, fmt.Errorf("strict pipeline reference profile %q: expected %#v, got %#v", observation.Profile, want, observation.Value)
				}
				return observation, nil
			},
		),
	}
}

func observePipelineRefStrict(profile string) (any, error) {
	switch profile {
	case "string-simple":
		return (atc.PipelineRef{Name: "some-pipeline"}).String(), nil
	case "string-instance":
		return (atc.PipelineRef{Name: "some-pipeline", InstanceVars: atc.InstanceVars{
			"field.1": map[string]any{
				"subfield:1": 1,
				"subfield 2": []any{"1", 2, map[string]any{"k": "v"}},
			},
			"other": "field",
		}}).String(), nil
	case "string-sorted":
		return (atc.PipelineRef{Name: "some-pipeline", InstanceVars: atc.InstanceVars{
			"b": map[string]any{"foo": 1, "bar": []any{"1", 2}},
			"a": "hello.world",
		}}).String(), nil
	case "string-special":
		return (atc.PipelineRef{Name: "some-pipeline", InstanceVars: atc.InstanceVars{
			"colon": "a:b", "comma": "a,b", "space": "a b", "slash": "a/b",
		}}).String(), nil
	case "string-yaml":
		return (atc.PipelineRef{Name: "some-pipeline", InstanceVars: atc.InstanceVars{
			"int": "123", "float": "4e+6", "bool": "true", "weird_bool": "yes", "empty": "",
		}}).String(), nil
	case "string-primitives":
		return (atc.PipelineRef{Name: "some-pipeline", InstanceVars: atc.InstanceVars{
			"int": 123, "float": 123.456, "bool": true, "nil": nil,
		}}).String(), nil
	case "query-empty":
		return (atc.PipelineRef{InstanceVars: nil}).QueryParams(), nil
	case "query-simple":
		return (atc.PipelineRef{InstanceVars: atc.InstanceVars{"hello": "world", "num": 123}}).QueryParams(), nil
	case "query-nested":
		return (atc.PipelineRef{InstanceVars: atc.InstanceVars{"hello": map[string]any{"foo": 123, "bar": false}}}).QueryParams(), nil
	case "query-quoted":
		return (atc.PipelineRef{InstanceVars: atc.InstanceVars{"hello.1": map[string]any{"foo:bar": "baz"}}}).QueryParams(), nil
	case "parse-empty":
		return atc.InstanceVarsFromQueryParams(url.Values{})
	case "parse-simple":
		return atc.InstanceVarsFromQueryParams(url.Values{
			"vars.hello": {`"world"`},
			"vars.foo":   {`"bar"`},
		})
	case "parse-complex":
		return atc.InstanceVarsFromQueryParams(url.Values{`vars."a.b".c."d:e"`: {`"f"`}})
	case "parse-json":
		return atc.InstanceVarsFromQueryParams(url.Values{`vars.foo"`: {`["a",{"b":123}]`}})
	case "parse-root":
		return atc.InstanceVarsFromQueryParams(url.Values{`vars`: {`{"foo":["a",{"b":123}]}`}})
	case "parse-root-subvars":
		return atc.InstanceVarsFromQueryParams(url.Values{
			`vars`:     {`{"foo":["a",{"b":123}]}`},
			`vars.bar`: {`"baz"`},
		})
	case "parse-ignore":
		return atc.InstanceVarsFromQueryParams(url.Values{
			`vars.foo`: {`123`},
			`varsfoo`:  {`whatever`},
			`ignore"`:  {`blah`},
		})
	case "parse-invalid-ref":
		return atc.InstanceVarsFromQueryParams(url.Values{`vars.foo.`: {`123`}})
	case "parse-invalid-json":
		return atc.InstanceVarsFromQueryParams(url.Values{`vars.foo`: {`"123`}})
	default:
		return nil, fmt.Errorf("unknown strict pipeline reference profile %q", profile)
	}
}

func expectedPipelineRefStrict(profile string) (any, string, error) {
	switch profile {
	case "string-simple":
		return "some-pipeline", "", nil
	case "string-instance":
		return `some-pipeline/"field.1"."subfield 2":["1",2,{"k":"v"}],"field.1"."subfield:1":1,other:field`, "", nil
	case "string-sorted":
		return `some-pipeline/a:hello.world,b.bar:["1",2],b.foo:1`, "", nil
	case "string-special":
		return `some-pipeline/colon:"a:b",comma:"a,b",slash:"a/b",space:"a b"`, "", nil
	case "string-yaml":
		return `some-pipeline/bool:"true",empty:"",float:"4e+6",int:"123",weird_bool:"yes"`, "", nil
	case "string-primitives":
		return `some-pipeline/bool:true,float:123.456,int:123,nil:null`, "", nil
	case "query-empty":
		return url.Values(nil), "", nil
	case "query-simple":
		return url.Values{"vars.hello": {`"world"`}, "vars.num": {`123`}}, "", nil
	case "query-nested":
		return url.Values{"vars.hello.foo": {`123`}, "vars.hello.bar": {`false`}}, "", nil
	case "query-quoted":
		return url.Values{`vars."hello.1"."foo:bar"`: {`"baz"`}}, "", nil
	case "parse-empty":
		return atc.InstanceVars(nil), "", nil
	case "parse-simple":
		return atc.InstanceVars{"hello": "world", "foo": "bar"}, "", nil
	case "parse-complex":
		return atc.InstanceVars{"a.b": map[string]any{"c": map[string]any{"d:e": "f"}}}, "", nil
	case "parse-json", "parse-root":
		return atc.InstanceVars{"foo": []any{"a", map[string]any{"b": 123.0}}}, "", nil
	case "parse-root-subvars":
		return atc.InstanceVars{"foo": []any{"a", map[string]any{"b": 123.0}}, "bar": "baz"}, "", nil
	case "parse-ignore":
		return atc.InstanceVars{"foo": 123.0}, "", nil
	case "parse-invalid-ref":
		return nil, "invalid var", nil
	case "parse-invalid-json":
		return nil, "unexpected end of JSON input", nil
	default:
		return nil, "", fmt.Errorf("unknown strict pipeline reference expectation %q", profile)
	}
}
