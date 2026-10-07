package hangaroutput_test

// The capture sequence against a real daemon, a real bucket and a real
// database: one row, six steps, and what each crash in between costs.
//
// The five acceptance criteria are A1-A5 below. Every one is asserted as an
// OUTCOME -- the row's state, the objects in the bucket, the marker on the
// node's disk, the reclaim jobs in the database -- and a call count appears
// only where the count is the claim ("recovery created nothing" is "publish
// was called once").

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/atc/hangaroutput/controller"
	"github.com/concourse/concourse/atc/hangaroutput/reclaimpass"
	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// capture is one capture, at whatever step the spec drove it to.
type capture struct {
	harness   *harness
	Execution executioncontrol.Identity
	Output    output.OutputName
	Pod       executioncontrol.PodUID
}

func (c *capture) Key() output.CaptureKey {
	return output.CaptureKey{ExecutionID: c.Execution.ExecutionID, Output: c.Output}
}

// admit is step 1's control-plane half: the node admits the execution and the
// pending row is inserted, both before any Pod exists.
func (h *harness) admit(t *testing.T) *capture {
	t.Helper()

	ctx := context.Background()
	admitted := &capture{
		harness:   h,
		Execution: executioncontrol.Identity{ExecutionID: executioncontrol.ExecutionID(uuid.NewString()), Fence: 1},
		Output:    "result",
		Pod:       executioncontrol.PodUID(uuid.NewString()),
	}

	if _, err := h.Daemon.Client.Admit(ctx, executioncontrol.Envelope{
		ProtocolVersion: executioncontrol.ProtocolVersion,
		Identity:        admitted.Execution,
		ActivationEpoch: harnessEpoch,
		NodeUID:         harnessNode,
		Capability:      "opaque-capability",
	}); err != nil {
		t.Fatalf("admitting the execution: %v", err)
	}
	if _, err := h.Coordinator.Insert(ctx, output.PendingCapture{
		Execution: admitted.Execution,
		Output:    admitted.Output,
		Node:      harnessNodeName,
		NodeUID:   harnessNode,
		Term:      24 * time.Hour,
	}); err != nil {
		t.Fatalf("inserting the pending capture: %v", err)
	}

	return admitted
}

// hold is the producing Pod's control init: the held marker, then the start.
func (c *capture) hold(t *testing.T) *capture {
	t.Helper()

	status, _ := c.harness.Daemon.holdSource(t, c.Execution, c.Output, c.Pod)
	if status != http.StatusOK {
		t.Fatalf("the hold was refused: %d", status)
	}

	return c.start(t)
}

// start records the producer's start, naming its Pod.
func (c *capture) start(t *testing.T) *capture {
	t.Helper()

	if _, err := c.harness.Daemon.Client.RecordStart(context.Background(), c.Execution, c.Pod,
		"producer-1"); err != nil {
		t.Fatalf("recording the start: %v", err)
	}

	return c
}

// write puts bytes in the step directory, as the producer does.
func (c *capture) write(t *testing.T, name, content string) *capture {
	t.Helper()

	directory := filepath.Join(c.harness.Daemon.StepsDir, c.Key().Directory())
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatalf("creating the step directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, name), []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}

	return c
}

// finish is the main container's exit: the node's finish statement.
func (c *capture) finish(t *testing.T, successful bool) *capture {
	t.Helper()

	code := 0
	if !successful {
		code = 2
	}
	if _, err := c.harness.Daemon.Client.RecordOutcome(context.Background(), c.Execution,
		executioncontrol.AcknowledgementFinish,
		executioncontrol.ExitOutcome{ExitCode: code}); err != nil {
		t.Fatalf("recording the outcome: %v", err)
	}

	return c
}

// terminate declares every container of the capture's Pod terminated.
func (c *capture) terminate(t *testing.T) *capture {
	t.Helper()

	if err := os.WriteFile(filepath.Join(c.harness.Daemon.Terminations, string(c.Pod)), nil, 0o600); err != nil {
		t.Fatalf("declaring the termination: %v", err)
	}

	return c
}

// produce is the whole happy producer: hold, bytes, exit, every container gone.
func (c *capture) produce(t *testing.T, content string) *capture {
	t.Helper()

	return c.hold(t).write(t, "artifact.txt", content).finish(t, true).terminate(t)
}

// advance takes the capture as far as it goes and fails the spec on an error.
func (c *capture) advance(t *testing.T) output.Capture {
	t.Helper()

	if err := c.harness.Coordinator.Advance(context.Background(), c.Key()); err != nil {
		t.Fatalf("advancing %s: %v", c.Key(), err)
	}

	return c.record(t)
}

func (c *capture) record(t *testing.T) output.Capture {
	t.Helper()

	tx, err := c.harness.Conn.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	record, err := c.harness.Repository.GetCapture(context.Background(), tx, c.Key())
	if err != nil {
		t.Fatalf("loading the capture: %v", err)
	}

	return record
}

// marker reads the node's marker for the capture straight off the disk.
func (c *capture) marker(t *testing.T) (string, bool) {
	t.Helper()

	var found []string
	_ = filepath.Walk(c.harness.Daemon.StorageDir, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && filepath.Base(path) ==
			"capture-"+string(c.Execution.ExecutionID)+"."+string(c.Output)+".json" {
			found = append(found, path)
		}
		return nil
	})
	if len(found) == 0 {
		return "", false
	}
	raw, err := os.ReadFile(found[0])
	if err != nil {
		t.Fatalf("reading the marker: %v", err)
	}

	return string(raw), true
}

