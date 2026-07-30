package pullrequest

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
	"github.com/concourse/concourse/agent/snapshot/snapshotfakes"
)

func TestSnapshotMonitorObservationInspectorReopensExactSealedRecord(
	t *testing.T,
) {
	body := contracts.PullRequestBody{
		Provider: "github", Repository: "acme/widget", ExternalID: "42",
		URL:          "https://github.example/acme/widget/pull/42",
		State:        contracts.PullRequestCompleted,
		Mergeability: contracts.PullRequestMergeable,
		SourceRef:    "refs/heads/agent/upgrade",
		SourceSHA:    monitorObjectID('a'),
		TargetRef:    "refs/heads/main",
		TargetSHA:    monitorObjectID('b'),
		Iteration:    "iteration-1",
		Trigger:      contracts.PullRequestCompletedTrigger,
	}
	record, err := contracts.NewRecord(
		snapshot.TypeRef("pull-request/v1"), nil, body,
	)
	if err != nil {
		t.Fatal(err)
	}
	recordJSON, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	raw := monitorObservationTar(t, recordJSON)
	tree, err := (snapshot.Canonicalizer{}).Capture(
		context.Background(), bytes.NewReader(raw),
	)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := os.ReadFile(tree.ArchivePath)
	if err != nil {
		t.Fatal(err)
	}
	manifest := snapshot.Snapshot{
		ID: 501, Type: "pull-request/v1", Digest: tree.Digest,
		ByteSize: tree.ByteSize, FileCount: tree.FileCount,
		Representation: "application/x-tar",
		ContentState:   snapshot.ContentStateAvailable,
		CreatedAt:      time.Now().UTC(),
	}
	if err := tree.Close(); err != nil {
		t.Fatal(err)
	}
	metadata := &snapshotfakes.FakeMetadataStore{}
	metadata.GetAuthorizedReturns(manifest, true, nil)
	content := &snapshotfakes.FakeContentStore{}
	content.OpenStub = func(
		context.Context,
		snapshot.Snapshot,
	) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(archive)), nil
	}
	inspector, err := NewSnapshotMonitorObservationInspector(
		metadata, content, snapshot.Canonicalizer{},
	)
	if err != nil {
		t.Fatal(err)
	}
	reference := snapshot.SnapshotRef{
		ID: manifest.ID, Type: manifest.Type, Digest: manifest.Digest,
	}

	got, err := inspector.InspectMonitorObservation(
		context.Background(), 17, reference,
	)
	if err != nil {
		t.Fatalf("InspectMonitorObservation() error = %v", err)
	}
	if !reflect.DeepEqual(got, body) {
		t.Fatalf("observation = %#v, want %#v", got, body)
	}
	_, teamID, snapshotID := metadata.GetAuthorizedArgsForCall(0)
	if teamID != 17 || snapshotID != reference.ID {
		t.Fatalf(
			"snapshot authorization = team %d snapshot %d",
			teamID, snapshotID,
		)
	}

	substituted := reference
	substituted.Digest = monitorSnapshotDigest('f')
	if _, err := inspector.InspectMonitorObservation(
		context.Background(), 17, substituted,
	); err == nil {
		t.Fatal("substituted observation digest was accepted")
	}

	partial := &monitorObservationReadCloser{
		Reader: bytes.NewReader(nil),
	}
	content.OpenReturns(partial, errors.New("object store interrupted"))
	if _, err := inspector.InspectMonitorObservation(
		context.Background(), 17, reference,
	); err == nil {
		t.Fatal("partial content open was accepted")
	}
	if !partial.closed {
		t.Fatal("partial content reader was not closed")
	}
}

func monitorObservationTar(t *testing.T, record []byte) []byte {
	t.Helper()
	var raw bytes.Buffer
	writer := tar.NewWriter(&raw)
	if err := writer.WriteHeader(&tar.Header{
		Name: "record.json", Mode: 0600,
		Size: int64(len(record)), Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(record); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return raw.Bytes()
}

type monitorObservationReadCloser struct {
	io.Reader
	closed bool
}

func (reader *monitorObservationReadCloser) Close() error {
	reader.closed = true
	return nil
}
