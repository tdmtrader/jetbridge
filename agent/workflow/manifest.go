package workflow

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	// MaxManifestFiles bounds one workflow source tree's file count.
	MaxManifestFiles = 512
	// MaxManifestFileBytes bounds one file's content.
	MaxManifestFileBytes = 1 << 20 // 1 MiB
	// MaxManifestBytes bounds the tree's total content.
	MaxManifestBytes = 10 << 20 // 10 MiB
)

const (
	// WorkflowFileName is the preferred manifest key/filename for a
	// workflow's primary definition.
	WorkflowFileName = "workflow.yaml"
	// LegacyWorkflowFileName is the original manifest key/filename.
	// Every manifest reader must keep accepting it: existing DB rows
	// (source_manifest and pre-manifest raw-YAML columns alike) were
	// written with this key, and it is never rewritten in place.
	LegacyWorkflowFileName = "workflow.yml"
)

// Manifest is a workflow source tree: relative path -> UTF-8 content
// (design 2026-07-17 §2). Its canonical serialization is the
// content-hash provenance unit for manifest imports — versions change
// iff the source files change, stable across fly and server upgrades.
type Manifest map[string]string

// Validate checks paths and caps. Dot-prefixed segments are refused:
// fly excludes hidden files at packaging, and the server refuses them
// so nothing can smuggle e.g. a .claude/ tree past that convention.
func (m Manifest) Validate() error {
	if len(m) == 0 {
		return fmt.Errorf("workflow: manifest has no files")
	}
	if len(m) > MaxManifestFiles {
		return fmt.Errorf("workflow: manifest has %d files (max %d)", len(m), MaxManifestFiles)
	}
	total := 0
	for path, content := range m {
		if err := validateManifestPath(path); err != nil {
			return err
		}
		if len(content) > MaxManifestFileBytes {
			return fmt.Errorf("workflow: manifest file %q is %d bytes (max %d)", path, len(content), MaxManifestFileBytes)
		}
		if !utf8.ValidString(content) {
			return fmt.Errorf("workflow: manifest file %q is not valid UTF-8 (binary assets are out of scope, design §2)", path)
		}
		total += len(content)
	}
	if total > MaxManifestBytes {
		return fmt.Errorf("workflow: manifest is %d bytes total (max %d)", total, MaxManifestBytes)
	}
	return nil
}

func validateManifestPath(path string) error {
	if path == "" {
		return fmt.Errorf("workflow: manifest contains an empty path")
	}
	if strings.HasPrefix(path, "/") {
		return fmt.Errorf("workflow: manifest path %q is absolute; paths must be relative", path)
	}
	if strings.Contains(path, `\`) {
		return fmt.Errorf("workflow: manifest path %q contains a backslash; use forward slashes", path)
	}
	for _, seg := range strings.Split(path, "/") {
		switch {
		case seg == "":
			return fmt.Errorf("workflow: manifest path %q contains an empty segment", path)
		case seg == "." || seg == "..":
			return fmt.Errorf("workflow: manifest path %q contains a %q segment", path, seg)
		case strings.HasPrefix(seg, "."):
			return fmt.Errorf("workflow: manifest path %q contains hidden segment %q", path, seg)
		}
	}
	return nil
}

// resolveManifestFile validates a logical source reference and performs an
// exact map lookup. It deliberately never cleans paths or consults the local
// filesystem: a manifest is the complete source boundary at compile time.
func resolveManifestFile(m Manifest, path string) (string, error) {
	if err := validateManifestPath(path); err != nil {
		return "", err
	}
	content, found := m[path]
	if !found {
		return "", fmt.Errorf("workflow: manifest file %q is not in the manifest", path)
	}
	return content, nil
}

// DefinitionSource returns the manifest's primary workflow definition,
// preferring WorkflowFileName and falling back to LegacyWorkflowFileName so
// manifests imported under either name compile identically. Every manifest
// reader must go through this instead of indexing a literal key.
func (m Manifest) DefinitionSource() (string, bool) {
	if content, ok := m[WorkflowFileName]; ok {
		return content, true
	}
	content, ok := m[LegacyWorkflowFileName]
	return content, ok
}

// Canonical is the deterministic serialization hashed for provenance:
// JSON with sorted keys (encoding/json sorts map keys; the pinned test
// vector in manifest_test.go guards against codec drift).
func (m Manifest) Canonical() []byte {
	out, _ := json.Marshal(m) // map[string]string cannot fail to marshal
	return out
}

// Hash is hex(sha256(Canonical())) — the version identity of a
// manifest import.
func (m Manifest) Hash() string {
	return Hash(m.Canonical())
}

// Paths returns the manifest's paths, sorted, for summaries and stable
// iteration.
func (m Manifest) Paths() []string {
	out := make([]string, 0, len(m))
	for p := range m {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