func (h *harness) bucketKeys(t *testing.T) []string {
	t.Helper()

	objects, _, err := h.Store.ListObjects(h.Bucket, "", "", false)
	if err != nil {
		t.Fatalf("listing the bucket: %v", err)
	}
	var keys []string
	for _, object := range objects {
		keys = append(keys, object.Name)
	}
	sort.Strings(keys)

	return keys
}

// treeEntries reads the one object in the bucket as the canonical tar it is.
func (h *harness) treeEntries(t *testing.T) map[string]string {
	t.Helper()

	keys := h.bucketKeys(t)
	if len(keys) != 1 {
		t.Fatalf("the bucket holds %d objects, want 1: %v", len(keys), keys)
	}
	object, err := h.Store.GetObject(h.Bucket, keys[0])
	if err != nil {
		t.Fatalf("reading the object: %v", err)
	}
	entries := map[string]string{}
	reader := tar.NewReader(bytes.NewReader(object.Content))
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("the object is not a canonical tar: %v", err)
		}
		content, _ := io.ReadAll(reader)
		entries[strings.TrimPrefix(header.Name, "./")] = string(content)
	}

	return entries
}

func (h *harness) reclaimJobs(t *testing.T) int {
	t.Helper()

	var jobs int
	if err := h.Conn.QueryRow(`SELECT count(*) FROM hangar_reclaim_jobs`).Scan(&jobs); err != nil {
		t.Fatalf("counting reclaim jobs: %v", err)
	}

	return jobs
}

// injectingDialer hands the coordinator the real daemon client, wrapped so a
// spec can lose the ANSWER to a call that really happened, or hold a call
// until the spec lets it go.
type injectingDialer struct {
	daemon *daemonProcess

	mu    sync.Mutex
	calls map[string]int
	lose  map[string]int
	gates map[string]chan struct{}
	// arrived is signalled when a gated call reaches its gate.
	arrived map[string]chan struct{}
}

var _ hangaroutput.SourceDialer = (*injectingDialer)(nil)

func (dialer *injectingDialer) ForNode(_ context.Context, name string, uid executioncontrol.NodeUID) (hangaroutput.SourceControl, error) {
	if name != harnessNodeName || uid != harnessNode {
		return nil, fmt.Errorf("%w: no daemon on node %s/%s", output.ErrInfrastructure, name, uid)
	}

	return &injectingSource{dialer: dialer, client: dialer.daemon.Client}, nil
}

// LoseAfter drops the answer to the next n calls of op, after they ran.
func (dialer *injectingDialer) LoseAfter(op string, n int) {
	dialer.mu.Lock()
	defer dialer.mu.Unlock()
	if dialer.lose == nil {
		dialer.lose = map[string]int{}
	}
	dialer.lose[op] = n
}

// Gate holds every call of op before it is made until the returned function
// is called, and returns a channel that is closed when the first one arrives.
func (dialer *injectingDialer) Gate(op string) (<-chan struct{}, func()) {
	dialer.mu.Lock()
	defer dialer.mu.Unlock()
	if dialer.gates == nil {
		dialer.gates, dialer.arrived = map[string]chan struct{}{}, map[string]chan struct{}{}
	}
	gate, arrived := make(chan struct{}), make(chan struct{})
	dialer.gates[op], dialer.arrived[op] = gate, arrived
	var once sync.Once

	return arrived, func() { once.Do(func() { close(gate) }) }
}

