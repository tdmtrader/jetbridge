package contracts

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
	"sort"
	"strings"

	"github.com/concourse/concourse/agent/snapshot"
)

const maxRepositoryPayloadBytes int64 = 10 << 30

type RepositoryChangeMetadata struct {
	RepositoryID   string   `json:"repository_id"`
	BaseSHA        string   `json:"base_sha"`
	ResultCommit   string   `json:"result_commit,omitempty"`
	ResultTree     string   `json:"result_tree"`
	Representation string   `json:"representation"`
	ChangedFiles   []string `json:"changed_files"`
}

// preRecordRepositoryChangeMetadata is the exact intrinsic-metadata shape the
// pre-record validator sealed into snapshots: result_sha / result_tree_sha, the
// "bundle" spelling of the bundle representation, and no changed_files at all.
// Sealed bytes are immutable, so every snapshot produced before this branch
// carries this shape forever and it is a permanent READ shape. Nothing writes
// it, and it must never gain a field.
type preRecordRepositoryChangeMetadata struct {
	RepositoryID   string `json:"repository_id"`
	BaseSHA        string `json:"base_sha"`
	ResultSHA      string `json:"result_sha,omitempty"`
	ResultTreeSHA  string `json:"result_tree_sha"`
	Representation string `json:"representation"`
}

// preRecordRepresentations maps every representation spelling the pre-record
// writer could emit onto its current name. A pre-record document naming
// anything else is refused rather than passed through, so this branch cannot
// widen the set of representations a reader will accept.
var preRecordRepresentations = map[string]string{
	"git-tree": "git-tree",
	"patch":    "patch",
	"bundle":   "git-bundle",
}

// DecodeRepositoryChangeMetadata decodes sealed repository-change/v1 intrinsic
// metadata into the current shape, accepting the pre-record shape on read.
//
// Two CLOSED shapes are tried, current first, pre-record only after the current
// decode fails. Both attempts use DisallowUnknownFields over a struct with no
// catch-all member and both refuse trailing JSON, so this is not a lenient
// fallback that swallows junk: a document that is not exactly one of the two
// shapes fails both. A document that mixes the spellings fails both as well,
// because each vocabulary's names are unknown fields to the other struct. The
// error reported is always the current-shape error, so a malformed modern
// document is not misdiagnosed as a legacy one.
//
// This is READ-ONLY normalization. The seal path keeps writing the current
// shape, and the values this returns are exactly the values the current writer
// would have produced for the same change.
func DecodeRepositoryChangeMetadata(raw []byte) (RepositoryChangeMetadata, error) {
	metadata, currentErr := decodeExactJSONDocument[RepositoryChangeMetadata](raw)
	if currentErr == nil {
		return metadata, nil
	}
	legacy, legacyErr := decodeExactJSONDocument[preRecordRepositoryChangeMetadata](raw)
	if legacyErr != nil {
		return RepositoryChangeMetadata{}, currentErr
	}
	representation, known := preRecordRepresentations[legacy.Representation]
	if !known {
		return RepositoryChangeMetadata{}, currentErr
	}
	return RepositoryChangeMetadata{
		RepositoryID:   legacy.RepositoryID,
		BaseSHA:        legacy.BaseSHA,
		ResultCommit:   legacy.ResultSHA,
		ResultTree:     legacy.ResultTreeSHA,
		Representation: representation,
	}, nil
}

