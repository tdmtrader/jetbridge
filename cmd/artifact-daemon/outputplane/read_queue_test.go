package outputplane

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/output"
)

// Managed reads queue for the one scratch slot they share with input
// publication and seals. The read's timeout is the read's: time spent waiting
// for the slot is the request's, bounded by its own context. These tests run
// a real daemon against a fake-gcs bucket behind a proxy that makes each
// object download slow, so a read takes most of its timeout.

// slowBucket is the object store's network, slowed for media downloads.
type slowBucket struct {
	delay     atomic.Int64
	downloads atomic.Int64
}

func (bucket *slowBucket) front(t *testing.T, target string) string {
	t.Helper()
	upstream, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet && isMediaDownload(request) {
			bucket.downloads.Add(1)
			select {
			case <-time.After(time.Duration(bucket.delay.Load())):
			case <-request.Context().Done():
				return
			}
		}
		proxy.ServeHTTP(w, request)
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func isMediaDownload(request *http.Request) bool {
	return request.URL.Query().Get("alt") == "media" ||
		strings.HasPrefix(request.URL.Path, "/download/") ||
		!strings.HasPrefix(request.URL.Path, "/storage/")
}

type readQueueFixture struct {
	*ledgerFixture
	bucket  *slowBucket
	server  *Server
	http    *httptest.Server
	signer  output.ReadWarrantMinter
	ref     hangar.TreeRef
	stepDir string
}

func newReadQueue(t *testing.T, timeout time.Duration) *readQueueFixture {
	t.Helper()

	objects, bucketName := emulator(t)
	bucket := &slowBucket{}
	config := validConfig(t, bucket.front(t, objects.URL()), bucketName)
	daemon, err := Build(t.Context(), config)
	if err != nil {
		t.Fatalf("building the daemon: %v", err)
	}
	ledger := newLedger(t)
	stepsDir := filepath.Join(ledger.dir, "steps")
	// A materialized volume is read-only; open it up so the temp dir can go.
	t.Cleanup(func() {
		_ = filepath.WalkDir(stepsDir, func(path string, entry fs.DirEntry, err error) error {
			if err == nil && entry.IsDir() {
				_ = os.Chmod(path, 0o755)
			}
			return nil
		})
	})
	capture, err := OpenCaptureLedger(ledger.store, ledger.ledger, daemon, CaptureLedgerConfig{
		Node: testNode, StepsDir: stepsDir, ScratchDir: config.ScratchDir,
		Terminations: newHeldPods(), SealWait: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("opening the capture ledger: %v", err)
	}
	t.Cleanup(func() { _ = capture.Close() })

	// One published tree for every read in the test to read.
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "result.txt"), []byte("produced"), 0o644); err != nil {
		t.Fatal(err)
	}
	tree, err := daemon.CanonicalizeDirectory(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Close()
	archive, err := os.Open(tree.ArchivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	published, err := daemon.Publisher().EnsurePublication(t.Context(),
		daemon.Namespace().MarkerFor(output.ReservationID(uuid.NewString()), tree.Digest, output.NewTimestamp(nowUTC())),
		archive, tree.ByteSize)
	if err != nil {
		t.Fatalf("publishing the tree: %v", err)
	}

	warrants, err := hangar.NewVerifier(hangarKey(), hangar.MaxWarrantTTL, nowUTC)
	if err != nil {
		t.Fatal(err)
	}
	spent, err := openSpentWarrants(ledger.store, nowUTC)
	if err != nil {
		t.Fatal(err)
	}
	server := NewServerWithSpool(daemon, ledger.ledger, capture, warrants, spent, 1)
	if err := server.configureReads(config, ledger.store); err != nil {
		t.Fatal(err)
	}
	server.reads.timeout = timeout
	front := httptest.NewServer(server.Handler())
	t.Cleanup(front.Close)

	signer, err := hangar.NewSigner(hangarKey(), time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	return &readQueueFixture{
		ledgerFixture: ledger, bucket: bucket, server: server, http: front,
		signer: output.ReadWarrantMinter{Signer: signer}, ref: published.Attributes.Ref, stepDir: stepsDir,
	}
}

// materialize asks the daemon to materialize the tree into handle's input-0,
// under a warrant minted for this node.
func (fixture *readQueueFixture) materialize(t *testing.T, ctx context.Context, handle string) (int, error) {
	t.Helper()

	now := time.Now().UTC()
	expires := output.NewTimestamp(now.Add(10 * time.Minute))
	claim := output.ClaimRecord{
		ClaimID:           output.ClaimID(uuid.NewString()),
		Ref:               fixture.ref,
		ConsumerBindingID: output.OpaqueID("result-read:" + handle),
		AcquiredAt:        output.NewTimestamp(now.Add(-time.Second)),
		ExpiresAt:         &expires,
	}
	destination := output.ReadDestination{Handle: handle, Volume: "input-0"}
	token, err := fixture.signer.Sign(claim, destination, testNode)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(output.ManagedReadRequest{Ref: fixture.ref, Destination: destination, Warrant: token})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, fixture.http.URL+"/read/v1/materialize", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	return response.StatusCode, nil
}

func (fixture *readQueueFixture) materialized(handle string) bool {
	_, err := os.Stat(filepath.Join(fixture.stepDir, handle, "input-0", ".hangar-materialized"))
	return err == nil
}

// Each read takes 600ms of a one-second read timeout. The second waits for
// the first to give the slot back; if its wait counted against its timeout it
// would run out 400ms into its own read.
func TestAQueuedMaterializeHasItsWholeReadTimeoutOnceItHoldsTheSlot(t *testing.T) {
	fixture := newReadQueue(t, time.Second)
	fixture.bucket.delay.Store(int64(600 * time.Millisecond))

	handles := []string{"consumer-a", "consumer-b"}
	statuses := make([]int, len(handles))
	errs := make([]error, len(handles))
	var wg sync.WaitGroup
	for i, handle := range handles {
		wg.Add(1)
		go func() {
			defer wg.Done()
			statuses[i], errs[i] = fixture.materialize(t, t.Context(), handle)
		}()
	}
	wg.Wait()

	for i, handle := range handles {
		if errs[i] != nil || statuses[i] != http.StatusNoContent {
			t.Errorf("%s: materialize answered %d, %v; want 204", handle, statuses[i], errs[i])
		}
		if !fixture.materialized(handle) {
			t.Errorf("%s: no materialization receipt", handle)
		}
	}
}

// A reader that goes away while it waits for the slot takes nothing with it:
// it never held the slot, never spent its warrant, and the next read runs.
func TestAMaterializeAbandonedInTheQueueDoesNotKeepTheSlot(t *testing.T) {
	fixture := newReadQueue(t, 5*time.Second)
	fixture.bucket.delay.Store(int64(500 * time.Millisecond))

	first := make(chan int, 1)
	go func() {
		status, _ := fixture.materialize(t, t.Context(), "consumer-a")
		first <- status
	}()
	// Wait until the first read holds the slot and is downloading.
	deadline := time.Now().Add(5 * time.Second)
	for fixture.bucket.downloads.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the first read never reached the bucket")
		}
		time.Sleep(5 * time.Millisecond)
	}

	queued, abandon := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer abandon()
	if _, err := fixture.materialize(t, queued, "consumer-b"); err == nil {
		t.Fatal("the abandoned request completed; it should have been waiting for the slot")
	}

	if status := <-first; status != http.StatusNoContent {
		t.Fatalf("the first read answered %d", status)
	}
	fixture.bucket.delay.Store(0)
	status, err := fixture.materialize(t, t.Context(), "consumer-c")
	if err != nil || status != http.StatusNoContent {
		t.Fatalf("a read after the abandoned one answered %d, %v; the slot leaked", status, err)
	}
	if fixture.materialized("consumer-b") {
		t.Error("the abandoned read materialized")
	}
	if held := len(fixture.server.spool); held != 0 {
		t.Errorf("%d scratch slots held after every read ended", held)
	}
}
