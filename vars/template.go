package vars

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/hashicorp/go-multierror"
	"go.yaml.in/yaml/v2"
)

type Template struct {
	bytes []byte
}

type EvaluateOpts struct {
	ExpectAllKeys     bool
	ExpectAllVarsUsed bool

	// ExcludeReference, when non-nil, decides whether an already-parsed
	// reference must be left in place rather than interpolated. Excluded
	// references are never looked up and take no part in missing- or
	// visited-variable accounting.
	ExcludeReference ReferenceExclusion
}

// ReferenceExclusion reports whether a reference should be left untouched by
// interpolation.
type ReferenceExclusion func(Reference) bool

// NewExactReferenceExclusion builds an exclusion predicate that matches only
// exact, unqualified references: ((name)) is excluded, while ((source:name))
// and ((name.field)) remain ordinary vars. The names are copied, so callers
// may reuse or mutate their input afterwards.
func NewExactReferenceExclusion(names []string) ReferenceExclusion {
	excluded := make(map[string]struct{}, len(names))
	for _, name := range names {
		excluded[name] = struct{}{}
	}
	return func(reference Reference) bool {
		if reference.Source != "" || len(reference.Fields) != 0 {
			return false
		}
		_, found := excluded[reference.Path]
		return found
	}
}

func NewTemplate(bytes []byte) Template {
	return Template{bytes: bytes}
}

func (t Template) ExtraVarNames() []string {
	return interpolator{}.extractVarNames(string(t.bytes))
}

func (t Template) Evaluate(vars Variables, opts EvaluateOpts) ([]byte, error) {
	var obj any

	// Note-1: if we do end up changing from "go.yaml.in/yaml/v2" to
	// "sigs.k8s.io/yaml" here, we'll want to ensure we call
	// `json.Decoder.UseNumber()` so that we don't lose precision unmarshaling
	// numbers to float64.
	//
	// Note-2: Trying to upgrade to yaml/v4 results in a difference in behaviour
	// with what we get passed back from Unmarshal(). If all the keys in a map
	// are of the same type, we'll get a map with the keys set to that type.
	// This breaks the switch statement in interpolater.Interpolate() as we
	// won't catch maps of specific types like map[string] or map[int]. Tried a
	// few different approaches but the problem scope blows up everytime. Feel
	// free to try upgrading and then running the tests in this package to see
	// all the things that break.
	err := yaml.Unmarshal(t.bytes, &obj)
	if err != nil {
		return []byte{}, err
	}

	obj, err = t.interpolateRoot(obj, newVarsTracker(vars, opts.ExpectAllKeys, opts.ExpectAllVarsUsed, opts.ExcludeReference))
	if err != nil {
		return []byte{}, err
	}

	bytes, err := yaml.Marshal(obj)
	if err != nil {
		return []byte{}, err
	}

	return bytes, nil
}

func (t Template) interpolateRoot(obj any, tracker varsTracker) (any, error) {
	var err error
	obj, err = interpolator{}.Interpolate(obj, tracker)
	if err != nil {
		return nil, err
	}

	return obj, tracker.Error()
}

type interpolator struct{}

var (
	interpolationRegex         = regexp.MustCompile(`\(\((([-/\.\w\pL]+\:)?[-/\.:@"\w\pL]+)\)\)`)
	interpolationAnchoredRegex = regexp.MustCompile("\\A" + interpolationRegex.String() + "\\z")
)

