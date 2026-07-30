// Package pullrequestresponse turns an agent-authored response draft into the
// exact provider-authorized pull-request-response/v1 value consumed by the
// publisher.
//
// An agent may describe only the completed review batch exposed in its sealed
// pull-request/v1 input. This deterministic gate reopens both typed inputs,
// requires the draft to bind that exact observation, and rejects replies to
// any thread outside the batch before writing the final record. It performs no
// provider I/O and receives no forge credential.
package pullrequestresponse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/concourse/concourse/agent/functions/repositorymerge"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

const (
	observationType snapshot.TypeRef = "pull-request/v1"
	responseType    snapshot.TypeRef = "pull-request-response/v1"
	recordFileName                   = "record.json"

	// Snapshot admission already bounds input trees. Keep a local fail-closed
	// ceiling as defense in depth so a malformed protected mount cannot turn
	// physical-alias detection into unbounded work.
	maxProtectedDirectories = 1 << 20
)

var errProtectedIdentityFound = errors.New(
	"pull request response: protected identity found",
)

// RecordAuthority is the response output identity declared by the platform.
// The schema is copied verbatim; the web node that issued it independently
// checks the sealed output against its own contract registry.
type RecordAuthority struct {
	Type   snapshot.TypeRef
	Schema snapshot.Digest
}

// Request carries only exact typed task inputs and output authority. Snapshot
// IDs are local task ordinals inside the pod, while type and digest are the
// platform-declared immutable identities rebound again at seal time.
type Request struct {
	Observation      snapshot.SnapshotRef
	ObservationInput string
	ObservationRoot  string

	Draft      snapshot.SnapshotRef
	DraftInput string
	DraftRoot  string

	ResponseAuthority RecordAuthority
	Canonicalizer     snapshot.Canonicalizer
}

func (request Request) validate() error {
	if err := request.Observation.Validate(); err != nil ||
		request.Observation.Type != observationType {
		return fmt.Errorf("pull request response: observation must be an exact %s snapshot", observationType)
	}
	if err := request.Draft.Validate(); err != nil ||
		request.Draft.Type != responseType {
		return fmt.Errorf("pull request response: draft must be an exact %s snapshot", responseType)
	}
	if !validPort(request.ObservationInput) || !validPort(request.DraftInput) ||
		request.ObservationInput == request.DraftInput {
		return fmt.Errorf("pull request response: distinct typed input ports are required")
	}
	if !absoluteCleanPath(request.ObservationRoot) ||
		!absoluteCleanPath(request.DraftRoot) ||
		pathsOverlap(request.ObservationRoot, request.DraftRoot) {
		return fmt.Errorf("pull request response: distinct absolute input roots are required")
	}
	if request.ResponseAuthority.Type != responseType ||
		request.ResponseAuthority.Schema.Validate() != nil {
		return fmt.Errorf("pull request response: response output authority is invalid")
	}
	return nil
}

