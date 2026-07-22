package contracts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/concourse/concourse/agent/snapshot"
)

const maxRepositoryPayloadBytes int64 = 10 << 30

type RepositoryChangeMetadata struct {
	RepositoryID   string `json:"repository_id"`
	BaseSHA        string `json:"base_sha"`
	ResultSHA      string `json:"result_sha,omitempty"`
	ResultTreeSHA  string `json:"result_tree_sha"`
	Representation string `json:"representation"`
}

func (d RepositoryChangeDocument) Validate() error {
	if err := validateDocumentVersion(d.SchemaVersion); err != nil {
		return err
	}
	if err := requireStrings([]namedString{
		{"repository_id", d.RepositoryID}, {"base_input", d.BaseInput},
		{"base_sha", d.BaseSHA}, {"result_tree_sha", d.ResultTreeSHA},
		{"representation", d.Representation}, {"payload_path", d.PayloadPath},
		{"payload_digest", d.PayloadDigest},
	}); err != nil {
		return err
	}
	if _, err := snapshot.ParseDigest(d.RepositoryID); err != nil {
		return fmt.Errorf("repository_id: %w", err)
	}
	if _, err := snapshot.ParseDigest(d.PayloadDigest); err != nil {
		return fmt.Errorf("payload_digest: %w", err)
	}
	objectFormat, err := objectFormatForID(d.BaseSHA)
	if err != nil {
		return fmt.Errorf("base_sha: %w", err)
	}
	if err := validateObjectID(objectFormat, d.ResultTreeSHA); err != nil {
		return fmt.Errorf("result_tree_sha: %w", err)
	}
	if err := validatePOSIXPath("payload_path", d.PayloadPath); err != nil {
		return err
	}
	if d.PayloadPath == "change.json" {
		return fmt.Errorf("payload_path must not name change.json")
	}
	switch d.Representation {
	case "patch":
		if d.ResultSHA != "" {
			return fmt.Errorf("result_sha must be omitted for patch representation because a patch proves only result_tree_sha")
		}
	case "git-tree", "bundle":
		if strings.TrimSpace(d.ResultSHA) == "" {
			return fmt.Errorf("result_sha is required for %s representation", d.Representation)
		}
		if err := validateObjectID(objectFormat, d.ResultSHA); err != nil {
			return fmt.Errorf("result_sha: %w", err)
		}
	default:
		return fmt.Errorf("representation must be one of git-tree, patch, bundle")
	}
	return nil
}

type repositoryChangeValidator struct{}

