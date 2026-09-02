package steps

import (
	"fmt"
	"sort"
	"strings"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/vars"
)

type RunStateStrictObservation struct {
	Value string
}

func RunStateStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[brine.Empty, RunStateStrictObservation](
			"production RunState evaluates profile {string}",
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder) (RunStateStrictObservation, error) {
				profile, ok := p.GetString(0)
				if !ok {
					return RunStateStrictObservation{}, fmt.Errorf("missing RunState profile")
				}
				value, err := observeRunStateStrict(profile)
				return RunStateStrictObservation{Value: value}, err
			},
		),
		brine.DefineCheck[RunStateStrictObservation](
			"the RunState observation is {string}",
			func(in RunStateStrictObservation, p brine.Params, _ *brine.Recorder) error {
				want, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("missing RunState observation")
				}
				if in.Value != want {
					return fmt.Errorf("expected RunState observation %q, got %q", want, in.Value)
				}
				return nil
			},
		),
	}
}

func newRunStateStrict() exec.RunState {
	return exec.NewRunState(nil, vars.StaticVariables{"k1": "v1", "k2": "v2", "k3": "v3"})
}

func observeRunStateStrict(profile string) (string, error) {
	state := newRunStateStrict()

	switch profile {
	case "result-missing-found":
		to := 42
		return fmt.Sprintf("found=%t", state.Result("some-id", &to)), nil
	case "result-missing-preserves":
		to := 42
		state.Result("some-id", &to)
		return fmt.Sprintf("destination=%d", to), nil
	case "result-other-found":
		state.StoreResult("other", 43)
		to := 42
		return fmt.Sprintf("found=%t", state.Result("some-id", &to)), nil
	case "result-other-preserves":
		state.StoreResult("other", 43)
		to := 42
		state.Result("some-id", &to)
		return fmt.Sprintf("destination=%d", to), nil
	case "result-present-found":
		state.StoreResult("some-id", 123)
		to := 42
		return fmt.Sprintf("found=%t", state.Result("some-id", &to)), nil
	case "result-present-mutates":
		state.StoreResult("some-id", 123)
		to := 42
		state.Result("some-id", &to)
		return fmt.Sprintf("destination=%d", to), nil
	case "result-wrong-type-found":
		state.StoreResult("some-id", "one hundred and twenty-three")
		to := 42
		return fmt.Sprintf("found=%t", state.Result("some-id", &to)), nil
	case "result-wrong-type-preserves":
		state.StoreResult("some-id", "one hundred and twenty-three")
		to := 42
		state.Result("some-id", &to)
		return fmt.Sprintf("destination=%d", to), nil
	case "get-credential":
		value, found, err := state.Get(vars.Reference{Path: "k1"})
		return fmt.Sprintf("error=%t;found=%t;value=%v", err != nil, found, value), nil
	case "get-missing-local-field":
		state.AddLocalVar("foo", map[string]any{"bar": "baz"}, false)
		_, _, err := state.Get(vars.Reference{Source: ".", Path: "foo", Fields: []string{"missing"}})
		return fmt.Sprintf("error=%t", err != nil), nil
	case "get-tracked-credentials":
		_, _, err := state.Get(vars.Reference{Path: "k1"})
		if err != nil {
			return "", err
		}
		_, _, err = state.Get(vars.Reference{Path: "k2"})
		if err != nil {
			return "", err
		}
		tracked := vars.TrackedVarsMap{}
		state.IterateInterpolatedCreds(tracked)
		_, hasK3 := tracked["k3"]
		return fmt.Sprintf("k1=%s;k2=%s;k3-absent=%t", tracked["k1"], tracked["k2"], !hasK3), nil
	case "list-credentials":
		refs, err := state.List()
		return fmt.Sprintf("error=%t;refs=%s", err != nil, runStateRefs(refs)), nil
	case "list-with-locals":
		state.AddLocalVar("l1", 1, false)
		state.AddLocalVar("l2", 2, false)
		refs, err := state.List()
		return fmt.Sprintf("error=%t;refs=%s", err != nil, runStateRefs(refs)), nil
	case "local-redacted-get":
		state.AddLocalVar("foo", "bar", true)
		value, found, err := state.Get(vars.Reference{Source: ".", Path: "foo"})
		return fmt.Sprintf("error=%t;found=%t;value=%v", err != nil, found, value), nil
	case "local-redacted-tracked":
		state.AddLocalVar("foo", "bar", true)
		tracked := vars.TrackedVarsMap{}
		state.IterateInterpolatedCreds(tracked)
		return fmt.Sprintf("foo=%s", tracked["foo"]), nil
	case "scope-parent":
		scope := state.NewLocalScope()
		return fmt.Sprintf("same=%t", scope.Parent() == state), nil
	case "scope-parent-local":
		state.AddLocalVar("hello", "world", false)
		value, _, _ := state.NewLocalScope().Get(vars.Reference{Source: ".", Path: "hello"})
		return fmt.Sprintf("value=%v", value), nil
	case "scope-child-isolated":
		scope := state.NewLocalScope()
		scope.AddLocalVar("hello", "world", false)
		_, found, _ := state.Get(vars.Reference{Source: ".", Path: "hello"})
		return fmt.Sprintf("found=%t", found), nil
	case "scope-shared-credential":
		value, _, _ := state.NewLocalScope().Get(vars.Reference{Path: "k1"})
		return fmt.Sprintf("value=%v", value), nil
	case "scope-late-parent-local":
		scope := state.NewLocalScope()
		state.AddLocalVar("hello", "world", false)
		value, _, _ := scope.Get(vars.Reference{Source: ".", Path: "hello"})
		return fmt.Sprintf("value=%v", value), nil
	case "scope-child-shadows":
		state.AddLocalVar("a", 1, false)
		scope := state.NewLocalScope()
		scope.AddLocalVar("a", 2, false)
		value, _, _ := scope.Get(vars.Reference{Source: ".", Path: "a"})
		return fmt.Sprintf("value=%v", value), nil
	case "scope-parent-result-in-child":
		child := state.NewLocalScope()
		state.StoreResult("id", "hello")
		var destination string
		child.Result("id", &destination)
		return fmt.Sprintf("destination=%s", destination), nil
	case "scope-child-result-in-parent":
		child := state.NewLocalScope()
		child.StoreResult("id", "hello")
		var destination string
		state.Result("id", &destination)
		return fmt.Sprintf("destination=%s", destination), nil
	case "scope-artifact-parent":
		scope := state.NewLocalScope()
		return fmt.Sprintf("same=%t", scope.ArtifactRepository().Parent() == state.ArtifactRepository()), nil
	case "scope-tracked-child-preferred":
		state.AddLocalVar("a", "from parent", true)
		scope := state.NewLocalScope()
		scope.AddLocalVar("a", "from child", true)
		tracked := vars.TrackedVarsMap{}
		scope.IterateInterpolatedCreds(tracked)
		return fmt.Sprintf("a=%s", tracked["a"]), nil
	default:
		return "", fmt.Errorf("unknown RunState profile %q", profile)
	}
}

func runStateRefs(refs []vars.Reference) string {
	values := make([]string, 0, len(refs))
	for _, ref := range refs {
		values = append(values, ref.Source+":"+ref.Path)
	}
	sort.Strings(values)
	return strings.Join(values, ",")
}
