package workflow

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ManifestFromDir builds a Manifest from a workflow source directory:
// fly's packaging half of the import pipeline (design 2026-07-17 §3 —
// fly packages, the server compiles). Symlinks to files AND directories
// are dereferenced — the sharing mechanism across workflows in one repo
// (develop/skills/tdd -> ../../lib/skills/tdd) — and hidden entries
// never reach the manifest. True cycles (a symlink to an ancestor) are
// detected via the recursion stack; diamonds (two names resolving to
// one target) are legal and simply duplicate content under both paths.
func ManifestFromDir(dir string) (Manifest, error) {
	m := Manifest{}
	if err := walkInto(dir, "", m, map[string]bool{}); err != nil {
		return nil, err
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return m, nil
}

func walkInto(dir, rel string, m Manifest, stack map[string]bool) error {
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", dir, err)
	}
	if stack[real] {
		return fmt.Errorf("workflow: symlink cycle at %s (resolves to already-visited %s)", dir, real)
	}
	stack[real] = true
	defer delete(stack, real)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue // hidden files never reach the manifest (design §3)
		}
		full := filepath.Join(dir, e.Name())
		relPath := e.Name()
		if rel != "" {
			relPath = rel + "/" + e.Name()
		}
		info, err := os.Stat(full) // follows symlinks
		if err != nil {
			return fmt.Errorf("stat %s: %w", full, err)
		}
		if info.IsDir() {
			if err := walkInto(full, relPath, m, stack); err != nil {
				return err
			}
			continue
		}
		if len(m) >= MaxManifestFiles {
			return fmt.Errorf("workflow: %s exceeds %d files", dir, MaxManifestFiles)
		}
		content, err := os.ReadFile(full) // follows symlinks
		if err != nil {
			return err
		}
		m[relPath] = string(content)
	}
	return nil
}

// DiscoverWorkflowDirs returns the workflow source directories under
// root: root itself when it directly contains a workflow definition file
// (WorkflowFileName, falling back to LegacyWorkflowFileName), otherwise
// every immediate subdirectory that does (a multi-workflow repo).
// Sorted for deterministic multi-import order.
func DiscoverWorkflowDirs(root string) ([]string, error) {
	if hasWorkflowFile(root) {
		return []string{root}, nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	dirs := []string{}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") || !e.IsDir() {
			continue
		}
		sub := filepath.Join(root, e.Name())
		if hasWorkflowFile(sub) {
			dirs = append(dirs, sub)
		}
	}
	if len(dirs) == 0 {
		return nil, fmt.Errorf("%s: no %s (or legacy %s) in the directory or its immediate subdirectories", root, WorkflowFileName, LegacyWorkflowFileName)
	}
	sort.Strings(dirs)
	return dirs, nil
}

// hasWorkflowFile reports whether dir directly contains a workflow
// definition file, preferring WorkflowFileName and falling back to
// LegacyWorkflowFileName (design 2026-07-26 — the .yaml rename).
func hasWorkflowFile(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, WorkflowFileName)); err == nil {
		return true
	}
	_, err := os.Stat(filepath.Join(dir, LegacyWorkflowFileName))
	return err == nil
}
