package publisher_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/snapshot"
)

func TestSnapshotChangeInspectorRevalidatesManifestMetadataDocumentAndPayload(t *testing.T) {
	fixture := newPublisherSnapshotFixture(t)
	inspector, err := publisher.NewSnapshotChangeInspector(fixture.metadata, fixture.content, snapshot.Canonicalizer{})
	if err != nil {
		t.Fatal(err)
	}
	change, err := inspector.Inspect(context.Background(), publisherGitRequest(fixture.ref))
	if err != nil {
		t.Fatal(err)
	}
	if change.BaseSHA != testBaseSHA || change.ResultSHA != testResultSHA || change.CanonicalArchivePath == "" || change.MaterializedRoot == "" {
		t.Fatalf("change = %+v", change)
	}
	_, gotTeam, _ := fixture.metadata.GetAuthorizedArgsForCall(0)
	if gotTeam != 9 {
		t.Fatalf("snapshot authorization team = %d, want 9", gotTeam)
	}
	root := filepath.Dir(change.MaterializedRoot)
	if err := change.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("captured tree still exists after Close: %v", err)
	}

	bad := fixture.manifest.Clone()
	bad.IntrinsicMetadata = json.RawMessage(`{"repository_id":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","base_sha":"9999999999999999999999999999999999999999","result_commit":"2222222222222222222222222222222222222222","result_tree":"3333333333333333333333333333333333333333","representation":"git-bundle","changed_files":[]}`)
	fixture.metadata.GetAuthorizedReturns(bad, true, nil)
	if _, err := inspector.Inspect(context.Background(), publisherGitRequest(fixture.ref)); err == nil || !strings.Contains(err.Error(), "intrinsic metadata") {
		t.Fatalf("mismatched metadata error = %v", err)
	}
}

func TestSnapshotChangeInspectorReopensExactTeamAuthorizedPRCandidate(t *testing.T) {
	fixture := newPublisherSnapshotFixture(t)
	inspector, err := publisher.NewSnapshotChangeInspector(fixture.metadata, fixture.content, snapshot.Canonicalizer{})
	if err != nil {
		t.Fatal(err)
	}

	change, err := inspector.InspectExactPRCandidate(context.Background(), 9, fixture.ref)
	if err != nil {
		t.Fatal(err)
	}
	if change.BaseSHA != testBaseSHA || change.ResultSHA != testResultSHA {
		t.Fatalf("change = %+v, want base %q result %q", change, testBaseSHA, testResultSHA)
	}
	if err := change.Close(); err != nil {
		t.Fatal(err)
	}
	_, gotTeam, gotID := fixture.metadata.GetAuthorizedArgsForCall(0)
	if gotTeam != 9 || gotID != fixture.ref.ID {
		t.Fatalf("snapshot authorization = (team %d, snapshot %d), want (9, %d)", gotTeam, gotID, fixture.ref.ID)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for name, testCase := range map[string]struct {
		ctx       context.Context
		teamID    int
		reference snapshot.SnapshotRef
	}{
		"nil context":       {ctx: nil, teamID: 9, reference: fixture.ref},
		"cancelled context": {ctx: cancelled, teamID: 9, reference: fixture.ref},
		"missing team":      {ctx: context.Background(), teamID: 0, reference: fixture.ref},
		"wrong type": {
			ctx: context.Background(), teamID: 9,
			reference: snapshot.SnapshotRef{ID: fixture.ref.ID, Type: "repository/v1", Digest: fixture.ref.Digest},
		},
		"invalid reference": {
			ctx: context.Background(), teamID: 9,
			reference: snapshot.SnapshotRef{ID: fixture.ref.ID, Type: fixture.ref.Type},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if change, err := inspector.InspectExactPRCandidate(testCase.ctx, testCase.teamID, testCase.reference); err == nil {
				_ = change.Close()
				t.Fatal("invalid exact candidate inspection succeeded")
			}
		})
	}
	if calls := fixture.metadata.GetAuthorizedCallCount(); calls != 1 {
		t.Fatalf("metadata authorization calls = %d, want only the valid inspection", calls)
	}
}

// preUpgradeIntrinsicMetadata is the EXACT intrinsic-metadata shape written by
// the currently deployed web (origin/jetbridge, 08f6d98950,
// agent/snapshot/contracts/repository_change.go). Sealed snapshots keep the
// metadata bytes they were sealed with forever, so every reader on this branch
// has to keep accepting this shape after the field rename.
type preUpgradeIntrinsicMetadata struct {
	RepositoryID   string `json:"repository_id"`
	BaseSHA        string `json:"base_sha"`
	ResultSHA      string `json:"result_sha,omitempty"`
	ResultTreeSHA  string `json:"result_tree_sha"`
	Representation string `json:"representation"`
}

