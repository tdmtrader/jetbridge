package resource

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"unicode/utf8"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

// validateMaterializationBounds accounts the complete checkout, including its
// Git object database. WalkDir and Root.Readlink never follow repository
// symlinks, so an attacker cannot shift accounting outside the checkout.
func validateMaterializationBounds(ctx context.Context, directory string, maxEntries, maxContentBytes, maxSymlinkTargetBytes int64) error {
	if ctx == nil || maxEntries <= 0 || maxContentBytes <= 0 || maxSymlinkTargetBytes <= 0 {
		return fmt.Errorf("forge-pr: materialization bounds are invalid")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return fmt.Errorf("forge-pr: open materialized repository")
	}
	defer root.Close()

	var entries, contentBytes int64
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if name == "." {
			return nil
		}
		entries++
		if entries > maxEntries || int64(len(filepath.ToSlash(name))) > snapshot.MaxSnapshotPathBytes {
			return fmt.Errorf("forge-pr: materialized repository exceeds entry bounds")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		switch {
		case info.Mode().IsRegular():
			size := info.Size()
			if size < 0 || size > maxContentBytes || contentBytes > maxContentBytes-size {
				return fmt.Errorf("forge-pr: materialized repository exceeds content bounds")
			}
			contentBytes += size
		case info.IsDir():
			return nil
		case info.Mode()&os.ModeSymlink != 0:
			target, err := root.Readlink(name)
			if err != nil {
				return err
			}
			if !utf8.ValidString(target) || int64(len(target)) > maxSymlinkTargetBytes {
				return fmt.Errorf("forge-pr: materialized repository symlink exceeds bounds")
			}
		default:
			return fmt.Errorf("forge-pr: materialized repository contains an unsupported filesystem node")
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("forge-pr: validate materialized repository bounds: %w", err)
	}
	return nil
}

// validateRepositoryEvidence reuses repository/v1's non-lossy controlled Git
// validation instead of trusting the directgit runner's deliberately
// truncated diagnostic capture as a complete index listing.
func validateRepositoryEvidence(ctx context.Context, directory string) error {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return fmt.Errorf("forge-pr: open repository evidence")
	}
	defer root.Close()
	registry, err := contracts.NewRegistry()
	if err != nil {
		return fmt.Errorf("forge-pr: initialize repository validator")
	}
	validator, err := registry.Lookup(snapshot.TypeRef("repository/v1"))
	if err != nil {
		return fmt.Errorf("forge-pr: resolve repository validator")
	}
	if _, err := validator.RevalidateSealed(ctx, root, snapshot.ValidationContext{}); err != nil {
		return fmt.Errorf("forge-pr: repository evidence is invalid: %w", err)
	}
	return nil
}
