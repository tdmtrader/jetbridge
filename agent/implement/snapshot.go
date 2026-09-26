// Package implement is the detached implementation workload: a repository
// snapshot and a brief go in, and an edit-only agent session produces a patch
// against the snapshot's base and a summary of what it did. The patch is
// applied locally as a commit on a new branch.
//
// It has no dependency on JetBridge's scheduler, database or artifact runtime.
package implement

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/concourse/concourse/agent/capture"
)

const (
	// InputVersion names the sealed snapshot format.
	InputVersion = "implement-input/v1"
	// MaxBriefBytes bounds the brief. It is given to the model whole.
	MaxBriefBytes = 256 << 10
)

// Manifest is the canonical description of a snapshot. Its JSON encoding is
// digested, so field order and tags are part of the contract.
type Manifest struct {
	Version    string `json:"version"`
	BaseCommit string `json:"base_commit"`
	// Files is the base tree; every Side is "base".
	Files       []capture.File `json:"files"`
	BriefDigest string         `json:"brief_digest"`
}

// Snapshot is one tree at a base commit plus the brief, sealed on disk as
// manifest.json, brief.md and base/….
type Snapshot struct {
	Dir      string
	Manifest Manifest
	Digest   string
}

type CaptureOptions struct{ Repo, Base, Brief, Output string }

// CaptureSnapshot publishes a new snapshot directory only after the base tree
// and brief are captured and verified. Output must not exist and must be
// outside the repository. The worktree must be clean so no local change is
// silently left out of what the developer believes they sent.
func CaptureSnapshot(ctx context.Context, opts CaptureOptions) (*Snapshot, error) {
	if opts.Repo == "" || opts.Brief == "" || opts.Output == "" {
		return nil, errors.New("repo, brief and output are required")
	}
	if opts.Base == "" {
		opts.Base = "HEAD"
	}
	brief, err := capture.ReadFileBounded(opts.Brief, MaxBriefBytes)
	if err != nil {
		return nil, fmt.Errorf("brief: %w (at most 256 KiB)", err)
	}
	if err := checkBrief(brief); err != nil {
		return nil, err
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
		return nil, errors.New("base must resolve to a commit")
	}
	stage, err := os.MkdirTemp(parent, ".implement-input-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)
	var total int64
	files, err := capture.CaptureTree(ctx, repo, base, "base", stage, &total)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(stage, "brief.md"), brief, 0600); err != nil {
		return nil, err
	}
	m := Manifest{Version: InputVersion, BaseCommit: base, Files: files, BriefDigest: capture.Digest(brief)}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(stage, "manifest.json"), data, 0600); err != nil {
		return nil, err
	}
	snapshot, err := LoadSnapshot(stage)
	if err != nil {
		return nil, err
	}
	if err := os.Rename(stage, output); err != nil {
		return nil, err
	}
	snapshot.Dir = output
	return snapshot, nil
}

func checkBrief(brief []byte) error {
	if !capture.Text(brief) || strings.TrimSpace(string(brief)) == "" {
		return errors.New("brief must be non-empty UTF-8 text")
	}
	return nil
}

// LoadSnapshot verifies the whole closed snapshot: inventory, types, sizes and
// every digest. A snapshot cannot smuggle auxiliary files into the worker.
func LoadSnapshot(dir string) (*Snapshot, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	data, err := capture.ReadRootFile(root, "manifest.json", capture.MaxFileBytes)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := capture.DecodeStrict(data, &m); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	if m.Version != InputVersion || !capture.CommitPattern.MatchString(m.BaseCommit) || m.Files == nil || len(m.BriefDigest) != 64 {
		return nil, errors.New("invalid snapshot manifest")
	}
	inv := capture.Inventory{Files: map[string]capture.Entry{"brief.md": {Digest: m.BriefDigest, Size: -1, Limit: MaxBriefBytes}}, Dirs: []string{"base"}}
	last := ""
	for _, file := range m.Files {
		if file.Side != "base" || !capture.SafePath(file.Path) || file.Path <= last || file.Size < 0 || file.Size > capture.MaxFileBytes || !capture.SupportedMode(file.Mode) {
			return nil, errors.New("invalid snapshot manifest file")
		}
		last = file.Path
		inv.Files["base/"+file.Path] = capture.Entry{Digest: file.Digest, Size: file.Size}
	}
	if err := capture.Verify(root, inv); err != nil {
		return nil, err
	}
	brief, err := capture.ReadRootFile(root, "brief.md", MaxBriefBytes)
	if err != nil {
		return nil, err
	}
	if err := checkBrief(brief); err != nil {
		return nil, err
	}
	canonical, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return &Snapshot{Dir: dir, Manifest: m, Digest: capture.Digest(canonical)}, nil
}

// Brief returns the verified brief text.
func (s *Snapshot) Brief() ([]byte, error) {
	root, err := os.OpenRoot(s.Dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	b, err := capture.ReadRootFile(root, "brief.md", MaxBriefBytes)
	if err != nil {
		return nil, err
	}
	if capture.Digest(b) != s.Manifest.BriefDigest {
		return nil, errors.New("brief changed after capture")
	}
	return b, nil
}

// tree reads the verified base tree, checking every digest again.
func (s *Snapshot) tree() (Tree, error) {
	root, err := os.OpenRoot(s.Dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	out := make(Tree, len(s.Manifest.Files))
	for _, f := range s.Manifest.Files {
		b, err := capture.ReadRootFile(root, "base/"+f.Path, capture.MaxFileBytes)
		if err != nil {
			return nil, err
		}
		if capture.Digest(b) != f.Digest {
			return nil, errors.New("snapshot changed after capture")
		}
		out[f.Path] = TreeFile{Data: b, Mode: f.Mode}
	}
	return out, nil
}