func (dialer *injectingDialer) Calls(op string) int {
	dialer.mu.Lock()
	defer dialer.mu.Unlock()

	return dialer.calls[op]
}

func (dialer *injectingDialer) before(op string) {
	dialer.mu.Lock()
	if dialer.calls == nil {
		dialer.calls = map[string]int{}
	}
	dialer.calls[op]++
	gate, arrived := dialer.gates[op], dialer.arrived[op]
	if arrived != nil {
		delete(dialer.arrived, op)
		close(arrived)
	}
	dialer.mu.Unlock()
	if gate != nil {
		<-gate
	}
}

func (dialer *injectingDialer) after(op string, err error) error {
	dialer.mu.Lock()
	defer dialer.mu.Unlock()
	if err == nil && dialer.lose[op] > 0 {
		dialer.lose[op]--
		return fmt.Errorf("%w: the answer to %s was lost", output.ErrInfrastructure, op)
	}

	return err
}

type injectingSource struct {
	dialer *injectingDialer
	client interface {
		output.SourceControl
		Observe(context.Context, executioncontrol.Identity, time.Duration) (executioncontrol.ObserveFinishOrStopResult, error)
	}
}

func (source *injectingSource) Observe(ctx context.Context, id executioncontrol.Identity,
	wait time.Duration) (executioncontrol.ObserveFinishOrStopResult, error) {
	source.dialer.before("observe")
	result, err := source.client.Observe(ctx, id, wait)
	return result, source.dialer.after("observe", err)
}

func (source *injectingSource) Seal(ctx context.Context, request output.CaptureSealRequest) (output.CaptureSealResult, error) {
	source.dialer.before("seal")
	result, err := source.client.Seal(ctx, request)
	return result, source.dialer.after("seal", err)
}

func (source *injectingSource) Publish(ctx context.Context, request output.CapturePublishRequest) (output.CapturePublishResult, error) {
	source.dialer.before("publish")
	result, err := source.client.Publish(ctx, request)
	return result, source.dialer.after("publish", err)
}

func (source *injectingSource) Release(ctx context.Context, request output.CaptureReleaseRequest) (output.CaptureReleaseAcknowledgement, error) {
	source.dialer.before("release")
	result, err := source.client.Release(ctx, request)
	return result, source.dialer.after("release", err)
}

func (source *injectingSource) Stat(ctx context.Context, request output.CaptureStatRequest) (output.CapturePublishResult, error) {
	source.dialer.before("stat")
	result, err := source.client.Stat(ctx, request)
	return result, source.dialer.after("stat", err)
}

// controllerTransactor adapts the connection to the reclaim pass's port.
type controllerTransactor struct{ inner *connTransactor }

func (transactor controllerTransactor) Begin() (controller.Transaction, error) {
	return transactor.inner.Begin()
}

func assertPublishedAndReleased(t *testing.T, record output.Capture) hangar.TreeRef {
	t.Helper()

	if record.State != output.CapturePublished {
		t.Fatalf("the capture is %s (%s), want published", record.State, record.Error)
	}
	if !record.Released() {
		t.Errorf("a published capture was never released")
	}
	ref, err := record.Ref()
	if err != nil {
		t.Fatalf("the published capture has no ref: %v", err)
	}

	return ref
}

// The happy path, end to end: the six steps, the claim, and the marker gone.
func TestACaptureIsOneRowFromHoldToRelease(t *testing.T) {
	h := newHarness(t)
	c := h.admit(t).produce(t, "the bytes a producer wrote\n")

	if marker, found := c.marker(t); !found || !strings.Contains(marker, `"held"`) {
		t.Fatalf("no held marker on the node before the seal: %q", marker)
	}

	record := c.advance(t)
	ref := assertPublishedAndReleased(t, record)
	if record.PodUID != c.Pod {
		t.Errorf("the row names pod %s, the producer ran in %s", record.PodUID, c.Pod)
	}
	if entries := h.treeEntries(t); entries["artifact.txt"] != "the bytes a producer wrote\n" {
		t.Errorf("the published tree is not the bytes the producer wrote: %v", entries)
	}
	if _, found := c.marker(t); found {
		t.Errorf("the marker survived the release")
	}

	var claims int
	if err := h.Conn.QueryRow(`SELECT count(*) FROM hangar_claims c
		JOIN hangar_exact_lifecycles l ON l.id = c.lifecycle_id
		WHERE c.claim_id = $1 AND l.digest = $2 AND c.released_at IS NULL`,
		string(c.Key().ClaimID()), string(ref.Digest)).Scan(&claims); err != nil {
		t.Fatalf("reading the claim: %v", err)
	}
	if claims != 1 {
		t.Errorf("the published capture holds %d claims on its generation, want 1", claims)
	}
}

