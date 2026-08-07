package directgit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

const (
	maxRepositoryPayloadBytes = int64(10 << 30)
	maxFailureDetailBytes     = 4096
	maxGitConfigBytes         = int64(1 << 20)
	publicationMarkerPrefix   = "refs/concourse/publications/"
)

var (
	ErrUnsupportedMode       = errors.New("direct git: unsupported publication mode")
	ErrAtomicPushUnsupported = errors.New("direct git: remote does not support atomic push")
	ErrStaleLease            = fmt.Errorf("direct git: destination lease is stale: %w", publisher.ErrDestinationChanged)
	ErrNonFastForward        = errors.New("direct git: destination rejected a non-fast-forward update")
	ErrAtomicityViolation    = errors.New("direct git: remote did not preserve the atomic publication")
)

// Backend publishes already-sealed repository-change/v1 values from a fresh
// private bare repository. It never adds a remote to, or pushes from, the
// materialized producer repository.
type Backend struct {
	runner       Runner
	tempParent   trustedTempParent
	canonicalize snapshot.Canonicalizer
}

func New(tempDir string) (*Backend, error) {
	runner, err := NewCommandRunner(tempDir)
	if err != nil {
		return nil, err
	}
	return NewBackend(runner, tempDir)
}

func NewBackend(runner Runner, tempDir string) (*Backend, error) {
	if nilRunner(runner) {
		return nil, fmt.Errorf("direct git: runner is required")
	}
	tempParent, err := newTrustedTempParent(tempDir)
	if err != nil {
		return nil, err
	}
	return &Backend{
		runner: runner, tempParent: tempParent,
	}, nil
}

func (backend *Backend) CurrentBase(
	ctx context.Context,
	credential publisher.Credential,
	_ string,
	targetBranch string,
) (string, error) {
	if err := validateContext(ctx); err != nil {
		return "", err
	}
	remote, err := validateCredential(credential)
	if err != nil {
		return "", err
	}
	targetRef, err := backend.branchRef(ctx, targetBranch)
	if err != nil {
		return "", err
	}
	result, err := backend.runAuthorized(ctx, credential, Command{
		Args:         []string{"ls-remote", "--exit-code", "--refs", remote, targetRef},
		NoRepository: true,
	})
	if err != nil {
		return "", err
	}
	if result.ExitCode != 0 {
		return "", commandFailure("resolve destination base", result, nil)
	}
	objectID, err := exactRemoteRef(result.Stdout, targetRef)
	if err != nil {
		return "", fmt.Errorf("direct git: resolve destination base: %w", err)
	}
	return objectID, nil
}

func (backend *Backend) Lookup(
	ctx context.Context,
	credential publisher.Credential,
	operationKey string,
) (publisher.GitResult, bool, error) {
	if err := validateContext(ctx); err != nil {
		return publisher.GitResult{}, false, err
	}
	marker, err := publicationMarker(operationKey)
	if err != nil {
		return publisher.GitResult{}, false, err
	}
	remote, err := validateCredential(credential)
	if err != nil {
		return publisher.GitResult{}, false, err
	}
	result, err := backend.runAuthorized(ctx, credential, Command{
		Args:         []string{"ls-remote", "--exit-code", "--refs", remote, marker},
		NoRepository: true,
	})
	if err != nil {
		return publisher.GitResult{}, false, err
	}
	if result.ExitCode == 2 {
		return publisher.GitResult{}, false, nil
	}
	if result.ExitCode != 0 {
		return publisher.GitResult{}, false, commandFailure("look up publication marker", result, nil)
	}
	objectID, err := exactRemoteRef(result.Stdout, marker)
	if err != nil {
		return publisher.GitResult{}, false, fmt.Errorf("direct git: look up publication marker: %w", err)
	}
	return publisher.GitResult{
		ExternalID: marker,
		URL:        remote,
		HeadSHA:    objectID,
	}, true, nil
}

