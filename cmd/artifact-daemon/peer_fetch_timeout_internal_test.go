package main

import (
	"archive/tar"
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
)

// tarWithFile builds a tar archive containing a single file of the given
// size, big enough to be delivered in many chunks.
func tarWithFile(t *testing.T, name string, size int) []byte {
	t.Helper()
	content := bytes.Repeat([]byte("a"), size)
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: name, Size: int64(len(content)), Mode: 0644}); err != nil {
		t.Fatalf("write tar header: %v", err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatalf("write tar body: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	return buf.Bytes()
}

func peerHostPort(t *testing.T, server *httptest.Server) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatalf("split peer address: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse peer port: %v", err)
	}
	return host, port
}

// TestPeerFetchClientHasNoWholeRequestTimeout pins the timeout policy for the
// peer fetch client. http.Client.Timeout covers reading the response body, so
// any value there caps how large an artifact can be transferred rather than
// how long a peer may be unresponsive.
func TestPeerFetchClientHasNoWholeRequestTimeout(t *testing.T) {
	resolver := NewPeerResolver(lagertest.NewTestLogger("test"), nil, "", "", 7780, "", nil)

	if resolver.fetchClient.Timeout != 0 {
		t.Errorf("fetch client has a whole-request timeout of %v; it would sever large artifact streams mid-body", resolver.fetchClient.Timeout)
	}

	transport, ok := resolver.fetchClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("fetch transport is %T, expected *http.Transport", resolver.fetchClient.Transport)
	}
	if transport.ResponseHeaderTimeout != peerFetchResponseHeaderTimeout {
		t.Errorf("fetch transport ResponseHeaderTimeout = %v, want %v; a dead peer must still fail fast",
			transport.ResponseHeaderTimeout, peerFetchResponseHeaderTimeout)
	}

	if http.DefaultTransport.(*http.Transport).ResponseHeaderTimeout != 0 {
		t.Error("the process-wide default transport was mutated; clone it instead")
	}
}

// TestPeerFetchOutlastsItsStallWindowWhileMakingProgress covers the case the
// old three-minute cap broke: a transfer that takes far longer than any single
// timeout, but never actually stalls.
func TestPeerFetchOutlastsItsStallWindowWhileMakingProgress(t *testing.T) {
	const (
		stallWindow = 400 * time.Millisecond
		gap         = 80 * time.Millisecond
		chunks      = 25
	)
	defer func(original time.Duration) { peerFetchStallTimeout = original }(peerFetchStallTimeout)
	peerFetchStallTimeout = stallWindow

	payload := tarWithFile(t, "big-file.bin", 12288)
	chunkSize := (len(payload) + chunks - 1) / chunks

	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		for offset := 0; offset < len(payload); offset += chunkSize {
			end := min(offset+chunkSize, len(payload))
			if _, err := w.Write(payload[offset:end]); err != nil {
				return
			}
			flusher.Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(gap):
			}
		}
	}))
	defer peer.Close()

	host, port := peerHostPort(t, peer)
	resolver := NewPeerResolver(lagertest.NewTestLogger("test"), nil, "", "", port, "", nil)

	destDir := filepath.Join(t.TempDir(), "trickled")
	start := time.Now()
	if err := resolver.Fetch(t.Context(), host, "slow-key", destDir); err != nil {
		t.Fatalf("Fetch of a slow but progressing transfer failed: %v", err)
	}
	elapsed := time.Since(start)

	if elapsed <= stallWindow {
		t.Fatalf("transfer took %v, which is inside the %v stall window — the test proves nothing", elapsed, stallWindow)
	}

	data, err := os.ReadFile(filepath.Join(destDir, "big-file.bin"))
	if err != nil {
		t.Fatalf("artifact not extracted: %v", err)
	}
	if len(data) != 12288 {
		t.Errorf("extracted %d bytes, want 12288 — the stream was truncated", len(data))
	}
}

// TestPeerFetchAbortsWhenPeerGoesSilent is the other half: without a whole
// request cap, something still has to cut loose a peer that sends headers and
// then stops. Progress, not elapsed time, is what keeps a fetch alive.
func TestPeerFetchAbortsWhenPeerGoesSilent(t *testing.T) {
	defer func(original time.Duration) { peerFetchStallTimeout = original }(peerFetchStallTimeout)
	peerFetchStallTimeout = 200 * time.Millisecond

	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer peer.Close()

	host, port := peerHostPort(t, peer)
	resolver := NewPeerResolver(lagertest.NewTestLogger("test"), nil, "", "", port, "", nil)

	done := make(chan error, 1)
	go func() {
		done <- resolver.Fetch(t.Context(), host, "silent-key", filepath.Join(t.TempDir(), "silent"))
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected a fetch from a silent peer to fail")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("fetch from a silent peer never returned — nothing bounds the body read")
	}
}

// TestPeerFetchBoundsTheConnectPhase confirms the fetch transport still
// inherits the default dial and TLS handshake deadlines, so the removal of the
// whole-request cap did not leave the pre-body phases unbounded.
func TestPeerFetchBoundsTheConnectPhase(t *testing.T) {
	resolver := NewPeerResolver(lagertest.NewTestLogger("test"), nil, "", "", 7780, "", nil)
	transport := resolver.fetchClient.Transport.(*http.Transport)

	if transport.TLSHandshakeTimeout == 0 {
		t.Error("fetch transport has no TLS handshake timeout")
	}
	if transport.DialContext == nil {
		t.Error("fetch transport has no DialContext, so it cannot carry a dial timeout")
	}

	// Sanity-check that DialContext is honoured at all by tracing a request
	// to a closed port; it should fail quickly rather than hang.
	closed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	host, port := peerHostPort(t, closed)
	closed.Close()

	req, err := http.NewRequestWithContext(
		httptrace.WithClientTrace(t.Context(), &httptrace.ClientTrace{}),
		http.MethodGet, "http://"+net.JoinHostPort(host, strconv.Itoa(port))+"/artifacts/steps/x", nil,
	)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if _, err := resolver.fetchClient.Do(req); err == nil {
		t.Error("expected a request to a closed port to fail")
	}
}