// A producer that failed publishes nothing, and its step is still released.
func TestAFailedProducerIsDiscardedAndReleased(t *testing.T) {
	h := newHarness(t)
	c := h.admit(t).hold(t).write(t, "artifact.txt", "half a result\n").finish(t, false).terminate(t)

	record := c.advance(t)
	if record.State != output.CaptureDiscarded || record.Error != output.DiscardProducerFailed {
		t.Fatalf("a failed producer's capture is %s (%s)", record.State, record.Error)
	}
	if !record.Released() {
		t.Errorf("the discarded capture was never released")
	}
	if keys := h.bucketKeys(t); len(keys) != 0 {
		t.Errorf("a failed producer published %v", keys)
	}
	if _, found := c.marker(t); found {
		t.Errorf("the marker survived the release")
	}
}

// A1: the daemon is SIGKILLed after the step exited and before it
// canonicalized. The marker is on disk; the restarted daemon seals from it
// and the capture publishes.
func TestA1ADaemonKilledBeforeCanonicalizingPublishesAfterItsRestart(t *testing.T) {
	h := newHarness(t)
	// The Pod has NOT terminated: the seal flips the marker and then waits,
	// and the kill lands inside that wait, before any canonical read.
	c := h.admit(t).hold(t).write(t, "artifact.txt", "survives a kill\n").finish(t, true)

	arrived, release := h.Dialer.Gate("seal")
	release()
	done := make(chan error, 1)
	go func() { done <- h.Coordinator.Advance(context.Background(), c.Key()) }()
	<-arrived
	// The marker is sealed on disk before the wait begins.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if marker, _ := c.marker(t); strings.Contains(marker, `"sealed"`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the seal never wrote its marker")
		}
		time.Sleep(20 * time.Millisecond)
	}

	h.Daemon.Kill()
	if err := <-done; err == nil {
		t.Fatalf("a seal answered by a killed daemon succeeded")
	}
	if record := c.record(t); record.State != output.CapturePending {
		t.Fatalf("the capture moved to %s without a seal answer", record.State)
	}
	if keys := h.bucketKeys(t); len(keys) != 0 {
		t.Fatalf("something was published before the seal: %v", keys)
	}

	h.Daemon.Restart(t)
	c.terminate(t)

	ref := assertPublishedAndReleased(t, c.advance(t))
	if entries := h.treeEntries(t); entries["artifact.txt"] != "survives a kill\n" {
		t.Errorf("the restarted daemon published %v", entries)
	}
	if ref.Digest == "" {
		t.Errorf("no digest")
	}
}

// A2: a sidecar writes into the step directory five seconds after the main
// container exited. The seal waits for every container of the exact Pod, so
// the late bytes are in the published tree.
func TestA2ASealWaitsForEveryWriterOfThePod(t *testing.T) {
	h := newHarness(t)
	c := h.admit(t).hold(t).write(t, "artifact.txt", "main\n").finish(t, true)

	writer := make(chan struct{})
	go func() {
		defer close(writer)
		time.Sleep(5 * time.Second)
		directory := filepath.Join(h.Daemon.StepsDir, c.Key().Directory())
		_ = os.WriteFile(filepath.Join(directory, "late.txt"), []byte("sidecar\n"), 0o644)
		_ = os.WriteFile(filepath.Join(h.Daemon.Terminations, string(c.Pod)), nil, 0o600)
	}()

	started := time.Now()
	record := c.advance(t)
	<-writer
	assertPublishedAndReleased(t, record)
	if time.Since(started) < 5*time.Second {
		t.Errorf("the seal answered in %s, before the Pod's last writer stopped", time.Since(started))
	}
	entries := h.treeEntries(t)
	if entries["artifact.txt"] != "main\n" || entries["late.txt"] != "sidecar\n" {
		t.Errorf("the published tree lost the late writer's bytes: %v", entries)
	}
}

