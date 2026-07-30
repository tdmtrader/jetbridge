// Package prmonitor contains deterministic boundaries used by pr-monitor-v3.
package prmonitor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/concourse/concourse/agent/functions/repositorymerge"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

const pullRequestType snapshot.TypeRef = "pull-request/v1"

// MaterializeRequest identifies the exact forge-pr resource snapshot and the
// two caller-owned task output mounts that will become repository/v1 values.
type MaterializeRequest struct {
	Observation      snapshot.SnapshotRef
	ObservationInput string
	ObservationRoot  string
	SourceOutput     string
	TargetOutput     string
	Canonicalizer    snapshot.Canonicalizer
}

// MaterializeResult is intrinsic repository evidence derived by the built-in
// repository/v1 validator. It contains no provider or credential data.
type MaterializeResult struct {
	Source contracts.RepositoryMetadata
	Target contracts.RepositoryMetadata
}

func (request MaterializeRequest) validate() error {
	if request.Observation.Validate() != nil ||
		request.Observation.Type != pullRequestType {
		return fmt.Errorf("pr monitor: exact pull-request/v1 observation is required")
	}
	if strings.TrimSpace(request.ObservationInput) != request.ObservationInput ||
		request.ObservationInput == "" {
		return fmt.Errorf("pr monitor: observation input port is required")
	}
	paths := []string{
		request.ObservationRoot,
		request.SourceOutput,
		request.TargetOutput,
	}
	for _, value := range paths {
		if !absoluteCleanPath(value) {
			return fmt.Errorf("pr monitor: materialization paths must be absolute and clean")
		}
	}
	for left := range paths {
		for right := left + 1; right < len(paths); right++ {
			if pathsOverlap(paths[left], paths[right]) {
				return fmt.Errorf("pr monitor: observation and output roots must not overlap")
			}
		}
	}
	return nil
}

