package steps

import (
	"fmt"
	"strings"

	"github.com/brine-dev/brine-go/pkg/brine"
)

// The check vocabulary.
//
// 244 of the 520 step definitions are checks, and most of them do the same
// four things in the same order: pull a parameter, derive a value from the
// live state, compare the two, and format a message. Spelled out that is
// sixteen lines; declared it is three or four, and the only part that was
// ever specific to the sentence — which value to derive — is the only part
// left on the page.
//
// The Gherkin is untouched by this. Consolidating how a step is BACKED is not
// the same as consolidating what its sentence says: two scenarios that
// genuinely assert different things keep their own sentences, and should.
//
// On the guard this replaces. Nearly every one of those checks opened with
//
//	want, ok := p.GetString(0)
//	if !ok {
//	    return fmt.Errorf("expected a name parameter")
//	}
//
// and that guard could not fire. brine's registry compiles {string} to
// "([^"]*)" and {int} to (-?\d+), and only dispatches a step after that
// pattern has matched the line, so a capture the pattern declares is always
// present and GetString's only failure mode — an out-of-range index — cannot
// arise. GetInt can still refuse a capture that overflows an int, which is
// why the numeric path below reports rather than assumes; but the
// missing-parameter arm was 150-odd copies of an assertion with no way to run.
//
// Here the case is handled once, at the point where it IS reachable: a
// definition that reads a parameter its pattern never declared is an
// authoring bug, and paramAt names the pattern so it can be found.

// paramAt reads the n-th capture, reporting against the pattern when a
// definition and its pattern disagree about how many there are.
func paramAt(pattern string, p brine.Params, n int) (string, error) {
	s, ok := p.GetString(n)
	if !ok {
		return "", fmt.Errorf("step %q reads parameter %d, but its pattern does not declare one", pattern, n)
	}
	return s, nil
}

func intAt(pattern string, p brine.Params, n int) (int, error) {
	v, ok := p.GetInt(n)
	if !ok {
		s, _ := p.GetString(n)
		return 0, fmt.Errorf("step %q: parameter %d is not usable as a number (%q)", pattern, n, s)
	}
	return v, nil
}

// check adapts a comparison to brine's handler signature. No check in this
// registry uses the Recorder, so the combinators do not carry it.
func check[T any](pattern string, run func(T, brine.Params) error) brine.StepDefinition {
	return brine.DefineCheck[T](pattern, func(in T, p brine.Params, _ *brine.Recorder) error {
		return run(in, p)
	})
}

// CheckThat backs a check that takes no parameter: the sentence names the
// condition outright and the body only decides whether it holds.
func CheckThat[T any](pattern string, assert func(T) error) brine.StepDefinition {
	return check[T](pattern, thatCheck(assert))
}

// CheckString backs "… is {string}": derive one value, compare for equality.
// subject names the thing being compared and is what a failure talks about —
// "the main container image", not "the value".
func CheckString[T any](pattern, subject string, get func(T) (string, error)) brine.StepDefinition {
	return check[T](pattern, stringCheck(pattern, subject, get))
}

// CheckContains is CheckString for sentences that mean the value MENTIONS
// something rather than equals it — build log output, error text.
func CheckContains[T any](pattern, subject string, get func(T) (string, error)) brine.StepDefinition {
	return check[T](pattern, containsCheck(pattern, subject, get))
}

// CheckInt backs "… is {int}".
func CheckInt[T any](pattern, subject string, get func(T) (int, error)) brine.StepDefinition {
	return check[T](pattern, intCheck(pattern, subject, get))
}

// CheckStringFor backs the two-parameter form, where the sentence names WHICH
// thing it is asking about before saying what it expects — "the artifact
// {string} is held on node {string}". The first parameter reaches the getter;
// the last is the expectation, which is how the sentence reads.
func CheckStringFor[T any](pattern, subject string, get func(T, string) (string, error)) brine.StepDefinition {
	return check[T](pattern, stringForCheck(pattern, subject, get))
}

// CheckContainsFor is CheckStringFor for sentences that mean "mentions".
func CheckContainsFor[T any](pattern, subject string, get func(T, string) (string, error)) brine.StepDefinition {
	return check[T](pattern, containsForCheck(pattern, subject, get))
}

// CheckIntFor is CheckStringFor for a numeric expectation.
func CheckIntFor[T any](pattern, subject string, get func(T, string) (int, error)) brine.StepDefinition {
	return check[T](pattern, intForCheck(pattern, subject, get))
}

// The comparisons themselves, separated from the Define call so they can be
// exercised directly. A combinator that silently passed would neuter every
// check built on it at once, so "does it still fail" is a property this layer
// has to be able to demonstrate on its own — see assert_test.go.

func thatCheck[T any](assert func(T) error) func(T, brine.Params) error {
	return func(in T, _ brine.Params) error { return assert(in) }
}

func stringCheck[T any](pattern, subject string, get func(T) (string, error)) func(T, brine.Params) error {
	return func(in T, p brine.Params) error {
		want, err := paramAt(pattern, p, 0)
		if err != nil {
			return err
		}
		got, err := get(in)
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("expected %s to be %q, got %q", subject, want, got)
		}
		return nil
	}
}

func containsCheck[T any](pattern, subject string, get func(T) (string, error)) func(T, brine.Params) error {
	return func(in T, p brine.Params) error {
		want, err := paramAt(pattern, p, 0)
		if err != nil {
			return err
		}
		got, err := get(in)
		if err != nil {
			return err
		}
		if !strings.Contains(got, want) {
			return fmt.Errorf("expected %s to mention %q, got %q", subject, want, got)
		}
		return nil
	}
}

func intCheck[T any](pattern, subject string, get func(T) (int, error)) func(T, brine.Params) error {
	return func(in T, p brine.Params) error {
		want, err := intAt(pattern, p, 0)
		if err != nil {
			return err
		}
		got, err := get(in)
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("expected %s to be %d, got %d", subject, want, got)
		}
		return nil
	}
}

func stringForCheck[T any](pattern, subject string, get func(T, string) (string, error)) func(T, brine.Params) error {
	return func(in T, p brine.Params) error {
		key, want, err := twoParams(pattern, p)
		if err != nil {
			return err
		}
		got, err := get(in, key)
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("expected %s for %q to be %q, got %q", subject, key, want, got)
		}
		return nil
	}
}

func containsForCheck[T any](pattern, subject string, get func(T, string) (string, error)) func(T, brine.Params) error {
	return func(in T, p brine.Params) error {
		key, want, err := twoParams(pattern, p)
		if err != nil {
			return err
		}
		got, err := get(in, key)
		if err != nil {
			return err
		}
		if !strings.Contains(got, want) {
			return fmt.Errorf("expected %s for %q to mention %q, got %q", subject, key, want, got)
		}
		return nil
	}
}

func intForCheck[T any](pattern, subject string, get func(T, string) (int, error)) func(T, brine.Params) error {
	return func(in T, p brine.Params) error {
		key, err := paramAt(pattern, p, 0)
		if err != nil {
			return err
		}
		want, err := intAt(pattern, p, 1)
		if err != nil {
			return err
		}
		got, err := get(in, key)
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("expected %s for %q to be %d, got %d", subject, key, want, got)
		}
		return nil
	}
}

func twoParams(pattern string, p brine.Params) (string, string, error) {
	key, err := paramAt(pattern, p, 0)
	if err != nil {
		return "", "", err
	}
	want, err := paramAt(pattern, p, 1)
	if err != nil {
		return "", "", err
	}
	return key, want, nil
}