// Authorize validates the agent draft against the exact completed review batch
// and derives the one record the publisher is allowed to consume.
func Authorize(
	ctx context.Context,
	request Request,
) (contracts.Record[contracts.PullRequestResponseBody], error) {
	var empty contracts.Record[contracts.PullRequestResponseBody]
	if ctx == nil {
		return empty, fmt.Errorf("pull request response: context is required")
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if err := request.validate(); err != nil {
		return empty, err
	}

	observationTree, err := repositorymerge.CaptureDirectory(
		ctx,
		request.Canonicalizer,
		request.ObservationRoot,
	)
	if err != nil {
		return empty, fmt.Errorf(
			"pull request response: capture observation: %w",
			err,
		)
	}
	defer observationTree.Close()
	if observationTree.Digest != request.Observation.Digest {
		return empty, fmt.Errorf(
			"pull request response: materialized bytes do not match the exact observation snapshot",
		)
	}
	observationRoot, err := os.OpenRoot(observationTree.Root)
	if err != nil {
		return empty, fmt.Errorf("pull request response: open observation: %w", err)
	}
	observation, observationErr := contracts.ReadSealedPullRequestRecord(
		ctx,
		observationRoot,
	)
	observationCloseErr := observationRoot.Close()
	if err := errors.Join(observationErr, observationCloseErr); err != nil {
		return empty, fmt.Errorf("pull request response: reopen exact observation: %w", err)
	}
	if observation.Body.Trigger != contracts.PullRequestReviewBatchTrigger {
		return empty, fmt.Errorf("pull request response: observation is not a completed review batch")
	}

	draftTree, err := repositorymerge.CaptureDirectory(
		ctx,
		request.Canonicalizer,
		request.DraftRoot,
	)
	if err != nil {
		return empty, fmt.Errorf(
			"pull request response: capture response draft: %w",
			err,
		)
	}
	defer draftTree.Close()
	if draftTree.Digest != request.Draft.Digest {
		return empty, fmt.Errorf(
			"pull request response: materialized bytes do not match the exact response draft snapshot",
		)
	}
	draftRoot, err := os.OpenRoot(draftTree.Root)
	if err != nil {
		return empty, fmt.Errorf("pull request response: open draft: %w", err)
	}
	draft, draftErr := contracts.ReadSealedPullRequestResponseRecord(ctx, draftRoot)
	draftCloseErr := draftRoot.Close()
	if err := errors.Join(draftErr, draftCloseErr); err != nil {
		return empty, fmt.Errorf("pull request response: reopen response draft: %w", err)
	}

	exactSubject := contracts.SubjectFromInput(
		"pull-request",
		contracts.SubjectRolePrimary,
		request.ObservationInput,
		request.Observation,
	)
	if len(draft.Subjects) != 1 || draft.Subjects[0] != exactSubject {
		return empty, fmt.Errorf(
			"pull request response: draft does not bind the exact pull request observation",
		)
	}
	if err := contracts.ValidatePullRequestResponseAgainst(
		draft.Body,
		observation.Body,
	); err != nil {
		return empty, fmt.Errorf("pull request response: authorize draft: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}

	body := cloneResponse(draft.Body)
	record := contracts.Record[contracts.PullRequestResponseBody]{
		RecordVersion: contracts.RecordVersion,
		Type:          request.ResponseAuthority.Type,
		Schema:        request.ResponseAuthority.Schema,
		Subjects:      []contracts.Subject{exactSubject},
		Body:          body,
	}
	if err := validateRecord(record); err != nil {
		return empty, err
	}
	return record, nil
}

// Write materializes one authorized response beneath a caller-owned output
// mount. It refuses to replace an existing record.
func Write(
	ctx context.Context,
	outputRoot string,
	record contracts.Record[contracts.PullRequestResponseBody],
	protectedRoots ...string,
) error {
	if ctx == nil {
		return fmt.Errorf("pull request response: context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !absoluteCleanPath(outputRoot) {
		return fmt.Errorf("pull request response: output root must be an absolute clean path")
	}
	if err := validateRecord(record); err != nil {
		return err
	}
	payload, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("pull request response: encode record: %w", err)
	}
	payload = append(payload, '\n')

	output, err := openResponseOutput(outputRoot)
	if err != nil {
		return err
	}
	defer output.root.Close()
	for _, protectedRoot := range protectedRoots {
		if err := output.rejectProtectedRoot(protectedRoot); err != nil {
			return err
		}
	}
	if err := output.requireEmpty(); err != nil {
		return err
	}
	if err := output.verifyIdentity(); err != nil {
		return err
	}

	file, err := output.root.OpenFile(
		recordFileName,
		os.O_WRONLY|os.O_CREATE|os.O_EXCL,
		0600,
	)
	if err != nil {
		return fmt.Errorf("pull request response: create record: %w", err)
	}
	// Do not remove by name on a later failure. There is no portable
	// identity-conditional unlink, so another writer could replace the name
	// after creation and make rollback delete an inode this call never owned.
	// Failed function outputs are not admitted; retaining our inode is safer
	// than performing a racy cleanup.
	written, writeErr := file.Write(payload)
	if writeErr == nil && written != len(payload) {
		writeErr = io.ErrShortWrite
	}
	syncErr := file.Sync()
	recordInfo, statErr := file.Stat()
	fileCloseErr := file.Close()
	if err := errors.Join(writeErr, syncErr, statErr, fileCloseErr); err != nil {
		return fmt.Errorf("pull request response: write record: %w", err)
	}
	if !recordInfo.Mode().IsRegular() {
		return fmt.Errorf("pull request response: record output is not a regular file")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := output.verifyExactRecord(recordInfo); err != nil {
		return err
	}
	for _, protectedRoot := range protectedRoots {
		if err := output.rejectProtectedRoot(protectedRoot); err != nil {
			return err
		}
	}
	return nil
}

type responseOutput struct {
	root          *os.Root
	path          string
	canonicalPath string
	identity      os.FileInfo
}

func openResponseOutput(path string) (*responseOutput, error) {
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("pull request response: inspect output root: %w", err)
	}
	if !pathInfo.IsDir() || pathInfo.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf(
			"pull request response: output root must be a directory and not a symlink",
		)
	}
	canonicalPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, fmt.Errorf(
			"pull request response: resolve output root ancestry: %w", err,
		)
	}
	canonicalInfo, err := os.Stat(canonicalPath)
	if err != nil {
		return nil, fmt.Errorf(
			"pull request response: inspect resolved output root: %w", err,
		)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, fmt.Errorf("pull request response: open output root: %w", err)
	}
	rootInfo, err := root.Stat(".")
	if err != nil ||
		!os.SameFile(pathInfo, rootInfo) ||
		!os.SameFile(canonicalInfo, rootInfo) {
		closeErr := root.Close()
		return nil, fmt.Errorf(
			"pull request response: output root identity changed while opening: %w",
			errors.Join(err, closeErr),
		)
	}
	return &responseOutput{
		root: root, path: path, canonicalPath: canonicalPath, identity: rootInfo,
	}, nil
}

func (output *responseOutput) rejectProtectedRoot(path string) error {
	if !absoluteCleanPath(path) {
		return fmt.Errorf(
			"pull request response: protected input root must be an absolute clean path",
		)
	}
	if pathsOverlap(output.path, path) {
		return fmt.Errorf(
			"pull request response: output root overlaps a protected input root",
		)
	}
	protected, err := openResponseOutput(path)
	if err != nil {
		return fmt.Errorf(
			"pull request response: inspect protected input root: %w", err,
		)
	}
	defer protected.root.Close()
	canonicalOverlap := pathsOverlap(
		output.canonicalPath,
		protected.canonicalPath,
	)
	outputWithinProtected, err := physicalAncestor(
		output.canonicalPath,
		protected.identity,
	)
	if err != nil {
		return fmt.Errorf(
			"pull request response: inspect output ancestry: %w", err,
		)
	}
	protectedWithinOutput, err := physicalAncestor(
		protected.canonicalPath,
		output.identity,
	)
	if err != nil {
		return fmt.Errorf(
			"pull request response: inspect protected input ancestry: %w", err,
		)
	}
	outputInsideProtectedTree, err := protected.containsIdentity(output.identity)
	if err != nil {
		return err
	}
	if canonicalOverlap || outputWithinProtected || protectedWithinOutput ||
		outputInsideProtectedTree {
		return fmt.Errorf(
			"pull request response: output root overlaps a protected input root",
		)
	}
	if err := output.verifyIdentity(); err != nil {
		return err
	}
	if err := protected.verifyIdentity(); err != nil {
		return fmt.Errorf(
			"pull request response: protected input root identity changed during inspection",
		)
	}
	return nil
}

func (output *responseOutput) containsIdentity(target os.FileInfo) (bool, error) {
	directories := 0
	err := fs.WalkDir(output.root.FS(), ".", func(
		name string,
		entry fs.DirEntry,
		walkErr error,
	) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() {
			return nil
		}
		directories++
		if directories > maxProtectedDirectories {
			return fmt.Errorf(
				"pull request response: protected input directory bound exceeded",
			)
		}
		info, err := output.root.Lstat(name)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf(
				"pull request response: protected input changed during inspection",
			)
		}
		if os.SameFile(info, target) {
			return errProtectedIdentityFound
		}
		return nil
	})
	if errors.Is(err, errProtectedIdentityFound) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf(
			"pull request response: inspect protected input tree: %w", err,
		)
	}
	return false, nil
}

