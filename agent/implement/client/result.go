package client

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/concourse/concourse/agent/capture"
	"github.com/concourse/concourse/agent/detached"
	"github.com/concourse/concourse/agent/implement"
)

// Change is a published change verified against its Run: the summary and the
// exact patch it describes.
type Change struct {
	Summary *implement.Summary `json:"summary"`
	Patch   string             `json:"patch"`
	// summary.json exactly as the Run published it.
	raw []byte
}

func (c *Change) RunID() int {
	if c.Summary.RunID == nil {
		return 0
	}
	return *c.Summary.RunID
}

// Markdown renders the change for a person.
func (c *Change) Markdown() string { return c.Summary.Markdown([]byte(c.Patch)) }

// Result retrieves the change a completed Run published. The shared client
// verifies the archive against the Run's immutable binding before the change
// is parsed, and the summary's run_id against the Run after. The patch must
// match the summary's digest and touch exactly the files it lists.
func (c *Client) Result(ctx context.Context, handle Handle, name string) (*Change, error) {
	parsed, err := c.Client.Result(ctx, handle, name, parseChange)
	if err != nil {
		return nil, err
	}
	return parsed.(*Change), nil
}

func parseChange(tree fs.FS) (detached.RunIdentified, error) {
	patch, err := detached.ReadResultFile(tree, implement.PatchFile, implement.MaxPatchBytes)
	if err != nil {
		return nil, err
	}
	raw, err := detached.ReadResultFile(tree, implement.SummaryFile, capture.MaxFileBytes)
	if err != nil {
		return nil, err
	}
	summary, err := implement.ParsePublishedChange(raw, patch)
	if err != nil {
		return nil, err
	}
	if summary.RunID == nil {
		return nil, errors.New("change does not name the Run that produced it")
	}
	return &Change{Summary: summary, Patch: string(patch), raw: raw}, nil
}

// WriteDir publishes the verified change.patch and summary.json, byte for byte
// as the Run published them, into output, which must not exist. The files are
// staged beside it and published with one rename, then read back the way
// `jb implement apply --result-dir` reads them.
func (c *Change) WriteDir(output string) error {
	if output == "" {
		return errors.New("an output directory is required")
	}
	if c.raw == nil {
		return errors.New("only a change retrieved from a Run can be written")
	}
	output, err := filepath.Abs(output)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(output); err == nil {
		return errors.New("output must not exist; a retrieved change is never overwritten")
	} else if !os.IsNotExist(err) {
		return err
	}
	stage, err := os.MkdirTemp(filepath.Dir(output), ".implement-result-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	for name, data := range map[string][]byte{implement.SummaryFile: c.raw, implement.PatchFile: []byte(c.Patch)} {
		if err := os.WriteFile(filepath.Join(stage, name), data, 0600); err != nil {
			return err
		}
	}
	if err := os.Rename(stage, output); err != nil {
		return err
	}
	summary, _, err := implement.ReadResult(output)
	if err != nil {
		return fmt.Errorf("written change does not read back: %w", err)
	}
	if summary.PatchDigest != c.Summary.PatchDigest {
		return errors.New("written change does not read back")
	}
	return nil
}
