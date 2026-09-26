// Package review captures committed changes and validates inspection-only reviews.
// It has no dependency on JetBridge's scheduler, database or artifact runtime.
package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/concourse/concourse/agent/capture"
)

const (
	inputVersion   = "review-input/v1"
	maxFileBytes   = capture.MaxFileBytes
	maxBundleBytes = capture.MaxTotalBytes
)

type CaptureOptions struct{ Repo, Base, Head, Plan, Output string }

// File is a captured leaf on the base or head side.
type File = capture.File

type Manifest struct {
	Version    string  `json:"version"`
	BaseCommit string  `json:"base_commit"`
	HeadCommit string  `json:"head_commit"`
	Files      []File  `json:"files"`
	DiffDigest string  `json:"diff_digest"`
	PlanDigest *string `json:"plan_digest"`
}

type Bundle struct {
	Dir      string
	Manifest Manifest
	Digest   string
}

func digest(data []byte) string        { return capture.Digest(data) }
func safePath(name string) bool        { return capture.SafePath(name) }
func within(parent, child string) bool { return capture.Within(parent, child) }

func readFileBounded(name string, limit int64) ([]byte, error) {
	return capture.ReadFileBounded(name, limit)
}

func readRootFile(root *os.Root, name string, limit int64) ([]byte, error) {
	return capture.ReadRootFile(root, name, limit)
}

func decodeStrict(data []byte, value any) error { return capture.DecodeStrict(data, value) }

// gitEnvironment is kept for tests that build fixtures under the same Git
// isolation as capture.
func gitEnvironment() []string { return capture.GitEnvironment() }

// Capture publishes a new directory only after both trees and optional plan are
// captured. Output must not exist and must be outside the submitted worktree.
func Capture(ctx context.Context, opts CaptureOptions) (*Bundle, error) {
	if opts.Base == "" || opts.Repo == "" || opts.Output == "" {
		return nil, errors.New("repo, base and output are required")
	}
	if opts.Head == "" {
		opts.Head = "HEAD"
	}
	repo, err := capture.Repository(ctx, opts.Repo)
	if err != nil {
		return nil, err
	}
	output, parent, err := capture.Output(repo, opts.Output)
	if err != nil {
		return nil, err
	}
	if err := capture.RequireClean(ctx, repo); err != nil {
		return nil, err
	}
	base, err := capture.ResolveCommit(ctx, repo, opts.Base)
	if err != nil {
		return nil, errors.New("base and head must resolve to commits")
	}
	head, err := capture.ResolveCommit(ctx, repo, opts.Head)
	if err != nil {
		return nil, errors.New("base and head must resolve to commits")
	}
	stage, err := os.MkdirTemp(parent, ".review-input-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)
	m := Manifest{Version: inputVersion, BaseCommit: base, HeadCommit: head, Files: []File{}}
	var total int64
	for _, tree := range []struct{ side, commit string }{{"base", base}, {"head", head}} {
		files, err := capture.CaptureTree(ctx, repo, tree.commit, tree.side, stage, &total)
		if err != nil {
			return nil, err
		}
		m.Files = append(m.Files, files...)
	}
	diff, err := capture.GitOutput(ctx, repo, "diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--binary", "--full-index", "--no-color", "--diff-algorithm=myers", "--src-prefix=a/", "--dst-prefix=b/", base, head, "--")
	if err != nil {
		return nil, err
	}
	m.DiffDigest = digest(diff)
	if err := os.WriteFile(filepath.Join(stage, "change.diff"), diff, 0600); err != nil {
		return nil, err
	}
	if opts.Plan != "" {
		b, err := readFileBounded(opts.Plan, maxFileBytes)
		if err != nil {
			return nil, err
		}
		if !capture.Text(b) {
			return nil, errors.New("plan must be UTF-8 text")
		}
		d := digest(b)
		m.PlanDigest = &d
		if err := os.WriteFile(filepath.Join(stage, "plan.md"), b, 0600); err != nil {
			return nil, err
		}
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(stage, "manifest.json"), data, 0600); err != nil {
		return nil, err
	}
	bundle, err := LoadBundle(stage)
	if err != nil {
		return nil, err
	}
	if err := os.Rename(stage, output); err != nil {
		return nil, err
	}
	bundle.Dir = output
	return bundle, nil
}

// LoadBundle verifies the entire closed input tree, including its inventory,
// hashes and types. A bundle cannot smuggle auxiliary files into the worker.
func LoadBundle(dir string) (*Bundle, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	data, err := readRootFile(root, "manifest.json", maxFileBytes)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := decodeStrict(data, &m); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	if m.Version != inputVersion || !capture.CommitPattern.MatchString(m.BaseCommit) || !capture.CommitPattern.MatchString(m.HeadCommit) || m.Files == nil {
		return nil, errors.New("invalid manifest")
	}
	inv := capture.Inventory{Files: map[string]capture.Entry{"change.diff": {Digest: m.DiffDigest, Size: -1, Limit: maxBundleBytes}}, Dirs: []string{"base", "head"}}
	if m.PlanDigest != nil {
		inv.Files["plan.md"] = capture.Entry{Digest: *m.PlanDigest, Size: -1}
	}
	last := ""
	for _, file := range m.Files {
		key := file.Side + "/" + file.Path
		if (file.Side != "base" && file.Side != "head") || !safePath(file.Path) || key <= last || file.Size < 0 || file.Size > maxFileBytes || !capture.SupportedMode(file.Mode) {
			return nil, errors.New("invalid manifest file")
		}
		last = key
		inv.Files[key] = capture.Entry{Digest: file.Digest, Size: file.Size}
	}
	if err := capture.Verify(root, inv); err != nil {
		return nil, err
	}
	canonical, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return &Bundle{Dir: dir, Manifest: m, Digest: digest(canonical)}, nil
}