func (repositoryChangeValidator) Validate(ctx context.Context, root *os.Root, validationContext snapshot.ValidationContext) (snapshot.ValidationResult, error) {
	var document RepositoryChangeDocument
	if err := decodeStrictDocument(ctx, root, "change.json", &document); err != nil {
		return snapshot.ValidationResult{}, err
	}
	if err := document.Validate(); err != nil {
		return snapshot.ValidationResult{}, fmt.Errorf("snapshot contracts: change.json: %w", err)
	}

	baseRef, found := validationContext.Input(document.BaseInput)
	if !found {
		return snapshot.ValidationResult{}, fmt.Errorf("snapshot contracts: base_input %q is not an exact declared input", document.BaseInput)
	}
	if baseRef.Type.String() != "repository/v1" {
		return snapshot.ValidationResult{}, fmt.Errorf("snapshot contracts: base_input %q must have type repository/v1", document.BaseInput)
	}
	baseReader, err := validationContext.OpenInput(ctx, document.BaseInput)
	if err != nil {
		return snapshot.ValidationResult{}, err
	}
	baseTree, captureErr := (snapshot.Canonicalizer{}).Capture(ctx, baseReader)
	closeErr := baseReader.Close()
	if err := errors.Join(captureErr, closeErr); err != nil {
		if baseTree != nil {
			_ = baseTree.Close()
		}
		return snapshot.ValidationResult{}, fmt.Errorf("snapshot contracts: capture base_input %q: %w", document.BaseInput, err)
	}
	defer baseTree.Close()
	if baseTree.Digest != baseRef.Digest {
		return snapshot.ValidationResult{}, fmt.Errorf("snapshot contracts: base_input %q content digest does not match its immutable reference", document.BaseInput)
	}
	baseRoot, err := os.OpenRoot(baseTree.Root)
	if err != nil {
		return snapshot.ValidationResult{}, fmt.Errorf("snapshot contracts: anchor base repository: %w", err)
	}
	baseMetadata, baseErr := validateRepository(ctx, baseRoot, "HEAD")
	baseCloseErr := baseRoot.Close()
	if err := errors.Join(baseErr, baseCloseErr); err != nil {
		return snapshot.ValidationResult{}, fmt.Errorf("snapshot contracts: base_input %q is not repository/v1: %w", document.BaseInput, err)
	}
	if document.RepositoryID != baseMetadata.RepositoryID {
		return snapshot.ValidationResult{}, fmt.Errorf("snapshot contracts: repository_id does not match base repository")
	}
	if document.BaseSHA != baseMetadata.HeadSHA {
		return snapshot.ValidationResult{}, fmt.Errorf("snapshot contracts: base_sha does not match base repository HEAD")
	}
	if objectFormat, _ := objectFormatForID(document.BaseSHA); objectFormat != baseMetadata.ObjectFormat {
		return snapshot.ValidationResult{}, fmt.Errorf("snapshot contracts: declared object IDs do not match base repository object format")
	}

	payload, err := spoolRepositoryPayload(ctx, root, document.PayloadPath)
	if err != nil {
		return snapshot.ValidationResult{}, fmt.Errorf("snapshot contracts: payload_path: %w", err)
	}
	defer payload.Close()
	if payload.digest != document.PayloadDigest {
		return snapshot.ValidationResult{}, fmt.Errorf("snapshot contracts: payload_digest does not match exact payload bytes")
	}

	switch document.Representation {
	case "git-tree":
		err = validateGitTreeChange(ctx, payload.path, document, baseMetadata)
	case "patch":
		err = validatePatchChange(ctx, baseTree.Root, payload.path, document, baseMetadata)
	case "bundle":
		err = validateBundleChange(ctx, baseTree.Root, payload.path, document, baseMetadata)
	}
	if err != nil {
		return snapshot.ValidationResult{}, err
	}
	metadata := RepositoryChangeMetadata{
		RepositoryID: document.RepositoryID, BaseSHA: document.BaseSHA,
		ResultSHA: document.ResultSHA, ResultTreeSHA: document.ResultTreeSHA,
		Representation: document.Representation,
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return snapshot.ValidationResult{}, fmt.Errorf("snapshot contracts: encode repository change metadata: %w", err)
	}
	return snapshot.ValidationResult{IntrinsicMetadata: encoded}, nil
}

func validateGitTreeChange(ctx context.Context, payloadPath string, document RepositoryChangeDocument, base RepositoryMetadata) error {
	payload, err := os.Open(payloadPath)
	if err != nil {
		return fmt.Errorf("snapshot contracts: open git-tree payload: %w", err)
	}
	resultTree, captureErr := (snapshot.Canonicalizer{}).Capture(ctx, payload)
	closeErr := payload.Close()
	if err := errors.Join(captureErr, closeErr); err != nil {
		if resultTree != nil {
			_ = resultTree.Close()
		}
		return fmt.Errorf("snapshot contracts: git-tree payload is not a canonical repository tar: %w", err)
	}
	defer resultTree.Close()
	if resultTree.Digest.String() != document.PayloadDigest {
		return fmt.Errorf("snapshot contracts: git-tree payload must be a canonical tar of the complete result repository")
	}
	root, err := os.OpenRoot(resultTree.Root)
	if err != nil {
		return fmt.Errorf("snapshot contracts: anchor git-tree result repository: %w", err)
	}
	metadata, validationErr := validateRepository(ctx, root, "HEAD")
	closeRootErr := root.Close()
	if err := errors.Join(validationErr, closeRootErr); err != nil {
		return fmt.Errorf("snapshot contracts: git-tree result is not repository/v1: %w", err)
	}
	if err := validateResultMetadata(metadata, document, base); err != nil {
		if metadata.HeadSHA != document.ResultSHA {
			return fmt.Errorf("snapshot contracts: git-tree actual HEAD does not equal result_sha")
		}
		return err
	}
	if _, err := (controlledGit{dir: resultTree.Root}).run(ctx, "merge-base", "--is-ancestor", document.BaseSHA, document.ResultSHA); err != nil {
		return fmt.Errorf("snapshot contracts: git-tree result_sha does not descend from base_sha: %w", err)
	}
	return nil
}

