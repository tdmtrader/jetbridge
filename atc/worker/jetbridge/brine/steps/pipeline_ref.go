package steps

import (
	"fmt"
	"net/url"
	"reflect"
	"strings"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
)

type PipelineRefObservation struct{ Value string }

func PipelineRefDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[brine.Empty, PipelineRefObservation](
			"the production pipeline reference handles profile {string}",
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder) (PipelineRefObservation, error) {
				profile, _ := p.GetString(0)
				if err := verifyPipelineRef(profile); err != nil {
					return PipelineRefObservation{}, err
				}
				return PipelineRefObservation{Value: "verified"}, nil
			},
		),
		CheckString[PipelineRefObservation]("the pipeline reference result is {string}", "pipeline reference result", func(in PipelineRefObservation) (string, error) {
			return in.Value, nil
		}),
	}
}

func verifyPipelineRef(profile string) error {
	stringCases := map[string]struct {
		ref  atc.PipelineRef
		want string
	}{
		"string-simple": {atc.PipelineRef{Name: "some-pipeline"}, "some-pipeline"},
		"string-instance": {atc.PipelineRef{Name: "some-pipeline", InstanceVars: atc.InstanceVars{
			"field.1": map[string]any{"subfield:1": 1, "subfield 2": []any{"1", 2, map[string]any{"k": "v"}}}, "other": "field",
		}}, `some-pipeline/"field.1"."subfield 2":["1",2,{"k":"v"}],"field.1"."subfield:1":1,other:field`},
		"string-sorted": {atc.PipelineRef{Name: "some-pipeline", InstanceVars: atc.InstanceVars{
			"b": map[string]any{"foo": 1, "bar": []any{"1", 2}}, "a": "hello.world",
		}}, `some-pipeline/a:hello.world,b.bar:["1",2],b.foo:1`},
		"string-special": {atc.PipelineRef{Name: "some-pipeline", InstanceVars: atc.InstanceVars{
			"colon": "a:b", "comma": "a,b", "space": "a b", "slash": "a/b",
		}}, `some-pipeline/colon:"a:b",comma:"a,b",slash:"a/b",space:"a b"`},
		"string-yaml": {atc.PipelineRef{Name: "some-pipeline", InstanceVars: atc.InstanceVars{
			"int": "123", "float": "4e+6", "bool": "true", "weird_bool": "yes", "empty": "",
		}}, `some-pipeline/bool:"true",empty:"",float:"4e+6",int:"123",weird_bool:"yes"`},
		"string-primitives": {atc.PipelineRef{Name: "some-pipeline", InstanceVars: atc.InstanceVars{
			"int": 123, "float": 123.456, "bool": true, "nil": nil,
		}}, `some-pipeline/bool:true,float:123.456,int:123,nil:null`},
	}
	if test, ok := stringCases[profile]; ok {
		if got := test.ref.String(); got != test.want {
			return fmt.Errorf("expected %q, got %q", test.want, got)
		}
		return nil
	}

	queryCases := map[string]struct {
		ref  atc.PipelineRef
		want url.Values
	}{
		"query-empty":  {atc.PipelineRef{}, nil},
		"query-simple": {atc.PipelineRef{InstanceVars: atc.InstanceVars{"hello": "world", "num": 123}}, url.Values{"vars.hello": {`"world"`}, "vars.num": {`123`}}},
		"query-nested": {atc.PipelineRef{InstanceVars: atc.InstanceVars{"hello": map[string]any{"foo": 123, "bar": false}}}, url.Values{"vars.hello.foo": {`123`}, "vars.hello.bar": {`false`}}},
		"query-quoted": {atc.PipelineRef{InstanceVars: atc.InstanceVars{"hello.1": map[string]any{"foo:bar": "baz"}}}, url.Values{`vars."hello.1"."foo:bar"`: {`"baz"`}}},
	}
	if test, ok := queryCases[profile]; ok {
		if got := test.ref.QueryParams(); !reflect.DeepEqual(got, test.want) {
			return fmt.Errorf("expected query %#v, got %#v", test.want, got)
		}
		return nil
	}

	parseCases := map[string]struct {
		query url.Values
		want  atc.InstanceVars
		err   string
	}{
		"parse-empty":        {url.Values{}, nil, ""},
		"parse-simple":       {url.Values{"vars.hello": {`"world"`}, "vars.foo": {`"bar"`}}, atc.InstanceVars{"hello": "world", "foo": "bar"}, ""},
		"parse-complex":      {url.Values{`vars."a.b".c."d:e"`: {`"f"`}}, atc.InstanceVars{"a.b": map[string]any{"c": map[string]any{"d:e": "f"}}}, ""},
		"parse-json":         {url.Values{`vars.foo`: {`["a",{"b":123}]`}}, atc.InstanceVars{"foo": []any{"a", map[string]any{"b": 123.0}}}, ""},
		"parse-root":         {url.Values{`vars`: {`{"foo":["a",{"b":123}]}`}}, atc.InstanceVars{"foo": []any{"a", map[string]any{"b": 123.0}}}, ""},
		"parse-root-subvars": {url.Values{`vars`: {`{"foo":["a",{"b":123}]}`}, `vars.bar`: {`"baz"`}}, atc.InstanceVars{"foo": []any{"a", map[string]any{"b": 123.0}}, "bar": "baz"}, ""},
		"parse-ignore":       {url.Values{`vars.foo`: {`123`}, `varsfoo`: {`whatever`}, `ignore`: {`blah`}}, atc.InstanceVars{"foo": 123.0}, ""},
		"parse-invalid-ref":  {url.Values{`vars.foo.`: {`123`}}, nil, "invalid var"},
		"parse-invalid-json": {url.Values{`vars.foo`: {`"123`}}, nil, "unexpected end of JSON input"},
	}
	if test, ok := parseCases[profile]; ok {
		got, err := atc.InstanceVarsFromQueryParams(test.query)
		if test.err != "" {
			if err == nil || !strings.Contains(err.Error(), test.err) {
				return fmt.Errorf("expected error containing %q, got %v", test.err, err)
			}
			return nil
		}
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(got, test.want) {
			return fmt.Errorf("expected vars %#v, got %#v", test.want, got)
		}
		return nil
	}
	return fmt.Errorf("unknown pipeline reference profile %q", profile)
}
