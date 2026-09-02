package steps

import (
	"fmt"
	"sort"
	"strings"

	"github.com/brine-dev/brine-go/pkg/brine"
	concoursevars "github.com/concourse/concourse/vars"
)

type MultiVariableObservation struct{ Value string }
type NamedVariableObservation struct{ Value string }

func VariableSourceDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[brine.Empty, MultiVariableObservation](
			"the production multi variable profile {string} is evaluated",
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder) (MultiVariableObservation, error) {
				profile, ok := p.GetString(0)
				if !ok {
					return MultiVariableObservation{}, fmt.Errorf("expected multi variable profile")
				}
				value, err := observeMultiVariables(profile)
				return MultiVariableObservation{Value: value}, err
			},
		),
		CheckString[MultiVariableObservation]("the multi variable observation is {string}", "multi variable observation",
			func(in MultiVariableObservation) (string, error) { return in.Value, nil }),
		brine.DefineMap[brine.Empty, NamedVariableObservation](
			"the production named variable profile {string} is evaluated",
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder) (NamedVariableObservation, error) {
				profile, ok := p.GetString(0)
				if !ok {
					return NamedVariableObservation{}, fmt.Errorf("expected named variable profile")
				}
				value, err := observeNamedVariables(profile)
				return NamedVariableObservation{Value: value}, err
			},
		),
		CheckString[NamedVariableObservation]("the named variable observation is {string}", "named variable observation",
			func(in NamedVariableObservation) (string, error) { return in.Value, nil }),
	}
}

func observeMultiVariables(profile string) (string, error) {
	switch profile {
	case "no-sources":
		value, found, err := concoursevars.NewMultiVars(nil).Get(concoursevars.Reference{})
		return variableGetObservation(value, found, err), nil
	case "missing-in-all":
		sources := []concoursevars.Variables{
			concoursevars.StaticVariables{"key1": "val"},
			concoursevars.StaticVariables{"key2": "val"},
		}
		value, found, err := concoursevars.NewMultiVars(sources).Get(concoursevars.Reference{Path: "key3"})
		return variableGetObservation(value, found, err), nil
	case "list":
		empty, emptyErr := concoursevars.NewMultiVars(nil).List()
		refs, err := concoursevars.NewMultiVars([]concoursevars.Variables{
			concoursevars.StaticVariables{"a": "1", "b": "2"},
			concoursevars.StaticVariables{"b": "3", "c": "4"},
		}).List()
		if emptyErr != nil || err != nil || len(empty) != 0 {
			return "list-error", nil
		}
		return "list=" + sortedReferenceNames(refs), nil
	default:
		return "", fmt.Errorf("unknown multi variable profile %q", profile)
	}
}

func observeNamedVariables(profile string) (string, error) {
	switch profile {
	case "no-sources":
		value, found, err := (concoursevars.NamedVariables{}).Get(concoursevars.Reference{})
		return variableGetObservation(value, found, err), nil
	case "missing-source":
		sources := concoursevars.NamedVariables{
			"s1": concoursevars.StaticVariables{"key1": "val"},
			"s2": concoursevars.StaticVariables{"key2": "val"},
		}
		value, found, err := sources.Get(concoursevars.Reference{Source: "s3", Path: "foo"})
		return variableGetObservation(value, found, err), nil
	case "no-source-name":
		sources := concoursevars.NamedVariables{
			"s1": concoursevars.StaticVariables{"key1": "val"},
			"s2": concoursevars.StaticVariables{"key2": "val"},
		}
		value, found, err := sources.Get(concoursevars.Reference{Path: "key1"})
		return variableGetObservation(value, found, err), nil
	case "list":
		empty, emptyErr := (concoursevars.NamedVariables{}).List()
		refs, err := (concoursevars.NamedVariables{
			"s1": concoursevars.StaticVariables{"a": "1", "b": "2"},
			"s2": concoursevars.StaticVariables{"b": "3", "c": "4"},
		}).List()
		if emptyErr != nil || err != nil || len(empty) != 0 {
			return "list-error", nil
		}
		return "list=" + sortedReferenceNames(refs), nil
	default:
		return "", fmt.Errorf("unknown named variable profile %q", profile)
	}
}

func variableGetObservation(value any, found bool, err error) string {
	if err != nil {
		return "error:" + err.Error()
	}
	if value == nil {
		return fmt.Sprintf("value=nil;found=%t;error=nil", found)
	}
	return fmt.Sprintf("value=%v;found=%t;error=nil", value, found)
}

func sortedReferenceNames(refs []concoursevars.Reference) string {
	names := make([]string, 0, len(refs))
	for _, ref := range refs {
		names = append(names, ref.String())
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}
