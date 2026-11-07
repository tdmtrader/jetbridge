package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"unicode"

	"github.com/concourse/concourse/agent/capture"
	"github.com/concourse/concourse/agent/review"
)

// verdict applies a severity floor to a published review.json, the landing
// queue's review gate: it fails, listing each blocking finding's id,
// severity and title, when any finding is at or above --block-at, and passes
// otherwise. The floor is review.Evaluate's ranking (low < medium < high <
// blocker); a severity outside it blocks. An incomplete review is not a
// finding: it passes, saying so and listing the review's limitations, since
// the gate's rule is the findings' severity alone.
func verdict(args []string, out io.Writer) error {
	f := flag.NewFlagSet("verdict", flag.ContinueOnError)
	f.SetOutput(out)
	reportPath := f.String("report", "", "published review.json (required)")
	blockAt := f.String("block-at", "high", "lowest severity that fails the gate: low, medium, high or blocker")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *reportPath == "" || f.NArg() != 0 {
		return errors.New("verdict requires --report and takes no positional arguments")
	}
	policy := review.Policy{BlockAt: *blockAt}
	if err := policy.Validate(); err != nil {
		return err
	}
	data, err := capture.ReadFileBounded(*reportPath, capture.MaxFileBytes)
	if err != nil {
		return fmt.Errorf("read review report: %w", err)
	}
	report, err := review.ParsePublishedReport(data)
	if err != nil {
		return fmt.Errorf("review report: %w", err)
	}
	decision, err := review.Evaluate(report, policy)
	if err != nil {
		return err
	}
	if len(decision.Blocking) > 0 {
		blocking := map[string]bool{}
		for _, id := range decision.Blocking {
			blocking[id] = true
		}
		fmt.Fprintf(out, "review gate: %d finding(s) at or above %s:\n", len(decision.Blocking), *blockAt)
		for _, finding := range report.Assessment.Findings {
			if blocking[finding.ID] {
				fmt.Fprintf(out, "  %s [%s] %s (%s/%s:%d)\n", finding.ID, finding.Severity, oneLine(finding.Title), finding.Location.Side, oneLine(finding.Location.Path), finding.Location.StartLine)
			}
		}
		return fmt.Errorf("review gate failed: %d finding(s) at or above %s", len(decision.Blocking), *blockAt)
	}
	if !report.Assessment.Complete {
		fmt.Fprintln(out, "review gate: the review is incomplete; it counts by its findings alone:")
		for _, limitation := range report.Assessment.Limitations {
			fmt.Fprintf(out, "  - %s\n", oneLine(limitation))
		}
	}
	fmt.Fprintf(out, "review gate passed: verdict %s, %d finding(s), none at or above %s\n", report.Verdict, len(report.Assessment.Findings), *blockAt)
	return nil
}

// oneLine keeps model-written text to one printable line of a build log.
func oneLine(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, text)
}
