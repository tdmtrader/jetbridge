package publisher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

// SnapshotValueInspectorFromStore reopens an exact team-authorized snapshot
// and canonicalizes it again immediately before a work-item side effect.
// Semantic validation happened at the sealing boundary; this check proves the
// offered bytes still match that immutable value.
type SnapshotValueInspectorFromStore struct {
	metadata      snapshot.MetadataStore
	content       snapshot.ContentStore
	canonicalizer snapshot.Canonicalizer
}

func NewSnapshotValueInspectorFromStore(
	metadata snapshot.MetadataStore,
	content snapshot.ContentStore,
	canonicalizer snapshot.Canonicalizer,
) (*SnapshotValueInspectorFromStore, error) {
	if nilInterface(metadata) || nilInterface(content) {
		return nil, fmt.Errorf("publisher snapshot inspector: metadata and content are required")
	}
	return &SnapshotValueInspectorFromStore{metadata: metadata, content: content, canonicalizer: canonicalizer}, nil
}

func (inspector *SnapshotValueInspectorFromStore) InspectValue(ctx context.Context, request Request) (SnapshotValue, error) {
	if ctx == nil {
		return SnapshotValue{}, fmt.Errorf("%w: context is required", ErrInvalidRequest)
	}
	if err := ctx.Err(); err != nil {
		return SnapshotValue{}, err
	}
	if err := request.ValidatePersisted(); err != nil {
		return SnapshotValue{}, err
	}
	manifest, found, err := inspector.metadata.GetAuthorized(ctx, request.Authority.TeamID, request.Input.ID)
	if err != nil {
		return SnapshotValue{}, fmt.Errorf("publisher snapshot inspector: authorize snapshot value: %w", err)
	}
	if !found {
		return SnapshotValue{}, fmt.Errorf("%w: publication input snapshot", snapshot.ErrNotFound)
	}
	if err := manifest.Validate(); err != nil {
		return SnapshotValue{}, fmt.Errorf("publisher snapshot inspector: invalid persisted snapshot value: %w", err)
	}
	if manifest.ID != request.Input.ID || manifest.Type != request.Input.Type || manifest.Digest != request.Input.Digest {
		return SnapshotValue{}, fmt.Errorf("publisher snapshot inspector: authorized snapshot does not match the exact requested value")
	}
	if manifest.ContentState != snapshot.ContentStateAvailable {
		return SnapshotValue{}, fmt.Errorf("%w: publication input snapshot is %s", snapshot.ErrContentUnavailable, manifest.ContentState)
	}
	reader, err := inspector.content.Open(ctx, manifest)
	if err != nil || reader == nil {
		return SnapshotValue{}, fmt.Errorf("%w: open publication input snapshot", snapshot.ErrContentUnavailable)
	}
	tree, captureErr := inspector.canonicalizer.Capture(ctx, reader)
	closeErr := reader.Close()
	if err := errors.Join(captureErr, closeErr); err != nil {
		if tree != nil {
			_ = tree.Close()
		}
		return SnapshotValue{}, fmt.Errorf("publisher snapshot inspector: capture publication input snapshot: %w", err)
	}
	if tree == nil {
		return SnapshotValue{}, fmt.Errorf("publisher snapshot inspector: publication input canonicalizer returned no value")
	}
	if tree.Digest != manifest.Digest || tree.ByteSize != manifest.ByteSize || tree.FileCount != manifest.FileCount {
		_ = tree.Close()
		return SnapshotValue{}, fmt.Errorf("publisher snapshot inspector: publication input content does not match its sealed manifest")
	}
	return SnapshotValue{CanonicalArchivePath: tree.ArchivePath, close: tree.Close}, nil
}

// NewSnapshotValueInspector retains the neutral constructor used by existing
// callers while returning the extracted store-backed implementation.
func NewSnapshotValueInspector(
	metadata snapshot.MetadataStore,
	content snapshot.ContentStore,
	canonicalizer snapshot.Canonicalizer,
) (*SnapshotValueInspectorFromStore, error) {
	return NewSnapshotValueInspectorFromStore(metadata, content, canonicalizer)
}

// SnapshotChangeInspector authorizes the exact snapshot for the persisted
// team, re-hashes its canonical bytes, and checks that record.json and its
// payload still agree with the sealed intrinsic metadata.
type SnapshotChangeInspector struct {
	metadata      snapshot.MetadataStore
	content       snapshot.ContentStore
	canonicalizer snapshot.Canonicalizer
}

func NewSnapshotChangeInspector(
	metadata snapshot.MetadataStore,
	content snapshot.ContentStore,
	canonicalizer snapshot.Canonicalizer,
) (*SnapshotChangeInspector, error) {
	if nilInterface(metadata) || nilInterface(content) {
		return nil, fmt.Errorf("publisher snapshot inspector: metadata and content are required")
	}
	return &SnapshotChangeInspector{metadata: metadata, content: content, canonicalizer: canonicalizer}, nil
}

