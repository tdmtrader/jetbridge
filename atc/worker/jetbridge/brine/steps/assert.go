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
func CheckString[T any](pattern, subject string, get func(T) (string, error), detail ...func(T) string) brine.StepDefinition {
	return check[T](pattern, stringCheck(pattern, subject, get, detail...))
}

// CheckContains is CheckString for sentences that mean the value MENTIONS
// something rather than equals it — build log output, error text.
func CheckContains[T any](pattern, subject string, get func(T) (string, error), detail ...func(T) string) brine.StepDefinition {
	return check[T](pattern, containsCheck(pattern, subject, get, detail...))
}

// CheckInt backs "… is {int}".
func CheckInt[T any](pattern, subject string, get func(T) (int, error), detail ...func(T) string) brine.StepDefinition {
	return check[T](pattern, intCheck(pattern, subject, get, detail...))
}

// CheckStringFor backs the two-parameter form, where the sentence names WHICH
// thing it is asking about before saying what it expects — "the artifact
// {string} is held on node {string}". The first parameter reaches the getter;
// the last is the expectation, which is how the sentence reads.
func CheckStringFor[T any](pattern, subject string, get func(T, string) (string, error), detail ...func(T) string) brine.StepDefinition {
	return check[T](pattern, stringForCheck(pattern, subject, get, detail...))
}

// CheckContainsFor is CheckStringFor for sentences that mean "mentions".
func CheckContainsFor[T any](pattern, subject string, get func(T, string) (string, error), detail ...func(T) string) brine.StepDefinition {
	return check[T](pattern, containsForCheck(pattern, subject, get, detail...))
}

// CheckIntFor is CheckStringFor for a numeric expectation.
func CheckIntFor[T any](pattern, subject string, get func(T, string) (int, error), detail ...func(T) string) brine.StepDefinition {
	return check[T](pattern, intForCheck(pattern, subject, get, detail...))
}

// The comparisons themselves, separated from the Define call so they can be
// exercised directly. A combinator that silently passed would neuter every
// check built on it at once, so "does it still fail" is a property this layer
// has to be able to demonstrate on its own — see assert_test.go.

func thatCheck[T any](assert func(T) error) func(T, brine.Params) error {
	return func(in T, _ brine.Params) error { return assert(in) }
}

func stringCheck[T any](pattern, subject string, get func(T) (string, error), detail ...func(T) string) func(T, brine.Params) error {
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
			return fmt.Errorf("expected %s to be %q, got %q%s", subject, want, abbrev(got), because(in, detail))
		}
		return nil
	}
}

func containsCheck[T any](pattern, subject string, get func(T) (string, error), detail ...func(T) string) func(T, brine.Params) error {
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
			return fmt.Errorf("expected %s to mention %q, got %q%s", subject, want, abbrev(got), because(in, detail))
		}
		return nil
	}
}

func intCheck[T any](pattern, subject string, get func(T) (int, error), detail ...func(T) string) func(T, brine.Params) error {
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
			return fmt.Errorf("expected %s to be %d, got %d%s", subject, want, got, because(in, detail))
		}
		return nil
	}
}

func stringForCheck[T any](pattern, subject string, get func(T, string) (string, error), detail ...func(T) string) func(T, brine.Params) error {
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
			return fmt.Errorf("expected %s for %q to be %q, got %q%s", subject, key, want, abbrev(got), because(in, detail))
		}
		return nil
	}
}

func containsForCheck[T any](pattern, subject string, get func(T, string) (string, error), detail ...func(T) string) func(T, brine.Params) error {
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
			return fmt.Errorf("expected %s for %q to mention %q, got %q%s", subject, key, want, abbrev(got), because(in, detail))
		}
		return nil
	}
}

