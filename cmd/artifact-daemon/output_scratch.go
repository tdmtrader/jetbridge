package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/concourse/concourse/hangar"
)

// validateOutputScratch refuses an output-plane scratch directory that
// overlaps the storage root. The plane sweeps its scratch of hangar-tree-*
// entries at startup, and the storage root's sweeper and registry own
// everything beneath it; two owners of one directory are two sets of rules
// deleting each other's files.
func validateOutputScratch(scratch, storage string) error {
	if scratch == "" {
		return nil
	}
	overlaps := func(a, b string) bool {
		a, b = filepath.Clean(a), filepath.Clean(b)
		return a == b || strings.HasPrefix(a, b+string(filepath.Separator)) ||
			strings.HasPrefix(b, a+string(filepath.Separator))
	}
	if overlaps(scratch, storage) {
		return fmt.Errorf("--output-scratch-dir %q and --storage-path %q must be disjoint", scratch, storage)
	}
	if err := os.MkdirAll(scratch, 0700); err != nil {
		return fmt.Errorf("create the output scratch directory: %w", err)
	}
	info, err := os.Lstat(scratch)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("--output-scratch-dir %q must be a real directory", scratch)
	}
	// Private before validated: the chart's scratch is an emptyDir, which
	// kubelet creates 0777 without the sticky bit, and the canonicalizer's
	// ValidateTempDir rightly refuses that as a temp parent. The directory is
	// the daemon's own to tighten.
	if err := os.Chmod(scratch, 0700); err != nil {
		return fmt.Errorf("set the output scratch directory permissions: %w", err)
	}
	return hangar.ValidateTempDir(scratch)
}
