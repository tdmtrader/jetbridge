package projection

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/concourse/concourse/agent/gitcheck"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

const (
	maxRepositoryChangeDocumentBytes int64 = 1 << 20
	maxRepositoryChangePayloadBytes  int64 = 10 << 30
	maxProjectionErrorBytes                = 4096
)

type RepositoryChangeProjectionStatus string

const (
	RepositoryChangeProjectionPending     RepositoryChangeProjectionStatus = "pending"
	RepositoryChangeProjectionReady       RepositoryChangeProjectionStatus = "ready"
	RepositoryChangeProjectionUnavailable RepositoryChangeProjectionStatus = "unavailable"
	RepositoryChangeProjectionInvalid     RepositoryChangeProjectionStatus = "invalid"
)

// RepositoryChangeInput is the immutable subject snapshot and its exact
// repository/v1 base input, materialized into private scratch directories.
// The projector may modify BaseRoot while applying a patch or bundle.
type RepositoryChangeInput struct {
	SnapshotID    snapshot.SnapshotID
	ChangeRoot    string
	BaseRoot      string
	BaseMetadata  contracts.RepositoryMetadata
	Canonicalizer snapshot.Canonicalizer
}

// RepositoryChange is the durable repository-change/v1 read model. The
// canonical snapshot remains the source of truth; this value is deterministic
// and can be discarded and rebuilt from those immutable bytes.
type RepositoryChange struct {
	SnapshotID       snapshot.SnapshotID              `json:"snapshot_id"`
	Status           RepositoryChangeProjectionStatus `json:"status"`
	ProjectionError  string                           `json:"projection_error,omitempty"`
	RepositoryID     string                           `json:"repository_id"`
	BaseSHA          string                           `json:"base_sha"`
	ResultSHA        string                           `json:"result_sha,omitempty"`
	ResultTreeSHA    string                           `json:"result_tree_sha"`
	Representation   string                           `json:"representation"`
	Files            []gitcheck.ChangedFile           `json:"files"`
	FileCount        int                              `json:"file_count"`
	LinesAdded       int                              `json:"lines_added"`
	LinesDeleted     int                              `json:"lines_deleted"`
	UnifiedDiff      string                           `json:"unified_diff"`
	Truncated        bool                             `json:"truncated"`
	TruncationReason string                           `json:"truncation_reason,omitempty"`
}

// RepositoryChangeBaseInput is one distinct immutable lineage value made
// available to a production of the subject snapshot. The document's
// base_input selects by Port after the subject bytes have been revalidated.
type RepositoryChangeBaseInput struct {
	Port     string
	Snapshot snapshot.Snapshot
}

type RepositoryChangeProjectionInput struct {
	Snapshot snapshot.Snapshot
	Inputs   []RepositoryChangeBaseInput
}

type RepositoryChangeStore interface {
	FindRepositoryChangeInput(context.Context, snapshot.SnapshotID) (RepositoryChangeProjectionInput, bool, error)
	ListUnprojectedRepositoryChanges(context.Context, int) ([]snapshot.SnapshotRef, error)
	SetRepositoryChangeProjectionStatus(context.Context, snapshot.SnapshotID, RepositoryChangeProjectionStatus, string) error
	UpsertRepositoryChangeProjection(context.Context, RepositoryChange) error
}

type RepositoryChangeContent interface {
	Open(context.Context, snapshot.Snapshot) (io.ReadCloser, error)
}

type RepositoryChangeProjector struct {
	store         RepositoryChangeStore
	content       RepositoryChangeContent
	canonicalizer snapshot.Canonicalizer
}

type RepositoryChangeProjectorOption func(*RepositoryChangeProjector) error

func WithRepositoryChangeCanonicalizer(canonicalizer snapshot.Canonicalizer) RepositoryChangeProjectorOption {
	return func(projector *RepositoryChangeProjector) error {
		if err := snapshot.ValidateTempDir(canonicalizer.TempDir); err != nil {
			return err
		}
		projector.canonicalizer = canonicalizer
		return nil
	}
}

