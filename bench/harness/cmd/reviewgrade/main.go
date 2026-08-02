// Command reviewgrade scores a produced review/v1 record against a bench
// corpus expected-findings/v1 oracle.
//
//	reviewgrade -expected bench/corpus/review-jb-004/ground_truth/expected_findings.yaml \
//	            -review  /tmp/run-1/record.json
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/concourse/concourse/bench/harness/reviewgrade"
)

func main() {
	expectedPath := flag.String("expected", "", "path to expected_findings.yaml (required)")
	reviewPath := flag.String("review", "", "path to the produced review/v1 record.json (required)")
	tolerance := flag.Int("tolerance", 10, "line-window tolerance on each side of an oracle region")
	asJSON := flag.Bool("json", false, "print the report as JSON")
	flag.Parse()

	if *expectedPath == "" || *reviewPath == "" {
		flag.Usage()
		os.Exit(2)
	}

	expectedRaw, err := os.ReadFile(*expectedPath)
	if err != nil {
		fail(err)
	}
	oracle, err := reviewgrade.ParseExpected(expectedRaw)
	if err != nil {
		fail(err)
	}

	reviewRaw, err := os.ReadFile(*reviewPath)
	if err != nil {
		fail(err)
	}
	review, err := decodeReview(reviewRaw)
	if err != nil {
		fail(err)
	}

	report := reviewgrade.Score(oracle, review, *tolerance)

	if *asJSON {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			fail(err)
		}
		return
	}

	fmt.Printf("case            %s\n", report.Case)
	fmt.Printf("conclusion      %s\n", report.Conclusion)
	fmt.Printf("candidate recall %d/%d  (%.0f%%, tolerance %d lines)\n",
		report.RequiredMatched, report.RequiredTotal, 100*report.Recall(), report.Tolerance)
	for _, match := range report.Matches {
		flagText := "optional"
		if match.Required {
			flagText = "REQUIRED"
		}
		fmt.Printf("  matched  %-28s <- %-20s [%s]\n", match.ExpectedID, match.ProducedID, flagText)
	}
	for _, missed := range report.MissedRequired {
		fmt.Printf("  MISSED   %s\n", missed)
	}
	for _, extra := range report.UnmatchedProduced {
		fmt.Printf("  unmatched produced finding: %s (judge on its own merits)\n", extra)
	}
	fmt.Println("\nMatches are LOCATION candidates only. Confirm each against the case's")
	fmt.Println("ground_truth/rubric.md before recording a result.")
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "reviewgrade: %v\n", err)
	os.Exit(1)
}

// decodeReview accepts either a sealed review/v1 record.json (the shape
// `fly agent snapshots download` produces) or a bare review body.
//
// A sealed record wraps the body in the record envelope. Unmarshalling those
// bytes straight into a Review silently yields an EMPTY review — no error, no
// findings, 0% recall — which reads exactly like an agent that found nothing.
// This scored a real, correct review as a total miss before it was caught, so
// the envelope case is handled explicitly and an empty result is refused.
func decodeReview(raw []byte) (reviewgrade.Review, error) {
	var envelope struct {
		Type string              `json:"type"`
		Body *reviewgrade.Review `json:"body"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil && envelope.Body != nil {
		if envelope.Type != "" && envelope.Type != "review/v1" {
			return reviewgrade.Review{}, fmt.Errorf("parse review record: record type is %q, not review/v1", envelope.Type)
		}
		return *envelope.Body, nil
	}
	var review reviewgrade.Review
	if err := json.Unmarshal(raw, &review); err != nil {
		return reviewgrade.Review{}, fmt.Errorf("parse review record: %w", err)
	}
	if review.Conclusion == "" && len(review.Findings) == 0 {
		return reviewgrade.Review{}, fmt.Errorf("parse review record: no conclusion and no findings — is this a review/v1 record or body?")
	}
	return review, nil
}
