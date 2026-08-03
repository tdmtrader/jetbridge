package jetbridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http/httptrace"
	"sync"
	"time"
)

// Snapshot uploads are the one daemon request whose response is deliberately
// deferred: PUT /artifacts/snapshots/sha256/<digest>.tar is not answered until
// the daemon has read the whole body, verified its digest, linked it into the
// immutable namespace, and durably committed it to Hangar. Its
// time-to-first-response-byte is therefore a function of snapshot size, not of
// daemon liveness.
//
// http.Transport.ResponseHeaderTimeout measures exactly that interval — the gap
// between "request fully written" and "first response header" — so it cannot
// bound the handshake of a deferred-response endpoint. It degenerates into a
// whole-request deadline that scales with the payload, which is why every large
// snapshot upload failed with "http2: timeout awaiting response headers"
// regardless of how healthy the daemon was.
//
// snapshotUploadGuard replaces that incidental bound with two real ones:
//
//   - accept: the daemon must answer "100 Continue" — which Go's HTTP server
//     emits only once the handler itself starts reading the body — within
//     bounds.accept. This proves a live handler entered the upload path, and it
//     proves it before a single archive byte is transmitted.
//   - stall: while the body is in flight, bytes must keep leaving the client.
//     The transport only pulls more bytes from the body when the socket (and,
//     under HTTP/2, the peer's flow-control window) accepts them, which happens
//     only while the daemon keeps reading. bounds.stall of no movement means the
//     daemon stopped consuming.
//
// The stall bound is released the moment the last body byte is handed to the
// transport. Everything after that point is durable-commit work whose duration
// is a property of the payload; the caller's context is its bound, and no
// constant here may masquerade as one.
//
// The accept bound is deliberately *not* released by delivery. http.Transport
// treats ExpectContinueTimeout as "proceed anyway", not as a failure: once it
// expires the archive is transmitted whether or not the daemon ever answered.
// An archive small enough to fit the peer's flow-control window then reaches
// "delivered" without the daemon having acknowledged anything, and retiring the
// accept bound at that point would leave the request waiting on a hung daemon
// forever. Only the daemon's acknowledgement, or the end of the request itself,
// clears it.
var (
	errSnapshotUploadNotAccepted = errors.New("artifact daemon did not accept the snapshot upload")
	errSnapshotUploadStalled     = errors.New("snapshot upload made no progress")
)

const (
	defaultSnapshotUploadAcceptTimeout = 30 * time.Second
	defaultSnapshotUploadStallTimeout  = 60 * time.Second
)

// snapshotUploadBounds carries the two liveness bounds. Zero values fall back
// to the package defaults so a partially configured client is never unbounded.
type snapshotUploadBounds struct {
	accept time.Duration
	stall  time.Duration
}

func (bounds snapshotUploadBounds) withDefaults() snapshotUploadBounds {
	if bounds.accept <= 0 {
		bounds.accept = defaultSnapshotUploadAcceptTimeout
	}
	if bounds.stall <= 0 {
		bounds.stall = defaultSnapshotUploadStallTimeout
	}
	return bounds
}

type snapshotUploadGuard struct {
	ctx       context.Context
	cancel    context.CancelCauseFunc
	accepted  chan struct{}
	progress  chan struct{}
	delivered chan struct{}
	finished  chan struct{}

	acceptOnce  sync.Once
	deliverOnce sync.Once
	finishOnce  sync.Once
}

// guardSnapshotUpload derives a context that is cancelled — with a cause that
// names the bound that was violated — when the daemon fails to accept the
// upload or stops consuming it. expectContinue reports whether the request
// actually carries the Expect header; without a body there is no 100 to wait
// for, so the accept phase is satisfied immediately rather than fabricating a
// timeout no daemon could ever clear.
func guardSnapshotUpload(
	ctx context.Context,
	bounds snapshotUploadBounds,
	expectContinue bool,
) (context.Context, *snapshotUploadGuard) {
	guarded, cancel := context.WithCancelCause(ctx)
	guard := &snapshotUploadGuard{
		ctx:       guarded,
		cancel:    cancel,
		accepted:  make(chan struct{}),
		progress:  make(chan struct{}, 1),
		delivered: make(chan struct{}),
		finished:  make(chan struct{}),
	}
	if !expectContinue {
		guard.markAccepted()
	}
	go guard.watch(bounds.withDefaults())
	return guarded, guard
}