func (backend *Backend) Publish(
	ctx context.Context,
	credential publisher.Credential,
	operation publisher.GitOperation,
) (publisher.GitResult, error) {
	if err := validateContext(ctx); err != nil {
		return publisher.GitResult{}, err
	}
	if operation.Mode != publisher.ModeBranch && operation.Mode != publisher.ModeMerge {
		return publisher.GitResult{}, ErrUnsupportedMode
	}
	marker, err := publicationMarker(operation.OperationKey)
	if err != nil {
		return publisher.GitResult{}, err
	}
	if !validObjectID(operation.BaseSHA) || !validObjectID(operation.ResultSHA) ||
		len(operation.BaseSHA) != len(operation.ResultSHA) {
		return publisher.GitResult{}, fmt.Errorf("direct git: repository change object identity is invalid")
	}
	if !validMaterializedRoot(operation.MaterializedRoot) {
		return publisher.GitResult{}, fmt.Errorf("direct git: materialized repository change root is invalid")
	}
	remote, err := validateCredential(credential)
	if err != nil {
		return publisher.GitResult{}, err
	}
	scratch, err := backend.tempParent.createPrivateScratch("concourse-direct-git-publish-")
	if err != nil {
		return publisher.GitResult{}, err
	}
	defer func() { _ = scratch.Close() }()
	if err := scratch.Verify(); err != nil {
		return publisher.GitResult{}, err
	}

	document, resultTree, err := backend.materializeVerifiedChange(ctx, operation, scratch)
	if err != nil {
		return publisher.GitResult{}, err
	}
	defer func() { _ = resultTree.Close() }()

	targetRef, err := backend.publicationRef(ctx, operation)
	if err != nil {
		return publisher.GitResult{}, err
	}
	if err := backend.verifyRepository(ctx, scratch, resultTree, document); err != nil {
		return publisher.GitResult{}, err
	}
	bareRoot, err := backend.importVerifiedObjects(ctx, scratch, resultTree, document)
	if err != nil {
		return publisher.GitResult{}, err
	}

	leaseObjectID := operation.BaseSHA
	if operation.Mode == publisher.ModeBranch {
		leaseObjectID, err = backend.observeRefForLease(
			ctx,
			credential,
			remote,
			targetRef,
			len(operation.ResultSHA),
		)
		if err != nil {
			return publisher.GitResult{}, err
		}
	}
	zeroObjectID := strings.Repeat("0", len(operation.ResultSHA))
	pushArguments := []string{
		"push",
		"--porcelain",
		"--atomic",
		"--force-with-lease=" + targetRef + ":" + leaseObjectID,
		"--force-with-lease=" + marker + ":" + zeroObjectID,
		remote,
		operation.ResultSHA + ":" + targetRef,
		operation.ResultSHA + ":" + marker,
	}
	probeArguments := append([]string(nil), pushArguments[:2]...)
	probeArguments = append(probeArguments, "--dry-run")
	probeArguments = append(probeArguments, pushArguments[2:]...)
	probe, err := backend.runScratchAuthorized(ctx, scratch, credential, Command{Dir: bareRoot, Args: probeArguments})
	if err != nil {
		return publisher.GitResult{}, err
	}
	if probe.ExitCode != 0 {
		return publisher.GitResult{}, classifyPushFailure("probe atomic publication", probe)
	}
	pushed, err := backend.runScratchAuthorized(ctx, scratch, credential, Command{Dir: bareRoot, Args: pushArguments})
	if err != nil {
		return publisher.GitResult{}, err
	}
	if pushed.ExitCode != 0 {
		return publisher.GitResult{}, classifyPushFailure("publish repository change", pushed)
	}
	if err := backend.verifyRemotePublication(ctx, credential, remote, targetRef, marker, operation.ResultSHA); err != nil {
		return publisher.GitResult{}, err
	}
	return publisher.GitResult{
		ExternalID: marker,
		URL:        remote,
		HeadSHA:    operation.ResultSHA,
	}, nil
}

