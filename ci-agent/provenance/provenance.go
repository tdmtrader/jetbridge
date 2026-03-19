package provenance

import (
	"crypto/sha256"
	"fmt"
	"os"
)

// Record captures the full configuration used for a phase run,
// enabling exact reproduction of the agent invocation.
type Record struct {
	PhaseConfig    FileRef   `json:"phase_config"`
	PromptFiles    []FileRef `json:"prompt_files"`
	Model          string    `json:"model,omitempty"`
	MCPTools       []string  `json:"mcp_tools,omitempty"`
}

// FileRef identifies a file and its content hash.
type FileRef struct {
	Path string `json:"path"`
	Hash string `json:"hash"`
}

// HashFile computes the SHA256 hash of a file's contents.
func HashFile(path string) (FileRef, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return FileRef{}, fmt.Errorf("hash file %s: %w", path, err)
	}
	h := sha256.Sum256(data)
	return FileRef{
		Path: path,
		Hash: fmt.Sprintf("%x", h),
	}, nil
}

// Build constructs a provenance record from the given inputs.
func Build(phaseConfigPath string, promptPaths []string, model string, mcpTools []string) (*Record, error) {
	configRef, err := HashFile(phaseConfigPath)
	if err != nil {
		return nil, err
	}

	var promptRefs []FileRef
	for _, p := range promptPaths {
		ref, err := HashFile(p)
		if err != nil {
			return nil, err
		}
		promptRefs = append(promptRefs, ref)
	}

	return &Record{
		PhaseConfig: configRef,
		PromptFiles: promptRefs,
		Model:       model,
		MCPTools:    mcpTools,
	}, nil
}
