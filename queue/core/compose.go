package core

import (
	"errors"
	"slices"
)

// ConflictError is a Composer's proof that one entry of the batch does not merge.
type ConflictError struct{ EntryID string }

func (c ConflictError) Error() string { return "compose: entry " + c.EntryID + " does not merge" }

// ComposeVerdict reads a failed Compose as a verdict for Decide or Bisect.Record.
// A nil error gives no verdict (Decide refuses it): the candidate must still be
// run. A ConflictError naming an entry in the batch is Fail, so bisect
// recomposes smaller runs until the conflict stands alone. Any other error
// proves nothing about any entry, so it is None: retry, then pause.
func ComposeVerdict(err error, batch []Entry) Verdict {
	if err == nil {
		return ""
	}
	var v ConflictError
	var p *ConflictError
	id := "" // a ConflictError by value or by pointer, anywhere in err's chain
	if errors.As(err, &v) {
		id = v.EntryID
	} else if errors.As(err, &p) && p != nil {
		id = p.EntryID
	}
	if id != "" && slices.ContainsFunc(batch, func(e Entry) bool { return e.ID == id }) {
		return Fail
	}
	return None
}
