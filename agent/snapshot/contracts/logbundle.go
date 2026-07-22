package contracts

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
)

type LogBundleMetadata struct {
	SchemaVersion string `json:"schema_version"`
	CapturedAt    string `json:"captured_at"`
	Source        string `json:"source"`
	Description   string `json:"description,omitempty"`
}

func (d LogBundleMetadata) Validate() error {
	if err := validateDocumentVersion(d.SchemaVersion); err != nil {
		return err
	}
	if err := requireStrings([]namedString{{"captured_at", d.CapturedAt}, {"source", d.Source}}); err != nil {
		return err
	}
	if _, err := time.Parse(time.RFC3339, d.CapturedAt); err != nil {
		return fmt.Errorf("captured_at must be RFC3339: %w", err)
	}
	return nil
}

type opaqueValidator struct{}

func (opaqueValidator) Validate(ctx context.Context, root *os.Root, _ snapshot.ValidationContext) (snapshot.ValidationResult, error) {
	if root == nil {
		return snapshot.ValidationResult{}, fmt.Errorf("snapshot contracts: snapshot root is required")
	}
	if err := ctx.Err(); err != nil {
		return snapshot.ValidationResult{}, err
	}
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return snapshot.ValidationResult{}, fmt.Errorf("snapshot contracts: inspect opaque tree: %w", err)
	}
	if len(entries) == 0 {
		return snapshot.ValidationResult{}, fmt.Errorf("snapshot contracts: opaque tree must be non-empty")
	}
	return snapshot.ValidationResult{}, nil
}

type logBundleValidator struct{}

func (logBundleValidator) Validate(ctx context.Context, root *os.Root, _ snapshot.ValidationContext) (snapshot.ValidationResult, error) {
	if root == nil {
		return snapshot.ValidationResult{}, fmt.Errorf("snapshot contracts: snapshot root is required")
	}
	if err := ctx.Err(); err != nil {
		return snapshot.ValidationResult{}, err
	}
	metadataFound := false
	logFiles := 0
	err := fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if name == "." || entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if name == "metadata.json" {
			if !info.Mode().IsRegular() {
				return fmt.Errorf("metadata.json must be a regular file")
			}
			metadataFound = true
			return nil
		}
		if info.Mode().IsRegular() {
			logFiles++
		}
		return nil
	})
	if err != nil {
		return snapshot.ValidationResult{}, fmt.Errorf("snapshot contracts: inspect log bundle: %w", err)
	}
	if logFiles == 0 {
		return snapshot.ValidationResult{}, fmt.Errorf("snapshot contracts: log bundle must contain at least one log regular file")
	}
	if metadataFound {
		var metadata LogBundleMetadata
		if err := decodeStrictDocument(ctx, root, "metadata.json", &metadata); err != nil {
			return snapshot.ValidationResult{}, err
		}
		if err := metadata.Validate(); err != nil {
			return snapshot.ValidationResult{}, fmt.Errorf("snapshot contracts: metadata.json: %w", err)
		}
	}
	return snapshot.ValidationResult{}, nil
}
