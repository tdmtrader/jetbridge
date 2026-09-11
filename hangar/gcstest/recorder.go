package gcstest

import (
	"context"
	"io"
	"sort"
	"sync"

	"github.com/concourse/concourse/hangar/objectstore"
)

// Recorder wraps any objectstore.Client and remembers which RPCs went through
// it.
//
// It is the substrate for exactly one claim, and the claim is narrow: **the
// code issues only its role's RPCs**. It is not evidence about IAM. No fake
// enforces a binding, so a green here says the publisher never calls delete --
// not that the publisher's service account could not. Requirement 41's IAM
// honesty and AC 16 are real-GCS evidence and are gathered in Phase 9.
//
// It wraps either tier, which is the point: the same assertion runs against the
// in-memory fake and against fake-gcs-server, so a role that only behaved on
// one of them is visible.
type Recorder struct {
	client objectstore.Client

	mu    sync.Mutex
	calls []objectstore.Operation
}

// Record wraps a client.
func Record(client objectstore.Client) *Recorder {
	return &Recorder{client: client}
}

func (recorder *Recorder) note(operation objectstore.Operation) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.calls = append(recorder.calls, operation)
}

// Calls is every RPC issued, in order.
func (recorder *Recorder) Calls() []objectstore.Operation {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()

	return append([]objectstore.Operation(nil), recorder.calls...)
}

// Kinds is the distinct set, sorted, which is what a role assertion compares.
func (recorder *Recorder) Kinds() []objectstore.Operation {
	seen := map[objectstore.Operation]bool{}
	for _, call := range recorder.Calls() {
		seen[call] = true
	}
	kinds := make([]objectstore.Operation, 0, len(seen))
	for kind := range seen {
		kinds = append(kinds, kind)
	}
	sort.Slice(kinds, func(i, j int) bool { return kinds[i] < kinds[j] })

	return kinds
}

// Reset clears the log between phases of one test.
func (recorder *Recorder) Reset() {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.calls = nil
}

func (recorder *Recorder) Object(bucket, key string) objectstore.Handle {
	return recordingHandle{recorder: recorder, handle: recorder.client.Object(bucket, key)}
}

func (recorder *Recorder) List(ctx context.Context, bucket string, request objectstore.ListRequest) (objectstore.Page, error) {
	recorder.note(objectstore.OpList)

	return recorder.client.List(ctx, bucket, request)
}

type recordingHandle struct {
	recorder *Recorder
	handle   objectstore.Handle
}

func (handle recordingHandle) If(conditions objectstore.Conditions) objectstore.Handle {
	return recordingHandle{recorder: handle.recorder, handle: handle.handle.If(conditions)}
}

func (handle recordingHandle) Generation(generation int64) objectstore.Handle {
	return recordingHandle{recorder: handle.recorder, handle: handle.handle.Generation(generation)}
}

func (handle recordingHandle) NewWriter(ctx context.Context) objectstore.Writer {
	handle.recorder.note(objectstore.OpCreate)

	return handle.handle.NewWriter(ctx)
}

func (handle recordingHandle) NewReader(ctx context.Context) (io.ReadCloser, error) {
	handle.recorder.note(objectstore.OpRead)

	return handle.handle.NewReader(ctx)
}

func (handle recordingHandle) Attrs(ctx context.Context) (objectstore.Attrs, error) {
	handle.recorder.note(objectstore.OpStat)

	return handle.handle.Attrs(ctx)
}

// The delete seam, recorded separately because it IS separate: Delete is no
// longer a method on objectstore.Handle, so a recorder that wrapped the full
// client would have nothing to record. A Recorder therefore wraps a delete
// client as well, and a test that wants both passes both.
type recordingDeleteClient struct {
	recorder *Recorder
	client   objectstore.DeleteClient
}

// RecordDeletes wraps a delete client so its calls land in the same log.
func (recorder *Recorder) RecordDeletes(client objectstore.DeleteClient) objectstore.DeleteClient {
	return recordingDeleteClient{recorder: recorder, client: client}
}

func (client recordingDeleteClient) ObjectToDelete(bucket, key string) objectstore.DeleteHandle {
	return recordingDeleteHandle{
		recorder: client.recorder,
		handle:   client.client.ObjectToDelete(bucket, key),
	}
}

type recordingDeleteHandle struct {
	recorder *Recorder
	handle   objectstore.DeleteHandle
}

func (handle recordingDeleteHandle) If(conditions objectstore.Conditions) objectstore.DeleteHandle {
	return recordingDeleteHandle{recorder: handle.recorder, handle: handle.handle.If(conditions)}
}

func (handle recordingDeleteHandle) Generation(generation int64) objectstore.DeleteHandle {
	return recordingDeleteHandle{
		recorder: handle.recorder,
		handle:   handle.handle.Generation(generation),
	}
}

func (handle recordingDeleteHandle) Attrs(ctx context.Context) (objectstore.Attrs, error) {
	handle.recorder.note(objectstore.OpStat)

	return handle.handle.Attrs(ctx)
}

func (handle recordingDeleteHandle) Delete(ctx context.Context) error {
	handle.recorder.note(objectstore.OpDelete)

	return handle.handle.Delete(ctx)
}

var (
	_ objectstore.Client       = (*Recorder)(nil)
	_ objectstore.DeleteClient = recordingDeleteClient{}
)
