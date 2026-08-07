package publisher_test

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
	"github.com/concourse/concourse/agent/snapshot/snapshotfakes"
)

const (
	testBaseSHA   = "1111111111111111111111111111111111111111"
	testResultSHA = "2222222222222222222222222222222222222222"
	testTreeSHA   = "3333333333333333333333333333333333333333"
)

type publisherSnapshotFixture struct {
	metadata *snapshotfakes.FakeMetadataStore
	content  *snapshotfakes.FakeContentStore
	manifest snapshot.Snapshot
	ref      snapshot.SnapshotRef
	archive  []byte
}

func newPublisherSnapshotFixture(t *testing.T) publisherSnapshotFixture {
	t.Helper()
	payload := []byte("bundle bytes")
	document := contracts.RepositoryChangeBody{
		RepositoryID: digestOf([]byte("repository")).String(),
		BaseSHA:      testBaseSHA, ResultCommit: testResultSHA, ResultTree: testTreeSHA,
		Representation: "git-bundle",
		Payload: contracts.ContentRef{
			Path: "content/change.bundle", Digest: digestOf(payload),
			MediaType: "application/x-git-bundle",
		},
	}
	record, err := contracts.NewRecord(
		snapshot.TypeRef("repository-change/v1"),
		[]contracts.Subject{{
			ID: "base", Role: contracts.SubjectRoleBase, Input: "repository",
			Type: snapshot.TypeRef("repository/v1"), Digest: digestOf([]byte("base")),
		}},
		document,
	)
	if err != nil {
		t.Fatal(err)
	}
	documentBytes, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	raw := tarBytes(t, map[string][]byte{"content/change.bundle": payload, "record.json": documentBytes})
	tree, err := (snapshot.Canonicalizer{}).Capture(context.Background(), bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	archive, err := os.ReadFile(tree.ArchivePath)
	if err != nil {
		t.Fatal(err)
	}
	digest, size, files := tree.Digest, tree.ByteSize, tree.FileCount
	if err := tree.Close(); err != nil {
		t.Fatal(err)
	}
	metadataBytes, err := json.Marshal(contracts.RepositoryChangeMetadata{
		RepositoryID: document.RepositoryID, BaseSHA: testBaseSHA, ResultCommit: testResultSHA,
		ResultTree: testTreeSHA, Representation: "git-bundle", ChangedFiles: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	manifest := snapshot.Snapshot{
		ID: 41, Type: "repository-change/v1", Digest: digest, ByteSize: size, FileCount: files,
		Representation: "application/x-tar", IntrinsicMetadata: metadataBytes,
		ContentState: snapshot.ContentStateAvailable, CreatedAt: time.Now().UTC(),
	}
	metadata := &snapshotfakes.FakeMetadataStore{}
	metadata.GetAuthorizedReturns(manifest, true, nil)
	content := &snapshotfakes.FakeContentStore{}
	content.OpenStub = func(context.Context, snapshot.Snapshot) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(archive)), nil
	}
	return publisherSnapshotFixture{
		metadata: metadata, content: content, manifest: manifest,
		ref: snapshot.SnapshotRef{ID: manifest.ID, Type: manifest.Type, Digest: manifest.Digest}, archive: archive,
	}
}

func publisherGitRequest(ref snapshot.SnapshotRef) publisher.Request {
	return publisher.Request{
		Publisher: publisher.GitPublisher, Input: ref, Destination: "git.example/acme/widget", Mode: publisher.ModeBranch,
		Parameters:            map[string]string{"source_branch": "agent/change", "target_branch": "main"},
		ApprovalPolicyVersion: "engineering/v1",
		Authority:             publisher.Authority{TeamID: 9, TeamName: "engineering", BuildID: 12, WorkflowRunID: 17, Actor: "build/12"},
	}
}

func tarBytes(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		content := files[name]
		if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func digestOf(content []byte) snapshot.Digest {
	digest := sha256.Sum256(content)
	return snapshot.Digest("sha256:" + hex.EncodeToString(digest[:]))
}