func NewRepositoryChangeProjector(
	store RepositoryChangeStore,
	content RepositoryChangeContent,
	options ...RepositoryChangeProjectorOption,
) (*RepositoryChangeProjector, error) {
	if store == nil {
		return nil, fmt.Errorf("projection: repository-change store is required")
	}
	if content == nil {
		return nil, fmt.Errorf("projection: repository-change content store is required")
	}
	projector := &RepositoryChangeProjector{store: store, content: content}
	for _, option := range options {
		if option == nil {
			return nil, fmt.Errorf("projection: repository-change projector option is required")
		}
		if err := option(projector); err != nil {
			return nil, fmt.Errorf("projection: repository-change projector option: %w", err)
		}
	}
	return projector, nil
}

func (projector *RepositoryChangeProjector) SnapshotType() snapshot.TypeRef {
	return "repository-change/v1"
}

func (projector *RepositoryChangeProjector) Candidates(ctx context.Context, limit int) ([]snapshot.SnapshotRef, error) {
	return projector.store.ListUnprojectedRepositoryChanges(ctx, limit)
}

func (projector *RepositoryChangeProjector) Project(ctx context.Context, ref snapshot.SnapshotRef) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	if ref.Type != projector.SnapshotType() {
		return fmt.Errorf("%w: repository-change projector requires exact type %q, got %q", snapshot.ErrUnsupportedType, projector.SnapshotType(), ref.Type)
	}
	input, found, err := projector.store.FindRepositoryChangeInput(ctx, ref.ID)
	if err != nil {
		return fmt.Errorf("load repository-change projection input: %w", err)
	}
	if !found {
		return fmt.Errorf("%w: snapshot %s", snapshot.ErrNotFound, ref.ID)
	}
	manifest := input.Snapshot
	if err := manifest.Validate(); err != nil {
		return fmt.Errorf("%w: invalid persisted repository-change manifest: %v", ErrCorruptSnapshot, err)
	}
	if manifest.ID != ref.ID || manifest.Type != ref.Type || manifest.Digest != ref.Digest {
		return fmt.Errorf("%w: persisted repository-change identity does not match sealed reference", ErrCorruptSnapshot)
	}
	if err := projector.store.SetRepositoryChangeProjectionStatus(ctx, manifest.ID, RepositoryChangeProjectionPending, ""); err != nil {
		return fmt.Errorf("mark repository-change projection pending: %w", err)
	}
	subjectTree, err := projector.capture(ctx, manifest, "repository-change")
	if err != nil {
		return projector.recordProjectionFailure(ctx, manifest.ID, err)
	}
	defer subjectTree.Close()
	document, err := readRepositoryChangeDocument(ctx, subjectTree.Root)
	if err != nil {
		return projector.recordProjectionFailure(ctx, manifest.ID, fmt.Errorf("%w: %v", ErrCorruptSnapshot, err))
	}

	baseManifest, err := selectRepositoryChangeBase(input.Inputs, document.BaseInput)
	if err != nil {
		return projector.recordProjectionFailure(ctx, manifest.ID, fmt.Errorf("%w: %v", ErrCorruptSnapshot, err))
	}
	baseTree, err := projector.capture(ctx, baseManifest, "base repository")
	if err != nil {
		return projector.recordProjectionFailure(ctx, manifest.ID, err)
	}
	defer baseTree.Close()
	baseMetadata, err := decodeRepositoryMetadata(baseManifest.IntrinsicMetadata)
	if err != nil {
		return projector.recordProjectionFailure(ctx, manifest.ID, fmt.Errorf("%w: base repository metadata: %v", ErrCorruptSnapshot, err))
	}
	value, err := DeriveRepositoryChange(ctx, RepositoryChangeInput{
		SnapshotID: manifest.ID, ChangeRoot: subjectTree.Root,
		BaseRoot: baseTree.Root, BaseMetadata: baseMetadata, Canonicalizer: projector.canonicalizer,
	})
	if err != nil {
		return projector.recordProjectionFailure(ctx, manifest.ID, fmt.Errorf("%w: %v", ErrCorruptSnapshot, err))
	}
	value.Status = RepositoryChangeProjectionReady
	if err := projector.store.UpsertRepositoryChangeProjection(ctx, value); err != nil {
		return fmt.Errorf("upsert repository-change projection: %w", err)
	}
	return nil
}

