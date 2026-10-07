package outputplane

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
// certificate in the task Pod. The warrant -- binding, window and remaining
// term -- is checked before opening the object. Archive and stat reads still
// require mTLS.
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
	// The warrant names its epoch and its node; on any other it opens nothing.
	if err != nil || claims.ActivationEpoch != server.daemon.ActivationEpoch() ||
		claims.NodeUID == "" || claims.NodeUID != server.daemon.nodeUID {
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
	if err := server.reads.spent.begin(claims); err != nil {
		readRefusal(w, err)
		return
	}
	// The warrant is spent once the materialization's outcome is known, unless
	// that outcome is one a retry can change: a transient failure leaves the
	// init its one warrant to retry with. The lease itself is not given back
	// from here -- this daemon holds no client for the web -- and closes at the
	// end of its term, swept by the web's abandoned-lease cleaner.
	if err := server.materialize(ctx, input, claims); err != nil {
		readRefusal(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

// materialize stages and installs one exact tree under a begun warrant, and
// ends the warrant's read with the outcome.
func (server *Server) materialize(ctx context.Context, input output.ManagedReadRequest, claims output.ReadWarrantClaims) (err error) {
	defer func() {
		if spendErr := server.reads.spent.end(claims, err); spendErr != nil && err == nil {
			err = spendErr
		}
	}()
	tree, _, err := server.stageRead(ctx, claims)
	if err != nil {
		return err
	}
	defer tree.Close()
	if err := tree.Materialize(ctx, server.source.steps, input.Ref, input.Destination.Handle, input.Destination.Volume); err != nil {
		if errors.Is(err, hangar.ErrConflict) || errors.Is(err, hangar.ErrCorrupt) || errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.ENOTDIR) {
			err = output.ErrConflict
		}
		return err
	}

	return nil
}