func physicalAncestor(path string, ancestor os.FileInfo) (bool, error) {
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Stat(current)
		if err != nil {
			return false, err
		}
		if os.SameFile(info, ancestor) {
			return true, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return false, nil
		}
	}
}

func (output *responseOutput) requireEmpty() error {
	directory, err := output.root.Open(".")
	if err != nil {
		return fmt.Errorf("pull request response: inspect output contents: %w", err)
	}
	names, readErr := directory.Readdirnames(1)
	closeErr := directory.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return fmt.Errorf(
			"pull request response: inspect output contents: %w",
			errors.Join(readErr, closeErr),
		)
	}
	if closeErr != nil {
		return fmt.Errorf(
			"pull request response: close output directory: %w", closeErr,
		)
	}
	if len(names) != 0 {
		return fmt.Errorf("pull request response: output root must be empty")
	}
	return nil
}

func (output *responseOutput) verifyIdentity() error {
	pathInfo, pathErr := os.Lstat(output.path)
	canonicalInfo, canonicalErr := os.Stat(output.canonicalPath)
	rootInfo, rootErr := output.root.Stat(".")
	if err := errors.Join(pathErr, canonicalErr, rootErr); err != nil {
		return fmt.Errorf(
			"pull request response: verify output root identity: %w", err,
		)
	}
	if !pathInfo.IsDir() || pathInfo.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(pathInfo, output.identity) ||
		!os.SameFile(canonicalInfo, output.identity) ||
		!os.SameFile(rootInfo, output.identity) {
		return fmt.Errorf(
			"pull request response: output root identity changed during write",
		)
	}
	return nil
}

