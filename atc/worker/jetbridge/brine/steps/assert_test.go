package steps

import (
	"errors"
	"strings"
	"testing"

	"github.com/brine-dev/brine-go/pkg/brine"
)

// The combinators in assert.go back 200-odd checks between them. A bug in one
// of them that made it pass unconditionally would silently disarm every check
// built on it, and the suite would stay green while asserting nothing — the
// failure mode this whole migration has been hunting.
//
// So each combinator has to demonstrate the same two things: it passes when
// the value matches, and it FAILS when the value does not. The negative half
// is the point. Params come from brine's own registry rather than being
// hand-built, so the pattern compilation and the capture indexing under test
// are the real ones.

type probe struct {
	value string
	num   int
	byKey map[string]string
	err   error
}

// paramsFor compiles the pattern through brine's registry and matches line
// against it, yielding the same Params a running scenario would produce.
func paramsFor(t *testing.T, def brine.StepDefinition, line string) brine.Params {
	t.Helper()
	_, p, ok := brine.NewStepRegistry([]brine.StepDefinition{def}).Lookup(line)
	if !ok {
		t.Fatalf("pattern %q did not match %q", def.Pattern(), line)
	}
	return p
}

func TestCheckStringPassesAndFails(t *testing.T) {
	const pat = "the value is {string}"
	def := CheckString[probe](pat, "the value", func(in probe) (string, error) { return in.value, nil })
	run := stringCheck(pat, "the value", func(in probe) (string, error) { return in.value, nil })

	p := paramsFor(t, def, `the value is "expected"`)

	if err := run(probe{value: "expected"}, p); err != nil {
		t.Fatalf("matching value should pass, got %v", err)
	}
	err := run(probe{value: "something else"}, p)
	if err == nil {
		t.Fatal("a value that does not match MUST fail; it passed")
	}
	if !strings.Contains(err.Error(), "the value") || !strings.Contains(err.Error(), "something else") {
		t.Fatalf("the failure should name the subject and what it found, got: %v", err)
	}
}

func TestCheckStringPropagatesGetterError(t *testing.T) {
	const pat = "the value is {string}"
	boom := errors.New("the state does not hold a value")
	def := CheckString[probe](pat, "the value", func(probe) (string, error) { return "", boom })
	run := stringCheck(pat, "the value", func(probe) (string, error) { return "", boom })
	if err := run(probe{}, paramsFor(t, def, `the value is "x"`)); !errors.Is(err, boom) {
		t.Fatalf("a getter that cannot derive its value must fail the check, got %v", err)
	}
}

func TestCheckContainsPassesAndFails(t *testing.T) {
	const pat = "the log mentions {string}"
	def := CheckContains[probe](pat, "the log", func(in probe) (string, error) { return in.value, nil })
	run := containsCheck(pat, "the log", func(in probe) (string, error) { return in.value, nil })
	p := paramsFor(t, def, `the log mentions "needle"`)

	if err := run(probe{value: "a haystack with a needle in it"}, p); err != nil {
		t.Fatalf("a substring should pass, got %v", err)
	}
	if err := run(probe{value: "a haystack"}, p); err == nil {
		t.Fatal("a missing substring MUST fail; it passed")
	}
	// Equality is not the test: an exact match still contains itself, but a
	// value that merely EQUALS nothing must not slip through.
	if err := run(probe{value: "needle"}, p); err != nil {
		t.Fatalf("an exact match should pass, got %v", err)
	}
}

func TestCheckIntPassesAndFails(t *testing.T) {
	const pat = "it exited {int}"
	def := CheckInt[probe](pat, "the exit status", func(in probe) (int, error) { return in.num, nil })
	run := intCheck(pat, "the exit status", func(in probe) (int, error) { return in.num, nil })
	p := paramsFor(t, def, "it exited 3")

	if err := run(probe{num: 3}, p); err != nil {
		t.Fatalf("a matching number should pass, got %v", err)
	}
	if err := run(probe{num: 0}, p); err == nil {
		t.Fatal("a different number MUST fail; it passed")
	}
	// Zero is the value a Go struct has when nothing set it, so a check that
	// treated the zero value as "not supplied" would pass vacuously.
	zero := paramsFor(t, def, "it exited 0")
	if err := run(probe{num: 1}, zero); err == nil {
		t.Fatal("expecting 0 and finding 1 MUST fail; it passed")
	}
}