// clientTrace reports the daemon's 100-continue. Both net/http and
// golang.org/x/net/http2 invoke Got100Continue, so the accept bound holds over
// HTTP/1.1 and HTTP/2 alike.
func (guard *snapshotUploadGuard) clientTrace() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{Got100Continue: guard.markAccepted}
}

// body wraps the archive so every byte the transport takes counts as progress,
// and so the stall bound stands down once the whole archive has been handed
// over. size is the declared Content-Length: an http.Transport with a known
// length reads through an io.LimitReader, which returns EOF without ever calling
// the wrapped reader again, so reaching size is the only reliable completion
// signal.
func (guard *snapshotUploadGuard) body(reader io.Reader, size int64) io.Reader {
	return &snapshotUploadBody{reader: reader, size: size, guard: guard}
}

func (guard *snapshotUploadGuard) markAccepted() {
	guard.acceptOnce.Do(func() { close(guard.accepted) })
}

func (guard *snapshotUploadGuard) markProgress() {
	select {
	case guard.progress <- struct{}{}:
	default:
	}
}

// markDelivered retires the stall bound: the archive is now the daemon's
// problem, and how long its durable commit takes says nothing about liveness.
func (guard *snapshotUploadGuard) markDelivered() {
	guard.deliverOnce.Do(func() { close(guard.delivered) })
}

// markFinished retires every bound. Only the end of the request itself does
// this, because until then the daemon still owes an answer.
func (guard *snapshotUploadGuard) markFinished() {
	guard.finishOnce.Do(func() { close(guard.finished) })
}

// stop retires the guard and releases the derived context. Callers must not
// invoke it until the response body has been consumed: the guarded context also
// governs that read.
func (guard *snapshotUploadGuard) stop() {
	guard.markFinished()
	guard.cancel(nil)
}

// explain leads with the violated bound and keeps the transport's own error
// behind it. A guard-cancelled request surfaces as a bare "context canceled",
// which names neither the bound that tripped nor why — but the transport error
// still carries the target URL, which is what identifies the replica in the
// field.
func (guard *snapshotUploadGuard) explain(err error) error {
	if err == nil {
		return nil
	}
	cause := context.Cause(guard.ctx)
	if errors.Is(cause, errSnapshotUploadNotAccepted) || errors.Is(cause, errSnapshotUploadStalled) {
		return fmt.Errorf("%w: %v", cause, err)
	}
	return err
}

func (guard *snapshotUploadGuard) watch(bounds snapshotUploadBounds) {
	timer := time.NewTimer(bounds.accept)
	defer timer.Stop()

	select {
	case <-guard.accepted:
	case <-guard.finished:
		return
	case <-timer.C:
		guard.cancel(fmt.Errorf("%w within %s", errSnapshotUploadNotAccepted, bounds.accept))
		return
	}

	for {
		timer.Stop()
		timer.Reset(bounds.stall)
		select {
		case <-guard.progress:
		case <-guard.delivered:
			return
		case <-guard.finished:
			return
		case <-timer.C:
			guard.cancel(fmt.Errorf("%w for %s", errSnapshotUploadStalled, bounds.stall))
			return
		}
	}
}

type snapshotUploadBody struct {
	reader io.Reader
	guard  *snapshotUploadGuard
	size   int64
	read   int64
}

func (body *snapshotUploadBody) Read(buffer []byte) (int, error) {
	n, err := body.reader.Read(buffer)
	if n > 0 {
		body.read += int64(n)
		body.guard.markProgress()
	}
	if err != nil || (body.size > 0 && body.read >= body.size) {
		body.guard.markDelivered()
	}
	return n, err
}
