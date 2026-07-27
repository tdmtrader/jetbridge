package projection_test

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
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/projection"
	"github.com/concourse/concourse/agent/repodiff"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

type repositoryProjectionStore struct {
	input      projection.RepositoryChangeProjectionInput
	found      bool
	findErr    error
	candidates []snapshot.SnapshotRef
	upsertErr  error
	upserts    []projection.RepositoryChange
	statuses   []repositoryProjectionStatusUpdate
}

type repositoryProjectionStatusUpdate struct {
	id     snapshot.SnapshotID
	status projection.RepositoryChangeProjectionStatus
	reason string
}

func (store *repositoryProjectionStore) FindRepositoryChangeInput(context.Context, snapshot.SnapshotID) (projection.RepositoryChangeProjectionInput, bool, error) {
	return store.input, store.found, store.findErr
}

func (store *repositoryProjectionStore) ListUnprojectedRepositoryChanges(context.Context, int) ([]snapshot.SnapshotRef, error) {
	return append([]snapshot.SnapshotRef(nil), store.candidates...), nil
}

func (store *repositoryProjectionStore) UpsertRepositoryChangeProjection(_ context.Context, value projection.RepositoryChange) error {
	value.Files = append([]repodiff.ChangedFile(nil), value.Files...)
	store.upserts = append(store.upserts, value)
	return store.upsertErr
}

func (store *repositoryProjectionStore) SetRepositoryChangeProjectionStatus(_ context.Context, id snapshot.SnapshotID, status projection.RepositoryChangeProjectionStatus, reason string) error {
	store.statuses = append(store.statuses, repositoryProjectionStatusUpdate{id: id, status: status, reason: reason})
	return nil
}

type repositoryProjectionContent struct {
	archives map[snapshot.SnapshotID][]byte
	err      error
}

func (content repositoryProjectionContent) Open(_ context.Context, manifest snapshot.Snapshot) (io.ReadCloser, error) {
	if content.err != nil {
		return nil, content.err
	}
	archive, found := content.archives[manifest.ID]
	if !found {
		return nil, errors.New("object unavailable")
	}
	return io.NopCloser(bytes.NewReader(archive)), nil
}

