package core

import "fmt"

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

// Policy is the part of the batch config that Decide reads.
type Policy struct {
	RetryNone int // retries allowed on no verdict before pausing
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

// Apply settles the batch for Land (all) and Eject (its one entry). Every
// other decision leaves the queue as it is; the caller carries it out.
func Apply(q *Queue, d Decision, batch []Entry) error {
	final := map[Decision]State{Land: Landed, Eject: Ejected}[d]
	if final == "" {
		return nil
	}
	for _, e := range batch {
		if err := q.Settle(e.ID, final); err != nil {
			return err
		}
	}
	return nil
}