func (output *responseOutput) verifyExactRecord(expected os.FileInfo) error {
	if err := output.verifyIdentity(); err != nil {
		return err
	}
	directory, err := output.root.Open(".")
	if err != nil {
		return fmt.Errorf("pull request response: verify output contents: %w", err)
	}
	names, readErr := directory.Readdirnames(2)
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return fmt.Errorf(
			"pull request response: verify output contents: %w",
			errors.Join(readErr, syncErr, closeErr),
		)
	}
	if err := errors.Join(syncErr, closeErr); err != nil {
		return fmt.Errorf("pull request response: sync output root: %w", err)
	}
	actual, err := output.root.Lstat(recordFileName)
	if err != nil || len(names) != 1 || names[0] != recordFileName ||
		!actual.Mode().IsRegular() || !os.SameFile(actual, expected) {
		return fmt.Errorf(
			"pull request response: output root does not contain exactly the authorized record",
		)
	}
	return output.verifyIdentity()
}

func validateRecord(
	record contracts.Record[contracts.PullRequestResponseBody],
) error {
	if record.RecordVersion != contracts.RecordVersion ||
		record.Type != responseType ||
		record.Schema.Validate() != nil {
		return fmt.Errorf("pull request response: output record authority is invalid")
	}
	if len(record.Subjects) != 1 ||
		record.Subjects[0].Role != contracts.SubjectRolePrimary ||
		record.Subjects[0].Type != observationType ||
		record.Subjects[0].Validate() != nil {
		return fmt.Errorf("pull request response: output record subject is invalid")
	}
	if err := record.Body.Validate(record.Subjects); err != nil {
		return fmt.Errorf("pull request response: output record body: %w", err)
	}
	return nil
}

func cloneResponse(
	body contracts.PullRequestResponseBody,
) contracts.PullRequestResponseBody {
	body.Replies = append([]contracts.PullRequestThreadResponse(nil), body.Replies...)
	return body
}

func validPort(value string) bool {
	if strings.TrimSpace(value) != value || value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z' ||
			character >= '0' && character <= '9' ||
			character == '-' || character == '_') {
			return false
		}
	}
	return true
}

func absoluteCleanPath(value string) bool {
	return value != "" &&
		filepath.IsAbs(value) &&
		filepath.Clean(value) == value &&
		value != string(filepath.Separator)
}

func pathsOverlap(left, right string) bool {
	relative, err := filepath.Rel(left, right)
	if err == nil && (relative == "." || filepath.IsLocal(relative) &&
		relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		return true
	}
	relative, err = filepath.Rel(right, left)
	return err == nil && (relative == "." || filepath.IsLocal(relative) &&
		relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}
