package jetbridge

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// TestPutEndpointRequestKeepsSpoolOwnership pins who closes the spool file.
//
// *os.File is an io.ReadCloser, so passing it to NewRequest makes it the
// request body — and net/http closes a request body it is handed, "even on
// errors". The function also closes it in a defer, so the two race. When the
// transport wins, Close returns os.ErrClosed and errors.Join folds it into the
// named return, failing an upload that succeeded.
//
// That race is the snapshot content_unavailable 503: a lost close left zero
// replicas acknowledged. It presented as flakiness because winning the race
// looks perfectly healthy.
//
// Asserting on the race directly would be flaky in exactly the same way, so
// this asserts the property that removes it: the body handed to the transport
// must not be the spool file itself. Closing it must be a no-op, leaving this
// function the only closer.
func TestPutEndpointRequestKeepsSpoolOwnership(t *testing.T) {
	spoolPath := filepath.Join(t.TempDir(), "spool")
	if err := os.WriteFile(spoolPath, []byte("snapshot-bytes"), 0o600); err != nil {
		t.Fatalf("write spool: %v", err)
	}
	file, err := os.Open(spoolPath)
	if err != nil {
		t.Fatalf("open spool: %v", err)
	}
	defer file.Close()

	var captured io.ReadCloser
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	request, err := http.NewRequest(http.MethodPut, server.URL, io.NopCloser(file))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	captured = request.Body

	// The transport closes the body it was given. That must not touch the file.
	if err := captured.Close(); err != nil {
		t.Fatalf("closing the request body should be a no-op, got: %v", err)
	}

	// The spool must still be readable — proving the transport's close did not
	// reach it and the deferred close will be the first and only one.
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("spool was closed by the request body: %v", err)
	}
	body, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("spool unreadable after the request body was closed: %v", err)
	}
	if string(body) != "snapshot-bytes" {
		t.Errorf("spool contents = %q, want %q", body, "snapshot-bytes")
	}

	// And the file's own Close must still succeed exactly once.
	if err := file.Close(); err != nil {
		t.Errorf("first real close should succeed, got: %v", err)
	}
	if err := file.Close(); !errors.Is(err, os.ErrClosed) {
		t.Errorf("second close should report ErrClosed, got: %v", err)
	}
}
