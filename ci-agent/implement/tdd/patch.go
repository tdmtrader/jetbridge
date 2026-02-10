package tdd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/concourse/ci-agent/implement/adapter"
)

// ApplyPatches writes file patches to the repo directory.
// Returns the list of relative file paths that were modified.
func ApplyPatches(repoDir string, patches []adapter.FilePatch) ([]string, error) {
	var modified []string
	absRepo, err := filepath.Abs(repoDir)
	if err != nil {
		return nil, err
	}

	for _, p := range patches {
		fullPath := filepath.Join(absRepo, p.Path)
		absPath, err := filepath.Abs(fullPath)
		if err != nil {
			return nil, err
		}

		// Guard against path traversal.
		if !strings.HasPrefix(absPath, absRepo+string(filepath.Separator)) && absPath != absRepo {
			return nil, fmt.Errorf("patch path %q resolves outside repo root: %s", p.Path, absPath)
		}

		if err := os.MkdirAll(filepath.Dir(absPath), 0755); err != nil {
			return nil, err
		}

		if err := os.WriteFile(absPath, []byte(p.Content), 0644); err != nil {
			return nil, err
		}

		modified = append(modified, p.Path)
	}

	return modified, nil
}
