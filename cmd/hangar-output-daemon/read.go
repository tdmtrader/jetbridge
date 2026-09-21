package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/output"
)

// Managed reads are independent of the producer's execution lifetime. The
// control plane asks this bucket-owning daemon for an exact stat before creating
// a read lease. A stat grants no authority to read the object's bytes.
func (server *Server) readStat(w http.ResponseWriter, request *http.Request) {
	if !server.authorizeRead(w, request) {
		return
	}
	var input struct {
		Ref hangar.TreeRef `json:"ref"`
	}
	if err := decodeRead(request, &input); err != nil {
		readRefusal(w, output.ErrIncomplete)
		return
	}
	if err := input.Ref.Validate(); err != nil {
		readRefusal(w, output.ErrIncomplete)
		return
	}
	object, err := server.daemon.publisher.StatExactObject(request.Context(), input.Ref)
	if err != nil {
		readRefusal(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, object)
}

func (server *Server) authorizeRead(w http.ResponseWriter, request *http.Request) bool {
	// Unlike execution operations, these requests have no execution capability.
	// Their caller must be a verified control-plane peer even in a local setup.
	if !server.mutualTLS || request.TLS == nil || len(request.TLS.VerifiedChains) == 0 {
		readRefusal(w, output.ErrUnauthorized)
		return false
	}
	return server.readPlaneReady(w)
}

func (server *Server) readPlaneReady(w http.ResponseWriter) bool {
	if server.unreadyBecause != "" {
		readRefusal(w, output.ErrInfrastructure)
		return false
	}
	if !server.daemon.OutputEnabled() {
		readRefusal(w, output.ErrCaptureDisabled)
		return false
	}
	return true
}

func decodeRead(request *http.Request, value any) error {
	defer request.Body.Close()
	decoder := json.NewDecoder(http.MaxBytesReader(nil, request.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return output.ErrIncomplete
	}
	return nil
}

// Error details may contain a bucket, namespace or key. Only a fixed class
// crosses this boundary; the exact object is already known to the caller.
func readRefusal(w http.ResponseWriter, err error) {
	writeError(w, readRefusalClass(err))
}

// readRefusalClass is the fixed class a read failure crosses the boundary as.
// ErrInfrastructure -- the reader's "unavailable, retry" -- is everything else.
func readRefusalClass(err error) error {
	for _, class := range []error{output.ErrUnauthorized, output.ErrNotFound,
		output.ErrConflict, output.ErrIncomplete, output.ErrCaptureDisabled} {
		if errors.Is(err, class) {
			return class
		}
	}
	return output.ErrInfrastructure
}
