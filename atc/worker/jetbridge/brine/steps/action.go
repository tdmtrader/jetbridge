package steps

import (
	"regexp"

	"github.com/brine-dev/brine-go/pkg/brine"
)

// Transform keeps fallible operations explicit while sharing parameter
// validation. Unlike Refine, its callback returns an error from the operation.
// The state transition and resource declarations remain ordinary brine maps.
func Transform[In, Out any](pattern string, run func(In, Args) (Out, error)) brine.StepDefinition {
	return brine.DefineMap[In, Out](pattern,
		func(in In, p brine.Params, _ *brine.Recorder) (Out, error) {
			return applyAction(pattern, in, p, run)
		})
}

func TransformUsing[In, Out any](pattern string, uses []string, run func(In, Args, brine.Resources) (Out, error)) brine.StepDefinition {
	return brine.DefineMapUsing[In, Out](pattern, uses,
		func(in In, p brine.Params, _ *brine.Recorder, res brine.Resources) (Out, error) {
			return applyAction(pattern, in, p, func(in In, a Args) (Out, error) {
				return run(in, a, res)
			})
		})
}

// These are brine's two positional capture types, in their declared order.
// Validate before the callback: an overflowing integer must not become a zero
// passed to a database write or a process invocation.
var actionCaptures = regexp.MustCompile(`\{string\}|\{int\}`)

func applyAction[In, Out any](pattern string, in In, p brine.Params, run func(In, Args) (Out, error)) (Out, error) {
	var zero Out
	a := Args{pattern: pattern, params: p, missing: &[]string{}}
	for i, kind := range actionCaptures.FindAllString(pattern, -1) {
		if kind == "{int}" {
			a.Int(i)
		} else {
			a.String(i)
		}
	}
	if err := a.Err(); err != nil {
		return zero, err
	}
	out, err := run(in, a)
	if err != nil {
		return out, err
	}
	if err := a.Err(); err != nil {
		return zero, err
	}
	return out, nil
}

// Assert keeps custom predicates and diagnostics intact while sharing the
// same capture validation as Transform. It is a check: no state transition.
func Assert[T any](pattern string, assert func(T, Args) error) brine.StepDefinition {
	return check[T](pattern, func(in T, p brine.Params) error {
		_, err := applyAction(pattern, in, p, func(in T, args Args) (brine.Empty, error) {
			return brine.Empty{}, assert(in, args)
		})
		return err
	})
}
