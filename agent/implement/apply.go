package implement

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"github.com/concourse/concourse/agent/capture"
)

type ApplyOptions struct {
	Repo, ResultDir string
	// Branch defaults to impl/run-<n>, or impl/local-<patch digest prefix>
	// for a change produced outside a Run.
	Branch string
}

type Applied struct {
	Branch     string `json:"branch"`
	Commit     string `json:"commit"`
	BaseCommit string `json:"base_commit"`
	RunID      *int   `json:"run_id"`
}

// Apply turns a verified change into one commit on a new local branch whose
// parent is the change's base commit. The base..branch range is exactly what
// `jb review capture` takes.
//
// Apply never checks the branch out, and never touches the worktree, the
// current index, HEAD or any existing ref: checking out agent-authored
// content would run the repository's configured filters on it, and could
// overwrite ignored files. The change is applied by this package's own
// applier, with no fuzz, to the base commit's actual files: it may only add
// a path that does not exist (under no file or link), and only modify or
// delete a regular file. Its blobs are written without filters into a
// private index. Git runs with hooks and fsmonitor disabled and without
// global or system configuration, so nothing in the agent's change is
// executed on the developer's machine.
func Apply(ctx context.Context, opts ApplyOptions) (*Applied, error) {
	if opts.Repo == "" || opts.ResultDir == "" {
		return nil, errors.New("repo and result directory are required")
	}
	summary, patch, err := ReadResult(opts.ResultDir)
	if err != nil {
		return nil, err
	}
	if len(summary.ChangedFiles) == 0 {
		return nil, errors.New("the change is empty; there is nothing to apply")
	}
	repo, err := capture.Repository(ctx, opts.Repo)
	if err != nil {
		return nil, err
	}
	base := summary.Provenance.BaseCommit
	if _, err := capture.GitOutput(ctx, repo, "cat-file", "-e", base+"^{commit}"); err != nil {
		return nil, fmt.Errorf("base commit %s is not in this repository; fetch it first", base)
	}
	branch := opts.Branch
	if branch == "" {
		if summary.RunID != nil {
			branch = fmt.Sprintf("impl/run-%d", *summary.RunID)
		} else {
			branch = "impl/local-" + summary.PatchDigest[:12]
		}
	}
	if _, err := capture.GitOutput(ctx, repo, "check-ref-format", "--branch", branch); err != nil || strings.HasPrefix(branch, "-") {
		return nil, fmt.Errorf("invalid branch name %q", branch)
	}
	if _, err := capture.GitOutput(ctx, repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
		return nil, fmt.Errorf("branch %s already exists", branch)
	}
	ident, err := identity(ctx, repo)
	if err != nil {
		return nil, err
	}
	scratch, err := os.MkdirTemp("", "jb-implement-apply-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(scratch)
	index := "GIT_INDEX_FILE=" + filepath.Join(scratch, "index")
	git := func(stdin []byte, env []string, args ...string) (string, error) {
		c := capture.GitCommand(ctx, repo, args...)
		c.Env = append(c.Env, env...)
		c.Env = append(c.Env, index)
		if stdin != nil {
			c.Stdin = bytes.NewReader(stdin)
		}
		var out, stderr bytes.Buffer
		c.Stdout, c.Stderr = &out, &stderr
		if err := c.Run(); err != nil {
			return "", fmt.Errorf("git %s failed: %s", args[0], strings.TrimSpace(stderr.String()))
		}
		return strings.TrimSpace(out.String()), nil
	}
	updates, err := applyToBase(ctx, repo, base, patch)
	if err != nil {
		return nil, err
	}
	if _, err := git(nil, nil, "read-tree", base); err != nil {
		return nil, err
	}
	var info bytes.Buffer
	for _, u := range updates {
		oid := strings.Repeat("0", len(base))
		mode := "0"
		if u.data != nil {
			if oid, err = git(u.data, nil, "hash-object", "-w", "--no-filters", "--stdin"); err != nil {
				return nil, err
			}
			mode = u.mode
		}
		fmt.Fprintf(&info, "%s %s\t%s\x00", mode, oid, u.path)
	}
	if _, err := git(info.Bytes(), nil, "update-index", "-z", "--index-info"); err != nil {
		return nil, err
	}
	tree, err := git(nil, nil, "write-tree")
	if err != nil {
		return nil, err
	}
	message := strings.TrimSpace(summary.Summary) + "\n\n"
	if summary.RunID != nil {
		message += fmt.Sprintf("JetBridge-Run: %d\n", *summary.RunID)
	}
	message += "JetBridge-Input: " + summary.Provenance.InputDigest + "\n"
	commit, err := git([]byte(message), ident, "commit-tree", tree, "-p", base, "-F", "-")
	if err != nil {
		return nil, err
	}
	if !capture.CommitPattern.MatchString(commit) {
		return nil, errors.New("git produced an invalid commit name")
	}
	// An empty old value creates the branch only if it still does not exist.
	if _, err := git(nil, nil, "update-ref", "-m", "jb implement apply", "refs/heads/"+branch, commit, ""); err != nil {
		return nil, err
	}
	return &Applied{Branch: branch, Commit: commit, BaseCommit: base, RunID: summary.RunID}, nil
}

// identity reads the developer's configured name and email through git's
// normal configuration lookup, which only reads files. The commit itself is
// written with the isolated environment.
func identity(ctx context.Context, repo string) ([]string, error) {
	get := func(key string) (string, error) {
		c := exec.CommandContext(ctx, "git", "-C", repo, "config", "--get", key)
		out, err := c.Output()
		value := strings.TrimSpace(string(out))
		if err != nil || value == "" {
			return "", errors.New("set git user.name and user.email before applying a change")
		}
		return value, nil
	}
	name, err := get("user.name")
	if err != nil {
		return nil, err
	}
	email, err := get("user.email")
	if err != nil {
		return nil, err
	}
	return []string{"GIT_AUTHOR_NAME=" + name, "GIT_AUTHOR_EMAIL=" + email, "GIT_COMMITTER_NAME=" + name, "GIT_COMMITTER_EMAIL=" + email}, nil
}

// indexUpdate sets one path in the commit's index: data under mode, or,
// with nil data, no entry.
type indexUpdate struct {
	path, mode string
	data       []byte
}

// applyToBase applies patch to the base commit's actual files and returns
// the index updates that turn base into the result. Only the paths the patch
// names are read. It refuses what this package's applier refuses -- context
// that does not match exactly, a modified link -- and also a path the base
// holds as anything but a regular file, and an added path beneath an
// existing file, link or submodule.
func applyToBase(ctx context.Context, repo, base string, patch []byte) ([]indexUpdate, error) {
	sections, err := ParsePatch(patch)
	if err != nil {
		return nil, err
	}
	listing, err := capture.GitOutput(ctx, repo, "ls-tree", "-r", "-z", "--full-tree", base)
	if err != nil {
		return nil, err
	}
	type entry struct{ mode, kind, oid string }
	entries := map[string]entry{}
	for _, record := range bytes.Split(bytes.TrimSuffix(listing, []byte{0}), []byte{0}) {
		meta, name, ok := strings.Cut(string(record), "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 3 {
			return nil, errors.New("cannot read the base commit's tree")
		}
		entries[name] = entry{fields[0], fields[1], fields[2]}
	}
	touched := Tree{}
	for _, s := range sections {
		e, exists := entries[s.Path]
		if s.Status == "added" {
			if exists {
				return nil, fmt.Errorf("the change adds %s, which exists at the base", s.Path)
			}
			for dir := path.Dir(s.Path); dir != "."; dir = path.Dir(dir) {
				if _, ok := entries[dir]; ok {
					return nil, fmt.Errorf("the change adds %s beneath %s, which is not a directory at the base", s.Path, dir)
				}
			}
			continue
		}
		if !exists || e.kind != "blob" || (e.mode != "100644" && e.mode != "100755") {
			return nil, fmt.Errorf("the change %s %s, which is not a regular file at the base", s.Status, s.Path)
		}
		data, err := capture.GitOutput(ctx, repo, "cat-file", "blob", e.oid)
		if err != nil || len(data) > capture.MaxFileBytes {
			return nil, fmt.Errorf("cannot read %s at the base", s.Path)
		}
		touched[s.Path] = TreeFile{Data: data, Mode: e.mode}
	}
	result, err := ApplyPatch(touched, sections)
	if err != nil {
		return nil, fmt.Errorf("change does not apply to its base: %w", err)
	}
	updates := make([]indexUpdate, 0, len(sections))
	for _, s := range sections {
		f, ok := result[s.Path]
		if !ok {
			updates = append(updates, indexUpdate{path: s.Path})
			continue
		}
		if f.Mode != "100644" && f.Mode != "100755" {
			return nil, fmt.Errorf("the change gives %s an unsupported mode", s.Path)
		}
		data := f.Data
		if data == nil {
			data = []byte{}
		}
		updates = append(updates, indexUpdate{path: s.Path, mode: f.Mode, data: data})
	}
	return updates, nil
}
