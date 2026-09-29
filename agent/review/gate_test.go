package review

import (
	"encoding/json"
	"reflect"
	"testing"
)

// gateAssessment builds a model assessment over the captureTest fixture with
// one located finding per severity, in order.
func gateAssessment(t *testing.T, complete bool, severities ...string) string {
	t.Helper()
	findings := []map[string]any{}
	for _, severity := range severities {
		findings = append(findings, map[string]any{
			"id": "model-id", "severity": severity, "dimension": "correctness",
			"title": "Check the length", "explanation": "One-byte input panics at index 1.",
			"recommendation": "Validate length before indexing.",
			"location":       map[string]any{"side": "head", "path": "parser.go", "start_line": 2, "end_line": 2},
		})
	}
	b, err := json.Marshal(map[string]any{
		"summary": "Gate fixture.", "complete": complete, "reviewed_files": []string{"parser.go", "deleted.txt"},
		"limitations": []string{}, "findings": findings,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestEvaluateSeverityFloor(t *testing.T) {
	b := captureTest(t)
	for _, tc := range []struct {
		name       string
		complete   bool
		severities []string
		blockAt    string
		pass       bool
		reason     string
		blocking   []string
	}{
		{"clean", true, nil, "medium", true, "", []string{}},
		{"clean at low", true, nil, "low", true, "", []string{}},
		{"low below medium", true, []string{"low"}, "medium", true, "", []string{}},
		{"medium at medium", true, []string{"medium"}, "medium", false, "findings at or above medium", []string{"f-001"}},
		{"mixed", true, []string{"high", "low", "blocker"}, "medium", false, "findings at or above medium", []string{"f-001", "f-003"}},
		{"high below blocker", true, []string{"high", "medium"}, "blocker", true, "", []string{}},
		{"blocker at blocker", true, []string{"blocker"}, "blocker", false, "findings at or above blocker", []string{"f-001"}},
		{"low at low", true, []string{"low"}, "low", false, "findings at or above low", []string{"f-001"}},
		{"incomplete clean", false, nil, "blocker", false, "incomplete review", []string{}},
		{"incomplete below the floor", false, []string{"low"}, "blocker", false, "incomplete review", []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := reportTest(t, b, gateAssessment(t, tc.complete, tc.severities...))
			if err != nil {
				t.Fatal(err)
			}
			d, err := Evaluate(r, Policy{BlockAt: tc.blockAt})
			if err != nil {
				t.Fatal(err)
			}
			if d.Pass != tc.pass || d.Reason != tc.reason {
				t.Fatalf("got pass=%v reason=%q, want pass=%v reason=%q", d.Pass, d.Reason, tc.pass, tc.reason)
			}
			if !reflect.DeepEqual(d.Blocking, tc.blocking) {
				t.Fatalf("got blocking=%v, want %v", d.Blocking, tc.blocking)
			}
		})
	}
}

func TestEvaluateRefusesUnknownInputs(t *testing.T) {
	b := captureTest(t)
	r, err := reportTest(t, b, cleanAssessment)
	if err != nil {
		t.Fatal(err)
	}
	for _, floor := range []string{"", "critical", "Medium"} {
		if _, err := Evaluate(r, Policy{BlockAt: floor}); err == nil {
			t.Fatalf("accepted floor %q", floor)
		}
	}
	if _, err := Evaluate(nil, Policy{BlockAt: "medium"}); err == nil {
		t.Fatal("accepted a missing report")
	}
	// A severity outside the enum can only arrive in a hand-built report; it
	// must not be tolerated as if it ranked below every floor.
	r.Assessment.Findings = []Finding{{ID: "f-001", Severity: "critical"}}
	r.Verdict = "findings"
	d, err := Evaluate(r, Policy{BlockAt: "blocker"})
	if err != nil {
		t.Fatal(err)
	}
	if d.Pass || !reflect.DeepEqual(d.Blocking, []string{"f-001"}) {
		t.Fatalf("tolerated an unknown severity: %+v", d)
	}
}
