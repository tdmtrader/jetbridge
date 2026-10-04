package core

import "fmt"

// SameAhead is the second half of the eject invariant: a verdict settles its
// run's entries only if the run was composed with exactly the queued entries
// that would land before them now, no more and no fewer. (The first half, that
// the run's Flight.BaseSHA is View.Main, the driver checks.) tested holds the
// IDs ahead when the run was planned, never when its verdict came in; now holds
// those that would land before it now. Order and repeats are ignored. When not
// ok, the verdict is no verdict: the entries are recomposed, and why says so.
func SameAhead(tested, now []string) (ok bool, why string) {
	t, n := idSet(tested), idSet(now)
	same := len(t) == len(n)
	for id := range n {
		same = same && t[id]
	}
	if same {
		return true, ""
	}
	return false, fmt.Sprintf("tested with %v ahead, but %v would land before it now", tested, now)
}

func idSet(ids []string) map[string]bool {
	s := make(map[string]bool, len(ids))
	for _, id := range ids {
		s[id] = true
	}
	return s
}
