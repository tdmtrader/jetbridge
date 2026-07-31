package reviewgrade_test

import (
	"testing"

	"github.com/concourse/concourse/bench/harness/reviewgrade"
)

func twoRequiredOracle() *reviewgrade.Expected {
	return &reviewgrade.Expected{
		Schema: "expected-findings/v1", Case: "c",
		Findings: []reviewgrade.ExpectedFinding{
			{ID: "F1", Required: true, File: "a.go", Region: reviewgrade.Region{Lines: "10-20"}},
			{ID: "F2", Required: true, File: "b.go", Region: reviewgrade.Region{Lines: "30-40"}},
			{ID: "F3", Required: false, File: "c.go", Region: reviewgrade.Region{Lines: "50-60"}},
		},
	}
}

func TestScoreCountsRecallOverRequiredOnly(t *testing.T) {
	review := reviewgrade.Review{
		Conclusion: "changes-required",
		Findings: []reviewgrade.Finding{
			{ID: "x", Evidence: []reviewgrade.Anchor{anchor("a.go", 12, 14)}},
			{ID: "y", Evidence: []reviewgrade.Anchor{anchor("c.go", 55, 56)}},
			{ID: "z", Evidence: []reviewgrade.Anchor{anchor("zz.go", 1, 2)}},
		},
	}
	report := reviewgrade.Score(twoRequiredOracle(), review, 0)

	if report.RequiredTotal != 2 || report.RequiredMatched != 1 {
		t.Fatalf("recall = %d/%d", report.RequiredMatched, report.RequiredTotal)
	}
	if len(report.MissedRequired) != 1 || report.MissedRequired[0] != "F2" {
		t.Fatalf("MissedRequired = %v", report.MissedRequired)
	}
	// c.go matched a non-required finding: credited, never penalized.
	if len(report.MatchedOptional) != 1 || report.MatchedOptional[0] != "F3" {
		t.Fatalf("MatchedOptional = %v", report.MatchedOptional)
	}
	// zz.go matched nothing in the oracle: reported for human judgment.
	if len(report.UnmatchedProduced) != 1 || report.UnmatchedProduced[0] != "z" {
		t.Fatalf("UnmatchedProduced = %v", report.UnmatchedProduced)
	}
}

func TestRecallFractionIsOneWhenAllRequiredMatch(t *testing.T) {
	review := reviewgrade.Review{
		Findings: []reviewgrade.Finding{
			{ID: "x", Evidence: []reviewgrade.Anchor{anchor("a.go", 11, 12)}},
			{ID: "y", Evidence: []reviewgrade.Anchor{anchor("b.go", 31, 32)}},
		},
	}
	report := reviewgrade.Score(twoRequiredOracle(), review, 0)
	if report.Recall() != 1.0 {
		t.Fatalf("Recall() = %v", report.Recall())
	}
}

func TestRecallIsZeroWithNoRequiredFindings(t *testing.T) {
	oracle := &reviewgrade.Expected{Schema: "expected-findings/v1", Case: "c",
		Findings: []reviewgrade.ExpectedFinding{{ID: "F3", Required: false, File: "c.go"}}}
	report := reviewgrade.Score(oracle, reviewgrade.Review{}, 0)
	if report.RequiredTotal != 0 || report.Recall() != 0 {
		t.Fatalf("report = %#v", report)
	}
}

func TestOneProducedFindingCanOnlyClaimOneExpected(t *testing.T) {
	oracle := &reviewgrade.Expected{Schema: "expected-findings/v1", Case: "c",
		Findings: []reviewgrade.ExpectedFinding{
			{ID: "F1", Required: true, File: "a.go", Region: reviewgrade.Region{Lines: "10-20"}},
			{ID: "F2", Required: true, File: "a.go", Region: reviewgrade.Region{Lines: "12-18"}},
		}}
	review := reviewgrade.Review{Findings: []reviewgrade.Finding{
		{ID: "x", Evidence: []reviewgrade.Anchor{anchor("a.go", 13, 14)}},
	}}
	report := reviewgrade.Score(oracle, review, 0)
	if report.RequiredMatched != 1 {
		t.Fatalf("one finding must not satisfy two overlapping expected findings: %d", report.RequiredMatched)
	}
}