func (backend *Backend) materializeVerifiedChange(
	ctx context.Context,
	operation publisher.GitOperation,
	scratch *privateScratch,
) (contracts.RepositoryChangeBody, *snapshot.CapturedTree, error) {
	root, err := os.OpenRoot(operation.MaterializedRoot)
	if err != nil {
		return contracts.RepositoryChangeBody{}, nil, fmt.Errorf("direct git: anchor repository change: %w", err)
	}
	defer root.Close()
	record, err := contracts.ReadSealedRepositoryChangeRecord(ctx, root)
	if err != nil {
		return contracts.RepositoryChangeBody{}, nil, fmt.Errorf("direct git: validate repository-change/v1: %w", err)
	}
	document := record.Body
	if document.Representation != "git-tree" {
		return contracts.RepositoryChangeBody{}, nil, fmt.Errorf(
			"direct git: repository-change representation %q is unsupported", document.Representation,
		)
	}
	if document.BaseSHA != operation.BaseSHA || document.ResultCommit != operation.ResultSHA {
		return contracts.RepositoryChangeBody{}, nil, fmt.Errorf(
			"direct git: repository-change/v1 does not match the publication operation",
		)
	}
	info, err := root.Lstat(document.Payload.Path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Size() < 0 || info.Size() > maxRepositoryPayloadBytes {
		return contracts.RepositoryChangeBody{}, nil, fmt.Errorf(
			"direct git: repository-change payload must be a bounded regular file",
		)
	}
	payload, err := root.Open(document.Payload.Path)
	if err != nil {
		return contracts.RepositoryChangeBody{}, nil, fmt.Errorf("direct git: open repository-change payload: %w", err)
	}
	tree, captureErr := backend.capturePayload(ctx, scratch, payload, info.Size(), document.Payload.Digest)
	closeErr := payload.Close()
	if err := errors.Join(captureErr, closeErr); err != nil {
		if tree != nil {
			_ = tree.Close()
		}
		return contracts.RepositoryChangeBody{}, nil, fmt.Errorf("direct git: materialize git-tree payload: %w", err)
	}
	if tree == nil || tree.Digest != document.Payload.Digest {
		if tree != nil {
			_ = tree.Close()
		}
		return contracts.RepositoryChangeBody{}, nil, fmt.Errorf(
			"direct git: materialized git-tree payload does not match its sealed digest",
		)
	}
	return document, tree, nil
}

func (backend *Backend) capturePayload(
	ctx context.Context,
	scratch *privateScratch,
	payload io.Reader,
	expectedSize int64,
	expectedDigest snapshot.Digest,
) (*snapshot.CapturedTree, error) {
	if err := scratch.Verify(); err != nil {
		return nil, err
	}
	spool, err := scratch.root.OpenFile("payload.spool", os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, fmt.Errorf("direct git: create repository-change payload spool: %w", err)
	}
	defer func() { _ = scratch.root.Remove("payload.spool") }()
	if err := spool.Chmod(0600); err != nil {
		_ = spool.Close()
		return nil, fmt.Errorf("direct git: protect repository-change payload spool: %w", err)
	}
	hash := sha256.New()
	copied, copyErr := copyWithContext(ctx, io.MultiWriter(spool, hash), payload)
	if copyErr != nil {
		_ = spool.Close()
		return nil, fmt.Errorf("direct git: spool repository-change payload: %w", copyErr)
	}
	if copied != expectedSize {
		_ = spool.Close()
		return nil, fmt.Errorf("direct git: repository-change payload changed while reading")
	}
	actualDigest := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	if actualDigest != expectedDigest.String() {
		_ = spool.Close()
		return nil, fmt.Errorf("direct git: repository-change payload bytes do not match the sealed digest")
	}
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		_ = spool.Close()
		return nil, fmt.Errorf("direct git: rewind repository-change payload: %w", err)
	}
	canonicalize := backend.canonicalize
	canonicalize.TempDir = scratch.path
	tree, captureErr := canonicalize.Capture(ctx, spool)
	closeErr := spool.Close()
	if err := errors.Join(captureErr, closeErr); err != nil {
		if tree != nil {
			_ = tree.Close()
		}
		return nil, err
	}
	if err := scratch.Verify(); err != nil {
		if tree != nil {
			_ = tree.Close()
		}
		return nil, err
	}
	return tree, nil
}

func (backend *Backend) publicationRef(ctx context.Context, operation publisher.GitOperation) (string, error) {
	switch operation.Mode {
	case publisher.ModeBranch:
		if !exactParameters(operation.Parameters, "source_branch", "target_branch") {
			return "", fmt.Errorf("direct git: branch parameters are invalid")
		}
		source, err := backend.branchRef(ctx, operation.Parameters["source_branch"])
		if err != nil {
			return "", err
		}
		target, err := backend.branchRef(ctx, operation.Parameters["target_branch"])
		if err != nil {
			return "", err
		}
		if source == target {
			return "", fmt.Errorf("direct git: branch publication cannot update the protected target branch")
		}
		return source, nil
	case publisher.ModeMerge:
		if !exactParameters(operation.Parameters, "target_branch", publisher.MergeBaseParameter) ||
			operation.Parameters[publisher.MergeBaseParameter] != operation.BaseSHA {
			return "", fmt.Errorf("direct git: direct-trunk parameters are invalid")
		}
		return backend.branchRef(ctx, operation.Parameters["target_branch"])
	default:
		return "", ErrUnsupportedMode
	}
}