func (projector *RepositoryChangeProjector) recordProjectionFailure(ctx context.Context, id snapshot.SnapshotID, projectionErr error) error {
	if errors.Is(projectionErr, context.Canceled) || errors.Is(projectionErr, context.DeadlineExceeded) {
		// Leave the pre-existing pending marker retryable. Recording with the
		// canceled context would fail and cancellation is not corrupt content.
		return projectionErr
	}
	status := RepositoryChangeProjectionInvalid
	if errors.Is(projectionErr, snapshot.ErrContentUnavailable) {
		status = RepositoryChangeProjectionUnavailable
	}
	reason := projectionErr.Error()
	if len(reason) > maxProjectionErrorBytes {
		reason = reason[:maxProjectionErrorBytes]
	}
	if err := projector.store.SetRepositoryChangeProjectionStatus(ctx, id, status, reason); err != nil {
		return errors.Join(projectionErr, fmt.Errorf("record repository-change projection status: %w", err))
	}
	return projectionErr
}

func (projector *RepositoryChangeProjector) capture(ctx context.Context, manifest snapshot.Snapshot, label string) (*snapshot.CapturedTree, error) {
	if manifest.ContentState != snapshot.ContentStateAvailable {
		return nil, fmt.Errorf("%w: %s snapshot %s is %s", snapshot.ErrContentUnavailable, label, manifest.ID, manifest.ContentState)
	}
	reader, err := projector.content.Open(ctx, manifest)
	if err != nil {
		if reader != nil {
			_ = reader.Close()
		}
		return nil, fmt.Errorf("%w: open %s snapshot %s: %v", snapshot.ErrContentUnavailable, label, manifest.ID, err)
	}
	if reader == nil {
		return nil, fmt.Errorf("%w: %s snapshot %s returned no content", snapshot.ErrContentUnavailable, label, manifest.ID)
	}
	tree, captureErr := projector.canonicalizer.Capture(ctx, reader)
	closeErr := reader.Close()
	if err := errors.Join(captureErr, closeErr); err != nil {
		if tree != nil {
			_ = tree.Close()
		}
		return nil, fmt.Errorf("%w: capture %s snapshot %s: %v", ErrCorruptSnapshot, label, manifest.ID, err)
	}
	if tree.Digest != manifest.Digest || tree.ByteSize != manifest.ByteSize || tree.FileCount != manifest.FileCount {
		_ = tree.Close()
		return nil, fmt.Errorf("%w: %s snapshot %s bytes do not match manifest", ErrCorruptSnapshot, label, manifest.ID)
	}
	return tree, nil
}

func selectRepositoryChangeBase(inputs []RepositoryChangeBaseInput, port string) (snapshot.Snapshot, error) {
	unique := make(map[snapshot.SnapshotID]snapshot.Snapshot)
	for _, input := range inputs {
		if input.Port != port {
			continue
		}
		if input.Snapshot.Type != "repository/v1" {
			return snapshot.Snapshot{}, fmt.Errorf("base_input %q is not repository/v1", port)
		}
		if existing, found := unique[input.Snapshot.ID]; found {
			if existing.Digest != input.Snapshot.Digest {
				return snapshot.Snapshot{}, fmt.Errorf("base_input %q has conflicting snapshot identities", port)
			}
			continue
		}
		unique[input.Snapshot.ID] = input.Snapshot
	}
	if len(unique) == 0 {
		return snapshot.Snapshot{}, fmt.Errorf("base_input %q has no exact persisted lineage", port)
	}
	if len(unique) != 1 {
		return snapshot.Snapshot{}, fmt.Errorf("base_input %q is ambiguous across subject productions", port)
	}
	for _, manifest := range unique {
		if err := manifest.Validate(); err != nil {
			return snapshot.Snapshot{}, fmt.Errorf("invalid base manifest: %w", err)
		}
		return manifest, nil
	}
	panic("unreachable")
}

