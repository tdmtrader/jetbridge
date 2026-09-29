package review

import (
	"errors"
	"fmt"
)

// severityRank orders the report's closed severity enum. A finding blocks when
// its rank is at or above the policy floor.
var severityRank = map[string]int{"low": 1, "medium": 2, "high": 3, "blocker": 4}

// Policy is the landing threshold evaluated over a verified report. BlockAt is
// the lowest severity that refuses a landing: "low", "medium", "high" or
// "blocker".
type Policy struct{ BlockAt string }

// Validate refuses an unknown floor before any review work starts.
func (p Policy) Validate() error {
	if _, ok := severityRank[p.BlockAt]; !ok {
		return fmt.Errorf("unknown severity floor %q; use low, medium, high or blocker", p.BlockAt)
	}
	return nil
}

// Decision is the outcome of Evaluate. Blocking lists the IDs of the findings
// at or above the floor in report order; it is empty, never nil.
type Decision struct {
	Pass     bool
	Reason   string
	Blocking []string
}

// Evaluate applies a severity floor to a report. It is pure: it reads only the
// report and performs no IO. An incomplete review always refuses, whatever the
// floor. A finding whose severity is outside the enum blocks rather than being
// tolerated. The only error is an unusable policy or a missing report.
func Evaluate(r *Report, p Policy) (Decision, error) {
	if err := p.Validate(); err != nil {
		return Decision{}, err
	}
	if r == nil {
		return Decision{}, errors.New("a review report is required")
	}
	floor := severityRank[p.BlockAt]
	d := Decision{Blocking: []string{}}
	for _, f := range r.Assessment.Findings {
		if rank, known := severityRank[f.Severity]; !known || rank >= floor {
			d.Blocking = append(d.Blocking, f.ID)
		}
	}
	switch {
	case r.Verdict == "incomplete" || !r.Assessment.Complete:
		d.Reason = "incomplete review"
	case len(d.Blocking) > 0:
		d.Reason = "findings at or above " + p.BlockAt
	default:
		d.Pass = true
	}
	return d, nil
}