func (backend *Backend) branchRef(ctx context.Context, branch string) (string, error) {
	if strings.TrimSpace(branch) != branch || branch == "" || len(branch) > 1024 ||
		strings.IndexByte(branch, 0) >= 0 {
		return "", fmt.Errorf("direct git: branch name is invalid")
	}
	ref := "refs/heads/" + branch
	result, err := backend.runLocal(ctx, Command{Args: []string{"check-ref-format", ref}})
	if err != nil {
		return "", err
	}
	if result.ExitCode != 0 {
		return "", fmt.Errorf("direct git: branch name is invalid")
	}
	return ref, nil
}

func (backend *Backend) verifyRepository(
	ctx context.Context,
	scratch *privateScratch,
	tree *snapshot.CapturedTree,
	document contracts.RepositoryChangeBody,
) error {
	inside, err := backend.sourceOutput(ctx, scratch, tree, "inspect result work tree", "rev-parse", "--is-inside-work-tree")
	if err != nil {
		return err
	}
	if inside != "true" {
		return fmt.Errorf("direct git: git-tree payload is not a work tree")
	}
	format, err := backend.sourceOutput(ctx, scratch, tree, "resolve object format", "rev-parse", "--show-object-format=storage")
	if err != nil {
		return err
	}
	if format != objectFormat(document.ResultCommit) {
		return fmt.Errorf("direct git: result repository object format does not match declared objects")
	}
	for _, check := range []struct {
		label string
		spec  string
		want  string
	}{
		{label: "resolve result HEAD", spec: "HEAD^{commit}", want: document.ResultCommit},
		{label: "resolve base commit", spec: document.BaseSHA + "^{commit}", want: document.BaseSHA},
		{label: "resolve result commit", spec: document.ResultCommit + "^{commit}", want: document.ResultCommit},
		{label: "resolve result tree", spec: document.ResultCommit + "^{tree}", want: document.ResultTree},
	} {
		got, err := backend.sourceOutput(ctx, scratch, tree, check.label, "rev-parse", "--verify", check.spec)
		if err != nil {
			return err
		}
		if got != check.want {
			return fmt.Errorf("direct git: %s does not match repository-change/v1", check.label)
		}
	}
	if err := backend.sourceSuccess(ctx, scratch, tree, "verify result ancestry",
		"merge-base", "--is-ancestor", document.BaseSHA, document.ResultCommit); err != nil {
		return fmt.Errorf("direct git: result commit does not descend from declared base: %w", err)
	}
	if err := backend.sourceSuccess(ctx, scratch, tree, "verify result object database",
		"fsck", "--full", "--strict", "--no-reflogs"); err != nil {
		return err
	}
	return nil
}

func (backend *Backend) importVerifiedObjects(
	ctx context.Context,
	scratch *privateScratch,
	tree *snapshot.CapturedTree,
	document contracts.RepositoryChangeBody,
) (string, error) {
	if err := scratch.Verify(); err != nil {
		return "", err
	}
	if err := scratch.root.Mkdir("repository.git", 0700); err != nil {
		return "", fmt.Errorf("direct git: create private bare repository: %w", err)
	}
	bareRoot := filepath.Join(scratch.path, "repository.git")
	if err := backend.scratchSuccess(ctx, scratch, "", "initialize private bare repository",
		"init", "--bare", "--object-format="+objectFormat(document.ResultCommit), bareRoot); err != nil {
		return "", err
	}
	bundlePath := filepath.Join(scratch.path, "verified.bundle")
	if err := backend.sourceSuccess(ctx, scratch, tree, "package verified result objects",
		"bundle", "create", bundlePath, "HEAD"); err != nil {
		return "", err
	}
	info, err := scratch.root.Lstat("verified.bundle")
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 {
		return "", fmt.Errorf("direct git: verified object bundle is unavailable")
	}
	if err := scratch.root.Chmod("verified.bundle", 0600); err != nil {
		return "", fmt.Errorf("direct git: protect verified object bundle: %w", err)
	}
	if err := backend.scratchSuccess(ctx, scratch, bareRoot, "verify object bundle", "bundle", "verify", bundlePath); err != nil {
		return "", err
	}
	if err := backend.scratchSuccess(ctx, scratch, bareRoot, "import verified objects", "bundle", "unbundle", bundlePath); err != nil {
		return "", err
	}
	for _, check := range []struct {
		label string
		spec  string
		want  string
	}{
		{label: "re-resolve imported base", spec: document.BaseSHA + "^{commit}", want: document.BaseSHA},
		{label: "re-resolve imported result", spec: document.ResultCommit + "^{commit}", want: document.ResultCommit},
		{label: "re-resolve imported tree", spec: document.ResultCommit + "^{tree}", want: document.ResultTree},
	} {
		got, err := backend.scratchOutput(ctx, scratch, bareRoot, check.label, "rev-parse", "--verify", check.spec)
		if err != nil {
			return "", err
		}
		if got != check.want {
			return "", fmt.Errorf("direct git: %s does not match repository-change/v1", check.label)
		}
	}
	if err := backend.scratchSuccess(ctx, scratch, bareRoot, "re-verify imported ancestry",
		"merge-base", "--is-ancestor", document.BaseSHA, document.ResultCommit); err != nil {
		return "", err
	}
	if err := backend.scratchSuccess(ctx, scratch, bareRoot, "verify imported object database",
		"fsck", "--full", "--strict", "--no-reflogs"); err != nil {
		return "", err
	}
	return bareRoot, nil
}