// Materialize verifies the exact outer observation snapshot, revalidates both
// contained repositories, requires their HEADs to equal the provider
// observation, and copies them from a private canonical capture to the two
// typed output mounts. Any failure rolls back only entries this call created.
func Materialize(
	ctx context.Context,
	request MaterializeRequest,
) (_ MaterializeResult, resultErr error) {
	if ctx == nil {
		return MaterializeResult{}, fmt.Errorf("pr monitor: context is required")
	}
	if err := ctx.Err(); err != nil {
		return MaterializeResult{}, err
	}
	if err := request.validate(); err != nil {
		return MaterializeResult{}, err
	}

	outer, err := repositorymerge.CaptureDirectory(
		ctx,
		request.Canonicalizer,
		request.ObservationRoot,
	)
	if err != nil {
		return MaterializeResult{}, fmt.Errorf("pr monitor: capture observation: %w", err)
	}
	defer outer.Close()
	if outer.Digest != request.Observation.Digest {
		return MaterializeResult{}, fmt.Errorf(
			"pr monitor: materialized bytes do not match the exact observation snapshot",
		)
	}
	if err := requireObservationMembership(outer.Root); err != nil {
		return MaterializeResult{}, err
	}

	outerRoot, err := os.OpenRoot(outer.Root)
	if err != nil {
		return MaterializeResult{}, fmt.Errorf("pr monitor: open observation: %w", err)
	}
	observation, observationErr := contracts.ReadSealedPullRequestRecord(
		ctx,
		outerRoot,
	)
	outerCloseErr := outerRoot.Close()
	if err := errors.Join(observationErr, outerCloseErr); err != nil {
		return MaterializeResult{}, fmt.Errorf("pr monitor: reopen observation: %w", err)
	}

	registry, err := contracts.NewRegistry(
		contracts.WithCanonicalizer(request.Canonicalizer),
	)
	if err != nil {
		return MaterializeResult{}, fmt.Errorf("pr monitor: build contract registry: %w", err)
	}
	validator, err := registry.Lookup("repository/v1")
	if err != nil {
		return MaterializeResult{}, err
	}
	sourceRoot := filepath.Join(outer.Root, "source-repository")
	targetRoot := filepath.Join(outer.Root, "target-repository")
	source, err := validateRepository(ctx, validator, sourceRoot)
	if err != nil {
		return MaterializeResult{}, fmt.Errorf("pr monitor: source repository: %w", err)
	}
	target, err := validateRepository(ctx, validator, targetRoot)
	if err != nil {
		return MaterializeResult{}, fmt.Errorf("pr monitor: target repository: %w", err)
	}
	if source.HeadSHA != observation.Body.SourceSHA {
		return MaterializeResult{}, fmt.Errorf(
			"pr monitor: source HEAD does not match the exact observation",
		)
	}
	if target.HeadSHA != observation.Body.TargetSHA {
		return MaterializeResult{}, fmt.Errorf(
			"pr monitor: target HEAD does not match the exact observation",
		)
	}
	if source.RepositoryID != target.RepositoryID ||
		source.ObjectFormat != target.ObjectFormat {
		return MaterializeResult{}, fmt.Errorf(
			"pr monitor: source and target must belong to the same repository",
		)
	}

	var outputs []*materializedOutput
	defer func() {
		for _, output := range outputs {
			resultErr = errors.Join(resultErr, output.close(resultErr == nil))
		}
	}()
	sourceOutput, err := copyRepository(
		ctx,
		validator,
		sourceRoot,
		request.SourceOutput,
		source,
	)
	if sourceOutput != nil {
		outputs = append(outputs, sourceOutput)
	}
	if err != nil {
		return MaterializeResult{}, fmt.Errorf("pr monitor: materialize source: %w", err)
	}
	targetOutput, err := copyRepository(
		ctx,
		validator,
		targetRoot,
		request.TargetOutput,
		target,
	)
	if targetOutput != nil {
		outputs = append(outputs, targetOutput)
	}
	if err != nil {
		return MaterializeResult{}, fmt.Errorf("pr monitor: materialize target: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return MaterializeResult{}, err
	}
	return MaterializeResult{Source: source, Target: target}, nil
}

func validateRepository(
	ctx context.Context,
	validator snapshot.Validator,
	path string,
) (contracts.RepositoryMetadata, error) {
	var metadata contracts.RepositoryMetadata
	root, err := os.OpenRoot(path)
	if err != nil {
		return metadata, err
	}
	result, validationErr := validator.RevalidateSealed(
		ctx,
		root,
		snapshot.ValidationContext{},
	)
	closeErr := root.Close()
	if err := errors.Join(validationErr, closeErr); err != nil {
		return metadata, err
	}
	if err := json.Unmarshal(result.IntrinsicMetadata, &metadata); err != nil {
		return contracts.RepositoryMetadata{}, fmt.Errorf(
			"decode intrinsic metadata: %w",
			err,
		)
	}
	return metadata, nil
}

type ownedEntry struct {
	name string
}

type materializedOutput struct {
	root     *os.Root
	path     string
	identity os.FileInfo
	entries  []ownedEntry
}

func copyRepository(
	ctx context.Context,
	validator snapshot.Validator,
	source,
	destination string,
	expected contracts.RepositoryMetadata,
) (*materializedOutput, error) {
	output, err := openMaterializedOutput(destination)
	if err != nil {
		return nil, err
	}
	entries, err := fs.ReadDir(output.root.FS(), ".")
	if err != nil || len(entries) != 0 {
		return output, fmt.Errorf("output must start empty")
	}
	err = filepath.WalkDir(
		source,
		func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			relative, err := filepath.Rel(source, path)
			if err != nil {
				return err
			}
			if relative == "." {
				return nil
			}
			if !filepath.IsLocal(relative) {
				return fmt.Errorf("repository entry escaped its root")
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			switch {
			case info.IsDir():
				if err := output.root.Mkdir(relative, info.Mode().Perm()); err != nil {
					return err
				}
				output.entries = append(
					output.entries,
					ownedEntry{name: relative},
				)
				return output.root.Chmod(relative, info.Mode().Perm())
			case info.Mode()&os.ModeSymlink != 0:
				target, err := os.Readlink(path)
				if err != nil {
					return err
				}
				if err := output.root.Symlink(target, relative); err != nil {
					return err
				}
				output.entries = append(
					output.entries,
					ownedEntry{name: relative},
				)
				return nil
			case info.Mode().IsRegular():
				return copyRegularFile(
					output.root,
					path,
					relative,
					info,
					output,
				)
			default:
				return fmt.Errorf("unsupported repository entry %q", relative)
			}
		},
	)
	if err != nil {
		return output, err
	}
	result, err := validator.RevalidateSealed(
		ctx,
		output.root,
		snapshot.ValidationContext{},
	)
	if err != nil {
		return output, err
	}
	var actual contracts.RepositoryMetadata
	if err := json.Unmarshal(result.IntrinsicMetadata, &actual); err != nil {
		return output, err
	}
	if !reflect.DeepEqual(actual, expected) {
		return output, fmt.Errorf("copied repository metadata changed")
	}
	if err := output.verifyIdentity(); err != nil {
		return output, err
	}
	return output, nil
}

func openMaterializedOutput(path string) (*materializedOutput, error) {
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect output root: %w", err)
	}
	if !pathInfo.IsDir() || pathInfo.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("output root must be a directory and not a symlink")
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, fmt.Errorf("open output root: %w", err)
	}
	rootInfo, err := root.Stat(".")
	if err != nil || !os.SameFile(pathInfo, rootInfo) {
		closeErr := root.Close()
		return nil, fmt.Errorf(
			"output root identity changed while opening: %w",
			errors.Join(err, closeErr),
		)
	}
	return &materializedOutput{
		root:     root,
		path:     path,
		identity: rootInfo,
	}, nil
}