func TestCheckIntRejectsAnUnusableNumber(t *testing.T) {
	// {int} compiles to (-?\d+), so a capture is always digits — but one too
	// large for an int still cannot be used, and must be reported rather than
	// silently compared against zero.
	const pat = "it exited {int}"
	def := CheckInt[probe](pat, "the exit status", func(in probe) (int, error) { return in.num, nil })
	run := intCheck(pat, "the exit status", func(in probe) (int, error) { return in.num, nil })
	p := paramsFor(t, def, "it exited 99999999999999999999999")
	err := run(probe{num: 0}, p)
	if err == nil {
		t.Fatal("a number that does not fit MUST fail rather than compare as zero")
	}
	if !strings.Contains(err.Error(), pat) {
		t.Fatalf("the failure should name the step, got: %v", err)
	}
}

func TestCheckStringForRoutesTheKeyAndComparesTheLast(t *testing.T) {
	const pat = "the artifact {string} is held on node {string}"
	get := func(in probe, key string) (string, error) { return in.byKey[key], nil }
	def := CheckStringFor[probe](pat, "the holding node", get)
	run := stringForCheck(pat, "the holding node", get)
	p := paramsFor(t, def, `the artifact "sha:abc" is held on node "node-1"`)

	state := probe{byKey: map[string]string{"sha:abc": "node-1", "sha:def": "node-2"}}
	if err := run(state, p); err != nil {
		t.Fatalf("the right node for the right key should pass, got %v", err)
	}
	// If the two parameters were transposed the getter would be handed
	// "node-1" and find nothing, so this also pins the argument order.
	if err := run(probe{byKey: map[string]string{"sha:abc": "node-2"}}, p); err == nil {
		t.Fatal("the wrong node MUST fail; it passed")
	}
	if err := run(probe{byKey: map[string]string{}}, p); err == nil {
		t.Fatal("a key the state does not hold MUST fail; it passed")
	}
}

func TestCheckIntForRoutesTheKeyAndComparesTheLast(t *testing.T) {
	const pat = "the queue {string} holds {int}"
	get := func(in probe, key string) (int, error) { return len(in.byKey[key]), nil }
	def := CheckIntFor[probe](pat, "the queue depth", get)
	run := intForCheck(pat, "the queue depth", get)
	p := paramsFor(t, def, `the queue "a" holds 3`)

	if err := run(probe{byKey: map[string]string{"a": "xyz"}}, p); err != nil {
		t.Fatalf("a matching depth should pass, got %v", err)
	}
	if err := run(probe{byKey: map[string]string{"a": "xy"}}, p); err == nil {
		t.Fatal("a different depth MUST fail; it passed")
	}
}

func TestCheckContainsForPassesAndFails(t *testing.T) {
	const pat = "the entry {string} mentions {string}"
	get := func(in probe, key string) (string, error) { return in.byKey[key], nil }
	def := CheckContainsFor[probe](pat, "the entry", get)
	run := containsForCheck(pat, "the entry", get)
	p := paramsFor(t, def, `the entry "a" mentions "needle"`)

	if err := run(probe{byKey: map[string]string{"a": "has a needle"}}, p); err != nil {
		t.Fatalf("a substring should pass, got %v", err)
	}
	if err := run(probe{byKey: map[string]string{"a": "has nothing"}}, p); err == nil {
		t.Fatal("a missing substring MUST fail; it passed")
	}
}

func TestCheckThatPassesAndFails(t *testing.T) {
	boom := errors.New("it did not hold")
	run := thatCheck(func(in probe) error { return in.err })
	if err := run(probe{}, brine.Params{}); err != nil {
		t.Fatalf("a condition that holds should pass, got %v", err)
	}
	if err := run(probe{err: boom}, brine.Params{}); !errors.Is(err, boom) {
		t.Fatalf("a condition that does not hold MUST fail, got %v", err)
	}
}

func TestParamAtNamesTheStepWhenThePatternDeclaresNoParameter(t *testing.T) {
	// The authoring bug the unreachable !ok guards were pretending to catch:
	// a definition that reads a parameter its sentence never declared.
	const pat = "the value is right"
	def := CheckString[probe](pat, "the value", func(in probe) (string, error) { return in.value, nil })
	run := stringCheck(pat, "the value", func(in probe) (string, error) { return in.value, nil })
	err := run(probe{value: "x"}, paramsFor(t, def, "the value is right"))
	if err == nil {
		t.Fatal("reading a parameter that the pattern does not declare MUST fail")
	}
	if !strings.Contains(err.Error(), pat) {
		t.Fatalf("the failure should name the step so it can be found, got: %v", err)
	}
}