func (backend *Backend) observeRefForLease(
	ctx context.Context,
	credential publisher.Credential,
	remote, ref string,
	objectIDLength int,
) (string, error) {
	result, err := backend.runAuthorized(ctx, credential, Command{
		Args:         []string{"ls-remote", "--exit-code", "--refs", remote, ref},
		NoRepository: true,
	})
	if err != nil {
		return "", err
	}
	if result.ExitCode == 2 {
		return strings.Repeat("0", objectIDLength), nil
	}
	if result.ExitCode != 0 {
		return "", commandFailure("observe branch publication lease", result, nil)
	}
	objectID, err := exactRemoteRef(result.Stdout, ref)
	if err != nil || len(objectID) != objectIDLength {
		return "", fmt.Errorf("direct git: observe branch publication lease: remote returned an invalid ref")
	}
	return objectID, nil
}

func (backend *Backend) verifyRemotePublication(
	ctx context.Context,
	credential publisher.Credential,
	remote, targetRef, marker, resultSHA string,
) error {
	result, err := backend.runAuthorized(ctx, credential, Command{
		Args:         []string{"ls-remote", "--exit-code", "--refs", remote, targetRef, marker},
		NoRepository: true,
	})
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("%w: verification read failed", ErrAtomicityViolation)
	}
	refs, err := exactRemoteRefs(result.Stdout, targetRef, marker)
	if err != nil || refs[targetRef] != resultSHA || refs[marker] != resultSHA {
		return fmt.Errorf("%w: target and marker do not name the exact result commit", ErrAtomicityViolation)
	}
	return nil
}

func (backend *Backend) output(
	ctx context.Context,
	dir, label string,
	arguments ...string,
) (string, error) {
	result, err := backend.runLocal(ctx, Command{Dir: dir, Args: arguments})
	if err != nil {
		return "", err
	}
	if result.ExitCode != 0 {
		return "", commandFailure(label, result, nil)
	}
	output := strings.TrimSuffix(result.Stdout, "\n")
	if output == "" ||
		strings.TrimSpace(output) != output ||
		strings.ContainsAny(output, "\r\n") {
		return "", fmt.Errorf("direct git: %s returned an invalid value", label)
	}
	return output, nil
}

func (backend *Backend) success(
	ctx context.Context,
	dir, label string,
	arguments ...string,
) error {
	result, err := backend.runLocal(ctx, Command{Dir: dir, Args: arguments})
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return commandFailure(label, result, nil)
	}
	return nil
}

func (backend *Backend) sourceOutput(
	ctx context.Context,
	scratch *privateScratch,
	tree *snapshot.CapturedTree,
	label string,
	arguments ...string,
) (string, error) {
	result, err := backend.runSourceLocal(ctx, scratch, tree, Command{Dir: tree.Root, Args: arguments})
	if err != nil {
		return "", err
	}
	if result.ExitCode != 0 {
		return "", commandFailure(label, result, nil)
	}
	output := strings.TrimSuffix(result.Stdout, "\n")
	if output == "" ||
		strings.TrimSpace(output) != output ||
		strings.ContainsAny(output, "\r\n") {
		return "", fmt.Errorf("direct git: %s returned an invalid value", label)
	}
	return output, nil
}

