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
	// MaxFindingsBytes bounds findings.json. It holds findings from one
	// review/v1 report, which is itself at most 16 MiB.
	MaxFindingsBytes = capture.MaxFileBytes
)

// Optional snapshot files: a previous change this one builds on, and review
// findings it addresses.
const (
	PriorFile    = "prior.patch"
	FindingsFile = "findings.json"
)

// Manifest is the canonical description of a snapshot. Its JSON encoding is
// digested, so field order and tags are part of the contract. The optional
// fields are omitted, never null, when absent, so a snapshot without a prior
// change or findings keeps the digest it had before they existed.
type Manifest struct {
	Version    string `json:"version"`
	BaseCommit string `json:"base_commit"`
	// Files is the base tree; every Side is "base".
	Files       []capture.File `json:"files"`
	BriefDigest string         `json:"brief_digest"`
	// PriorPatchDigest is the digest of prior.patch, a verified change
	// against the same base. PriorRun is the ID of the Run that published
	// it, when it was verified against one.
	PriorPatchDigest *string `json:"prior_patch_digest,omitempty"`
	PriorRun         *int    `json:"prior_run,omitempty"`
	// FindingsDigest is the digest of findings.json, the findings of a
	// verified review/v1 result. FindingsRun is the review Run's ID.
	FindingsDigest *string `json:"findings_digest,omitempty"`
	FindingsRun    *int    `json:"findings_run,omitempty"`
}

// Snapshot is one tree at a base commit plus the brief, sealed on disk as
// manifest.json, brief.md and base/…, with prior.patch and findings.json when
// the manifest names them.
type Snapshot struct {
	Dir      string
	Manifest Manifest
	Digest   string
}

type CaptureOptions struct {
	Repo, Base, Brief, Output string
	// Prior is a verified change to build on. Its base must be Base.
	Prior *PriorChange
	// Findings are review findings to address.
	Findings *ReviewFindings
}

// PriorChange is a published change already verified by its reader: from a
// Run through the detached client, or from a result directory by ReadResult.
// CaptureSnapshot checks it again against the captured base.
type PriorChange struct {
	Summary *Summary
	Patch   []byte
	// RunID is the Run the change was verified against, or nil for a change
	// read from disk, whose run_id nothing has confirmed.
	RunID *int
}

// ReviewFindings is the findings array of a review/v1 report retrieved and
// verified from a review Run, in the report's own published encoding, with
// the reviewed range from the report's provenance. The implement workload
// depends on that published schema, not on the review package: it checks
// only that the findings are a non-empty JSON array of objects and that the
// reviewed range starts or ends at the snapshot's base, so their locations
// describe this repository line.
type ReviewFindings struct {
	JSON  []byte
	RunID int
	// ReviewedBase and ReviewedHead are the report's base_commit and
	// head_commit.
	ReviewedBase, ReviewedHead string
}

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
	if opts.Prior != nil {
		if err := checkPrior(opts.Prior, base, stage, files); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(stage, PriorFile), opts.Prior.Patch, 0600); err != nil {
			return nil, err
		}
		digest := capture.Digest(opts.Prior.Patch)
		m.PriorPatchDigest, m.PriorRun = &digest, opts.Prior.RunID
	}
	if opts.Findings != nil {
		if opts.Findings.RunID < 1 {
			return nil, errors.New("review findings must name the review Run they were verified against")
		}
		if opts.Findings.ReviewedBase != base && opts.Findings.ReviewedHead != base {
			return nil, fmt.Errorf("review Run %d reviewed %s..%s, which neither starts nor ends at the requested base %s", opts.Findings.RunID, opts.Findings.ReviewedBase, opts.Findings.ReviewedHead, base)
		}
		if err := checkFindings(opts.Findings.JSON); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(stage, FindingsFile), opts.Findings.JSON, 0600); err != nil {
			return nil, err
		}
		digest, run := capture.Digest(opts.Findings.JSON), opts.Findings.RunID
		m.FindingsDigest, m.FindingsRun = &digest, &run
	}
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

// checkPrior refuses a prior change made against a different base, one whose
// patch does not match its summary, and one that does not apply exactly to
// the captured base tree.
func checkPrior(prior *PriorChange, base, stage string, files []capture.File) error {
	if prior.Summary == nil {
		return errors.New("prior change has no summary")
	}
	if prior.RunID != nil && (*prior.RunID < 1 || prior.Summary.RunID == nil || *prior.Summary.RunID != *prior.RunID) {
		return errors.New("prior change does not name the Run it was verified against")
	}
	if prior.Summary.Provenance.BaseCommit != base {
		return fmt.Errorf("prior change was made against %s, not the requested base %s", prior.Summary.Provenance.BaseCommit, base)
	}
	if capture.Digest(prior.Patch) != prior.Summary.PatchDigest {
		return errors.New("prior change.patch does not match its summary's patch digest")
	}
	if len(prior.Patch) == 0 {
		return errors.New("prior change is empty; there is nothing to build on")
	}
	sections, err := ParsePatch(prior.Patch)
	if err != nil {
		return fmt.Errorf("prior change: %w", err)
	}
	root, err := os.OpenRoot(stage)
	if err != nil {
		return err
	}
	defer root.Close()
	tree, err := readTree(root, files)
	if err != nil {
		return err
	}
	if _, err := ApplyPatch(tree, sections); err != nil {
		return fmt.Errorf("prior change does not apply to the base: %w", err)
	}
	return nil
}