func TestRepositoryChangeProjectorRevalidatesCanonicalInputsAndUpsertsIdempotently(t *testing.T) {
	base, metadata := projectionBaseRepository(t)
	changeRoot := buildChangeSnapshot(t, base, metadata, "patch")
	changeArchive := projectionTar(t, changeRoot)
	baseArchive := projectionTar(t, base)
	encodedMetadata, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	changeManifest := repositoryProjectionManifest(t, 51, "repository-change/v1", changeArchive, nil)
	baseManifest := repositoryProjectionManifest(t, 50, "repository/v1", baseArchive, encodedMetadata)
	store := &repositoryProjectionStore{found: true, input: projection.RepositoryChangeProjectionInput{
		Snapshot: changeManifest,
		Inputs:   []projection.RepositoryChangeBaseInput{{Port: "base", Snapshot: baseManifest}},
	}}
	projector, err := projection.NewRepositoryChangeProjector(store, repositoryProjectionContent{archives: map[snapshot.SnapshotID][]byte{
		changeManifest.ID: changeArchive, baseManifest.ID: baseArchive,
	}}, projection.WithRepositoryChangeCanonicalizer(snapshot.Canonicalizer{TempDir: t.TempDir()}))
	if err != nil {
		t.Fatal(err)
	}
	ref := snapshot.SnapshotRef{ID: changeManifest.ID, Type: changeManifest.Type, Digest: changeManifest.Digest}
	if err := projector.Project(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	if err := projector.Project(context.Background(), ref); err != nil {
		t.Fatalf("idempotent retry: %v", err)
	}
	if len(store.upserts) != 2 || !reflect.DeepEqual(store.upserts[0], store.upserts[1]) {
		t.Fatalf("upserts are not deterministic: %#v", store.upserts)
	}
	value := store.upserts[0]
	if value.SnapshotID != changeManifest.ID || value.RepositoryID != metadata.RepositoryID ||
		value.Status != projection.RepositoryChangeProjectionReady || value.FileCount != 1 ||
		value.Files[0].Path != "README.md" || value.Files[0].Status != repodiff.ChangeModified {
		t.Fatalf("projection = %#v", value)
	}
	if len(store.statuses) != 2 || store.statuses[0].status != projection.RepositoryChangeProjectionPending ||
		store.statuses[1].status != projection.RepositoryChangeProjectionPending {
		t.Fatalf("status updates = %#v, want pending before each idempotent derivation", store.statuses)
	}
}

func TestRepositoryChangeProjectorKeepsUnavailableContentRetryable(t *testing.T) {
	manifest := snapshot.Snapshot{
		ID: 52, Type: "repository-change/v1", Digest: testDigest('a'), ByteSize: 1, FileCount: 1,
		Representation: "filesystem-tree-v1", ContentState: snapshot.ContentStateAvailable, CreatedAt: time.Now().UTC(),
	}
	store := &repositoryProjectionStore{found: true, input: projection.RepositoryChangeProjectionInput{Snapshot: manifest}}
	projector, err := projection.NewRepositoryChangeProjector(store, repositoryProjectionContent{err: errors.New("object store offline")})
	if err != nil {
		t.Fatal(err)
	}
	err = projector.Project(context.Background(), snapshot.SnapshotRef{ID: manifest.ID, Type: manifest.Type, Digest: manifest.Digest})
	if !errors.Is(err, snapshot.ErrContentUnavailable) {
		t.Fatalf("error = %v, want ErrContentUnavailable", err)
	}
	if len(store.upserts) != 0 {
		t.Fatal("unavailable content was projected")
	}
	if len(store.statuses) != 2 || store.statuses[0].status != projection.RepositoryChangeProjectionPending ||
		store.statuses[1].status != projection.RepositoryChangeProjectionUnavailable || store.statuses[1].reason == "" {
		t.Fatalf("status updates = %#v", store.statuses)
	}
}

func TestDeriveRepositoryChangeSupportsEverySealedRepresentation(t *testing.T) {
	base, metadata := projectionBaseRepository(t)
	for _, representation := range []string{"git-tree", "patch", "git-bundle"} {
		t.Run(representation, func(t *testing.T) {
			changeRoot := buildChangeSnapshot(t, base, metadata, representation)
			baseRoot := cloneProjectionRepository(t, base)
			value, err := projection.DeriveRepositoryChange(context.Background(), projection.RepositoryChangeInput{
				SnapshotID: 41, ChangeRoot: changeRoot, BaseRoot: baseRoot, BaseMetadata: metadata,
			})
			if err != nil {
				t.Fatal(err)
			}
			if value.SnapshotID != snapshot.SnapshotID(41) || value.RepositoryID != metadata.RepositoryID ||
				value.BaseSHA != metadata.HeadSHA || value.Representation != representation {
				t.Fatalf("projection identity = %+v", value)
			}
			if value.FileCount != 1 || value.LinesAdded != 1 || value.LinesDeleted != 1 || len(value.Files) != 1 {
				t.Fatalf("projection statistics = %+v", value)
			}
			if value.Files[0].Path != "README.md" || value.Files[0].Status != "modified" ||
				!strings.Contains(value.UnifiedDiff, "diff --git") || value.Truncated {
				t.Fatalf("projection diff = %+v", value)
			}
		})
	}
}

func TestDeriveRepositoryChangeRejectsBaseThatDoesNotMatchSealedLineage(t *testing.T) {
	base, metadata := projectionBaseRepository(t)
	changeRoot := buildChangeSnapshot(t, base, metadata, "patch")
	metadata.HeadSHA = strings.Repeat("0", 40)
	_, err := projection.DeriveRepositoryChange(context.Background(), projection.RepositoryChangeInput{
		SnapshotID: 42, ChangeRoot: changeRoot, BaseRoot: cloneProjectionRepository(t, base), BaseMetadata: metadata,
	})
	if err == nil || !strings.Contains(err.Error(), "base") {
		t.Fatalf("DeriveRepositoryChange() error = %v, want base mismatch", err)
	}
}

func projectionBaseRepository(t *testing.T) (string, contracts.RepositoryMetadata) {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "base")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	projectionGit(t, directory, "init", "-q", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(directory, "README.md"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	projectionGit(t, directory, "add", ".")
	projectionGit(t, directory, "commit", "-q", "-m", "base")
	head := projectionGit(t, directory, "rev-parse", "HEAD")
	tree := projectionGit(t, directory, "rev-parse", "HEAD^{tree}")
	root := projectionGit(t, directory, "rev-list", "--max-parents=0", "HEAD")
	objectFormat := projectionGit(t, directory, "rev-parse", "--show-object-format=storage")
	identity := sha256.Sum256([]byte("concourse.repository/v1\n" + objectFormat + "\n" + root + "\n"))
	return directory, contracts.RepositoryMetadata{
		RepositoryID: "sha256:" + hex.EncodeToString(identity[:]), ObjectFormat: objectFormat,
		HeadSHA: head, TreeSHA: tree, RootCommits: []string{root},
	}
}

func buildChangeSnapshot(t *testing.T, base string, baseMetadata contracts.RepositoryMetadata, representation string) string {
	t.Helper()
	changeRoot := t.TempDir()
	result := cloneProjectionRepository(t, base)
	if err := os.WriteFile(filepath.Join(result, "README.md"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	document := contracts.RepositoryChangeBody{
		RepositoryID: baseMetadata.RepositoryID,
		BaseSHA:      baseMetadata.HeadSHA, Representation: representation,
	}
	payloadPath := ""
	var payload []byte
	switch representation {
	case "patch":
		payload = []byte(projectionGit(t, result, "diff", "--binary", "--no-ext-diff", "HEAD") + "\n")
		projectionGit(t, result, "add", "README.md")
		document.ResultTree = projectionGit(t, result, "write-tree")
		payloadPath = "content/change.patch"
	case "git-tree":
		projectionGit(t, result, "add", "README.md")
		projectionGit(t, result, "commit", "-q", "-m", "result")
		document.ResultCommit = projectionGit(t, result, "rev-parse", "HEAD")
		document.ResultTree = projectionGit(t, result, "rev-parse", "HEAD^{tree}")
		payload = projectionTar(t, result)
		payloadPath = "content/result.tar"
	case "git-bundle":
		projectionGit(t, result, "add", "README.md")
		projectionGit(t, result, "commit", "-q", "-m", "result")
		document.ResultCommit = projectionGit(t, result, "rev-parse", "HEAD")
		document.ResultTree = projectionGit(t, result, "rev-parse", "HEAD^{tree}")
		bundlePath := filepath.Join(t.TempDir(), "result.bundle")
		projectionGit(t, result, "bundle", "create", bundlePath, "HEAD", "^"+baseMetadata.HeadSHA)
		var err error
		payload, err = os.ReadFile(bundlePath)
		if err != nil {
			t.Fatal(err)
		}
		payloadPath = "content/result.bundle"
	default:
		t.Fatalf("unknown representation %q", representation)
	}
	digest := sha256.Sum256(payload)
	payloadDigest, err := snapshot.ParseDigest("sha256:" + hex.EncodeToString(digest[:]))
	if err != nil {
		t.Fatal(err)
	}
	document.Payload = contracts.ContentRef{
		Path: payloadPath, Digest: payloadDigest, MediaType: "application/octet-stream",
	}
	baseTree, err := (snapshot.Canonicalizer{}).Capture(context.Background(), bytes.NewReader(projectionTar(t, base)))
	if err != nil {
		t.Fatal(err)
	}
	baseDigest := baseTree.Digest
	if err := baseTree.Close(); err != nil {
		t.Fatal(err)
	}
	record, err := contracts.NewRecord(
		snapshot.TypeRef("repository-change/v1"),
		[]contracts.Subject{{
			ID: "base", Role: contracts.SubjectRoleBase, Input: "base",
			Type: snapshot.TypeRef("repository/v1"), Digest: baseDigest,
		}},
		document,
	)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(changeRoot, "record.json"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(changeRoot, "content"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(changeRoot, filepath.FromSlash(payloadPath)), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	return changeRoot
}

func cloneProjectionRepository(t *testing.T, source string) string {
	t.Helper()
	destination := filepath.Join(t.TempDir(), "repository")
	projectionGit(t, filepath.Dir(destination), "clone", "-q", "--no-hardlinks", source, destination)
	return destination
}

func projectionGit(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	command.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=projection-test", "GIT_AUTHOR_EMAIL=projection@example.test",
		"GIT_COMMITTER_NAME=projection-test", "GIT_COMMITTER_EMAIL=projection@example.test",
		"GIT_TERMINAL_PROMPT=0",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
	return strings.TrimSpace(string(output))
}

func projectionTar(t *testing.T, directory string) []byte {
	t.Helper()
	paths := []string{}
	if err := filepath.WalkDir(directory, func(name string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if name != directory {
			paths = append(paths, name)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for _, name := range paths {
		info, err := os.Lstat(name)
		if err != nil {
			t.Fatal(err)
		}
		relative, err := filepath.Rel(directory, name)
		if err != nil {
			t.Fatal(err)
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			t.Fatal(err)
		}
		header.Name = filepath.ToSlash(relative)
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if info.Mode().IsRegular() {
			file, err := os.Open(name)
			if err != nil {
				t.Fatal(err)
			}
			_, copyErr := io.Copy(writer, file)
			closeErr := file.Close()
			if copyErr != nil || closeErr != nil {
				t.Fatalf("archive %s: %v / %v", relative, copyErr, closeErr)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	tree, err := (snapshot.Canonicalizer{}).Capture(context.Background(), bytes.NewReader(buffer.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Close()
	canonical, err := os.ReadFile(tree.ArchivePath)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func repositoryProjectionManifest(t *testing.T, id snapshot.SnapshotID, typeRef snapshot.TypeRef, archive []byte, metadata json.RawMessage) snapshot.Snapshot {
	t.Helper()
	tree, err := (snapshot.Canonicalizer{}).Capture(context.Background(), bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Close()
	return snapshot.Snapshot{
		ID: id, Type: typeRef, Digest: tree.Digest, ByteSize: tree.ByteSize, FileCount: tree.FileCount,
		Representation: "filesystem-tree-v1", IntrinsicMetadata: append(json.RawMessage(nil), metadata...),
		ContentState: snapshot.ContentStateAvailable, CreatedAt: time.Now().UTC(),
	}
}