func decodeRepositoryMetadata(raw json.RawMessage) (contracts.RepositoryMetadata, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var metadata contracts.RepositoryMetadata
	if err := decoder.Decode(&metadata); err != nil {
		return contracts.RepositoryMetadata{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return contracts.RepositoryMetadata{}, fmt.Errorf("metadata contains trailing JSON")
	}
	if metadata.RepositoryID == "" || metadata.ObjectFormat == "" || metadata.HeadSHA == "" || metadata.TreeSHA == "" || len(metadata.RootCommits) == 0 {
		return contracts.RepositoryMetadata{}, fmt.Errorf("required repository identity fields are missing")
	}
	return metadata, nil
}

// DeriveRepositoryChange verifies the sealed document against its exact base
// lineage and derives a bounded diff without resolving a remote or executing
// repository-controlled hooks, filters, attributes, or helpers.
func DeriveRepositoryChange(ctx context.Context, input RepositoryChangeInput) (RepositoryChange, error) {
	if ctx == nil {
		return RepositoryChange{}, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return RepositoryChange{}, err
	}
	if input.SnapshotID <= 0 {
		return RepositoryChange{}, fmt.Errorf("repository-change projection: snapshot ID is required")
	}
	if input.ChangeRoot == "" || input.BaseRoot == "" {
		return RepositoryChange{}, fmt.Errorf("repository-change projection: change and base roots are required")
	}

	document, err := readRepositoryChangeDocument(ctx, input.ChangeRoot)
	if err != nil {
		return RepositoryChange{}, err
	}
	if document.RepositoryID != input.BaseMetadata.RepositoryID {
		return RepositoryChange{}, fmt.Errorf("repository-change projection: repository_id does not match base repository")
	}
	if document.BaseSHA != input.BaseMetadata.HeadSHA {
		return RepositoryChange{}, fmt.Errorf("repository-change projection: base_sha does not match base repository HEAD")
	}
	if err := verifyBaseRepository(ctx, input.BaseRoot, input.BaseMetadata); err != nil {
		return RepositoryChange{}, err
	}

	payload, err := spoolRepositoryChangePayload(ctx, input.ChangeRoot, document.PayloadPath, input.Canonicalizer.TempDir)
	if err != nil {
		return RepositoryChange{}, fmt.Errorf("repository-change projection: payload: %w", err)
	}
	defer payload.Close()
	if payload.digest != document.PayloadDigest {
		return RepositoryChange{}, fmt.Errorf("repository-change projection: payload_digest does not match exact payload bytes")
	}

	var diff gitcheck.RepositoryDiff
	switch document.Representation {
	case "git-tree":
		diff, err = deriveGitTreeChange(ctx, payload.path, document, input.BaseMetadata, input.Canonicalizer)
	case "patch":
		diff, err = derivePatchChange(ctx, input.BaseRoot, payload.path, document)
	case "bundle":
		diff, err = deriveBundleChange(ctx, input.BaseRoot, payload.path, document)
	default:
		err = fmt.Errorf("unsupported representation %q", document.Representation)
	}
	if err != nil {
		return RepositoryChange{}, fmt.Errorf("repository-change projection: derive %s: %w", document.Representation, err)
	}

	return RepositoryChange{
		SnapshotID: input.SnapshotID, Status: RepositoryChangeProjectionReady, RepositoryID: document.RepositoryID,
		BaseSHA: document.BaseSHA, ResultSHA: document.ResultSHA,
		ResultTreeSHA: document.ResultTreeSHA, Representation: document.Representation,
		Files: diff.Files, FileCount: diff.FileCount, LinesAdded: diff.LinesAdded,
		LinesDeleted: diff.LinesDeleted, UnifiedDiff: diff.UnifiedDiff,
		Truncated: diff.Truncated, TruncationReason: diff.TruncationReason,
	}, nil
}

func readRepositoryChangeDocument(ctx context.Context, rootPath string) (contracts.RepositoryChangeDocument, error) {
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return contracts.RepositoryChangeDocument{}, fmt.Errorf("repository-change projection: anchor subject snapshot: %w", err)
	}
	defer root.Close()
	info, err := root.Lstat("change.json")
	if err != nil {
		return contracts.RepositoryChangeDocument{}, fmt.Errorf("repository-change projection: read change.json: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > maxRepositoryChangeDocumentBytes {
		return contracts.RepositoryChangeDocument{}, fmt.Errorf("repository-change projection: change.json must be a bounded regular file")
	}
	file, err := root.Open("change.json")
	if err != nil {
		return contracts.RepositoryChangeDocument{}, fmt.Errorf("repository-change projection: open change.json: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(contextProjectionReader{ctx: ctx, reader: file}, maxRepositoryChangeDocumentBytes+1))
	decoder.DisallowUnknownFields()
	var document contracts.RepositoryChangeDocument
	if err := decoder.Decode(&document); err != nil {
		return contracts.RepositoryChangeDocument{}, fmt.Errorf("repository-change projection: decode change.json: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = fmt.Errorf("multiple JSON values")
		}
		return contracts.RepositoryChangeDocument{}, fmt.Errorf("repository-change projection: decode change.json: %w", err)
	}
	if err := document.Validate(); err != nil {
		return contracts.RepositoryChangeDocument{}, fmt.Errorf("repository-change projection: change.json: %w", err)
	}
	return document, nil
}

func verifyBaseRepository(ctx context.Context, directory string, metadata contracts.RepositoryMetadata) error {
	runner := projectionGit{dir: directory}
	actualHead, err := runner.run(ctx, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || actualHead != metadata.HeadSHA {
		return fmt.Errorf("repository-change projection: materialized base HEAD does not match sealed base metadata")
	}
	actualTree, err := runner.run(ctx, "rev-parse", "--verify", "HEAD^{tree}")
	if err != nil || actualTree != metadata.TreeSHA {
		return fmt.Errorf("repository-change projection: materialized base tree does not match sealed base metadata")
	}
	objectFormat, err := runner.run(ctx, "rev-parse", "--show-object-format=storage")
	if err != nil || objectFormat != metadata.ObjectFormat {
		return fmt.Errorf("repository-change projection: materialized base object format does not match sealed base metadata")
	}
	return nil
}

func deriveGitTreeChange(
	ctx context.Context,
	payloadPath string,
	document contracts.RepositoryChangeDocument,
	base contracts.RepositoryMetadata,
	canonicalizer snapshot.Canonicalizer,
) (gitcheck.RepositoryDiff, error) {
	payload, err := os.Open(payloadPath)
	if err != nil {
		return gitcheck.RepositoryDiff{}, err
	}
	tree, captureErr := canonicalizer.Capture(ctx, payload)
	closeErr := payload.Close()
	if err := errors.Join(captureErr, closeErr); err != nil {
		if tree != nil {
			_ = tree.Close()
		}
		return gitcheck.RepositoryDiff{}, fmt.Errorf("git-tree payload is not a canonical repository tar: %w", err)
	}
	defer tree.Close()
	if tree.Digest.String() != document.PayloadDigest {
		return gitcheck.RepositoryDiff{}, fmt.Errorf("git-tree payload is not the canonical tar named by payload_digest")
	}
	runner := projectionGit{dir: tree.Root}
	if result, err := runner.run(ctx, "rev-parse", "--verify", "HEAD^{commit}"); err != nil || result != document.ResultSHA {
		return gitcheck.RepositoryDiff{}, fmt.Errorf("git-tree HEAD does not match result_sha")
	}
	if resultTree, err := runner.run(ctx, "rev-parse", "--verify", document.ResultSHA+"^{tree}"); err != nil || resultTree != document.ResultTreeSHA {
		return gitcheck.RepositoryDiff{}, fmt.Errorf("git-tree result does not match result_tree_sha")
	}
	if objectFormat, err := runner.run(ctx, "rev-parse", "--show-object-format=storage"); err != nil || objectFormat != base.ObjectFormat {
		return gitcheck.RepositoryDiff{}, fmt.Errorf("git-tree object format differs from base")
	}
	if _, err := runner.run(ctx, "merge-base", "--is-ancestor", document.BaseSHA, document.ResultSHA); err != nil {
		return gitcheck.RepositoryDiff{}, fmt.Errorf("result_sha does not descend from base_sha: %w", err)
	}
	return gitcheck.DeriveRepositoryDiff(ctx, tree.Root, document.BaseSHA, document.ResultSHA)
}

func derivePatchChange(ctx context.Context, baseDirectory, payloadPath string, document contracts.RepositoryChangeDocument) (gitcheck.RepositoryDiff, error) {
	runner := projectionGit{dir: baseDirectory}
	if _, err := runner.run(ctx, "update-index", "--refresh"); err != nil {
		return gitcheck.RepositoryDiff{}, fmt.Errorf("refresh scratch repository index: %w", err)
	}
	if _, err := runner.run(ctx, "apply", "--check", "--index", "--whitespace=nowarn", payloadPath); err != nil {
		return gitcheck.RepositoryDiff{}, fmt.Errorf("patch failed git apply --check --index: %w", err)
	}
	if _, err := runner.run(ctx, "apply", "--index", "--whitespace=nowarn", payloadPath); err != nil {
		return gitcheck.RepositoryDiff{}, fmt.Errorf("apply patch: %w", err)
	}
	resultTree, err := runner.run(ctx, "write-tree")
	if err != nil {
		return gitcheck.RepositoryDiff{}, fmt.Errorf("calculate patched result tree: %w", err)
	}
	if resultTree != document.ResultTreeSHA {
		return gitcheck.RepositoryDiff{}, fmt.Errorf("result_tree_sha does not match applied patch")
	}
	return gitcheck.DeriveRepositoryDiff(ctx, baseDirectory, document.BaseSHA, resultTree)
}

func deriveBundleChange(ctx context.Context, baseDirectory, payloadPath string, document contracts.RepositoryChangeDocument) (gitcheck.RepositoryDiff, error) {
	runner := projectionGit{dir: baseDirectory}
	if _, err := runner.run(ctx, "bundle", "verify", payloadPath); err != nil {
		return gitcheck.RepositoryDiff{}, fmt.Errorf("bundle verify: %w", err)
	}
	heads, err := runner.run(ctx, "bundle", "list-heads", payloadPath)
	if err != nil {
		return gitcheck.RepositoryDiff{}, fmt.Errorf("list bundle heads: %w", err)
	}
	lines := make([][]string, 0, 1)
	for _, line := range strings.Split(heads, "\n") {
		if fields := strings.Fields(line); len(fields) >= 2 {
			lines = append(lines, fields)
		}
	}
	if len(lines) != 1 || lines[0][0] != document.ResultSHA {
		return gitcheck.RepositoryDiff{}, fmt.Errorf("bundle must expose exactly result_sha")
	}
	if _, err := runner.run(ctx, "bundle", "unbundle", payloadPath); err != nil {
		return gitcheck.RepositoryDiff{}, fmt.Errorf("unbundle: %w", err)
	}
	resultTree, err := runner.run(ctx, "rev-parse", "--verify", document.ResultSHA+"^{tree}")
	if err != nil || resultTree != document.ResultTreeSHA {
		return gitcheck.RepositoryDiff{}, fmt.Errorf("bundled result does not match result_tree_sha")
	}
	if _, err := runner.run(ctx, "merge-base", "--is-ancestor", document.BaseSHA, document.ResultSHA); err != nil {
		return gitcheck.RepositoryDiff{}, fmt.Errorf("result_sha does not descend from base_sha: %w", err)
	}
	return gitcheck.DeriveRepositoryDiff(ctx, baseDirectory, document.BaseSHA, document.ResultSHA)
}

type repositoryChangePayload struct {
	directory string
	path      string
	digest    string
}

func (payload repositoryChangePayload) Close() error { return os.RemoveAll(payload.directory) }

func spoolRepositoryChangePayload(ctx context.Context, rootPath, name, tempDir string) (repositoryChangePayload, error) {
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return repositoryChangePayload{}, err
	}
	defer root.Close()
	info, err := root.Lstat(name)
	if err != nil {
		return repositoryChangePayload{}, err
	}
	if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maxRepositoryChangePayloadBytes {
		return repositoryChangePayload{}, fmt.Errorf("payload must be a bounded regular file")
	}
	source, err := root.Open(name)
	if err != nil {
		return repositoryChangePayload{}, err
	}
	openedInfo, err := source.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		_ = source.Close()
		return repositoryChangePayload{}, fmt.Errorf("payload changed while opening or is not a regular file")
	}
	directory, err := os.MkdirTemp(tempDir, "concourse-repository-projection-")
	if err != nil {
		_ = source.Close()
		return repositoryChangePayload{}, err
	}
	path := filepath.Join(directory, "payload")
	destination, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		_ = source.Close()
		_ = os.RemoveAll(directory)
		return repositoryChangePayload{}, err
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(destination, hash), io.LimitReader(contextProjectionReader{ctx: ctx, reader: source}, maxRepositoryChangePayloadBytes+1))
	closeErr := errors.Join(destination.Close(), source.Close())
	if err := errors.Join(copyErr, closeErr); err != nil {
		_ = os.RemoveAll(directory)
		return repositoryChangePayload{}, err
	}
	if written > maxRepositoryChangePayloadBytes || written != info.Size() {
		_ = os.RemoveAll(directory)
		return repositoryChangePayload{}, fmt.Errorf("payload changed size while reading or exceeds limit")
	}
	return repositoryChangePayload{directory: directory, path: path, digest: "sha256:" + hex.EncodeToString(hash.Sum(nil))}, nil
}

type contextProjectionReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader contextProjectionReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(buffer)
}

