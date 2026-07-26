package contracttest

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
	"github.com/concourse/concourse/agent/snapshot/snapshotfakes"
)

// maxFixtureRecordBytes bounds the record.json a caller-supplied tar may carry.
const maxFixtureRecordBytes = 1 << 20

// digestOf mirrors the helper agent/publisher/gateway_test.go uses. There is no
// exported helper for this in agent/snapshot (verified: no MustDigestOf).
func digestOf(content []byte) snapshot.Digest {
	sum := sha256.Sum256(content)
	return snapshot.Digest("sha256:" + hex.EncodeToString(sum[:]))
}

// ChangeFixture is an exact sealed repository-change/v1 value plus its
// canonical archive bytes. NewSyntheticChange builds one for the in-repo run;
// LoadChangeFixture wraps a real canonical tar for a live run against a
// gateway that will actually apply it.
type ChangeFixture struct {
	Manifest  snapshot.Snapshot
	Archive   []byte
	BaseSHA   string
	ResultSHA string
}

func (fixture ChangeFixture) stores() (snapshot.MetadataStore, snapshot.ContentStore) {
	metadata := &snapshotfakes.FakeMetadataStore{}
	metadata.GetAuthorizedReturns(fixture.Manifest, true, nil)
	content := &snapshotfakes.FakeContentStore{}
	archive := fixture.Archive
	content.OpenStub = func(context.Context, snapshot.Snapshot) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(archive)), nil
	}
	return metadata, content
}

func (fixture ChangeFixture) ref() snapshot.SnapshotRef {
	return snapshot.SnapshotRef{
		ID: fixture.Manifest.ID, Type: fixture.Manifest.Type, Digest: fixture.Manifest.Digest,
	}
}

// NewSyntheticChange builds a valid repository-change/v1 whose payload is not
// a real Git bundle. It is enough for the reference gateway and for every
// read-only check; a live gateway that applies the change needs
// LoadChangeFixture instead.
func NewSyntheticChange(t *testing.T, baseSHA, resultSHA, resultTree string) ChangeFixture {
	t.Helper()
	payload := []byte("bundle bytes")
	body := contracts.RepositoryChangeBody{
		RepositoryID: digestOf([]byte("repository")).String(),
		BaseSHA:      baseSHA, ResultCommit: resultSHA, ResultTree: resultTree,
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
		body,
	)
	if err != nil {
		t.Fatal(err)
	}
	document, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	raw := referenceTar(t, map[string][]byte{"content/change.bundle": payload, "record.json": document})
	return captureChange(t, raw, body, baseSHA, resultSHA, resultTree)
}

// LoadChangeFixture wraps a caller-supplied repository-change/v1 tar. Use it
// for a live gateway that will really apply the change.
//
// The body is read back out of the tar's own record.json rather than
// constructed here: the manifest this builds is what the publisher's
// SnapshotChangeInspector cross-checks against record.json, and a synthesized
// body would make the two disagree for every real change (the inspector
// rejects that with "repository-change intrinsic metadata does not match exact
// content").
func LoadChangeFixture(t *testing.T, tarPath, baseSHA, resultSHA, resultTree string) ChangeFixture {
	t.Helper()
	raw, err := os.ReadFile(tarPath)
	if err != nil {
		t.Fatal(err)
	}
	body := recordBodyFromTar(t, raw)
	if body.BaseSHA != baseSHA || body.ResultCommit != resultSHA || body.ResultTree != resultTree {
		t.Fatalf(
			"%s record.json describes base %s / result %s / tree %s, but the caller declared base %s / result %s / tree %s",
			tarPath, body.BaseSHA, body.ResultCommit, body.ResultTree, baseSHA, resultSHA, resultTree,
		)
	}
	return captureChange(t, raw, body, baseSHA, resultSHA, resultTree)
}

// captureChange canonicalizes raw, keeps its archive bytes in memory, and
// assembles the sealed manifest that describes exactly those bytes. The
// intrinsic metadata is derived from body — the same record.json the archive
// carries — because the publisher re-reads both and refuses a change whose
// manifest and record disagree.
func captureChange(
	t *testing.T,
	raw []byte,
	body contracts.RepositoryChangeBody,
	baseSHA, resultSHA, resultTree string,
) ChangeFixture {
	t.Helper()
	tree, err := (snapshot.Canonicalizer{}).Capture(context.Background(), bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	archive, readErr := os.ReadFile(tree.ArchivePath)
	digest, size, files := tree.Digest, tree.ByteSize, tree.FileCount
	if err := errors.Join(readErr, tree.Close()); err != nil {
		t.Fatal(err)
	}
	metadata, err := json.Marshal(contracts.RepositoryChangeMetadata{
		RepositoryID: body.RepositoryID, BaseSHA: baseSHA, ResultCommit: resultSHA,
		ResultTree: resultTree, Representation: body.Representation, ChangedFiles: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return ChangeFixture{
		Manifest: snapshot.Snapshot{
			ID: 41, Type: "repository-change/v1", Digest: digest, ByteSize: size, FileCount: files,
			Representation: "application/x-tar", IntrinsicMetadata: metadata,
			ContentState: snapshot.ContentStateAvailable, CreatedAt: time.Now().UTC(),
		},
		Archive:   archive,
		BaseSHA:   baseSHA,
		ResultSHA: resultSHA,
	}
}

// recordBodyFromTar reads the repository-change record out of a raw tar. It
// reads the tar directly rather than the captured tree so a caller-supplied
// archive can be described before it is canonicalized.
func recordBodyFromTar(t *testing.T, raw []byte) contracts.RepositoryChangeBody {
	t.Helper()
	reader := tar.NewReader(bytes.NewReader(raw))
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			t.Fatal("repository-change tar contains no record.json")
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Name != "record.json" {
			continue
		}
		document, err := io.ReadAll(io.LimitReader(reader, maxFixtureRecordBytes))
		if err != nil {
			t.Fatal(err)
		}
		var record contracts.Record[contracts.RepositoryChangeBody]
		if err := json.Unmarshal(document, &record); err != nil {
			t.Fatal(err)
		}
		return record.Body
	}
}

// referenceTar writes a deterministic tar, matching the writer
// agent/publisher/gateway_test.go uses so the two fixtures stay in agreement.
func referenceTar(t *testing.T, files map[string][]byte) []byte {
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
		if err := writer.WriteHeader(&tar.Header{
			Name: name, Mode: 0600, Size: int64(len(content)), Typeflag: tar.TypeReg,
		}); err != nil {
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
