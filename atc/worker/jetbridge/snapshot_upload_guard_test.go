package jetbridge

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"k8s.io/client-go/kubernetes/fake"
)

// snapshotUploadTestDaemon stands up an HTTP/2-over-TLS stand-in for the
// artifact daemon and returns a DaemonClient wired to it. HTTP/2 is not
// incidental: the live failure surfaced as "http2: timeout awaiting response
// headers", and the response-header bound is enforced by a different code path
// in golang.org/x/net/http2 than in net/http. The transports themselves are the
// ones NewDaemonClient builds — only the trust anchor is injected — so the
// tests exercise the real client configuration.
func snapshotUploadTestDaemon(t *testing.T, handler http.Handler) *DaemonClient {
	t.Helper()

	server := httptest.NewUnstartedServer(handler)
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)

	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(target.Port())
	if err != nil {
		t.Fatal(err)
	}

	client := NewDaemonClient(
		lagertest.NewTestLogger("snapshot-upload"),
		fake.NewSimpleClientset(),
		"cicd",
		"artifact-daemon",
		port,
		nil,
	)
	client.scheme = "https"

	pool := x509.NewCertPool()
	pool.AddCert(server.Certificate())
	for _, httpClient := range []*http.Client{client.uploadClient, client.streamingClient, client.client} {
		httpClient.Transport.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: pool}
	}
	return client
}

func snapshotUploadTestStore(t *testing.T, client *DaemonClient) *SnapshotContentStore {
	t.Helper()
	store, err := NewSnapshotContentStore(client, &locationResolverStub{}, 1, testSnapshotArchiveLimits)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func snapshotUploadSpool(t *testing.T, size int) (string, int64) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "snapshot-spool.tar")
	payload := make([]byte, size)
	for index := range payload {
		payload[index] = byte(index)
	}
	if err := os.WriteFile(path, payload, 0600); err != nil {
		t.Fatal(err)
	}
	return path, int64(size)
}

var snapshotUploadTestEndpoint = DaemonEndpoint{NodeName: "node-a", Address: "127.0.0.1"}

const snapshotUploadTestKey = "snapshots/sha256/" +
	"0000000000000000000000000000000000000000000000000000000000000000.tar"

// A snapshot PUT is answered only after the daemon has durably committed the
// archive, so the interval between the last request byte and the first response
// byte is a function of snapshot size. A daemon that is slow throughout — slow
// to drain the upload, then slow to commit it — must still succeed.
//
// Both of the guard's bounds are set below the durations this daemon takes, so
// the test also pins down why it succeeds. The upload outlasts the accept bound,
// which can only be survived because the daemon's 100 Continue satisfied that
// bound at the start; and the commit outlasts the stall bound, which can only be
// survived because both bounds retire once the archive has been delivered.
//
// (Over HTTP/2 the server consumes the Expect header rather than exposing it on
// the request, so the acknowledgement is asserted by its effect, not by reading
// it back out of the handler.)
func TestSnapshotUploadSucceedsWhileDaemonDrainsAndCommitsSlowly(t *testing.T) {
	const (
		archiveSize    = 4 * 1024 * 1024
		drainChunk     = 256 * 1024
		drainInterval  = 40 * time.Millisecond
		commitDuration = 750 * time.Millisecond
	)

	var overHTTP2 atomic.Bool
	client := snapshotUploadTestDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		overHTTP2.Store(r.ProtoMajor == 2)
		// Drain in slow, steady chunks. The first read is what makes the
		// server emit 100 Continue, and HTTP/2's upload window means the
		// client cannot outrun this loop: the request write takes as long as
		// the daemon takes to consume it.
		for {
			time.Sleep(drainInterval)
			if _, err := io.CopyN(io.Discard, r.Body, drainChunk); err != nil {
				break
			}
		}
		// Stand-in for digest verification, linking, and the Hangar commit:
		// all of it happens before a single response byte is written.
		time.Sleep(commitDuration)
		w.WriteHeader(http.StatusCreated)
	}))
	// Both bounds sit below the durations this daemon takes: the drain (~16
	// chunks x 40ms) outlasts the accept bound, and the commit outlasts the
	// stall bound, while each individual drain step stays well inside it.
	client.uploadAcceptTimeout = 300 * time.Millisecond
	client.uploadStallTimeout = 300 * time.Millisecond

	store := snapshotUploadTestStore(t, client)
	spool, size := snapshotUploadSpool(t, archiveSize)

	started := time.Now()
	status, _, err := store.putEndpointRequest(
		context.Background(), snapshotUploadTestEndpoint, snapshotUploadTestKey, spool, size,
	)
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("slow-but-progressing upload failed after %s: %v", elapsed, err)
	}
	if status != http.StatusCreated {
		t.Fatalf("upload status = %d, want %d", status, http.StatusCreated)
	}
	if !overHTTP2.Load() {
		t.Fatal("upload did not negotiate HTTP/2, so it did not exercise the transport that failed in production")
	}
	if elapsed < client.uploadAcceptTimeout+commitDuration {
		t.Fatalf(
			"upload completed in %s, too fast to have outlasted the accept bound (%s) and the commit (%s)",
			elapsed, client.uploadAcceptTimeout, commitDuration,
		)
	}
}