func (backend *Backend) sourceSuccess(
	ctx context.Context,
	scratch *privateScratch,
	tree *snapshot.CapturedTree,
	label string,
	arguments ...string,
) error {
	result, err := backend.runSourceLocal(ctx, scratch, tree, Command{Dir: tree.Root, Args: arguments})
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return commandFailure(label, result, nil)
	}
	return nil
}

func (backend *Backend) scratchOutput(
	ctx context.Context,
	scratch *privateScratch,
	dir, label string,
	arguments ...string,
) (string, error) {
	result, err := backend.runScratchLocal(ctx, scratch, Command{Dir: dir, Args: arguments})
	if err != nil {
		return "", err
	}
	if result.ExitCode != 0 {
		return "", commandFailure(label, result, nil)
	}
	output := strings.TrimSuffix(result.Stdout, "\n")
	if output == "" ||
		strings.TrimSpace(output) != output ||
		strings.ContainsAny(output, "\r\n") {
		return "", fmt.Errorf("direct git: %s returned an invalid value", label)
	}
	return output, nil
}

func (backend *Backend) scratchSuccess(
	ctx context.Context,
	scratch *privateScratch,
	dir, label string,
	arguments ...string,
) error {
	result, err := backend.runScratchLocal(ctx, scratch, Command{Dir: dir, Args: arguments})
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return commandFailure(label, result, nil)
	}
	return nil
}

func (backend *Backend) runSourceLocal(
	ctx context.Context,
	scratch *privateScratch,
	tree *snapshot.CapturedTree,
	command Command,
) (CommandResult, error) {
	if err := verifySourceRepository(scratch, tree); err != nil {
		return CommandResult{}, err
	}
	result, err := backend.runLocal(ctx, command)
	if err != nil {
		return CommandResult{}, err
	}
	if err := verifySourceRepository(scratch, tree); err != nil {
		return CommandResult{}, err
	}
	return result, nil
}

func (backend *Backend) runScratchLocal(
	ctx context.Context,
	scratch *privateScratch,
	command Command,
) (CommandResult, error) {
	if err := scratch.Verify(); err != nil {
		return CommandResult{}, err
	}
	result, err := backend.runLocal(ctx, command)
	if err != nil {
		return CommandResult{}, err
	}
	if err := scratch.Verify(); err != nil {
		return CommandResult{}, err
	}
	return result, nil
}

func (backend *Backend) runScratchAuthorized(
	ctx context.Context,
	scratch *privateScratch,
	credential publisher.Credential,
	command Command,
) (CommandResult, error) {
	if err := scratch.Verify(); err != nil {
		return CommandResult{}, err
	}
	result, err := backend.runAuthorized(ctx, credential, command)
	if err != nil {
		return CommandResult{}, err
	}
	if err := scratch.Verify(); err != nil {
		return CommandResult{}, err
	}
	return result, nil
}

func verifySourceRepository(scratch *privateScratch, tree *snapshot.CapturedTree) error {
	if err := scratch.Verify(); err != nil {
		return err
	}
	root, err := tree.OpenRoot()
	if err != nil {
		return fmt.Errorf("direct git: open sealed source repository: %w", err)
	}
	defer root.Close()
	return rejectGitObjectIndirections(root)
}

func rejectGitObjectIndirections(root *os.Root) error {
	gitInfo, err := root.Lstat(".git")
	if err != nil || !gitInfo.IsDir() || gitInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("direct git: git-tree payload has no contained .git directory")
	}
	gitRoot, err := root.OpenRoot(".git")
	if err != nil {
		return fmt.Errorf("direct git: open contained .git directory: %w", err)
	}
	defer gitRoot.Close()
	if err := rejectGitLazyFetchConfiguration(gitRoot); err != nil {
		return err
	}
	for _, name := range []string{"commondir", "gitdir"} {
		if _, err := gitRoot.Lstat(name); err == nil {
			return fmt.Errorf("direct git: git-tree payload contains external repository indirection %q", name)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("direct git: inspect repository indirection %q: %w", name, err)
		}
	}
	objects, present, err := openContainedDirectory(gitRoot, "objects")
	if err != nil {
		return fmt.Errorf("direct git: inspect object database: %w", err)
	}
	if !present {
		return nil
	}
	defer objects.Close()
	info, present, err := openContainedDirectory(objects, "info")
	if err != nil {
		return fmt.Errorf("direct git: inspect object database info: %w", err)
	}
	if !present {
		return nil
	}
	defer info.Close()
	for _, name := range []string{"alternates", "http-alternates"} {
		if _, err := info.Lstat(name); err == nil {
			return fmt.Errorf("direct git: git-tree payload contains alternate object storage %q", name)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("direct git: inspect alternate object storage %q: %w", name, err)
		}
	}
	return nil
}

