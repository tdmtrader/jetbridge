package core

import (
	"errors"
	"slices"
)

// ConflictError is a Composer's proof that one entry of the batch does not merge.
type ConflictError struct{ EntryID string }

func (c ConflictError) Error() string { return "compose: entry " + c.EntryID + " does not merge" }

// conflictID finds a ConflictError, by value or by pointer, anywhere in err's chain.
func conflictID(err error) (string, bool) {
	var v ConflictError
	if errors.As(err, &v) {
		return v.EntryID, true
	}
	var p *ConflictError
	if errors.As(err, &p) && p != nil {
		return p.EntryID, true
	}
	return "", false
}

// ComposeVerdict reads a failed Compose as a verdict for Decide or Bisect.Record.
// A nil error gives no verdict (Decide refuses it): the candidate must still be
// run. A ConflictError naming an entry in the batch is Fail, so bisect
// recomposes smaller runs until the conflict stands alone. Any other error
// proves nothing about any entry, so it is None: retry, then pause.
func ComposeVerdict(err error, batch []Entry) Verdict {
	if err == nil {
		return ""
	}
	id, ok := conflictID(err)
	if ok && id != "" && slices.ContainsFunc(batch, func(e Entry) bool { return e.ID == id }) {
		return Fail
	}
	return None
}
