package fix

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// ParseFixPatches parses the agent's JSON output into file patches.
func ParseFixPatches(raw []byte) ([]FilePatch, error) {
	var patches []FilePatch
	if err := json.Unmarshal(raw, &patches); err != nil {
		return nil, fmt.Errorf("parsing fix patches: %w", err)
	}

	for _, p := range patches {
		if filepath.IsAbs(p.Path) {
			return nil, fmt.Errorf("absolute path not allowed: %s", p.Path)
		}
		if strings.Contains(p.Path, "..") {
			return nil, fmt.Errorf("path traversal not allowed: %s", p.Path)
		}
	}

	return patches, nil
}