func intForCheck[T any](pattern, subject string, get func(T, string) (int, error), detail ...func(T) string) func(T, brine.Params) error {
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
			return fmt.Errorf("expected %s for %q to be %d, got %d%s", subject, key, want, got, because(in, detail))
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

// shownValueLimit caps how much of a derived value a failure message prints.
//
// A check compares the WHOLE value and shows part of it, and those are not the
// same decision. Two checks over the build log used to hand-roll their own
// truncation for exactly this reason, and could not move onto a combinator
// because a detail function can only add text to a message — it cannot shorten
// what the comparison already printed. Truncating inside the getter was the
// other option and it is wrong: it narrows the assertion, because a match past
// the cut stops matching.
const shownValueLimit = 600

// abbrev shortens a value FOR DISPLAY, keeping both ends — a mismatch is as
// often at the end of a log as at the start — and says how much it dropped so
// the message is never quietly misleading about what was compared.
func abbrev(s string) string {
	r := []rune(s)
	if len(r) <= shownValueLimit {
		return s
	}
	head := shownValueLimit / 2
	tail := shownValueLimit - head
	return fmt.Sprintf("%s\n… %d characters elided …\n%s",
		string(r[:head]), len(r)-shownValueLimit, string(r[len(r)-tail:]))
}

// because renders the optional detail a check supplies for its failure.
//
// This is what kept most of the remaining checks off the combinators. Their
// bodies were boilerplate except for one thing: the mismatch message named
// something beyond want and got — the build log, the pods on the cluster, the
// mounts the step actually has. That context is frequently the whole
// diagnosis, and trading it for brevity would be a bad bargain, so the
// combinator carries it instead of the call site being left to hand-roll
// sixteen lines around it.
func because[T any](in T, detail []func(T) string) string {
	var parts []string
	for _, d := range detail {
		if d == nil {
			continue
		}
		if s := d(in); s != "" {
			parts = append(parts, s)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, "; ") + ")"
}

// CheckCount backs "… {int} <things>": the state holds a collection, the
// sentence says how many, and a wrong count is only diagnosable from seeing
// what is actually in there — so the failure always lists it.
func CheckCount[T any](pattern, subject string, get func(T) ([]string, error), detail ...func(T) string) brine.StepDefinition {
	return check[T](pattern, countCheck(pattern, subject, get, detail...))
}

// CheckMember backs "… {string}" where the sentence asserts the collection
// CONTAINS something rather than equals it — a mount among the mounts, a pod
// among the pods. Membership is not equality on one derived value, and the
// failure lists every member, which is how you see what was there instead.
func CheckMember[T any](pattern, subject string, get func(T) ([]string, error), detail ...func(T) string) brine.StepDefinition {
	return check[T](pattern, memberCheck(pattern, subject, get, true, detail...))
}

// CheckNotMember is CheckMember for a sentence that asserts absence. It takes
// a parameter, so CheckThat cannot express it, and it fails when the member is
// PRESENT — which no comparison combinator does.
func CheckNotMember[T any](pattern, subject string, get func(T) ([]string, error), detail ...func(T) string) brine.StepDefinition {
	return check[T](pattern, memberCheck(pattern, subject, get, false, detail...))
}

func countCheck[T any](pattern, subject string, get func(T) ([]string, error), detail ...func(T) string) func(T, brine.Params) error {
	return func(in T, p brine.Params) error {
		want, err := intAt(pattern, p, 0)
		if err != nil {
			return err
		}
		got, err := get(in)
		if err != nil {
			return err
		}
		if len(got) != want {
			return fmt.Errorf("expected %d %s, found %d: %v%s", want, subject, len(got), got, because(in, detail))
		}
		return nil
	}
}

func memberCheck[T any](pattern, subject string, get func(T) ([]string, error), want bool, detail ...func(T) string) func(T, brine.Params) error {
	return func(in T, p brine.Params) error {
		member, err := paramAt(pattern, p, 0)
		if err != nil {
			return err
		}
		got, err := get(in)
		if err != nil {
			return err
		}
		found := false
		for _, g := range got {
			if g == member {
				found = true
				break
			}
		}
		if found != want {
			if want {
				return fmt.Errorf("expected %s to include %q, found %v%s", subject, member, got, because(in, detail))
			}
			return fmt.Errorf("expected %s not to include %q, but it does: %v%s", subject, member, got, because(in, detail))
		}
		return nil
	}
}

// -----------------------------------------------------------------------
// Refinements
// -----------------------------------------------------------------------

// Refine backs a map step whose In and Out are the same type: a step that
// adjusts a draft rather than transforming it into something else. Because the
// live state's type is unchanged, any number of them may appear in any order
// before the thing under description is built.
//
// 87 of them were spelled out by hand at ~10 lines each, and every one of those
// lines except the assignment was ceremony: the handler signature, the
// parameter extraction, the unreachable guard, and the two-value return.
//
//	Refine[ContainerDraft]("it caches {string}",
//	    func(in ContainerDraft, a Args) ContainerDraft {
//	        in.Caches = append(in.Caches, a.String(0))
//	        return in
//	    }),
//
// One combinator covers every arity and any mix of {string} and {int}, so
// there is one name to learn rather than a family named after shapes.
//
// A refinement here cannot fail. That is not a limitation being worked around
// — it is what these steps are. A step that CAN fail describes something the
// runtime might refuse, and it keeps brine.DefineMap so the failure is on the
// page where a reader will look for it.
func Refine[T any](pattern string, apply func(T, Args) T) brine.StepDefinition {
	return brine.DefineMap[T, T](pattern, refineHandler(pattern, apply))
}

// refineHandler is separated from the Define call for the same reason the
// comparisons above are: a combinator that dropped the refinement on the floor
// would leave every step built on it doing nothing, and the suite would stay
// green because the next step reads a state that merely looks unchanged. A
// first version of the test for this only inspected the definition's types and
// drove Args directly, and a mutation returning `in` instead of the refined
// value went unnoticed.
func refineHandler[T any](pattern string, apply func(T, Args) T) func(T, brine.Params, *brine.Recorder) (T, error) {
	return func(in T, p brine.Params, _ *brine.Recorder) (T, error) {
		args := Args{pattern: pattern, params: p, missing: &[]string{}}
		out := apply(in, args)
		if bad := *args.missing; len(bad) > 0 {
			var zero T
			return zero, fmt.Errorf("step %q reads %s, which its pattern does not declare",
				pattern, strings.Join(bad, " and "))
		}
		return out, nil
	}
}

// Args reads a step's parameters without the four lines of guard each read
// used to need.
//
// Those guards could not fire — brine only dispatches a step after its pattern
// matched, so a declared capture is always there. What CAN happen is a
// definition reading a parameter its sentence never declared, and that is an
// authoring bug rather than a runtime condition. Args records such a read and
// Refine reports it once, naming the pattern, instead of every call site
// carrying a branch for it.
type Args struct {
	pattern string
	params  brine.Params
	missing *[]string
}

// String returns the n-th capture.
func (a Args) String(n int) string {
	s, ok := a.params.GetString(n)
	if !ok {
		*a.missing = append(*a.missing, fmt.Sprintf("parameter %d", n))
	}
	return s
}

// Int returns the n-th capture as a number. {int} compiles to (-?\d+), so the
// only way this fails is a value too large to hold, which is reported rather
// than quietly compared as zero.
func (a Args) Int(n int) int {
	v, ok := a.params.GetInt(n)
	if !ok {
		raw, present := a.params.GetString(n)
		if !present {
			*a.missing = append(*a.missing, fmt.Sprintf("parameter %d", n))
		} else {
			*a.missing = append(*a.missing, fmt.Sprintf("parameter %d as a number (%q)", n, raw))
		}
	}
	return v
}
