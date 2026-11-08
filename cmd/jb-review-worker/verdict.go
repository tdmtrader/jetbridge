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
// queue's review gate, and answers in its exit status:
//
//   - 0, passed: the review is complete and no finding is at or above
//     --block-at.
//   - 1, failed: a finding is at or above --block-at, complete review or not;
//     each is listed with its id, severity and title. This is the only answer
//     that rejects a candidate.
//   - 3, no verdict: the review is incomplete and none of its findings reaches
//     the floor, or verdict cannot read the report or its flags. The gate
//     neither passes nor rejects on it.
//
// The floor is review.Evaluate's ranking (low < medium < high < blocker); a
// severity outside it blocks.
func verdict(args []string, out io.Writer) error {
	err := judge(args, out)
	var blocked blockedError
	if err == nil {
		return nil
	}
	if errors.As(err, &blocked) {
		return exitCode{code: verdictFailed, err: err}
	}
	return exitCode{code: verdictNone, err: fmt.Errorf("review gate: no verdict: %w", err)}
}

// The exit statuses verdict answers with besides 0 (passed).
const (
	verdictFailed = 1
	verdictNone   = 3
)

// exitCode is an error main exits with a status other than 1 for.
type exitCode struct {
	code int
	err  error
}

func (e exitCode) Error() string { return e.err.Error() }
func (e exitCode) Unwrap() error { return e.err }

// blockedError is the failed answer: a finding at or above the floor.
type blockedError struct{ error }

func judge(args []string, out io.Writer) error {
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
		return blockedError{fmt.Errorf("review gate failed: %d finding(s) at or above %s", len(decision.Blocking), *blockAt)}
	}
	if !report.Assessment.Complete {
		fmt.Fprintln(out, "review gate: the review is incomplete:")
		for _, limitation := range report.Assessment.Limitations {
			fmt.Fprintf(out, "  - %s\n", oneLine(limitation))
		}
		return fmt.Errorf("the review is incomplete and none of its %d finding(s) is at or above %s", len(report.Assessment.Findings), *blockAt)
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