type projectionGit struct{ dir string }

func (git projectionGit) run(ctx context.Context, arguments ...string) (string, error) {
	configured := []string{
		"--git-dir=" + filepath.Join(git.dir, ".git"), "--work-tree=" + git.dir,
		"-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsmonitor=false",
		"-c", "core.untrackedCache=false", "-c", "core.autocrlf=false",
		"-c", "core.safecrlf=false", "-c", "core.attributesFile=" + os.DevNull,
		"-c", "core.excludesFile=" + os.DevNull, "-c", "credential.helper=",
		"-c", "protocol.allow=never", "-c", "fetch.recurseSubmodules=false",
		"-c", "submodule.recurse=false", "-c", "filter.lfs.process=",
		"-c", "filter.lfs.smudge=", "-c", "filter.lfs.clean=",
	}
	command := exec.CommandContext(ctx, "git", append(configured, arguments...)...)
	command.Dir = git.dir
	command.Env = projectionGitEnvironment()
	var output boundedProjectionBuffer
	command.Stdout, command.Stderr = &output, &output
	if err := command.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(arguments, " "), err, strings.TrimSpace(output.String()))
	}
	return strings.TrimSpace(output.String()), nil
}

func projectionGitEnvironment() []string {
	environment := make([]string, 0, len(os.Environ())+7)
	for _, variable := range os.Environ() {
		name := strings.SplitN(variable, "=", 2)[0]
		if strings.HasPrefix(name, "GIT_") || name == "LC_ALL" {
			continue
		}
		environment = append(environment, variable)
	}
	return append(environment, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "GIT_NO_REPLACE_OBJECTS=1",
		"GIT_ATTR_NOSYSTEM=1", "LC_ALL=C")
}

type boundedProjectionBuffer struct{ bytes.Buffer }

func (buffer *boundedProjectionBuffer) Write(contents []byte) (int, error) {
	const limit = 8 << 20
	if len(contents) > limit-buffer.Len() {
		remaining := limit - buffer.Len()
		if remaining > 0 {
			_, _ = buffer.Buffer.Write(contents[:remaining])
		}
		return 0, fmt.Errorf("git output exceeds %d-byte limit", limit)
	}
	return buffer.Buffer.Write(contents)
}