func (i interpolator) Interpolate(node any, tracker varsTracker) (any, error) {
	switch typedNode := node.(type) {
	case map[any]any:
		for k, v := range typedNode {
			evaluatedValue, err := i.Interpolate(v, tracker)
			if err != nil {
				return nil, err
			}

			evaluatedKey, err := i.Interpolate(k, tracker)
			if err != nil {
				return nil, err
			}

			delete(typedNode, k) // delete in case key has changed
			typedNode[evaluatedKey] = evaluatedValue
		}

	case []any:
		for idx, x := range typedNode {
			var err error
			typedNode[idx], err = i.Interpolate(x, tracker)
			if err != nil {
				return nil, err
			}
		}

	case string:
		for _, name := range i.extractVarNames(typedNode) {
			reference, err := ParseReference(name)
			if err != nil {
				return nil, err
			}
			if tracker.Excludes(reference) {
				continue
			}

			foundVal, found, err := tracker.GetReference(reference)
			if err != nil {
				return nil, err
			}

			if found {
				// ensure that value type is preserved when replacing the entire field
				if interpolationAnchoredRegex.MatchString(typedNode) {
					return foundVal, nil
				}

				switch foundVal.(type) {
				case string, bool, int, int16, int32, int64, uint, uint16, uint32, uint64, json.Number:
					foundValStr := fmt.Sprintf("%v", foundVal)
					typedNode = strings.ReplaceAll(typedNode, fmt.Sprintf("((%s))", name), foundValStr)
				default:
					return nil, InvalidInterpolationError{
						Name:  name,
						Value: foundVal,
					}
				}
			}
		}

		return typedNode, nil
	}

	return node, nil
}

func (i interpolator) extractVarNames(value string) []string {
	var names []string

	for _, match := range interpolationRegex.FindAllSubmatch([]byte(value), -1) {
		names = append(names, string(match[1]))
	}

	return names
}

type varsTracker struct {
	vars Variables

	expectAllFound   bool
	expectAllUsed    bool
	excludeReference ReferenceExclusion

	missing map[string]struct{}
	visited map[string]struct{} // track all var names that were accessed
}

func newVarsTracker(vars Variables, expectAllFound, expectAllUsed bool, excludeReference ReferenceExclusion) varsTracker {
	return varsTracker{
		vars:             vars,
		expectAllFound:   expectAllFound,
		expectAllUsed:    expectAllUsed,
		excludeReference: excludeReference,
		missing:          map[string]struct{}{},
		visited:          map[string]struct{}{},
	}
}

func (t varsTracker) Excludes(varRef Reference) bool {
	return t.excludeReference != nil && t.excludeReference(varRef)
}

// GetReference gets the value of an already-parsed var reference, recording
// it as visited and, when absent, as missing.
func (t varsTracker) GetReference(varRef Reference) (any, bool, error) {
	t.visited[identifier(varRef)] = struct{}{}

	val, found, err := t.vars.Get(varRef)
	if !found || err != nil {
		t.missing[varRef.String()] = struct{}{}
		return val, found, err
	}

	return val, true, err
}

func (t varsTracker) Error() error {
	missingErr := t.MissingError()
	extraErr := t.ExtraError()
	if missingErr != nil && extraErr != nil {
		return multierror.Append(missingErr, extraErr)
	} else if missingErr != nil {
		return missingErr
	} else if extraErr != nil {
		return extraErr
	}

	return nil
}

func (t varsTracker) MissingError() error {
	if !t.expectAllFound || len(t.missing) == 0 {
		return nil
	}

	return UndefinedVarsError{Vars: names(t.missing)}
}

func (t varsTracker) ExtraError() error {
	if !t.expectAllUsed {
		return nil
	}

	allRefs, err := t.vars.List()
	if err != nil {
		return err
	}

	unusedNames := map[string]struct{}{}

	for _, ref := range allRefs {
		id := identifier(ref)
		if _, found := t.visited[id]; !found {
			unusedNames[id] = struct{}{}
		}
	}

	if len(unusedNames) == 0 {
		return nil
	}

	return UnusedVarsError{Vars: names(unusedNames)}
}

func names(mapWithNames map[string]struct{}) []string {
	var names []string
	for name, _ := range mapWithNames {
		names = append(names, name)
	}

	sort.Strings(names)

	return names
}

func identifier(varRef Reference) string {
	id := varRef.Path

	if varRef.Source != "" {
		id = fmt.Sprintf("%s:%s", varRef.Source, id)
	}

	return id
}