func validatePatchChange(ctx context.Context, baseDirectory, payloadPath string, document RepositoryChangeDocument, base RepositoryMetadata) error {
	runner := controlledGit{dir: baseDirectory}
	// Canonical extraction necessarily changes inode and timestamp metadata
	// recorded in Git's index. Refresh that cache in the disposable scratch
	// repository before asking --index to compare semantic file contents.
	if _, err := runner.run(ctx, "update-index", "--refresh"); err != nil {
		return fmt.Errorf("snapshot contracts: refresh scratch repository index: %w", err)
	}
	if _, err := runner.run(ctx, "apply", "--check", "--index", "--whitespace=nowarn", payloadPath); err != nil {
		return fmt.Errorf("snapshot contracts: patch failed git apply --check --index: %w", err)
	}
	if _, err := runner.run(ctx, "apply", "--index", "--whitespace=nowarn", payloadPath); err != nil {
		return fmt.Errorf("snapshot contracts: apply patch in controlled scratch repository: %w", err)
	}
	resultTree, err := runner.run(ctx, "write-tree")
	if err != nil {
		return fmt.Errorf("snapshot contracts: calculate patched result_tree_sha: %w", err)
	}
	if err := validateObjectID(base.ObjectFormat, resultTree); err != nil {
		return fmt.Errorf("snapshot contracts: patched result tree: %w", err)
	}
	if resultTree != document.ResultTreeSHA {
		return fmt.Errorf("snapshot contracts: result_tree_sha does not match the applied patch")
	}
	if err := validateCommitTree(ctx, runner, resultTree, base.ObjectFormat); err != nil {
		return fmt.Errorf("snapshot contracts: patched result tree is unsafe: %w", err)
	}
	return nil
}

func validateBundleChange(ctx context.Context, baseDirectory, payloadPath string, document RepositoryChangeDocument, base RepositoryMetadata) error {
	runner := controlledGit{dir: baseDirectory}
	if _, err := runner.run(ctx, "bundle", "verify", payloadPath); err != nil {
		return fmt.Errorf("snapshot contracts: bundle failed local git bundle verify: %w", err)
	}
	heads, err := runner.run(ctx, "bundle", "list-heads", payloadPath)
	if err != nil {
		return fmt.Errorf("snapshot contracts: list bundle heads: %w", err)
	}
	exposedHeads := make([][]string, 0, 1)
	for _, line := range strings.Split(heads, "\n") {
		if fields := strings.Fields(line); len(fields) >= 2 {
			exposedHeads = append(exposedHeads, fields)
		}
	}
	if len(exposedHeads) != 1 || exposedHeads[0][0] != document.ResultSHA {
		return fmt.Errorf("snapshot contracts: bundle must expose exactly one intended result head equal to result_sha")
	}
	if _, err := runner.run(ctx, "bundle", "unbundle", payloadPath); err != nil {
		return fmt.Errorf("snapshot contracts: unbundle into controlled scratch repository: %w", err)
	}
	root, err := os.OpenRoot(baseDirectory)
	if err != nil {
		return fmt.Errorf("snapshot contracts: anchor bundled scratch repository: %w", err)
	}
	metadata, validationErr := validateRepository(ctx, root, document.ResultSHA)
	closeErr := root.Close()
	if err := errors.Join(validationErr, closeErr); err != nil {
		return fmt.Errorf("snapshot contracts: bundled result is invalid: %w", err)
	}
	if err := validateResultMetadata(metadata, document, base); err != nil {
		return err
	}
	if _, err := runner.run(ctx, "merge-base", "--is-ancestor", document.BaseSHA, document.ResultSHA); err != nil {
		return fmt.Errorf("snapshot contracts: bundle result_sha does not descend from base_sha: %w", err)
	}
	return nil
}