func (inspector *SnapshotChangeInspector) Inspect(ctx context.Context, request Request) (RepositoryChange, error) {
	if ctx == nil {
		return RepositoryChange{}, fmt.Errorf("%w: context is required", ErrInvalidRequest)
	}
	if err := ctx.Err(); err != nil {
		return RepositoryChange{}, err
	}
	if err := request.ValidatePersisted(); err != nil {
		return RepositoryChange{}, err
	}
	if request.Publisher != GitPublisher || request.Input.Type != snapshot.TypeRef("repository-change/v1") {
		return RepositoryChange{}, fmt.Errorf("%w: snapshot change inspector requires repository-change/v1", ErrInvalidRequest)
	}
	return inspector.inspectExactRepositoryChange(ctx, request.Authority.TeamID, request.Input)
}

// InspectExactPRCandidate reopens one exact repository-change/v1 value using
// the team authority already carried by a durable workflow run. It is a
// read-only lookup: it asserts nothing about publication authority, so a
// mutation path must derive its own authorization before writing.
func (inspector *SnapshotChangeInspector) InspectExactPRCandidate(
	ctx context.Context,
	teamID int,
	reference snapshot.SnapshotRef,
) (RepositoryChange, error) {
	if ctx == nil {
		return RepositoryChange{}, fmt.Errorf("%w: context is required", ErrInvalidRequest)
	}
	if err := ctx.Err(); err != nil {
		return RepositoryChange{}, err
	}
	if teamID <= 0 {
		return RepositoryChange{}, fmt.Errorf("%w: team is required", ErrInvalidRequest)
	}
	if err := reference.Validate(); err != nil {
		return RepositoryChange{}, fmt.Errorf("%w: candidate snapshot: %v", ErrInvalidRequest, err)
	}
	if reference.Type != snapshot.TypeRef("repository-change/v1") {
		return RepositoryChange{}, fmt.Errorf("%w: snapshot change inspector requires repository-change/v1", ErrInvalidRequest)
	}
	return inspector.inspectExactRepositoryChange(ctx, teamID, reference)
}

func (inspector *SnapshotChangeInspector) inspectExactRepositoryChange(
	ctx context.Context,
	teamID int,
	reference snapshot.SnapshotRef,
) (RepositoryChange, error) {
	if reference.Type != snapshot.TypeRef("repository-change/v1") {
		return RepositoryChange{}, fmt.Errorf("%w: snapshot change inspector requires repository-change/v1", ErrInvalidRequest)
	}
	manifest, found, err := inspector.metadata.GetAuthorized(ctx, teamID, reference.ID)
	if err != nil {
		return RepositoryChange{}, fmt.Errorf("publisher snapshot inspector: authorize repository change: %w", err)
	}
	if !found {
		return RepositoryChange{}, fmt.Errorf("%w: repository change snapshot", snapshot.ErrNotFound)
	}
	if err := manifest.Validate(); err != nil {
		return RepositoryChange{}, fmt.Errorf("publisher snapshot inspector: invalid persisted repository change: %w", err)
	}
	if manifest.ID != reference.ID || manifest.Type != reference.Type || manifest.Digest != reference.Digest {
		return RepositoryChange{}, fmt.Errorf("publisher snapshot inspector: authorized snapshot does not match the exact requested reference")
	}
	if manifest.ContentState != snapshot.ContentStateAvailable {
		return RepositoryChange{}, fmt.Errorf("%w: repository change snapshot is %s", snapshot.ErrContentUnavailable, manifest.ContentState)
	}
	metadata, err := decodeRepositoryChangeMetadata(manifest.IntrinsicMetadata)
	if err != nil {
		return RepositoryChange{}, fmt.Errorf("publisher snapshot inspector: repository-change intrinsic metadata: %w", err)
	}
	reader, err := inspector.content.Open(ctx, manifest)
	if err != nil {
		return RepositoryChange{}, fmt.Errorf("%w: open repository change: %v", snapshot.ErrContentUnavailable, err)
	}
	if reader == nil {
		return RepositoryChange{}, fmt.Errorf("%w: repository change returned no content", snapshot.ErrContentUnavailable)
	}
	tree, captureErr := inspector.canonicalizer.Capture(ctx, reader)
	closeErr := reader.Close()
	if err := errors.Join(captureErr, closeErr); err != nil {
		if tree != nil {
			_ = tree.Close()
		}
		return RepositoryChange{}, fmt.Errorf("publisher snapshot inspector: capture repository change: %w", err)
	}
	if tree.Digest != manifest.Digest || tree.ByteSize != manifest.ByteSize || tree.FileCount != manifest.FileCount {
		_ = tree.Close()
		return RepositoryChange{}, fmt.Errorf("publisher snapshot inspector: repository change content does not match its sealed manifest")
	}
	record, err := inspectRepositoryChangeRecord(ctx, tree.Root, manifest.ByteSize)
	if err != nil {
		_ = tree.Close()
		return RepositoryChange{}, err
	}
	document := record.Body
	if document.RepositoryID != metadata.RepositoryID || document.BaseSHA != metadata.BaseSHA ||
		document.ResultCommit != metadata.ResultCommit || document.ResultTree != metadata.ResultTree ||
		document.Representation != metadata.Representation {
		_ = tree.Close()
		return RepositoryChange{}, fmt.Errorf("publisher snapshot inspector: repository-change intrinsic metadata does not match exact content")
	}
	if metadata.ResultCommit == "" {
		_ = tree.Close()
		return RepositoryChange{}, fmt.Errorf("publisher snapshot inspector: repository change has no publishable result_commit")
	}
	return RepositoryChange{
		BaseSHA: metadata.BaseSHA, ResultSHA: metadata.ResultCommit,
		MaterializedRoot: tree.Root, CanonicalArchivePath: tree.ArchivePath,
		close: tree.Close,
	}, nil
}

