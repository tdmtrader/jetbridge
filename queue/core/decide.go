package core

import (
	"context"
	"fmt"
)

const (
	Pass Verdict = "pass"
	Fail Verdict = "fail"
	None Verdict = "none" // no result at all, e.g. the run never scheduled
)

const (
	Land  Decision = "land"
	Eject Decision = "eject"
	Split Decision = "split"
	Retry Decision = "retry"
	Pause Decision = "pause"
)

// Order says which green suspect may land ahead of arrival order.
type Order string

const (
	ProvenFirst Order = "proven-first" // a green suspect lands wherever it sits (the zero value)
	Strict      Order = "strict"       // a suspect runs alone only if it is first in the batch
)

// Policy is the part of the batch config that Decide and Bisect read.
type Policy struct {
	RetryNone int   // retries allowed on no verdict before pausing
	Order     Order // empty means ProvenFirst
}

// Decide says what to do with a batch after its verdict. retries counts the
// earlier no-verdict retries of this batch. No verdict never ejects.
func Decide(v Verdict, batch []Entry, retries int, p Policy) (Decision, error) {
	if len(batch) == 0 {
		return "", fmt.Errorf("decide: empty batch")
	}
	switch v {
	case Pass:
		return Land, nil
	case None:
		if retries < p.RetryNone {
			return Retry, nil
		}
		return Pause, nil
	case Fail:
		if len(batch) == 1 {
			return Eject, nil
		}
		return Split, nil
	}
	return "", fmt.Errorf("decide: unknown verdict %q", v)
}

// Apply settles the one entry of an Eject. Land is refused: only LandBatch
// settles a batch Landed. Every other decision leaves the queue as it is.
func Apply(q *Queue, d Decision, batch []Entry) error {
	switch d {
	case Land:
		return fmt.Errorf("apply: a land is settled only by LandBatch")
	case Eject:
		return settleAll(q, batch, Ejected)
	}
	return nil
}

// LandBatch moves main to the candidate through the Lander, with fence, and
// marks the batch Landed only if that returns OK. On error nothing is settled.
func LandBatch(ctx context.Context, l Lander, q *Queue, main, candidate string, fence uint64, batch []Entry) error {
	if err := l.Land(ctx, main, candidate, fence); err != nil {
		return err
	}
	return settleAll(q, batch, Landed)
}

// ReconcileLanding settles a saved Landing's batch Landed if main already holds
// its candidate. It asks with a fence above in.Fence, so the Land it records is
// fenced out first. On error nothing is settled.
func ReconcileLanding(ctx context.Context, l Lander, q *Queue, in Landing, fence uint64) (bool, error) {
	landed, err := l.Contains(ctx, in.Main, in.Candidate, fence)
	if err != nil || !landed {
		return false, err
	}
	return true, settleAll(q, in.Entries, Landed)
}

func settleAll(q *Queue, batch []Entry, s State) error {
	for _, e := range batch {
		if err := q.Settle(e.ID, s); err != nil {
			return err
		}
	}
	return nil
}
