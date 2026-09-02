package steps

import (
	"fmt"
	"reflect"
	"sort"

	"github.com/brine-dev/brine-go/pkg/brine"
	concoursevars "github.com/concourse/concourse/vars"
)

type StaticVariableObservation struct{ Value string }

func StaticVariableDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[brine.Empty, StaticVariableObservation](
			"the production static variable profile {string} is evaluated",
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder) (StaticVariableObservation, error) {
				profile, ok := p.GetString(0)
				if !ok {
					return StaticVariableObservation{}, fmt.Errorf("expected static variable profile")
				}
				value, err := observeStaticVariable(profile)
				return StaticVariableObservation{Value: value}, err
			},
		),
		CheckString[StaticVariableObservation]("the static variable observation is {string}", "static variable observation",
			func(in StaticVariableObservation) (string, error) { return in.Value, nil }),
	}
}

func observeStaticVariable(profile string) (string, error) {
	get := func(v concoursevars.StaticVariables, ref concoursevars.Reference) string {
		value, found, err := v.Get(ref)
		if err != nil {
			switch err.(type) {
			case concoursevars.MissingFieldError:
				return "error=missing-field"
			case concoursevars.InvalidFieldError:
				return "error=invalid-field"
			default:
				return "error=" + err.Error()
			}
		}
		if value == nil {
			return fmt.Sprintf("value=nil;found=%t;error=nil", found)
		}
		return fmt.Sprintf("value=%v;found=%t;error=nil", value, found)
	}

	switch profile {
	case "get-found":
		return get(concoursevars.StaticVariables{"a": "foo"}, concoursevars.Reference{Path: "a"}), nil
	case "get-missing":
		return get(concoursevars.StaticVariables{"a": "foo"}, concoursevars.Reference{Path: "b"}), nil
	case "get-local-source":
		return get(concoursevars.StaticVariables{"a": "foo"}, concoursevars.Reference{Source: ".", Path: "a"}), nil
	case "get-named-source":
		return get(concoursevars.StaticVariables{"a": "foo"}, concoursevars.Reference{Source: "some-var-source", Path: "a"}), nil
	case "get-fields":
		v := concoursevars.StaticVariables{"a": map[string]any{"subkey1": map[any]any{"subkey2": "foo"}}}
		return get(v, concoursevars.Reference{Path: "a", Fields: []string{"subkey1", "subkey2"}}), nil
	case "get-missing-field":
		v := concoursevars.StaticVariables{"a": map[string]any{"subkey1": map[any]any{"subkey2": "foo"}}}
		return get(v, concoursevars.Reference{Path: "a", Fields: []string{"subkey1", "bad_key"}}), nil
	case "get-invalid-field":
		v := concoursevars.StaticVariables{"a": map[string]any{"subkey1": map[any]any{"subkey2": "foo"}}}
		return get(v, concoursevars.Reference{Path: "a", Fields: []string{"subkey1", "subkey2", "cant_go_deeper"}}), nil
	case "list":
		empty, emptyErr := (concoursevars.StaticVariables{}).List()
		refs, err := (concoursevars.StaticVariables{"a": "1", "b": "2"}).List()
		names := make([]string, 0, len(refs))
		for _, ref := range refs {
			names = append(names, ref.String())
		}
		sort.Strings(names)
		if emptyErr != nil || err != nil || len(empty) != 0 || !reflect.DeepEqual(names, []string{"a", "b"}) {
			return "list-mismatch", nil
		}
		return "list-preserved", nil
	case "flatten":
		got := concoursevars.StaticVariables{
			"hello": "world",
			"foo":   map[string]any{"bar": "baz", "abc": map[any]any{"def": "ghi", "jkl": "mno"}},
		}.Flatten()
		want := concoursevars.KVPairs{
			{Ref: concoursevars.Reference{Path: "hello"}, Value: "world"},
			{Ref: concoursevars.Reference{Path: "foo", Fields: []string{"bar"}}, Value: "baz"},
			{Ref: concoursevars.Reference{Path: "foo", Fields: []string{"abc", "def"}}, Value: "ghi"},
			{Ref: concoursevars.Reference{Path: "foo", Fields: []string{"abc", "jkl"}}, Value: "mno"},
		}
		if !sameKVPairs(got, want) {
			return "flatten-mismatch", nil
		}
		return "flatten-preserved", nil
	case "expand-simple":
		got := (concoursevars.KVPairs{
			{Ref: concoursevars.Reference{Path: "hello"}, Value: "world"},
			{Ref: concoursevars.Reference{Path: "foo"}, Value: map[string]any{"bar": "baz"}},
		}).Expand()
		want := concoursevars.StaticVariables{"hello": "world", "foo": map[string]any{"bar": "baz"}}
		return shapeResult(profile, got, want), nil
	case "expand-recursive":
		got := (concoursevars.KVPairs{
			{Ref: concoursevars.Reference{Path: "hello", Fields: []string{"a", "b"}}, Value: "world"},
			{Ref: concoursevars.Reference{Path: "foo"}, Value: map[string]any{"bar": map[string]any{"abc": "def"}}},
			{Ref: concoursevars.Reference{Path: "foo", Fields: []string{"bar", "ghi"}}, Value: "jkl"},
		}).Expand()
		want := concoursevars.StaticVariables{"hello": map[string]any{"a": map[string]any{"b": "world"}}, "foo": map[string]any{"bar": map[string]any{"abc": "def", "ghi": "jkl"}}}
		return shapeResult(profile, got, want), nil
	case "expand-overwrite-nonmap":
		got := (concoursevars.KVPairs{
			{Ref: concoursevars.Reference{Path: "foo"}, Value: map[string]any{"bar": "baz"}},
			{Ref: concoursevars.Reference{Path: "foo", Fields: []string{"bar", "ghi"}}, Value: "jkl"},
		}).Expand()
		want := concoursevars.StaticVariables{"foo": map[string]any{"bar": map[string]any{"ghi": "jkl"}}}
		return shapeResult(profile, got, want), nil
	case "expand-overwrite-full":
		got := (concoursevars.KVPairs{
			{Ref: concoursevars.Reference{Path: "foo"}, Value: map[string]any{"bar": "baz"}},
			{Ref: concoursevars.Reference{Path: "foo"}, Value: "jkl"},
		}).Expand()
		want := concoursevars.StaticVariables{"foo": "jkl"}
		return shapeResult(profile, got, want), nil
	default:
		return "", fmt.Errorf("unknown static variable profile %q", profile)
	}
}

func sameKVPairs(left, right concoursevars.KVPairs) bool {
	key := func(pair concoursevars.KVPair) string {
		return pair.Ref.String() + "=" + fmt.Sprintf("%v", pair.Value)
	}
	l, r := make([]string, 0, len(left)), make([]string, 0, len(right))
	for _, pair := range left {
		l = append(l, key(pair))
	}
	for _, pair := range right {
		r = append(r, key(pair))
	}
	sort.Strings(l)
	sort.Strings(r)
	return reflect.DeepEqual(l, r)
}

func shapeResult(profile string, got, want concoursevars.StaticVariables) string {
	if !reflect.DeepEqual(got, want) {
		return profile + "-mismatch"
	}
	return profile + "-preserved"
}