func validateResultMetadata(metadata RepositoryMetadata, document RepositoryChangeDocument, base RepositoryMetadata) error {
	if metadata.ObjectFormat != base.ObjectFormat {
		return fmt.Errorf("snapshot contracts: result repository object format differs from base")
	}
	if metadata.RepositoryID != base.RepositoryID || metadata.RepositoryID != document.RepositoryID {
		return fmt.Errorf("snapshot contracts: result repository_id differs from base")
	}
	if metadata.HeadSHA != document.ResultSHA {
		return fmt.Errorf("snapshot contracts: result_sha does not match result repository commit")
	}
	if metadata.TreeSHA != document.ResultTreeSHA {
		return fmt.Errorf("snapshot contracts: result_tree_sha does not match result repository tree")
	}
	return nil
}

func objectFormatForID(objectID string) (string, error) {
	switch len(objectID) {
	case 40:
		if err := validateObjectID("sha1", objectID); err != nil {
			return "", err
		}
		return "sha1", nil
	case 64:
		if err := validateObjectID("sha256", objectID); err != nil {
			return "", err
		}
		return "sha256", nil
	default:
		return "", fmt.Errorf("object ID must be a full sha1 or sha256 hexadecimal value")
	}
}

type repositoryPayload struct {
	directory string
	path      string
	digest    string
}

func (p repositoryPayload) Close() error {
	return os.RemoveAll(p.directory)
}

func spoolRepositoryPayload(ctx context.Context, root *os.Root, name string) (repositoryPayload, error) {
	if err := validatePOSIXPath("payload_path", name); err != nil {
		return repositoryPayload{}, err
	}
	info, err := root.Lstat(name)
	if err != nil {
		return repositoryPayload{}, err
	}
	if !info.Mode().IsRegular() {
		return repositoryPayload{}, fmt.Errorf("payload must be a regular file")
	}
	if info.Size() < 0 || info.Size() > maxRepositoryPayloadBytes {
		return repositoryPayload{}, fmt.Errorf("payload exceeds size limit of %d bytes", maxRepositoryPayloadBytes)
	}
	source, err := root.Open(name)
	if err != nil {
		return repositoryPayload{}, err
	}
	openedInfo, err := source.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		_ = source.Close()
		return repositoryPayload{}, fmt.Errorf("payload changed while opening or is not a regular file")
	}
	directory, err := os.MkdirTemp("", "concourse-repository-payload-")
	if err != nil {
		_ = source.Close()
		return repositoryPayload{}, err
	}
	destinationPath := directory + string(os.PathSeparator) + "payload"
	destination, err := os.OpenFile(destinationPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		_ = source.Close()
		_ = os.RemoveAll(directory)
		return repositoryPayload{}, err
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(destination, hash), io.LimitReader(contextReader{ctx: ctx, reader: source}, maxRepositoryPayloadBytes+1))
	closeErr := errors.Join(destination.Close(), source.Close())
	if err := errors.Join(copyErr, closeErr); err != nil {
		_ = os.RemoveAll(directory)
		return repositoryPayload{}, err
	}
	if written > maxRepositoryPayloadBytes || written != info.Size() {
		_ = os.RemoveAll(directory)
		return repositoryPayload{}, fmt.Errorf("payload size changed while reading or exceeds limit")
	}
	return repositoryPayload{
		directory: directory,
		path:      destinationPath,
		digest:    "sha256:" + hex.EncodeToString(hash.Sum(nil)),
	}, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}
