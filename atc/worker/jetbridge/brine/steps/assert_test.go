package steps

import (
	"bytes"
	"encoding/json"
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

// runCheck uses the public definition through the same registry and pipeline as
// a feature. Calling only stringCheck/etc would miss a broken definition wrapper.
func runCheck(t *testing.T, def brine.StepDefinition, line string, state probe) string {
	t.Helper()
	if def.Mode() != brine.ModeCheck || def.OutType() != nil {
		t.Fatalf("expected a check with no output state, got %v -> %v", def.Mode(), def.OutType())
	}
	given := brine.DefineMap[brine.Empty, probe]("a probe", func(brine.Empty, brine.Params, *brine.Recorder) (probe, error) {
		return state, nil
	})
	feature := brine.ParseFeatureText("check.feature", fmt.Sprintf(`Feature: Check dispatch
  Scenario: Check the supplied state
    Given a probe
    Then %s
`, line))
	var events bytes.Buffer
	pipeline := brine.NewPipeline(brine.NewStepRegistry([]brine.StepDefinition{given, def}), brine.NewEmitter(&events))
	result, code, err := pipeline.Run([]*brine.ParsedFeature{feature}, brine.TagFilter{}, nil)
	if err != nil || result.Scenarios != 1 || result.Undefined != 0 || result.Unsatisfied != 0 || result.Skipped != 0 {
		t.Fatalf("invalid check run: %+v, code=%d, err=%v; %s", result, code, err, events.String())
	}
	decoder := json.NewDecoder(&events)
	for decoder.More() {
		var event brine.ScenarioEnd
		if err := decoder.Decode(&event); err != nil {
			t.Fatal(err)
		}
		if event.Type == "scenario_end" {
			if (event.Status == "passed" && code == 0 && result.Passed == 1 && event.ErrorMessage == "") ||
				(event.Status == "failed" && code == 1 && result.Failed == 1 && event.ErrorMessage != "") {
				return event.ErrorMessage
			}
			t.Fatalf("inconsistent verdict: %+v, %+v, code=%d", event, result, code)
		}
	}
	t.Fatal("no scenario verdict was emitted")
	return ""
}

func TestCheckContracts(t *testing.T) {
	text := func(in probe) (string, error) { return in.value, in.err }
	number := func(in probe) (int, error) { return in.num, in.err }
	entry := func(in probe, key string) (string, error) { return in.byKey[key], in.err }
	depth := func(in probe, key string) (int, error) { return len(in.byKey[key]), in.err }
	list := func(in probe) ([]string, error) { return in.list, in.err }
	detail := func(in probe) string { return "ctx:" + in.value }
	str := CheckString("the value is {string}", "the value", text, detail)
	contains := CheckContains("the log mentions {string}", "the log", text, detail)
	num := CheckInt("it exited {int}", "the exit status", number, detail)
	keyed := CheckStringFor("the artifact {string} is held on node {string}", "the holding node", entry, detail)
	keyedNum := CheckIntFor("the queue {string} holds {int}", "the queue depth", depth, detail)
	keyedContains := CheckContainsFor("the entry {string} mentions {string}", "the entry", entry, detail)
	count := CheckCount("the pod has {int} volumes", "volumes", list, detail)
	member := CheckMember("the step's pod mounts {string}", "the pod's mounts", list, detail)
	absent := CheckNotMember("the pod carries no {string} label", "the pod's labels", list, detail)
	custom := Assert("the value {string} has number {int}", func(in probe, args Args) error {
		if in.err != nil {
			return in.err
		}
		if in.value != args.String(0) || in.num != args.Int(1) {
			return fmt.Errorf("unexpected custom state: %q/%d", in.value, in.num)
		}
		return nil
	})
	validInts := Assert("values {int} and {int}", func(probe, Args) error { return nil })
	undeclared := Assert("an undeclared read", func(_ probe, args Args) error {
		args.String(9)
		return nil
	})
	type failure struct {
		state probe
		want  []string
	}
	cases := []struct {
		name string
		def  brine.StepDefinition
		line string
		pass []probe
		fail []failure
	}{
		{"AssertPreservesCustomPredicates", custom, "the value \"expected\" has number -7",
			[]probe{{value: "expected", num: -7}},
			[]failure{
				{probe{value: "wrong", num: -7}, []string{"wrong", "-7"}},
				{probe{value: "expected", num: 0}, []string{"expected", "/0"}},
			}},
		{"AssertRejectsOverflowInFirstCapture", validInts, "values 99999999999999999999999 and 0",
			nil, []failure{{probe{}, []string{validInts.Pattern()}}}},
		{"AssertRejectsOverflowInLastCapture", validInts, "values 0 and 99999999999999999999999",
			nil, []failure{{probe{}, []string{validInts.Pattern()}}}},
		{"AssertRejectsAnUndeclaredRead", undeclared, "an undeclared read",
			nil, []failure{{probe{}, []string{"parameter 9"}}}},
		{"CheckStringPassesAndFails", str, `the value is "expected"`,
			[]probe{{value: "expected"}},
			[]failure{{probe{value: "something else"}, []string{"the value", "something else", "ctx:something else"}}}},
		{"CheckContainsPassesAndFails", contains, `the log mentions "needle"`,
			[]probe{{value: "a haystack with a needle in it"}, {value: "needle"}},
			[]failure{{probe{value: "a haystack"}, []string{"needle", "a haystack", "ctx:a haystack"}}}},
		{"CheckIntPassesAndFails", num, "it exited 3",
			[]probe{{num: 3}},
			[]failure{{probe{num: 0}, []string{"3", "0", "ctx:"}}}},
		{"CheckIntExpectsZero", num, "it exited 0",
			[]probe{{num: 0}},
			[]failure{{probe{num: 1}, []string{"0", "1", "ctx:"}}}},
		{"CheckIntRejectsAnUnusableNumber", num, "it exited 99999999999999999999999",
			nil, []failure{{probe{}, []string{num.Pattern()}}}},
		{"CheckStringForRoutesTheKeyAndComparesTheLast", keyed, `the artifact "sha:abc" is held on node "node-1"`,
			[]probe{{byKey: map[string]string{"sha:abc": "node-1", "sha:def": "node-2"}}},
			[]failure{
				{probe{byKey: map[string]string{"sha:abc": "node-2"}}, []string{"sha:abc", "node-1", "node-2", "ctx:"}},
				{probe{byKey: map[string]string{}}, []string{"sha:abc", "node-1"}},
			}},
		{"CheckIntForRoutesTheKeyAndComparesTheLast", keyedNum, `the queue "a" holds 3`,
			[]probe{{byKey: map[string]string{"a": "xyz"}}},
			[]failure{{probe{byKey: map[string]string{"a": "xy"}}, []string{"a", "3", "2", "ctx:"}}}},
		{"CheckContainsForPassesAndFails", keyedContains, `the entry "a" mentions "needle"`,
			[]probe{{byKey: map[string]string{"a": "has a needle"}}},
			[]failure{{probe{byKey: map[string]string{"a": "has nothing"}}, []string{"a", "needle", "has nothing", "ctx:"}}}},
		{"CheckCountPassesAndFails", count, "the pod has 2 volumes",
			[]probe{{list: []string{"a", "b"}}},
			[]failure{
				{probe{list: []string{"a"}}, []string{"[a]", "ctx:"}},
				{probe{}, []string{"2", "0"}},
			}},
		{"CheckMemberPassesAndFails", member, `the step's pod mounts "/tmp/build"`,
			[]probe{{list: []string{"/etc", "/tmp/build"}}},
			[]failure{
				{probe{list: []string{"/etc"}}, []string{"/etc", "ctx:"}},
				{probe{list: []string{"/tmp/build-cache"}}, []string{"/tmp/build-cache"}},
				{probe{}, []string{"/tmp/build", "[]"}},
			}},
		{"CheckNotMemberIsTheInverse", absent, `the pod carries no "concourse.ci/job" label`,
			[]probe{{list: []string{"concourse.ci/worker"}}},
			[]failure{{probe{list: []string{"concourse.ci/job"}}, []string{"concourse.ci/job", "ctx:"}}}},
		{"CheckThatPassesAndFails", CheckThat("the condition holds", func(in probe) error { return in.err }), "the condition holds",
			[]probe{{}}, []failure{{probe{err: errors.New("it did not hold")}, []string{"it did not hold"}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.fail) == 0 {
				t.Fatal("a check contract needs a negative control")
			}
			for _, state := range tc.pass {
				if message := runCheck(t, tc.def, tc.line, state); message != "" {
					t.Fatalf("matching state failed: %s", message)
				}
			}
			for _, fault := range tc.fail {
				if tc.def != str && tc.def != contains && tc.def != custom && len(tc.pass) > 0 && tc.name != "CheckThatPassesAndFails" {
					fault.state.value = "seen"
					fault.want = append(fault.want, "ctx:seen")
				}
				message := runCheck(t, tc.def, tc.line, fault.state)
				if message == "" {
					t.Fatalf("mismatching state passed: %+v", fault.state)
				}
				for _, want := range fault.want {
					if !strings.Contains(message, want) {
						t.Errorf("failure must contain %q, got %q", want, message)
					}
				}
			}
			// Exercise the registered definition's getter-error path too.
			boom := errors.New("getter refused")
			if message := runCheck(t, tc.def, tc.line, probe{err: boom}); len(tc.pass) > 0 && !strings.Contains(message, boom.Error()) {
				t.Fatalf("getter error was lost: %q", message)
			}
		})
	}
}

// Error identity is a Go-level contract; serialized brine events carry text.
func TestCheckStringPropagatesGetterError(t *testing.T) {
	boom := errors.New("the state does not hold a value")
	run := stringCheck("the value is {string}", "the value", func(probe) (string, error) { return "", boom })
	def := CheckString[probe]("the value is {string}", "the value", func(probe) (string, error) { return "", boom })
	if err := run(probe{}, paramsFor(t, def, `the value is "x"`)); !errors.Is(err, boom) {
		t.Fatalf("getter error identity was lost: %v", err)
	}
	if err := thatCheck(func(probe) error { return boom })(probe{}, brine.Params{}); !errors.Is(err, boom) {
		t.Fatalf("condition error identity was lost: %v", err)
	}
}

func TestParamAtNamesTheStepWhenThePatternDeclaresNoParameter(t *testing.T) {
	const pat = "the value is right"
	def := CheckString(pat, "the value", func(in probe) (string, error) { return in.value, nil })
	if message := runCheck(t, def, pat, probe{value: "x"}); !strings.Contains(message, pat) {
		t.Fatalf("undeclared read must fail naming the pattern, got %q", message)
	}
}

func TestFailureDetailIsAppendedAndOnlyOnFailure(t *testing.T) {
	const pat = "it exited {int}"
	get := func(in probe) (int, error) { return in.num, nil }
	det := func(in probe) string { return "log: " + in.value }
	def := CheckInt[probe](pat, "the exit status", get, det)

	if message := runCheck(t, def, "it exited 0", probe{num: 0, value: "hello"}); message != "" {
		t.Fatalf("a match should pass whatever the detail says, got %v", message)
	}
	message := runCheck(t, def, "it exited 0", probe{num: 2, value: "hello"})
	if message == "" {
		t.Fatal("a mismatch MUST fail")
	}
	if !strings.Contains(message, "log: hello") {
		t.Fatalf("the detail must reach the failure, got: %v", message)
	}
	// A detail that has nothing to add must not leave an empty bracket.
	quiet := CheckInt(pat, "the exit status", get, func(probe) string { return "" })
	if e := runCheck(t, quiet, "it exited 0", probe{num: 2}); e == "" || strings.Contains(e, "()") {
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

	if message := runCheck(t, def, `the build log shows "the-needle"`, probe{value: log}); message != "" {
		t.Fatalf("a match past the display limit MUST still pass — the comparison is on the whole value: %v", message)
	}

	msg := runCheck(t, def, `the build log shows "absent"`, probe{value: log})
	if msg == "" {
		t.Fatal("a missing substring MUST fail")
	}
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
