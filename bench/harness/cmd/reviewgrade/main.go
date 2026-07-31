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
	var review reviewgrade.Review
	if err := json.Unmarshal(reviewRaw, &review); err != nil {
		fail(fmt.Errorf("parse review record: %w", err))
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
