package snapshot

import (
	"errors"
	"fmt"
)

// clientDetailError carries a message that is safe to return to the API
// caller.
//
// Safe means one thing precisely: every byte of the message derives from one
// of three sources and nothing else —
//
//  1. the caller's OWN submitted content;
//  2. fixed strings and build-time constants this repository wrote — type
//     refs, the current schema digest for a type, size limits, a time layout;
//  3. values the platform already declared TO this caller for this request:
//     the exposed input port names and type refs the sealer handed the
//     producing step (contracts/record.go:163). The API upload path builds an
//     empty ValidationContext, so nothing from source 3 reaches a client there
//     at all.
//
// Host paths, scratch directories, storage-node identities, snapshot IDs, the
// digest of any snapshot other than the caller's own, and database state are
// none of the three and must never be marked. Neither is a dependency error's
// text UNLESS the marking site can name the dependency and argue its message
// is composed only from sources 1 and 2 — encoding/json's decoder text is not
// (it names Go struct types); time.ParseError's is (caller value + our
// layout). Formatting %v of an error you did not author is the usual way this
// gets broken by accident.
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
//
// Choose between the two constructors by asking who authored the text:
//
//   - This repository authored it AND it is the explanation the caller needs
//     ("adapter is required") — ClientDetailf, formatting it in with %v. The
//     chain is dropped, so first confirm no caller matches a sentinel through
//     the site.
//   - A dependency authored it (encoding/json, os) — WrapClientDetailf, and
//     describe the failure yourself without restating err. Error() still
//     appends the wrapped text for the log; ClientDetail never discloses it.
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