func TestSnapshotChangeInspectorReadsPreUpgradeIntrinsicMetadata(t *testing.T) {
	fixture := newPublisherSnapshotFixture(t)
	inspector, err := publisher.NewSnapshotChangeInspector(fixture.metadata, fixture.content, snapshot.Canonicalizer{})
	if err != nil {
		t.Fatal(err)
	}

	legacy, err := json.Marshal(preUpgradeIntrinsicMetadata{
		RepositoryID: digestOf([]byte("repository")).String(),
		BaseSHA:      testBaseSHA, ResultSHA: testResultSHA, ResultTreeSHA: testTreeSHA,
		Representation: "bundle",
	})
	if err != nil {
		t.Fatal(err)
	}
	sealed := fixture.manifest.Clone()
	sealed.IntrinsicMetadata = legacy
	fixture.metadata.GetAuthorizedReturns(sealed, true, nil)

	change, err := inspector.Inspect(context.Background(), publisherGitRequest(fixture.ref))
	if err != nil {
		t.Fatalf("pre-upgrade intrinsic metadata was rejected: %v", err)
	}
	if change.BaseSHA != testBaseSHA || change.ResultSHA != testResultSHA {
		t.Fatalf("change = %+v, want base %q result %q", change, testBaseSHA, testResultSHA)
	}
	if err := change.Close(); err != nil {
		t.Fatal(err)
	}
}

// Tolerating the pre-upgrade names must not become a hole. Anything that is
// neither exactly the modern shape nor exactly the pre-upgrade shape — including
// a document that mixes the two spellings — still has to be refused.
func TestSnapshotChangeInspectorRefusesMalformedIntrinsicMetadata(t *testing.T) {
	repositoryID := digestOf([]byte("repository")).String()
	for name, metadata := range map[string]string{
		"mixed old and new result names": `{"repository_id":"` + repositoryID + `","base_sha":"` + testBaseSHA +
			`","result_sha":"` + testResultSHA + `","result_commit":"` + testResultSHA +
			`","result_tree_sha":"` + testTreeSHA + `","result_tree":"` + testTreeSHA + `","representation":"bundle"}`,
		"old names with an unknown field": `{"repository_id":"` + repositoryID + `","base_sha":"` + testBaseSHA +
			`","result_sha":"` + testResultSHA + `","result_tree_sha":"` + testTreeSHA +
			`","representation":"bundle","smuggled":"junk"}`,
		"old names with a modern representation": `{"repository_id":"` + repositoryID + `","base_sha":"` + testBaseSHA +
			`","result_sha":"` + testResultSHA + `","result_tree_sha":"` + testTreeSHA + `","representation":"git-bundle"}`,
		"new names with the retired representation": `{"repository_id":"` + repositoryID + `","base_sha":"` + testBaseSHA +
			`","result_commit":"` + testResultSHA + `","result_tree":"` + testTreeSHA +
			`","representation":"bundle","changed_files":[]}`,
		"old names with trailing JSON": `{"repository_id":"` + repositoryID + `","base_sha":"` + testBaseSHA +
			`","result_sha":"` + testResultSHA + `","result_tree_sha":"` + testTreeSHA + `","representation":"bundle"}{}`,
		"old names with an abbreviated result": `{"repository_id":"` + repositoryID + `","base_sha":"` + testBaseSHA +
			`","result_sha":"abc1234","result_tree_sha":"` + testTreeSHA + `","representation":"bundle"}`,
		"arbitrary document": `{"totally":"unrelated"}`,
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newPublisherSnapshotFixture(t)
			inspector, err := publisher.NewSnapshotChangeInspector(fixture.metadata, fixture.content, snapshot.Canonicalizer{})
			if err != nil {
				t.Fatal(err)
			}
			sealed := fixture.manifest.Clone()
			sealed.IntrinsicMetadata = json.RawMessage(metadata)
			fixture.metadata.GetAuthorizedReturns(sealed, true, nil)
			change, err := inspector.Inspect(context.Background(), publisherGitRequest(fixture.ref))
			if err == nil {
				_ = change.Close()
				t.Fatal("malformed intrinsic metadata was accepted")
			}
		})
	}
}

func TestSnapshotValueInspectorFromStoreRehashesExactAuthorizedBytes(t *testing.T) {
	fixture := newPublisherSnapshotFixture(t)
	inspector, err := publisher.NewSnapshotValueInspectorFromStore(fixture.metadata, fixture.content, snapshot.Canonicalizer{})
	if err != nil {
		t.Fatal(err)
	}
	request := publisherGitRequest(fixture.ref)
	value, err := inspector.InspectValue(context.Background(), request)
	if err != nil || value.CanonicalArchivePath == "" {
		t.Fatalf("InspectValue = (%+v, %v)", value, err)
	}
	root := filepath.Dir(value.CanonicalArchivePath)
	if err := value.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("captured generic snapshot tree still exists after Close: %v", err)
	}

	fixture.content.OpenStub = func(context.Context, snapshot.Snapshot) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(tarBytes(t, map[string][]byte{"changed": []byte("bytes")}))), nil
	}
	if _, err := inspector.InspectValue(context.Background(), request); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("corrupt generic snapshot error = %v", err)
	}
}