// A daemon that accepts the connection but never enters the upload handler must
// fail the upload before the archive is transmitted, not hang on it.
func TestSnapshotUploadFailsFastWhenDaemonNeverAccepts(t *testing.T) {
	client := snapshotUploadTestDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	client.uploadAcceptTimeout = 200 * time.Millisecond
	client.uploadStallTimeout = 30 * time.Second

	store := snapshotUploadTestStore(t, client)
	spool, size := snapshotUploadSpool(t, 512*1024)

	started := time.Now()
	_, _, err := store.putEndpointRequest(
		context.Background(), snapshotUploadTestEndpoint, snapshotUploadTestKey, spool, size,
	)
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("upload to a daemon that never accepted it reported success")
	}
	if !errors.Is(err, errSnapshotUploadNotAccepted) {
		t.Fatalf("upload error = %v, want the unaccepted-upload bound", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("upload took %s to fail against an unresponsive daemon", elapsed)
	}
}

// http.Transport treats ExpectContinueTimeout (1s by default) as "send the body
// anyway", not as a failure. An archive that then fits inside the peer's
// flow-control window is fully delivered without the daemon ever acknowledging
// it, so delivery must not be mistaken for acceptance: the accept bound has to
// outlive it, or the upload waits on a hung daemon forever.
func TestSnapshotUploadFailsFastWhenDeliveryOutrunsAcknowledgement(t *testing.T) {
	client := snapshotUploadTestDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	// Deliberately past the transport's own 1s expect-continue timeout, so the
	// archive is on the wire and fully delivered well before this bound.
	client.uploadAcceptTimeout = 2 * time.Second
	client.uploadStallTimeout = 30 * time.Second

	store := snapshotUploadTestStore(t, client)
	// Small enough to fit HTTP/2's default 1 MiB upload window in one go.
	spool, size := snapshotUploadSpool(t, 256*1024)

	started := time.Now()
	_, _, err := store.putEndpointRequest(
		context.Background(), snapshotUploadTestEndpoint, snapshotUploadTestKey, spool, size,
	)
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("upload delivered to a daemon that never acknowledged it reported success")
	}
	if !errors.Is(err, errSnapshotUploadNotAccepted) {
		t.Fatalf("upload error = %v, want the unaccepted-upload bound", err)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("upload took %s to fail after delivery outran acknowledgement", elapsed)
	}
}

// A daemon that accepts the upload and then stops consuming it must fail too.
// Progress is measured by bytes the transport takes from the archive, which it
// can only do while the peer's flow-control window keeps opening.
func TestSnapshotUploadFailsFastWhenDaemonStopsConsuming(t *testing.T) {
	client := snapshotUploadTestDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Reading anything makes the server emit 100 Continue, so the upload
		// is accepted; it then wedges without draining the rest.
		if _, err := io.CopyN(io.Discard, r.Body, 4096); err != nil {
			return
		}
		<-r.Context().Done()
	}))
	client.uploadAcceptTimeout = 10 * time.Second
	client.uploadStallTimeout = 300 * time.Millisecond

	store := snapshotUploadTestStore(t, client)
	// Comfortably past HTTP/2's default 1 MiB connection and stream upload
	// windows, so the transport must block once the handler stops reading.
	spool, size := snapshotUploadSpool(t, 8*1024*1024)

	started := time.Now()
	_, _, err := store.putEndpointRequest(
		context.Background(), snapshotUploadTestEndpoint, snapshotUploadTestKey, spool, size,
	)
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("upload to a daemon that stopped consuming it reported success")
	}
	if !errors.Is(err, errSnapshotUploadStalled) {
		t.Fatalf("upload error = %v, want the stalled-upload bound", err)
	}
	if elapsed > 20*time.Second {
		t.Fatalf("upload took %s to fail against a stalled daemon", elapsed)
	}
}

// The upload transport must stay free of a response-header bound, and must stay
// separate from the read transport that legitimately keeps one.
func TestSnapshotUploadTransportHasNoResponseHeaderTimeout(t *testing.T) {
	client := NewDaemonClient(
		lagertest.NewTestLogger("upload-transport"),
		fake.NewSimpleClientset(),
		"cicd",
		"artifact-daemon",
		7780,
		nil,
	)

	upload, ok := client.uploadClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("upload transport = %T, want *http.Transport", client.uploadClient.Transport)
	}
	if upload.ResponseHeaderTimeout != 0 {
		t.Fatalf(
			"upload transport ResponseHeaderTimeout = %s; a snapshot PUT answers only after its durable commit, so this bounds the commit, not the handshake",
			upload.ResponseHeaderTimeout,
		)
	}
	if client.uploadClient.Timeout != 0 {
		t.Fatalf("upload client timeout = %s, want no whole-request timeout", client.uploadClient.Timeout)
	}

	streaming, ok := client.streamingClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("streaming transport = %T, want *http.Transport", client.streamingClient.Transport)
	}
	if streaming.ResponseHeaderTimeout <= 0 {
		t.Fatal("snapshot reads answer before they stream, so their transport must keep a response-header bound")
	}
	if upload == streaming {
		t.Fatal("upload and read paths share one transport, so neither can be bounded on its own terms")
	}
}

func TestSnapshotUploadBoundsFallBackToDefaults(t *testing.T) {
	bounds := snapshotUploadBounds{}.withDefaults()
	if bounds.accept != defaultSnapshotUploadAcceptTimeout || bounds.stall != defaultSnapshotUploadStallTimeout {
		t.Fatalf("zero bounds = %+v, want the package defaults", bounds)
	}

	var nilClient *DaemonClient
	if nilBounds := nilClient.snapshotUploadBounds(); nilBounds != bounds {
		t.Fatalf("nil-client bounds = %+v, want %+v", nilBounds, bounds)
	}
}
