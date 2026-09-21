package main

import (
	"context"
	"errors"
	"net/http"
	"syscall"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/output"
)

// readMaterialize accepts the task init's destination-bound read warrant. This
// node-local route returns no object bytes and needs no control-plane client
// certificate in the task Pod. Both the warrant and its live committed lease are
// checked before opening the object. Archive and stat reads still require mTLS.
func (server *Server) readMaterialize(w http.ResponseWriter, request *http.Request) {
	if !server.readPlaneReady(w) {
		return
	}
	if server.reads == nil || server.source == nil || server.source.steps == nil {
		readRefusal(w, output.ErrCaptureDisabled)
		return
	}
	var input output.ManagedReadRequest
	if err := decodeRead(request, &input); err != nil || input.Validate() != nil {
		readRefusal(w, output.ErrIncomplete)
		return
	}
	claims, err := server.reads.verifier.Verify(input.Warrant, input.Ref, input.Destination)
	if err != nil || claims.ActivationEpoch != server.daemon.ActivationEpoch() {
		readRefusal(w, output.ErrUnauthorized)
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), server.reads.timeout)
	defer cancel()
	release, err := server.spooling(ctx)
	if err != nil {
		readRefusal(w, err)
		return
	}
	defer release()
	tree, _, releaseLease, err := server.stageRead(ctx, input)
	if err != nil {
		readRefusal(w, err)
		return
	}
	defer tree.Close()
	// The lease is held until the materialization's outcome is known, so a
	// transient failure leaves the reader a lease to retry with.
	if err := tree.Materialize(ctx, server.source.steps, input.Ref, input.Destination.Handle, input.Destination.Volume); err != nil {
		if errors.Is(err, hangar.ErrConflict) || errors.Is(err, hangar.ErrCorrupt) || errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.ENOTDIR) {
			err = output.ErrConflict
		}
		readRefusal(w, errors.Join(err, releaseLease(err)))
		return
	}
	// The tree is published and its receipt sealed; the reader verifies the
	// receipt, not this answer. A lease the control plane would not take back
	// closes at the end of its term, so a failed release is not the read's.
	_ = releaseLease(nil)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}