// checkFindings accepts a non-empty JSON array of objects, UTF-8, with
// nothing after it. Their shape is review/v1's, verified by the review client
// before capture; this package does not interpret them.
func checkFindings(data []byte) error {
	if len(data) > MaxFindingsBytes || !capture.Text(data) {
		return errors.New("findings must be UTF-8 JSON of at most 16 MiB")
	}
	var findings []map[string]json.RawMessage
	if err := capture.DecodeStrict(data, &findings); err != nil {
		return errors.New("findings must be one JSON array of objects")
	}
	if len(findings) == 0 {
		return errors.New("review has no findings to address")
	}
	for _, f := range findings {
		if f == nil {
			return errors.New("findings must be a JSON array of objects")
		}
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
	// A Run is named only for an input that exists, and findings always name
	// the review Run they came from.
	if (m.PriorRun != nil && (m.PriorPatchDigest == nil || *m.PriorRun < 1)) ||
		(m.PriorPatchDigest != nil && len(*m.PriorPatchDigest) != 64) ||
		((m.FindingsDigest == nil) != (m.FindingsRun == nil)) ||
		(m.FindingsDigest != nil && (len(*m.FindingsDigest) != 64 || *m.FindingsRun < 1)) {
		return nil, errors.New("invalid snapshot manifest inputs")
	}
	inv := capture.Inventory{Files: map[string]capture.Entry{"brief.md": {Digest: m.BriefDigest, Size: -1, Limit: MaxBriefBytes}}, Dirs: []string{"base"}}
	if m.PriorPatchDigest != nil {
		inv.Files[PriorFile] = capture.Entry{Digest: *m.PriorPatchDigest, Size: -1, Limit: MaxPatchBytes}
	}
	if m.FindingsDigest != nil {
		inv.Files[FindingsFile] = capture.Entry{Digest: *m.FindingsDigest, Size: -1, Limit: MaxFindingsBytes}
	}
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
	if m.PriorPatchDigest != nil {
		prior, err := capture.ReadRootFile(root, PriorFile, MaxPatchBytes)
		if err != nil {
			return nil, err
		}
		if len(prior) == 0 {
			return nil, errors.New("snapshot prior change is empty")
		}
		if _, err := ParsePatch(prior); err != nil {
			return nil, fmt.Errorf("snapshot prior change: %w", err)
		}
	}
	if m.FindingsDigest != nil {
		findings, err := capture.ReadRootFile(root, FindingsFile, MaxFindingsBytes)
		if err != nil {
			return nil, err
		}
		if err := checkFindings(findings); err != nil {
			return nil, err
		}
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
	return readTree(root, s.Manifest.Files)
}

func readTree(root *os.Root, files []capture.File) (Tree, error) {
	out := make(Tree, len(files))
	for _, f := range files {
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

// input reads one optional snapshot file, checking its digest again. It
// returns nil when the manifest names no such file.
func (s *Snapshot) input(name string, digest *string, limit int64) ([]byte, error) {
	if digest == nil {
		return nil, nil
	}
	root, err := os.OpenRoot(s.Dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	b, err := capture.ReadRootFile(root, name, limit)
	if err != nil {
		return nil, err
	}
	if capture.Digest(b) != *digest {
		return nil, fmt.Errorf("%s changed after capture", name)
	}
	return b, nil
}

// Prior returns the verified prior change, or nil when there is none.
func (s *Snapshot) Prior() ([]byte, error) {
	return s.input(PriorFile, s.Manifest.PriorPatchDigest, MaxPatchBytes)
}

// Findings returns the verified review findings, or nil when there are none.
func (s *Snapshot) Findings() ([]byte, error) {
	return s.input(FindingsFile, s.Manifest.FindingsDigest, MaxFindingsBytes)
}

// start is the tree the session begins from: the base, with the prior change
// applied when there is one. The published patch is still against the base,
// so it carries the prior change forward and applies on its own.
func (s *Snapshot) start(base Tree) (Tree, error) {
	prior, err := s.Prior()
	if err != nil || prior == nil {
		return base, err
	}
	sections, err := ParsePatch(prior)
	if err != nil {
		return nil, fmt.Errorf("prior change: %w", err)
	}
	tree, err := ApplyPatch(base, sections)
	if err != nil {
		return nil, fmt.Errorf("prior change does not apply to the base: %w", err)
	}
	return tree, nil
}