func (d RepositoryChangeBody) Validate(subjects []Subject) error {
	if err := requireStrings([]namedString{
		{"repository_id", d.RepositoryID}, {"base_sha", d.BaseSHA},
		{"result_tree", d.ResultTree}, {"representation", d.Representation},
	}); err != nil {
		return err
	}
	if len(subjects) != 1 || subjects[0].Role != SubjectRoleBase {
		return fmt.Errorf("repository-change record requires exactly one base subject")
	}
	if subjects[0].Type.String() != "repository/v1" {
		return fmt.Errorf("repository-change base subject must have type repository/v1")
	}
	if _, err := snapshot.ParseDigest(d.RepositoryID); err != nil {
		return fmt.Errorf("repository_id: %w", err)
	}
	if err := d.Payload.Validate(); err != nil {
		return fmt.Errorf("payload: %w", err)
	}
	objectFormat, err := objectFormatForID(d.BaseSHA)
	if err != nil {
		return fmt.Errorf("base_sha: %w", err)
	}
	if err := validateObjectID(objectFormat, d.ResultTree); err != nil {
		return fmt.Errorf("result_tree: %w", err)
	}
	switch d.Representation {
	case "patch":
		if d.ResultCommit != "" {
			return fmt.Errorf("result_commit must be omitted for patch representation because a patch proves only result_tree")
		}
	case "git-tree", "git-bundle":
		if strings.TrimSpace(d.ResultCommit) == "" {
			return fmt.Errorf("result_commit is required for %s representation", d.Representation)
		}
		if err := validateObjectID(objectFormat, d.ResultCommit); err != nil {
			return fmt.Errorf("result_commit: %w", err)
		}
	default:
		return fmt.Errorf("representation must be one of patch, git-bundle, git-tree")
	}
	return nil
}

type repositoryChangeValidator struct {
	canonicalizer snapshot.Canonicalizer
}

// AdmitForSeal runs the SEAL-TIME gate: the candidate an agent just wrote must
// pin the current contract identity and bind its base subject to a
// server-declared exposed input.
func (validator repositoryChangeValidator) AdmitForSeal(ctx context.Context, root *os.Root, declarations snapshot.ValidationContext) (snapshot.ValidationResult, error) {
	record, err := admitRecordForSeal[RepositoryChangeBody](ctx, root, repositoryChangeType, declarations)
	if err != nil {
		return snapshot.ValidationResult{}, err
	}
	if err := repositoryChangeBody(record); err != nil {
		return snapshot.ValidationResult{}, err
	}
	return validator.verifyAgainstBase(ctx, root, record, declarations)
}

// RevalidateSealed runs the READ-TIME gate over an already-sealed candidate: an
// offline merge or a delivery gate re-deriving whether a stored change still
// applies to the base it names.
//
// Unlike the other record contracts, this one keeps the subject binding at read
// time. The whole meaning of a repository-change is "these bytes apply to THAT
// base repository", and the caller has to expose that base for the git lineage
// to be verifiable at all — so dropping the binding here would drop a check
// rather than relax an unavailable one. Readers that only want the document,
// with no base exposed, use ReadSealedRepositoryChangeRecord instead.
func (validator repositoryChangeValidator) RevalidateSealed(ctx context.Context, root *os.Root, exposed snapshot.ValidationContext) (snapshot.ValidationResult, error) {
	record, err := ReadSealedRepositoryChangeRecord(ctx, root)
	if err != nil {
		return snapshot.ValidationResult{}, err
	}
	if err := record.RebindSubjectsToExposedInputs(exposed); err != nil {
		return snapshot.ValidationResult{}, fmt.Errorf("snapshot contracts: record.json: %w", err)
	}
	return validator.verifyAgainstBase(ctx, root, record, exposed)
}