func (output *materializedOutput) verifyIdentity() error {
	pathInfo, pathErr := os.Lstat(output.path)
	rootInfo, rootErr := output.root.Stat(".")
	if err := errors.Join(pathErr, rootErr); err != nil {
		return fmt.Errorf("verify output root identity: %w", err)
	}
	if !pathInfo.IsDir() ||
		pathInfo.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(pathInfo, output.identity) ||
		!os.SameFile(rootInfo, output.identity) {
		return fmt.Errorf("output root identity changed during materialization")
	}
	return nil
}

func copyRegularFile(
	root *os.Root,
	source,
	destination string,
	info os.FileInfo,
	output *materializedOutput,
) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	file, err := root.OpenFile(
		destination,
		os.O_WRONLY|os.O_CREATE|os.O_EXCL,
		info.Mode().Perm(),
	)
	if err != nil {
		_ = input.Close()
		return err
	}
	output.entries = append(output.entries, ownedEntry{name: destination})
	_, copyErr := io.Copy(file, io.LimitReader(input, info.Size()+1))
	chmodErr := file.Chmod(info.Mode().Perm())
	syncErr := file.Sync()
	fileCloseErr := file.Close()
	inputCloseErr := input.Close()
	if err := errors.Join(
		copyErr,
		chmodErr,
		syncErr,
		fileCloseErr,
		inputCloseErr,
	); err != nil {
		return err
	}
	copied, err := root.Stat(destination)
	if err != nil ||
		!copied.Mode().IsRegular() ||
		copied.Size() != info.Size() {
		return fmt.Errorf("repository file changed while copying")
	}
	return nil
}

func (output *materializedOutput) close(keep bool) error {
	if output == nil || output.root == nil {
		return nil
	}
	var result error
	if !keep {
		for index := len(output.entries) - 1; index >= 0; index-- {
			entry := output.entries[index]
			if err := output.root.Remove(entry.name); err != nil &&
				!errors.Is(err, fs.ErrNotExist) {
				result = errors.Join(result, err)
			}
		}
	}
	return errors.Join(result, output.root.Close())
}

func requireObservationMembership(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("pr monitor: inspect observation: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	expected := []string{"record.json", "source-repository", "target-repository"}
	if !reflect.DeepEqual(names, expected) {
		return fmt.Errorf("pr monitor: observation has unexpected materialized entries")
	}
	return nil
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
		relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		return true
	}
	relative, err = filepath.Rel(right, left)
	return err == nil && (relative == "." || filepath.IsLocal(relative) &&
		relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}