// decodeRepositoryChangeMetadata reads the sealed intrinsic metadata and then
// applies the semantic rules publication needs. The shape half is delegated so
// snapshots sealed before the intrinsic-metadata rename stay readable; every
// rule below still runs over the normalized current-shape values.
func decodeRepositoryChangeMetadata(raw json.RawMessage) (contracts.RepositoryChangeMetadata, error) {
	metadata, err := contracts.DecodeRepositoryChangeMetadata(raw)
	if err != nil {
		return contracts.RepositoryChangeMetadata{}, err
	}
	if _, err := snapshot.ParseDigest(metadata.RepositoryID); err != nil {
		return contracts.RepositoryChangeMetadata{}, err
	}
	if !validGitObjectID(metadata.BaseSHA) || !validGitObjectID(metadata.ResultTree) ||
		(metadata.ResultCommit != "" && !validGitObjectID(metadata.ResultCommit)) {
		return contracts.RepositoryChangeMetadata{}, fmt.Errorf("repository object identity is invalid")
	}
	if metadata.Representation != "git-tree" && metadata.Representation != "patch" && metadata.Representation != "git-bundle" {
		return contracts.RepositoryChangeMetadata{}, fmt.Errorf("representation is invalid")
	}
	if !sort.StringsAreSorted(metadata.ChangedFiles) {
		return contracts.RepositoryChangeMetadata{}, fmt.Errorf("changed_files must be sorted")
	}
	for index, name := range metadata.ChangedFiles {
		if strings.TrimSpace(name) == "" || path.IsAbs(name) || path.Clean(name) != name || strings.HasPrefix(name, "../") {
			return contracts.RepositoryChangeMetadata{}, fmt.Errorf("changed_files[%d] is invalid", index)
		}
		if index > 0 && metadata.ChangedFiles[index-1] == name {
			return contracts.RepositoryChangeMetadata{}, fmt.Errorf("changed_files contains duplicate %q", name)
		}
	}
	return metadata, nil
}

func inspectRepositoryChangeRecord(ctx context.Context, rootPath string, maximumPayloadBytes int64) (contracts.Record[contracts.RepositoryChangeBody], error) {
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return contracts.Record[contracts.RepositoryChangeBody]{}, fmt.Errorf("publisher snapshot inspector: anchor repository change: %w", err)
	}
	defer root.Close()
	record, err := contracts.ReadSealedRepositoryChangeRecord(ctx, root)
	if err != nil {
		return contracts.Record[contracts.RepositoryChangeBody]{}, fmt.Errorf("publisher snapshot inspector: validate repository-change record: %w", err)
	}
	document := record.Body
	payloadInfo, err := root.Lstat(document.Payload.Path)
	if err != nil || !payloadInfo.Mode().IsRegular() || payloadInfo.Size() > maximumPayloadBytes {
		return contracts.Record[contracts.RepositoryChangeBody]{}, fmt.Errorf("publisher snapshot inspector: repository change payload must be a bounded regular file")
	}
	payload, err := root.Open(document.Payload.Path)
	if err != nil {
		return contracts.Record[contracts.RepositoryChangeBody]{}, fmt.Errorf("publisher snapshot inspector: open repository change payload: %w", err)
	}
	hash := sha256.New()
	copied, copyErr := copyWithContext(ctx, hash, io.LimitReader(payload, maximumPayloadBytes+1))
	closeErr := payload.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return contracts.Record[contracts.RepositoryChangeBody]{}, fmt.Errorf("publisher snapshot inspector: hash repository change payload: %w", err)
	}
	if copied > maximumPayloadBytes {
		return contracts.Record[contracts.RepositoryChangeBody]{}, fmt.Errorf("publisher snapshot inspector: repository change payload exceeds its sealed snapshot")
	}
	digest := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	if digest != document.Payload.Digest.String() {
		return contracts.Record[contracts.RepositoryChangeBody]{}, fmt.Errorf("publisher snapshot inspector: repository change payload digest does not match record.json")
	}
	return record, nil
}

func validGitObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}

func copyWithContext(ctx context.Context, destination io.Writer, source io.Reader) (int64, error) {
	buffer := make([]byte, 32<<10)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		read, readErr := source.Read(buffer)
		if read > 0 {
			written, writeErr := destination.Write(buffer[:read])
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
			if written != read {
				return total, io.ErrShortWrite
			}
		}
		if errors.Is(readErr, io.EOF) {
			return total, nil
		}
		if readErr != nil {
			return total, readErr
		}
	}
}