func (validator repositoryChangeValidator) verifyAgainstBase(
	ctx context.Context,
	root *os.Root,
	record Record[RepositoryChangeBody],
	validationContext snapshot.ValidationContext,
) (snapshot.ValidationResult, error) {
	document := record.Body
	baseSubject := record.Subjects[0]

	baseRef, found := validationContext.Input(baseSubject.Input)
	if !found {
		return snapshot.ValidationResult{}, fmt.Errorf("snapshot contracts: base subject input %q is not an exact declared input", baseSubject.Input)
	}
	if baseRef.Type.String() != "repository/v1" {
		return snapshot.ValidationResult{}, fmt.Errorf("snapshot contracts: base subject input %q must have type repository/v1", baseSubject.Input)
	}
	baseReader, err := validationContext.OpenInput(ctx, baseSubject.Input)
	if err != nil {
		return snapshot.ValidationResult{}, err
	}
	baseTree, captureErr := validator.canonicalizer.Capture(ctx, baseReader)
	closeErr := baseReader.Close()
	if err := errors.Join(captureErr, closeErr); err != nil {
		if baseTree != nil {
			_ = baseTree.Close()
		}
		return snapshot.ValidationResult{}, fmt.Errorf("snapshot contracts: capture base subject input %q: %w", baseSubject.Input, err)
	}
	defer baseTree.Close()
	if baseTree.Digest != baseRef.Digest {
		return snapshot.ValidationResult{}, fmt.Errorf("snapshot contracts: base subject input %q content digest does not match its immutable reference", baseSubject.Input)
	}
	baseRoot, err := os.OpenRoot(baseTree.Root)
	if err != nil {
		return snapshot.ValidationResult{}, fmt.Errorf("snapshot contracts: anchor base repository: %w", err)
	}
	baseMetadata, baseErr := validateRepository(ctx, baseRoot, "HEAD")
	baseCloseErr := baseRoot.Close()
	if err := errors.Join(baseErr, baseCloseErr); err != nil {
		return snapshot.ValidationResult{}, fmt.Errorf("snapshot contracts: base subject input %q is not repository/v1: %w", baseSubject.Input, err)
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

	payload, err := spoolRepositoryPayload(ctx, root, document.Payload.Path, validator.canonicalizer.TempDir)
	if err != nil {
		return snapshot.ValidationResult{}, fmt.Errorf("snapshot contracts: payload.path: %w", err)
	}
	defer payload.Close()
	if payload.digest != document.Payload.Digest.String() {
		return snapshot.ValidationResult{}, fmt.Errorf("snapshot contracts: payload.digest does not match exact payload bytes")
	}

	var changedFiles []string
	switch document.Representation {
	case "git-tree":
		changedFiles, err = validateGitTreeChange(ctx, payload.path, document, baseMetadata, validator.canonicalizer)
	case "patch":
		changedFiles, err = validatePatchChange(ctx, baseTree.Root, payload.path, document, baseMetadata)
	case "git-bundle":
		changedFiles, err = validateBundleChange(ctx, baseTree.Root, payload.path, document, baseMetadata)
	}
	if err != nil {
		return snapshot.ValidationResult{}, err
	}
	metadata := RepositoryChangeMetadata{
		RepositoryID: document.RepositoryID, BaseSHA: document.BaseSHA,
		ResultCommit: document.ResultCommit, ResultTree: document.ResultTree,
		Representation: document.Representation, ChangedFiles: changedFiles,
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return snapshot.ValidationResult{}, fmt.Errorf("snapshot contracts: encode repository change metadata: %w", err)
	}
	return snapshot.ValidationResult{IntrinsicMetadata: encoded}, nil
}

// ReadSealedRepositoryChangeRecord re-validates one stored repository-change/v1
// record.json at the READ-TIME gate. It takes no step declarations, because a
// reader loading a stored record has none.
func ReadSealedRepositoryChangeRecord(ctx context.Context, root *os.Root) (Record[RepositoryChangeBody], error) {
	record, err := readSealedRecord[RepositoryChangeBody](ctx, root, repositoryChangeType)
	if err != nil {
		return Record[RepositoryChangeBody]{}, err
	}
	if err := repositoryChangeBody(record); err != nil {
		return Record[RepositoryChangeBody]{}, err
	}
	return record, nil
}

// repositoryChangeBody is the shape half of both gates: the declared core first,
// then the type's own semantic rules. The git lineage half — apply, verify, descend
// — lives in verifyAgainstBase and needs the bound base repository.
func repositoryChangeBody(record Record[RepositoryChangeBody]) error {
	if err := validateDeclaredBody(repositoryChangeType, record.Subjects, record.Body); err != nil {
		return err
	}
	if err := record.Body.Validate(record.Subjects); err != nil {
		return fmt.Errorf("snapshot contracts: record.json body: %w", err)
	}
	return nil
}

func validateGitTreeChange(
	ctx context.Context,
	payloadPath string,
	document RepositoryChangeBody,
	base RepositoryMetadata,
	canonicalizer snapshot.Canonicalizer,
) ([]string, error) {
	payload, err := os.Open(payloadPath)
	if err != nil {
		return nil, fmt.Errorf("snapshot contracts: open git-tree payload: %w", err)
	}
	resultTree, captureErr := canonicalizer.Capture(ctx, payload)
	closeErr := payload.Close()
	if err := errors.Join(captureErr, closeErr); err != nil {
		if resultTree != nil {
			_ = resultTree.Close()
		}
		return nil, fmt.Errorf("snapshot contracts: git-tree payload is not a canonical repository tar: %w", err)
	}
	defer resultTree.Close()
	if resultTree.Digest != document.Payload.Digest {
		return nil, fmt.Errorf("snapshot contracts: git-tree payload must be a canonical tar of the complete result repository")
	}
	root, err := os.OpenRoot(resultTree.Root)
	if err != nil {
		return nil, fmt.Errorf("snapshot contracts: anchor git-tree result repository: %w", err)
	}
	metadata, validationErr := validateRepository(ctx, root, "HEAD")
	closeRootErr := root.Close()
	if err := errors.Join(validationErr, closeRootErr); err != nil {
		return nil, fmt.Errorf("snapshot contracts: git-tree result is not repository/v1: %w", err)
	}
	if err := validateResultMetadata(metadata, document, base); err != nil {
		if metadata.HeadSHA != document.ResultCommit {
			return nil, fmt.Errorf("snapshot contracts: git-tree actual HEAD does not equal result_commit")
		}
		return nil, err
	}
	runner := controlledGit{dir: resultTree.Root}
	if _, err := runner.run(ctx, "merge-base", "--is-ancestor", document.BaseSHA, document.ResultCommit); err != nil {
		return nil, fmt.Errorf("snapshot contracts: git-tree result_commit does not descend from base_sha: %w", err)
	}
	return deriveChangedFiles(ctx, runner, base.TreeSHA, document.ResultTree)
}

func validatePatchChange(ctx context.Context, baseDirectory, payloadPath string, document RepositoryChangeBody, base RepositoryMetadata) ([]string, error) {
	runner := controlledGit{dir: baseDirectory}
	// Canonical extraction necessarily changes inode and timestamp metadata
	// recorded in Git's index. Refresh that cache in the disposable scratch
	// repository before asking --index to compare semantic file contents.
	if _, err := runner.run(ctx, "update-index", "--refresh"); err != nil {
		return nil, fmt.Errorf("snapshot contracts: refresh scratch repository index: %w", err)
	}
	if _, err := runner.run(ctx, "apply", "--check", "--index", "--whitespace=nowarn", payloadPath); err != nil {
		return nil, fmt.Errorf("snapshot contracts: patch failed git apply --check --index: %w", err)
	}
	if _, err := runner.run(ctx, "apply", "--index", "--whitespace=nowarn", payloadPath); err != nil {
		return nil, fmt.Errorf("snapshot contracts: apply patch in controlled scratch repository: %w", err)
	}
	resultTree, err := runner.run(ctx, "write-tree")
	if err != nil {
		return nil, fmt.Errorf("snapshot contracts: calculate patched result_tree: %w", err)
	}
	if err := validateObjectID(base.ObjectFormat, resultTree); err != nil {
		return nil, fmt.Errorf("snapshot contracts: patched result tree: %w", err)
	}
	if resultTree != document.ResultTree {
		return nil, fmt.Errorf("snapshot contracts: result_tree does not match the applied patch")
	}
	if err := validateCommitTree(ctx, runner, resultTree, base.ObjectFormat); err != nil {
		return nil, fmt.Errorf("snapshot contracts: patched result tree is unsafe: %w", err)
	}
	return deriveChangedFiles(ctx, runner, base.TreeSHA, resultTree)
}

func validateBundleChange(ctx context.Context, baseDirectory, payloadPath string, document RepositoryChangeBody, base RepositoryMetadata) ([]string, error) {
	runner := controlledGit{dir: baseDirectory}
	if _, err := runner.run(ctx, "bundle", "verify", payloadPath); err != nil {
		return nil, fmt.Errorf("snapshot contracts: bundle failed local git bundle verify: %w", err)
	}
	heads, err := runner.run(ctx, "bundle", "list-heads", payloadPath)
	if err != nil {
		return nil, fmt.Errorf("snapshot contracts: list bundle heads: %w", err)
	}
	exposedHeads := make([][]string, 0, 1)
	for _, line := range strings.Split(heads, "\n") {
		if fields := strings.Fields(line); len(fields) >= 2 {
			exposedHeads = append(exposedHeads, fields)
		}
	}
	if len(exposedHeads) != 1 || exposedHeads[0][0] != document.ResultCommit {
		return nil, fmt.Errorf("snapshot contracts: bundle must expose exactly one intended result head equal to result_commit")
	}
	if _, err := runner.run(ctx, "bundle", "unbundle", payloadPath); err != nil {
		return nil, fmt.Errorf("snapshot contracts: unbundle into controlled scratch repository: %w", err)
	}
	root, err := os.OpenRoot(baseDirectory)
	if err != nil {
		return nil, fmt.Errorf("snapshot contracts: anchor bundled scratch repository: %w", err)
	}
	metadata, validationErr := validateRepository(ctx, root, document.ResultCommit)
	closeErr := root.Close()
	if err := errors.Join(validationErr, closeErr); err != nil {
		return nil, fmt.Errorf("snapshot contracts: bundled result is invalid: %w", err)
	}
	if err := validateResultMetadata(metadata, document, base); err != nil {
		return nil, err
	}
	if _, err := runner.run(ctx, "merge-base", "--is-ancestor", document.BaseSHA, document.ResultCommit); err != nil {
		return nil, fmt.Errorf("snapshot contracts: bundle result_commit does not descend from base_sha: %w", err)
	}
	return deriveChangedFiles(ctx, runner, base.TreeSHA, document.ResultTree)
}

func validateResultMetadata(metadata RepositoryMetadata, document RepositoryChangeBody, base RepositoryMetadata) error {
	if metadata.ObjectFormat != base.ObjectFormat {
		return fmt.Errorf("snapshot contracts: result repository object format differs from base")
	}
	if metadata.RepositoryID != base.RepositoryID || metadata.RepositoryID != document.RepositoryID {
		return fmt.Errorf("snapshot contracts: result repository_id differs from base")
	}
	if metadata.HeadSHA != document.ResultCommit {
		return fmt.Errorf("snapshot contracts: result_commit does not match result repository commit")
	}
	if metadata.TreeSHA != document.ResultTree {
		return fmt.Errorf("snapshot contracts: result_tree does not match result repository tree")
	}
	return nil
}

func deriveChangedFiles(ctx context.Context, runner controlledGit, baseTree, resultTree string) ([]string, error) {
	output, err := runner.runRaw(ctx, "diff-tree", "--no-commit-id", "--name-only", "-r", "-z", baseTree, resultTree)
	if err != nil {
		return nil, fmt.Errorf("snapshot contracts: derive changed files: %w", err)
	}
	parts := bytes.Split(output, []byte{0})
	files := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		if len(part) == 0 {
			continue
		}
		name := string(part)
		if err := validatePOSIXPath("changed file", name); err != nil {
			return nil, fmt.Errorf("snapshot contracts: derived changed file: %w", err)
		}
		if _, found := seen[name]; found {
			return nil, fmt.Errorf("snapshot contracts: derived changed file %q is duplicate", name)
		}
		seen[name] = struct{}{}
		files = append(files, name)
	}
	sort.Strings(files)
	return files, nil
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

func spoolRepositoryPayload(ctx context.Context, root *os.Root, name, tempDir string) (repositoryPayload, error) {
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
	directory, err := os.MkdirTemp(tempDir, "concourse-repository-payload-")
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
