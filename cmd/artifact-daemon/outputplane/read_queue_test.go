package outputplane

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
// a real daemon over HTTP against a fake-gcs bucket. The test itself holds the
// slot, standing in for the read or publication ahead in the queue, for as
// long as it likes; the reads' own work is a tiny tree with the whole read
// timeout to do it in.

type readQueueFixture struct {
	*ledgerFixture
	server   *Server
	http     *httptest.Server
	inFlight atomic.Int64
	signer   output.ReadWarrantMinter
	ref      hangar.TreeRef
	stepDir  string
}

// holdSlot takes the server's one scratch slot and returns its release.
func (fixture *readQueueFixture) holdSlot(t *testing.T) func() {
	t.Helper()
	release, err := fixture.server.spooling(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	t.Cleanup(func() { once.Do(release) })
	return func() { once.Do(release) }
}

func newReadQueue(t *testing.T, timeout time.Duration) *readQueueFixture {
	t.Helper()

	objects, bucketName := emulator(t)
	config := validConfig(t, objects.URL(), bucketName)
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

	signer, err := hangar.NewSigner(hangarKey(), time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &readQueueFixture{
		ledgerFixture: ledger, server: server,
		signer: output.ReadWarrantMinter{Signer: signer}, ref: published.Attributes.Ref, stepDir: stepsDir,
	}
	handler := server.Handler()
	fixture.http = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		fixture.inFlight.Add(1)
		defer fixture.inFlight.Add(-1)
		handler.ServeHTTP(w, request)
	}))
	t.Cleanup(fixture.http.Close)
	return fixture
}

// materialize asks the daemon to materialize the tree into handle's input-0,
// under a warrant minted for this node.
func (fixture *readQueueFixture) materialize(t *testing.T, ctx context.Context, handle string) (int, error) {
	t.Helper()

	now := time.Now().UTC()
	expires := output.NewTimestamp(now.Add(10 * time.Minute))
	claim := output.ClaimRecord{
		ClaimID:           output.ClaimID(fixture.claimFor(handle)),
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

// claimFor is the reader's claim id for one handle, stable within a test.
func (fixture *readQueueFixture) claimFor(handle string) string {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(fixture.dir+"/"+handle)).String()
}

func (fixture *readQueueFixture) materialized(handle string) bool {
	_, err := os.Stat(filepath.Join(fixture.stepDir, handle, "input-0", ".hangar-materialized"))
	return err == nil
}

// Two reads queue behind whatever holds the slot for longer than a read's
// whole timeout. If their wait counted against it, both would answer 503 from
// the queue; they each get the full two seconds for a tiny tree once they
// hold the slot.
func TestAQueuedMaterializeHasItsWholeReadTimeoutOnceItHoldsTheSlot(t *testing.T) {
	const timeout = 2 * time.Second
	fixture := newReadQueue(t, timeout)
	release := fixture.holdSlot(t)

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
	waitForQueue(t, func() bool { return fixture.inFlight.Load() == int64(len(handles)) })
	time.Sleep(timeout + timeout/4)
	release()
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

// A reader that goes away while it waits for the slot gives up its place at
// once -- its handler returns while the slot is still held by another -- and
// takes nothing with it: no slot, no spent warrant, no materialization. A
// waiter that kept its place past its client would show here as a request
// still in flight, and later as a read nobody asked for.
func TestAMaterializeAbandonedInTheQueueGivesUpItsPlace(t *testing.T) {
	fixture := newReadQueue(t, 5*time.Second)
	release := fixture.holdSlot(t)

	queued, abandon := context.WithCancel(t.Context())
	abandoned := make(chan error, 1)
	go func() {
		_, err := fixture.materialize(t, queued, "consumer-b")
		abandoned <- err
	}()
	waitForQueue(t, func() bool { return fixture.inFlight.Load() == 1 })
	abandon()
	if err := <-abandoned; err == nil {
		t.Fatal("the abandoned request completed; it should have been waiting for the slot")
	}
	// The slot is still held: only the waiter's own giving up ends its handler.
	waitForQueue(t, func() bool { return fixture.inFlight.Load() == 0 })

	release()
	status, err := fixture.materialize(t, t.Context(), "consumer-c")
	if err != nil || status != http.StatusNoContent {
		t.Fatalf("a read after the abandoned one answered %d, %v", status, err)
	}
	if fixture.materialized("consumer-b") {
		t.Error("the abandoned read materialized")
	}
	if _, spent := fixture.server.reads.spent.used[fixture.claimFor("consumer-b")]; spent {
		t.Error("the abandoned read spent its warrant")
	}
	if held := len(fixture.server.spool); held != 0 {
		t.Errorf("%d scratch slots held after every read ended", held)
	}
}

func waitForQueue(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the read queue")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

