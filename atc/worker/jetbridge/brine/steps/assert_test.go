package steps

import (
	"errors"
	"fmt"
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
	list  []string
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

func TestCheckCountPassesAndFails(t *testing.T) {
	const pat = "the pod has {int} volumes"
	get := func(in probe) ([]string, error) { return in.list, nil }
	def := CheckCount[probe](pat, "volumes", get)
	run := countCheck(pat, "volumes", get)
	p := paramsFor(t, def, "the pod has 2 volumes")

	if err := run(probe{list: []string{"a", "b"}}, p); err != nil {
		t.Fatalf("a matching count should pass, got %v", err)
	}
	err := run(probe{list: []string{"a"}}, p)
	if err == nil {
		t.Fatal("a different count MUST fail; it passed")
	}
	// The whole reason this combinator exists rather than CheckInt: a wrong
	// count is only diagnosable from what is actually in the collection.
	if !strings.Contains(err.Error(), "[a]") {
		t.Fatalf("the failure must list the collection, got: %v", err)
	}
	// An empty collection is the state a struct has when nothing filled it, so
	// a check that treated empty as "not supplied" would pass vacuously.
	if err := run(probe{}, p); err == nil {
		t.Fatal("an empty collection MUST fail against a count of 2; it passed")
	}
}

func TestCheckMemberPassesAndFails(t *testing.T) {
	const pat = "the step's pod mounts {string}"
	get := func(in probe) ([]string, error) { return in.list, nil }
	def := CheckMember[probe](pat, "the pod's mounts", get)
	run := memberCheck(pat, "the pod's mounts", get, true)
	p := paramsFor(t, def, `the step's pod mounts "/tmp/build"`)

	if err := run(probe{list: []string{"/etc", "/tmp/build"}}, p); err != nil {
		t.Fatalf("a present member should pass, got %v", err)
	}
	err := run(probe{list: []string{"/etc"}}, p)
	if err == nil {
		t.Fatal("an absent member MUST fail; it passed")
	}
	if !strings.Contains(err.Error(), "/etc") {
		t.Fatalf("the failure must list what was there instead, got: %v", err)
	}
	// Membership is equality on an element, not a substring of one: a mount at
	// /tmp/build-cache must not satisfy a sentence about /tmp/build.
	if err := run(probe{list: []string{"/tmp/build-cache"}}, p); err == nil {
		t.Fatal("a member that merely CONTAINS the wanted string MUST fail; it passed")
	}
	if err := run(probe{}, p); err == nil {
		t.Fatal("an empty collection MUST fail; it passed")
	}
}

func TestCheckNotMemberIsTheInverse(t *testing.T) {
	const pat = "the pod carries no {string} label"
	get := func(in probe) ([]string, error) { return in.list, nil }
	def := CheckNotMember[probe](pat, "the pod's labels", get)
	run := memberCheck(pat, "the pod's labels", get, false)
	p := paramsFor(t, def, `the pod carries no "concourse.ci/job" label`)

	if err := run(probe{list: []string{"concourse.ci/worker"}}, p); err != nil {
		t.Fatalf("an absent member should pass, got %v", err)
	}
	// The direction that matters: PRESENCE is the failure. A check that shared
	// CheckMember's polarity would pass exactly when it should fail.
	if err := run(probe{list: []string{"concourse.ci/job"}}, p); err == nil {
		t.Fatal("a present member MUST fail a not-member check; it passed")
	}
}

func TestFailureDetailIsAppendedAndOnlyOnFailure(t *testing.T) {
	const pat = "it exited {int}"
	get := func(in probe) (int, error) { return in.num, nil }
	det := func(in probe) string { return "log: " + in.value }
	def := CheckInt[probe](pat, "the exit status", get, det)
	run := intCheck(pat, "the exit status", get, det)
	p := paramsFor(t, def, "it exited 0")

	if err := run(probe{num: 0, value: "hello"}, p); err != nil {
		t.Fatalf("a match should pass whatever the detail says, got %v", err)
	}
	err := run(probe{num: 2, value: "hello"}, p)
	if err == nil {
		t.Fatal("a mismatch MUST fail")
	}
	if !strings.Contains(err.Error(), "log: hello") {
		t.Fatalf("the detail must reach the failure, got: %v", err)
	}
	// A detail that has nothing to add must not leave an empty bracket.
	quiet := intCheck(pat, "the exit status", get, func(probe) string { return "" })
	if e := quiet(probe{num: 2}, p).Error(); strings.Contains(e, "()") {
		t.Fatalf("an empty detail should be omitted, got: %v", e)
	}
}

func TestLongValuesAreShortenedForDisplayButComparedInFull(t *testing.T) {
	const pat = "the build log shows {string}"
	// A needle in the MIDDLE of a value far longer than the display limit —
	// exactly the region abbreviation drops. A first attempt at this test put
	// the needle at the END, which abbrev keeps, so truncating the value before
	// comparing it still passed. The point is that the comparison sees text the
	// message never shows.
	log := strings.Repeat("x", shownValueLimit*2) + "the-needle" +
		strings.Repeat("y", shownValueLimit*2) + "the-tail"
	get := func(in probe) (string, error) { return in.value, nil }
	def := CheckContains[probe](pat, "the build log", get)
	run := containsCheck(pat, "the build log", get)

	if err := run(probe{value: log}, paramsFor(t, def, `the build log shows "the-needle"`)); err != nil {
		t.Fatalf("a match past the display limit MUST still pass — the comparison is on the whole value: %v", err)
	}

	err := run(probe{value: log}, paramsFor(t, def, `the build log shows "absent"`))
	if err == nil {
		t.Fatal("a missing substring MUST fail")
	}
	msg := err.Error()
	if len(msg) > shownValueLimit*2 {
		t.Fatalf("the failure printed %d characters; long values are meant to be abbreviated", len(msg))
	}
	// Abbreviating must not be able to masquerade as the whole value.
	if !strings.Contains(msg, "elided") {
		t.Fatalf("an abbreviated message must say what it dropped, got: %v", msg)
	}
	// Both ends survive: a mismatch is as often at the end as the start.
	if !strings.Contains(msg, "the-tail") {
		t.Fatalf("the tail of the value must survive abbreviation, got: %v", msg)
	}
	if !strings.HasPrefix(strings.SplitN(msg, "xxx", 2)[1], "x") {
		t.Fatalf("the head of the value must survive abbreviation, got: %v", msg)
	}
	// A short value is untouched.
	if got := abbrev("short"); got != "short" {
		t.Fatalf("a short value must pass through unchanged, got %q", got)
	}
}

func TestDetailReachesEveryCombinator(t *testing.T) {
	// The exemption this fixes: authors were told a check could not move
	// because "the For combinators take no detail func". Whether a combinator
	// carries context should not depend on which one it is.
	det := func(in probe) string { return "ctx:" + in.value }
	cases := map[string]func(probe, brine.Params) error{
		"CheckStringFor": stringForCheck("p {string} q {string}", "s",
			func(in probe, k string) (string, error) { return in.byKey[k], nil }, det),
		"CheckIntFor": intForCheck("p {string} q {int}", "s",
			func(in probe, k string) (int, error) { return len(in.byKey[k]), nil }, det),
		"CheckCount": countCheck("p {int} q", "things",
			func(in probe) ([]string, error) { return in.list, nil }, det),
		"CheckMember": memberCheck("p {string} q", "things",
			func(in probe) ([]string, error) { return in.list, nil }, true, det),
		"CheckNotMember": memberCheck("p {string} q", "things",
			func(in probe) ([]string, error) { return in.list, nil }, false, det),
	}
	lines := map[string]string{
		"CheckStringFor": `p "k" q "want"`,
		"CheckIntFor":    `p "k" q 9`,
		"CheckCount":     `p 9 q`,
		"CheckMember":    `p "absent" q`,
		"CheckNotMember": `p "here" q`,
	}
	patterns := map[string]string{
		"CheckStringFor": "p {string} q {string}",
		"CheckIntFor":    "p {string} q {int}",
		"CheckCount":     "p {int} q",
		"CheckMember":    "p {string} q",
		"CheckNotMember": "p {string} q",
	}
	state := probe{value: "seen", byKey: map[string]string{"k": "other"}, list: []string{"here"}}
	for name, run := range cases {
		def := CheckThat[probe](patterns[name], func(probe) error { return nil })
		err := run(state, paramsFor(t, def, lines[name]))
		if err == nil {
			t.Errorf("%s: expected a failure to attach detail to", name)
			continue
		}
		if !strings.Contains(err.Error(), "ctx:seen") {
			t.Errorf("%s: detail did not reach the failure: %v", name, err)
		}
	}
}

// A draft the refinement steps adjust, standing in for ContainerDraft.
type draft struct {
	paths []string
	dir   string
	n     int
	flag  bool
}

func TestRefineAppliesTheChangeAndCarriesItForward(t *testing.T) {
	def := Refine[draft]("it caches {string}", func(in draft, a Args) draft {
		in.paths = append(in.paths, a.String(0))
		return in
	})
	if def.Pattern() != "it caches {string}" {
		t.Fatalf("the pattern must reach the definition unchanged, got %q", def.Pattern())
	}
	if def.Mode() != brine.ModeMap {
		t.Fatal("a refinement is a map step; a check would not carry its result forward")
	}
	if def.InType() != def.OutType() {
		t.Fatalf("a refinement's In and Out must be the same type, got %v -> %v", def.InType(), def.OutType())
	}
}

func TestRefineReadsEveryParameterShape(t *testing.T) {
	const pat = "it belongs to job {int} step {string} with {int} retries"
	def := Refine[draft](pat, func(in draft, a Args) draft {
		in.n = a.Int(0)
		in.dir = a.String(1)
		in.paths = append(in.paths, fmt.Sprint(a.Int(2)))
		return in
	})
	// Drive the real handler the way the pipeline would.
	reg := brine.NewStepRegistry([]brine.StepDefinition{def})
	if _, _, ok := reg.Lookup(`it belongs to job 7 step "build" with 3 retries`); !ok {
		t.Fatal("the pattern did not match a line it declares")
	}
	// Mixed {int}/{string} in one pattern is the case a family of
	// shape-named combinators would have needed a separate name for.
	_, p, _ := reg.Lookup(`it belongs to job 7 step "build" with 3 retries`)
	var missing []string
	a := Args{pattern: pat, params: p, missing: &missing}
	if got := a.Int(0); got != 7 {
		t.Errorf("Int(0) = %d, want 7", got)
	}
	if got := a.String(1); got != "build" {
		t.Errorf("String(1) = %q, want \"build\"", got)
	}
	if got := a.Int(2); got != 3 {
		t.Errorf("Int(2) = %d, want 3", got)
	}
	if len(missing) != 0 {
		t.Errorf("no read should have been recorded as missing, got %v", missing)
	}
}

func TestRefineReportsAReadThePatternDoesNotDeclare(t *testing.T) {
	// The authoring bug the unreachable guards pretended to catch. Without
	// this, a refinement reading a parameter its sentence never declared
	// would silently apply the zero value and pass.
	const pat = "it runs privileged"
	var missing []string
	a := Args{pattern: pat, params: brine.Params{}, missing: &missing}
	if got := a.String(0); got != "" {
		t.Errorf("a read past the declared parameters should yield the zero value, got %q", got)
	}
	if len(missing) != 1 {
		t.Fatalf("the out-of-range read MUST be recorded, got %v", missing)
	}
	if !strings.Contains(missing[0], "parameter 0") {
		t.Errorf("the record should name which parameter, got %v", missing)
	}
}

func TestRefineReportsANumberItCannotUse(t *testing.T) {
	const pat = "it retries {int} times"
	def := Refine[draft](pat, func(in draft, a Args) draft { in.n = a.Int(0); return in })
	_, p, ok := brine.NewStepRegistry([]brine.StepDefinition{def}).
		Lookup("it retries 99999999999999999999999 times")
	if !ok {
		t.Fatal("the pattern did not match")
	}
	var missing []string
	a := Args{pattern: pat, params: p, missing: &missing}
	if got := a.Int(0); got != 0 {
		t.Errorf("an unusable number should yield zero, got %d", got)
	}
	if len(missing) != 1 || !strings.Contains(missing[0], "as a number") {
		t.Fatalf("a number that does not fit MUST be recorded rather than compared as zero, got %v", missing)
	}
}

func TestRefineCarriesTheRefinedValueOut(t *testing.T) {
	// The end-to-end property, and the one an earlier version of these tests
	// missed: a Refine that returned its INPUT instead of the refined value
	// passed everything above, because none of it ran the handler.
	const pat = "it caches {string}"
	apply := func(in draft, a Args) draft {
		in.paths = append(in.paths, a.String(0))
		in.flag = true
		return in
	}
	def := Refine[draft](pat, apply)
	run := refineHandler(pat, apply)

	_, p, ok := brine.NewStepRegistry([]brine.StepDefinition{def}).Lookup(`it caches "/tmp/cache"`)
	if !ok {
		t.Fatal("the pattern did not match")
	}
	out, err := run(draft{}, p, nil)
	if err != nil {
		t.Fatalf("a refinement must not fail, got %v", err)
	}
	if len(out.paths) != 1 || out.paths[0] != "/tmp/cache" {
		t.Fatalf("the refinement MUST reach the value carried forward, got %v", out.paths)
	}
	if !out.flag {
		t.Fatal("every field the refinement set MUST survive, not just the one read from a parameter")
	}
	// Refinements compose: the second must see the first's result.
	out2, err := run(out, p, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out2.paths) != 2 {
		t.Fatalf("a second refinement MUST build on the first, got %v", out2.paths)
	}
}

func TestRefineFailsTheStepOnAnUndeclaredRead(t *testing.T) {
	const pat = "it runs privileged"
	apply := func(in draft, a Args) draft { in.dir = a.String(0); return in }
	def := Refine[draft](pat, apply)
	run := refineHandler(pat, apply)

	_, p, ok := brine.NewStepRegistry([]brine.StepDefinition{def}).Lookup("it runs privileged")
	if !ok {
		t.Fatal("the pattern did not match")
	}
	_, err := run(draft{}, p, nil)
	if err == nil {
		t.Fatal("reading a parameter the pattern does not declare MUST fail the step, not apply a zero value")
	}
	if !strings.Contains(err.Error(), pat) {
		t.Fatalf("the failure must name the step so it can be found, got %v", err)
	}
}