func rejectGitLazyFetchConfiguration(gitRoot *os.Root) error {
	if _, err := gitRoot.Lstat("config.worktree"); err == nil {
		return fmt.Errorf("direct git: git-tree payload contains lazy object fetch configuration")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("direct git: inspect worktree repository configuration: %w", err)
	}
	info, err := gitRoot.Lstat("config")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxGitConfigBytes {
		return fmt.Errorf("direct git: git-tree payload has an unsafe repository configuration")
	}
	config, err := gitRoot.Open("config")
	if err != nil {
		return fmt.Errorf("direct git: open repository configuration: %w", err)
	}
	body, readErr := io.ReadAll(io.LimitReader(config, maxGitConfigBytes+1))
	closeErr := config.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return fmt.Errorf("direct git: read repository configuration: %w", err)
	}
	configuration := strings.ToLower(string(body))
	if len(body) > int(maxGitConfigBytes) ||
		strings.Contains(configuration, "partialclone") ||
		strings.Contains(configuration, "promisor") ||
		strings.Contains(configuration, "worktreeconfig") ||
		strings.Contains(configuration, "[include") ||
		strings.Contains(configuration, "include.path") {
		return fmt.Errorf("direct git: git-tree payload contains lazy object fetch configuration")
	}
	return nil
}

func openContainedDirectory(parent *os.Root, name string) (*os.Root, bool, error) {
	info, err := parent.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, false, fmt.Errorf("%q is not a contained directory", name)
	}
	child, err := parent.OpenRoot(name)
	if err != nil {
		return nil, false, err
	}
	opened, statErr := child.Stat(".")
	if statErr != nil || !opened.IsDir() || !os.SameFile(info, opened) {
		_ = child.Close()
		return nil, false, fmt.Errorf("%q changed while opening", name)
	}
	return child, true, nil
}

func (backend *Backend) runLocal(ctx context.Context, command Command) (CommandResult, error) {
	result, err := backend.runner.Run(ctx, command)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return CommandResult{}, contextErr
		}
		return CommandResult{}, fmt.Errorf("direct git: run controlled command: %w", err)
	}
	return result, nil
}

func (backend *Backend) runAuthorized(
	ctx context.Context,
	credential publisher.Credential,
	command Command,
) (CommandResult, error) {
	secret := credential.Secret()
	if len(secret) == 0 {
		return CommandResult{}, fmt.Errorf("direct git: destination credential is unavailable")
	}
	command.Credential = secret
	result, err := backend.runner.Run(ctx, command)
	result.Stdout = redactCommandText(result.Stdout, secret, "")
	result.Stderr = redactCommandText(result.Stderr, secret, "")
	var safeRunnerErr error
	if err != nil {
		safeRunnerErr = errors.New(redactCommandText(err.Error(), secret, ""))
	}
	wipeBytes(secret)
	wipeBytes(command.Credential)
	if safeRunnerErr != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return CommandResult{}, contextErr
		}
		return CommandResult{}, fmt.Errorf("direct git: run authorized command: %w", safeRunnerErr)
	}
	return result, nil
}

func validateContext(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("direct git: context is required")
	}
	return ctx.Err()
}

func validateCredential(credential publisher.Credential) (string, error) {
	if err := credential.Validate(); err != nil {
		return "", err
	}
	if credential.AdapterKind() != publisher.AdapterDirectGit {
		return "", fmt.Errorf("direct git: destination credential is authorized for a different adapter")
	}
	remote := credential.RemoteURL()
	secret := credential.Secret()
	defer wipeBytes(secret)
	if remote == "" || len(secret) == 0 {
		return "", fmt.Errorf("direct git: resolved destination authorization is incomplete")
	}
	parsed, err := url.Parse(remote)
	if err != nil || !parsed.IsAbs() || parsed.Opaque != "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return "", fmt.Errorf("direct git: resolved destination remote is invalid")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https", "ssh":
		if parsed.Hostname() == "" || parsed.Path == "" || parsed.Path == "/" {
			return "", fmt.Errorf("direct git: resolved destination remote is invalid")
		}
	case "file":
		if parsed.Host != "" || !filepath.IsAbs(filepath.FromSlash(parsed.Path)) || parsed.Path == "/" {
			return "", fmt.Errorf("direct git: resolved destination remote is invalid")
		}
	default:
		return "", fmt.Errorf("direct git: resolved destination remote is invalid")
	}
	return remote, nil
}

