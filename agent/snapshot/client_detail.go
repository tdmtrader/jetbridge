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

// Error composes the disclosable detail with the wrapped error's own text
// (if any) so that nothing is lost from the pod log: writeSnapshotError logs
// the full error chain, and a marking site wrapping e.g. an *os.PathError or
// a decode failure must not make that text vanish from the log just because
// it was also marked. ClientDetail below deliberately does NOT include this
// wrapped text — only e.detail is ever disclosable. Callers of
// WrapClientDetailf must therefore not restate the wrapped error's text in
// the format string; Error() already appends it for the log.
func (e *clientDetailError) Error() string {
	if e.err == nil {
		return e.detail
	}
	return e.detail + ": " + e.err.Error()
}
func (e *clientDetailError) Unwrap() error { return e.err }

// ClientDetailf creates a new error whose message is safe to disclose.
func ClientDetailf(format string, args ...any) error {
	return &clientDetailError{detail: fmt.Sprintf(format, args...)}
}

// WrapClientDetailf marks err with a safe message while preserving err for
// errors.Is/As. Use it when the underlying error must keep travelling but the
// disclosable phrasing is decided here.
//
// The wrapped error's own text stays reachable via Error() for logging (see
// the type's Error method) but never appears in the disclosed detail from
// ClientDetail — so format must describe the failure in terms safe to
// disclose on its own, and must not interpolate err's text (e.g. via %v of
// err) to avoid smuggling unsafe content into the disclosable channel.
func WrapClientDetailf(err error, format string, args ...any) error {
	if err == nil {
		return nil
	}
	return &clientDetailError{detail: fmt.Sprintf(format, args...), err: err}
}

// ClientDetail returns the first disclosable message errors.As reaches: the
// outermost mark on a wrap chain, and — inside errors.Join — the leftmost
// branch's mark regardless of how deep in that branch it sits. Every join in
// the seal path puts the causal error in branch 0 and cleanup errors after it,
// so that ordering is the one we want; a marking site that breaks it would
// mislabel the failure.
func ClientDetail(err error) (string, bool) {
	var detail *clientDetailError
	if errors.As(err, &detail) {
		return detail.detail, true
	}
	return "", false
}
