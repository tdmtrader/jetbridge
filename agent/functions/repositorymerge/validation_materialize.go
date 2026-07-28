package repositorymerge

// This file exposes the one narrow repository-change materialization seam
// consumed by authoritative dev validation. It deliberately uses the sealed
// contract validator rather than interpreting candidate metadata or payload
// bytes as a source of authority.

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/concourse/concourse/agent/repodiff"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

type ValidationMaterialization struct {
	ChangedPaths []string
}

// MaterializeForValidation authenticates a repository-change/v1 candidate and
// its explicitly declared repository/v1 base, then writes the candidate result
// into an empty workspace. The only currently supported representation is the
// canonical git-tree payload: patch/bundle are rejected rather than bypassing
// the contract's base/lineage checks with ad-hoc extraction.
func MaterializeForValidation(ctx context.Context, canonicalizer snapshot.Canonicalizer, candidateRoot string, candidate snapshot.SnapshotRef, baseInput string, baseRoot string, base snapshot.SnapshotRef, workspace string) (ValidationMaterialization, error) {
	if candidate.Type != repositoryChangeType || base.Type != snapshot.TypeRef("repository/v1") || baseInput == "" {
		return ValidationMaterialization{}, fmt.Errorf("repository validation materialization requires repository-change/v1 candidate and named repository/v1 base")
	}
	if err := candidate.Validate(); err != nil {
		return ValidationMaterialization{}, err
	}
	if err := base.Validate(); err != nil {
		return ValidationMaterialization{}, err
	}
	candidateTree, err := CaptureDirectory(ctx, canonicalizer, candidateRoot)
	if err != nil {
		return ValidationMaterialization{}, fmt.Errorf("capture candidate: %w", err)
	}
	defer candidateTree.Close()
	if candidateTree.Digest != candidate.Digest {
		return ValidationMaterialization{}, fmt.Errorf("candidate content does not match exact reference")
	}
	baseTree, err := CaptureDirectory(ctx, canonicalizer, baseRoot)
	if err != nil {
		return ValidationMaterialization{}, fmt.Errorf("capture base: %w", err)
	}
	defer baseTree.Close()
	if baseTree.Digest != base.Digest {
		return ValidationMaterialization{}, fmt.Errorf("base content does not match exact reference")
	}

	registry, err := contracts.NewRegistry(contracts.WithCanonicalizer(canonicalizer))
	if err != nil {
		return ValidationMaterialization{}, err
	}
	validator, err := registry.Lookup(repositoryChangeType)
	if err != nil {
		return ValidationMaterialization{}, err
	}
	contextInputs, err := snapshot.NewValidationContext(map[string]snapshot.SnapshotRef{baseInput: base}, func(_ context.Context, name string, ref snapshot.SnapshotRef) (io.ReadCloser, error) {
		if name != baseInput || ref != base {
			return nil, fmt.Errorf("unexpected base input")
		}
		return os.Open(baseTree.ArchivePath)
	})
	if err != nil {
		return ValidationMaterialization{}, err
	}
	root, err := os.OpenRoot(candidateRoot)
	if err != nil {
		return ValidationMaterialization{}, err
	}
	_, validationErr := validator.RevalidateSealed(ctx, root, contextInputs)
	closeErr := root.Close()
	if validationErr != nil {
		return ValidationMaterialization{}, fmt.Errorf("revalidate repository change: %w", validationErr)
	}
	if closeErr != nil {
		return ValidationMaterialization{}, closeErr
	}
	record, err := readChangeRecord(ctx, candidateRoot)
	if err != nil {
		return ValidationMaterialization{}, err
	}
	if len(record.Subjects) != 1 || record.Subjects[0].Input != baseInput || record.Subjects[0].Digest != base.Digest {
		return ValidationMaterialization{}, fmt.Errorf("repository change does not bind the declared exact base")
	}
	if record.Body.Representation != "git-tree" {
		return ValidationMaterialization{}, fmt.Errorf("repository validation materialization supports only canonical git-tree candidates")
	}
	payloadRoot, err := os.OpenRoot(candidateRoot)
	if err != nil {
		return ValidationMaterialization{}, err
	}
	payload, err := payloadRoot.Open(record.Body.Payload.Path)
	_ = payloadRoot.Close()
	if err != nil {
		return ValidationMaterialization{}, err
	}
	resultTree, captureErr := canonicalizer.Capture(ctx, payload)
	closePayloadErr := payload.Close()
	if captureErr != nil || closePayloadErr != nil {
		return ValidationMaterialization{}, fmt.Errorf("capture result tree: %w", captureErr)
	}
	defer resultTree.Close()
	if resultTree.Digest != record.Body.Payload.Digest {
		return ValidationMaterialization{}, fmt.Errorf("result tree payload does not match sealed digest")
	}
	if err := copyValidationTree(resultTree.Root, workspace); err != nil {
		return ValidationMaterialization{}, err
	}
	diff, err := repodiff.DeriveRepositoryDiff(ctx, resultTree.Root, record.Body.BaseSHA, record.Body.ResultTree)
	if err != nil {
		return ValidationMaterialization{}, fmt.Errorf("derive exact repository diff: %w", err)
	}
	paths := make([]string, 0, len(diff.Files)*2)
	seen := map[string]struct{}{}
	for _, file := range diff.Files {
		for _, path := range []string{file.Path, file.PreviousPath} {
			if path != "" {
				if _, exists := seen[path]; !exists {
					seen[path] = struct{}{}
					paths = append(paths, path)
				}
			}
		}
	}
	sort.Strings(paths)
	return ValidationMaterialization{ChangedPaths: paths}, nil
}

func copyValidationTree(source, destination string) error {
	entries, err := os.ReadDir(destination)
	if err != nil || len(entries) != 0 {
		return fmt.Errorf("validation workspace must start empty")
	}
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(destination, rel)
		if entry.Type()&os.ModeSymlink != 0 {
			value, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(value, target)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported repository entry %q", rel)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, in)
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
}