// A3: no held marker, so nothing is captured -- even with bytes sitting in
// exactly the directory a capture would have read.
func TestA3NoHeldMarkerIsRefusedAndNothingIsPublished(t *testing.T) {
	h := newHarness(t)
	c := h.admit(t).start(t).write(t, "artifact.txt", "nothing held this\n").finish(t, true).terminate(t)

	record := c.advance(t)
	if record.State != output.CaptureFailed || !strings.Contains(record.Error, "no held marker") {
		t.Fatalf("a capture with no held marker is %s (%s)", record.State, record.Error)
	}
	if h.Dialer.Calls("publish") != 0 {
		t.Errorf("the coordinator asked to publish a step nothing held")
	}
	if keys := h.bucketKeys(t); len(keys) != 0 {
		t.Errorf("a step nothing held was published: %v", keys)
	}
	if !record.Released() {
		t.Errorf("the failed capture was never released")
	}
}

// A4: two identical trees captured concurrently while the reclaimer runs.
// One object, two published rows, zero deletes: a generation whose digest a
// live capture is about to join is never admitted for reclaim.
func TestA4IdenticalTreesShareOneObjectAndTheReclaimerDeletesNothing(t *testing.T) {
	h := newHarness(t)
	first := h.admit(t).produce(t, "identical\n")
	second := h.admit(t).produce(t, "identical\n")

	// Both seal concurrently; both rows record the same digest before any
	// object exists.
	arrived, release := h.Dialer.Gate("publish")
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, c := range []*capture{first, second} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = h.Coordinator.Advance(context.Background(), c.Key())
		}()
	}
	<-arrived
	deadline := time.Now().Add(30 * time.Second)
	for first.record(t).State != output.CapturePublishing || second.record(t).State != output.CapturePublishing {
		if time.Now().After(deadline) {
			t.Fatalf("both captures never reached publishing: %s, %s",
				first.record(t).State, second.record(t).State)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if first.record(t).Digest != second.record(t).Digest {
		t.Fatalf("identical trees sealed to different digests")
	}

	// The reclaimer runs, with grace elapsed, while both are publishing: there
	// is nothing to admit yet, and nothing it may admit.
	pass := &reclaimpass.AdmissionPass{
		Repository: h.Repository,
		Transactor: controllerTransactor{inner: &connTransactor{conn: h.Conn}},
		Grace:      time.Millisecond,
		Term:       time.Minute,
		OwnerID:    "harness-reclaimer",
	}
	lease := output.OperationLease{ActivationEpoch: harnessEpoch}
	reclaim := func() {
		if _, err := pass.Run(context.Background(), lease); err != nil {
			t.Fatalf("the reclaim admission pass: %v", err)
		}
	}
	reclaim()

	release()
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("capture %d: %v", i, err)
		}
	}
	firstRef := assertPublishedAndReleased(t, first.record(t))
	secondRef := assertPublishedAndReleased(t, second.record(t))
	if firstRef != secondRef {
		t.Errorf("identical trees published two generations: %v, %v", firstRef, secondRef)
	}
	if keys := h.bucketKeys(t); len(keys) != 1 {
		t.Errorf("identical trees left %d objects: %v", len(keys), keys)
	}

	// Release the first capture's claim and age the generation past grace.
	// The second capture's claim still protects it.
	tx, err := h.Conn.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := h.Repository.ReleaseClaim(context.Background(), tx, output.ClaimRelease{
		ProtocolVersion: output.ProtocolVersion, ClaimID: first.Key().ClaimID(), Ref: firstRef,
		RequestedAt: output.NewTimestamp(time.Now()),
	}); err != nil {
		t.Fatalf("releasing the first claim: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	reclaim()

	if jobs := h.reclaimJobs(t); jobs != 0 {
		t.Errorf("the reclaimer admitted %d deletes of a generation two captures published", jobs)
	}
	if keys := h.bucketKeys(t); len(keys) != 1 {
		t.Errorf("the shared object is gone: %v", keys)
	}
}

// A5: the web process dies after the object was created and before the
// publishing -> published commit. Recovery asks the store and completes onto
// the object that is there; it never creates a second one.
func TestA5RecoveryAfterALostPublishCompletesWithoutASecondObject(t *testing.T) {
	h := newHarness(t)
	c := h.admit(t).produce(t, "created once\n")

	h.Dialer.LoseAfter("publish", 1)
	if err := h.Coordinator.Advance(context.Background(), c.Key()); err == nil {
		t.Fatalf("advancing past a lost publish answer succeeded")
	}
	if record := c.record(t); record.State != output.CapturePublishing {
		t.Fatalf("after a lost publish the capture is %s", record.State)
	}
	if keys := h.bucketKeys(t); len(keys) != 1 {
		t.Fatalf("the publish whose answer was lost left %d objects", len(keys))
	}
	created, err := h.Store.GetObject(h.Bucket, h.bucketKeys(t)[0])
	if err != nil {
		t.Fatalf("reading the object: %v", err)
	}

	// A fresh coordinator: nothing survives the crash but the row.
	recovered := &hangaroutput.Coordinator{
		Transactor: h.Coordinator.Transactor, Rows: h.Repository,
		Dialer: h.Dialer, ActivationEpoch: harnessEpoch,
	}
	if err := recovered.Run(context.Background()); err != nil {
		t.Fatalf("the recovery pass: %v", err)
	}

	ref := assertPublishedAndReleased(t, c.record(t))
	if h.Dialer.Calls("publish") != 1 {
		t.Errorf("recovery published again: %d publish calls", h.Dialer.Calls("publish"))
	}
	if h.Dialer.Calls("stat") != 1 {
		t.Errorf("recovery asked the store %d times", h.Dialer.Calls("stat"))
	}
	if keys := h.bucketKeys(t); len(keys) != 1 {
		t.Errorf("recovery left %d objects: %v", len(keys), keys)
	}
	if ref.Generation != created.Generation {
		t.Errorf("the row names generation %d, the store created %d", ref.Generation, created.Generation)
	}
}

// lostAnswer is what a caller sees when the work happened and the answer did
// not arrive.
var lostAnswer = errors.New("the answer was lost after the work was done")

// ambiguousTransactor commits for real and then reports failure.
//
// This is the only honest shape for an ambiguous PostgreSQL commit: the rows
// are there and the caller does not know it. A transactor that rolled back
// instead would be testing against a commit that did not happen.
type ambiguousTransactor struct {
	inner hangaroutput.Transactor

	// LoseNext makes the next successful Commit report an error after it has
	// committed. Consumed, so the retry can succeed.
	LoseNext bool

	// Skip lets that many successful commits through first.
	Skip int

	mu sync.Mutex
}

func (transactor *ambiguousTransactor) Begin() (hangaroutput.Transaction, error) {
	tx, err := transactor.inner.Begin()
	if err != nil {
		return nil, err
	}

	return &ambiguousTransaction{Transaction: tx, owner: transactor}, nil
}

type ambiguousTransaction struct {
	hangaroutput.Transaction

	owner *ambiguousTransactor
}

func (tx *ambiguousTransaction) Commit() error {
	if err := tx.Transaction.Commit(); err != nil {
		return err
	}

	tx.owner.mu.Lock()
	defer tx.owner.mu.Unlock()
	if tx.owner.Skip > 0 {
		tx.owner.Skip--

		return nil
	}
	if tx.owner.LoseNext {
		tx.owner.LoseNext = false

		return lostAnswer
	}

	return nil
}

// Every CAS commit whose answer is lost is resolved by reading the row
// again: the next advance takes the capture on from wherever it durably is,
// and nothing is published twice.
func TestALostCommitAtAnyStepIsResolvedFromTheRow(t *testing.T) {
	for skip, step := range []string{"pending -> publishing", "publishing -> published", "released_at"} {
		t.Run(step, func(t *testing.T) {
			h := newHarness(t)
			c := h.admit(t).produce(t, "committed once\n")

			ambiguous := &ambiguousTransactor{inner: h.Coordinator.Transactor, LoseNext: true, Skip: skip}
			h.Coordinator.Transactor = ambiguous
			if err := h.Coordinator.Advance(context.Background(), c.Key()); !errors.Is(err, lostAnswer) {
				t.Fatalf("the lost %s commit was not reported: %v", step, err)
			}

			assertPublishedAndReleased(t, c.advance(t))
			if h.Dialer.Calls("publish") != 1 {
				t.Errorf("a lost %s commit published %d times", step, h.Dialer.Calls("publish"))
			}
			if keys := h.bucketKeys(t); len(keys) != 1 {
				t.Errorf("a lost %s commit left %d objects", step, len(keys))
			}
		})
	}
}