func publicationMarker(operationKey string) (string, error) {
	if len(operationKey) != len("sha256:")+64 || !strings.HasPrefix(operationKey, "sha256:") {
		return "", fmt.Errorf("direct git: operation key is invalid")
	}
	hexDigest := strings.TrimPrefix(operationKey, "sha256:")
	for _, character := range hexDigest {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return "", fmt.Errorf("direct git: operation key is invalid")
		}
	}
	return publicationMarkerPrefix + hexDigest, nil
}

func exactRemoteRef(output, expectedRef string) (string, error) {
	refs, err := exactRemoteRefs(output, expectedRef)
	if err != nil {
		return "", err
	}
	return refs[expectedRef], nil
}

func exactRemoteRefs(output string, expectedRefs ...string) (map[string]string, error) {
	expected := make(map[string]struct{}, len(expectedRefs))
	for _, ref := range expectedRefs {
		expected[ref] = struct{}{}
	}
	found := make(map[string]string, len(expectedRefs))
	lines := strings.Split(strings.TrimSuffix(output, "\n"), "\n")
	if len(lines) != len(expectedRefs) {
		return nil, fmt.Errorf("remote returned an unexpected number of refs")
	}
	for _, line := range lines {
		fields := strings.Split(line, "\t")
		if len(fields) != 2 || !validObjectID(fields[0]) {
			return nil, fmt.Errorf("remote returned an invalid ref record")
		}
		if _, wanted := expected[fields[1]]; !wanted {
			return nil, fmt.Errorf("remote returned an unexpected ref")
		}
		if _, duplicate := found[fields[1]]; duplicate {
			return nil, fmt.Errorf("remote returned a duplicate ref")
		}
		found[fields[1]] = fields[0]
	}
	return found, nil
}

func validObjectID(value string) bool {
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

func objectFormat(objectID string) string {
	if len(objectID) == 64 {
		return "sha256"
	}
	return "sha1"
}

func validMaterializedRoot(root string) bool {
	return root != "" && filepath.IsAbs(root) && filepath.Clean(root) == root &&
		root != string(filepath.Separator)
}

func exactParameters(parameters map[string]string, names ...string) bool {
	if len(parameters) != len(names) {
		return false
	}
	for _, name := range names {
		if strings.TrimSpace(parameters[name]) == "" {
			return false
		}
	}
	return true
}

func classifyPushFailure(operation string, result CommandResult) error {
	detail := strings.ToLower(result.Stdout + "\n" + result.Stderr)
	switch {
	case strings.Contains(detail, "does not support --atomic") ||
		strings.Contains(detail, "atomic push is not supported") ||
		strings.Contains(detail, "the receiving end does not support atomic push"):
		return errors.Join(ErrAtomicPushUnsupported, commandFailure(operation, result, nil))
	case strings.Contains(detail, "stale info") ||
		strings.Contains(detail, "stale lease"):
		return errors.Join(ErrStaleLease, commandFailure(operation, result, nil))
	case strings.Contains(detail, "non-fast-forward"):
		return errors.Join(ErrNonFastForward, commandFailure(operation, result, nil))
	default:
		return commandFailure(operation, result, nil)
	}
}

func commandFailure(operation string, result CommandResult, secret []byte) error {
	detail := strings.TrimSpace(result.Stderr)
	if detail == "" {
		detail = strings.TrimSpace(result.Stdout)
	}
	detail = redactCommandText(detail, secret, "")
	if len(detail) > maxFailureDetailBytes {
		detail = detail[:maxFailureDetailBytes] + " [truncated]"
	}
	if detail == "" {
		return fmt.Errorf("direct git: %s failed with exit code %d", operation, result.ExitCode)
	}
	return fmt.Errorf("direct git: %s failed with exit code %d: %s", operation, result.ExitCode, detail)
}

func nilRunner(runner Runner) bool {
	if runner == nil {
		return true
	}
	value := reflect.ValueOf(runner)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
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

var _ publisher.GitBackend = (*Backend)(nil)
