package snapshot

import (
	"errors"
	"fmt"
)

// clientDetailError carries a message that is safe to return to the API
// caller.
//
// Safe means one thing precisely: every byte of the message is derived from
// the caller's OWN submitted content plus fixed strings this repository
// wrote. Host paths, scratch directories, storage-node identities, database
// state, and wrapped dependency errors are none of those, and must never be
// marked.
//
// The channel is opt-in because the default has to be silence: the snapshot
// API maps failures onto fixed strings on purpose, and one blanket "just
// return the error" would undo that for every future error path at once.
// Marking is therefore a decision made at the site that FORMATS the message,
// where the safety argument is checkable, and nowhere else.
type clientDetailError struct {
	detail string
	err    error
}

func (e *clientDetailError) Error() string { return e.detail }
func (e *clientDetailError) Unwrap() error { return e.err }

// ClientDetailf creates a new error whose message is safe to disclose.
func ClientDetailf(format string, args ...any) error {
	return &clientDetailError{detail: fmt.Sprintf(format, args...)}
}

// WrapClientDetailf marks err with a safe message while preserving err for
// errors.Is/As. Use it when the underlying error must keep travelling but the
// disclosable phrasing is decided here.
func WrapClientDetailf(err error, format string, args ...any) error {
	if err == nil {
		return nil
	}
	return &clientDetailError{detail: fmt.Sprintf(format, args...), err: err}
}

// ClientDetail returns the outermost disclosable message in err's tree.
//
// errors.As walks both wrapped and joined errors, so a mark survives the
// sealer's errors.Join(category, fmt.Errorf("...: %w", err)) composition
// without every intermediate layer having to know about it.
func ClientDetail(err error) (string, bool) {
	var detail *clientDetailError
	if errors.As(err, &detail) {
		return detail.detail, true
	}
	return "", false
}
