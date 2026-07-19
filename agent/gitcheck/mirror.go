// Package gitcheck maintains bare --mirror clones on the web node and
// answers the merge/ancestor/human-delta/patch-id/diff questions the
// outcome watcher needs — native repo checking, no SCM webhooks
// (shared-contracts §1.11.1, spec §9).
//
// Honesty note: this is the THIRD git-credential/repo-cache system in the
// platform, after (1) the harvest push path (agent/harvest/runner.go:
// in-pod push-by-sha with a temp GIT_ASKPASS helper) and (2) the dispatch
// git resource (the pipeline's git-resource checkout of the ticket repo in
// the worker container). It is kept separate because its shape is
// different: web-node-resident READ-ONLY mirrors polled by the watcher,
// versus an in-pod push at harvest time, versus a per-build resource
// checkout. Credential handling here reuses the harvest GIT_ASKPASS
// pattern by pattern, not by import (https-only, temp credential helper,
// token never on argv); consolidation is future work.
package gitcheck

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Auth carries optional https fetch credentials, injected via a temp
// GIT_ASKPASS helper (never argv), matching harvest push (§8.3).
type Auth struct {
	Username string
	Token    string
}

// Mirror is a bare --mirror clone of one repo's origin.
type Mirror struct {
	dir  string // the bare git dir
	repo string // canonical slug
	url  string
	auth Auth
}

// OpenMirror ensures a --mirror clone of url exists under cacheDir/<slug>.git,
// cloning it on first use. slug is used only for the on-disk directory name.
func OpenMirror(cacheDir, repo, url string, auth Auth) (*Mirror, error) {
	safe := strings.NewReplacer("/", "_", ":", "_", "@", "_").Replace(repo)
	dir := filepath.Join(cacheDir, safe+".git")
	m := &Mirror{dir: dir, repo: repo, url: url, auth: auth}
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if err := os.MkdirAll(cacheDir, 0o700); err != nil {
			return nil, err
		}
		if _, err := m.runNetwork(cacheDir, "clone", "--mirror", url, dir); err != nil {
			return nil, fmt.Errorf("clone --mirror %s: %w", repo, err)
		}
	} else if err != nil {
		return nil, err
	}
	return m, nil
}

// Fetch refreshes all refs, pruning deleted ones ("polite": one call per tick).
func (m *Mirror) Fetch() error {
	_, err := m.runNetwork(m.dir, "fetch", "--prune", "origin", "+refs/*:refs/*")
	return err
}

// IsAncestor reports whether sha is reachable from refs/heads/<branch>.
func (m *Mirror) IsAncestor(sha, branch string) (bool, error) {
	cmd := m.command(m.dir, "merge-base", "--is-ancestor", sha, "refs/heads/"+branch)
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
		return false, nil // definitively not an ancestor
	}
	return false, fmt.Errorf("is-ancestor: %w", err)
}

// BranchHead returns the remote head of branch, or "" (not an error) when
// the ref is absent — the outcome watcher's fallback pushed_sha source.
func (m *Mirror) BranchHead(branch string) (string, error) {
	cmd := m.command(m.dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return "", nil // ref absent — the caller treats this as "no fallback"
		}
		return "", fmt.Errorf("branch-head %s: %w", branch, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// MergePoint describes how pushedSha landed on the target branch.
type MergePoint struct {
	Merged        bool
	MergedSha     string // the merge commit (or fast-forward tip / pushedSha)
	TipAtMerge    string // the branch tip at the moment it merged (for the delta window)
	FastForwarded bool   // true when no merge commit was found (ff / rebase-merge onto target)
}

// MergePoint resolves the merge commit that brought pushedSha into the TARGET
// branch (the `branch` arg is the target, e.g. "main"). Precondition:
// IsAncestor(pushedSha, branch) is true. Note: MergePoint does NOT know the
// agent branch's name, so for a fast-forward it can only fall back to
// pushedSha for TipAtMerge; the §1.11.1 "agent branch remote head" refinement
// is applied by Detect (which owns the agent branch name) — see FastForwarded.
func (m *Mirror) MergePoint(pushedSha, branch string) (MergePoint, error) {
	// oldest merge commit on the ancestry path from pushedSha to the branch head
	out, err := m.run(m.dir, "rev-list", "--ancestry-path", "--merges", "--reverse",
		pushedSha+"..refs/heads/"+branch)
	if err != nil {
		return MergePoint{}, err
	}
	lines := nonEmptyLines(out)
	if len(lines) == 0 {
		// Fast-forward: no merge commit on the ancestry path. With only the
		// target branch in hand, the documented fallback is pushedSha itself
		// (§1.11.1: "the agent branch's remote head if the branch still
		// exists, else pushed_sha"). Detect refines TipAtMerge to the agent
		// branch head when FastForwarded is set.
		return MergePoint{Merged: true, MergedSha: pushedSha, TipAtMerge: pushedSha, FastForwarded: true}, nil
	}
	mergeCommit := lines[0]
	tip := pushedSha
	// second parent of the merge commit, when it descends from pushedSha, is
	// the branch tip that was merged.
	if parents, err := m.run(m.dir, "rev-list", "--parents", "-n", "1", mergeCommit); err == nil {
		fields := strings.Fields(parents)
		if len(fields) >= 3 {
			second := fields[2]
			if descends, _ := m.commitContains(second, pushedSha); descends {
				tip = second
			}
		}
	}
	return MergePoint{Merged: true, MergedSha: mergeCommit, TipAtMerge: tip}, nil
}

// commitContains reports whether ancestor is reachable from commit.
func (m *Mirror) commitContains(commit, ancestor string) (bool, error) {
	cmd := m.command(m.dir, "merge-base", "--is-ancestor", ancestor, commit)
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

// Delta is the human-touch delta over a commit window.
type Delta struct {
	CommitCount  int
	LinesAdded   int
	LinesDeleted int
}

// HumanDelta sums numstat lines of non-bot commits in pushedSha..tip
// (first-parent walk, merge commits excluded). Decision 18 / §1.11.1.
func (m *Mirror) HumanDelta(pushedSha, tip string) (Delta, error) {
	if pushedSha == "" || tip == "" || pushedSha == tip {
		return Delta{}, nil
	}
	// list non-merge commits on the first-parent path, with author name.
	out, err := m.run(m.dir, "log", "--first-parent", "--no-merges",
		"--format=%H%x1f%an", pushedSha+".."+tip)
	if err != nil {
		return Delta{}, err
	}
	var d Delta
	for _, line := range nonEmptyLines(out) {
		parts := strings.SplitN(line, "\x1f", 2)
		if len(parts) != 2 {
			continue
		}
		sha, author := parts[0], parts[1]
		if author == BotAuthor {
			continue
		}
		d.CommitCount++
		added, deleted, err := m.numstat(sha)
		if err != nil {
			return Delta{}, err
		}
		d.LinesAdded += added
		d.LinesDeleted += deleted
	}
	return d, nil
}

// BotAuthor mirrors outcomes.BotAuthor without importing that package
// (gitcheck is a leaf). Kept identical to §8.3.
const BotAuthor = "concourse-agent[bot]"

func (m *Mirror) numstat(sha string) (added, deleted int, err error) {
	out, err := m.run(m.dir, "show", "--numstat", "--format=", sha)
	if err != nil {
		return 0, 0, err
	}
	for _, line := range nonEmptyLines(out) {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		a, aerr := strconv.Atoi(fields[0]) // "-" for binary => Atoi fails => 0
		d, derr := strconv.Atoi(fields[1])
		if aerr == nil {
			added += a
		}
		if derr == nil {
			deleted += d
		}
	}
	return added, deleted, nil
}

// PatchMatch is a squash-merge patch-id hit.
type PatchMatch struct {
	Found bool
	Sha   string
}

// PatchIDMatch compares the combined patch base..branchTip against the
// patch-ids of the newest scanLimit first-parent commits on branch.
func (m *Mirror) PatchIDMatch(base, branchTip, branch string, scanLimit int) (PatchMatch, error) {
	if base == "" || branchTip == "" {
		return PatchMatch{}, nil
	}
	wantID, err := m.combinedPatchID(base, branchTip)
	if err != nil || wantID == "" {
		return PatchMatch{}, err
	}
	shas, err := m.run(m.dir, "rev-list", "--first-parent", "-n", strconv.Itoa(scanLimit),
		"refs/heads/"+branch)
	if err != nil {
		return PatchMatch{}, err
	}
	for _, sha := range nonEmptyLines(shas) {
		id, err := m.commitPatchID(sha)
		if err != nil || id == "" {
			continue
		}
		if id == wantID {
			return PatchMatch{Found: true, Sha: sha}, nil
		}
	}
	return PatchMatch{}, nil
}

func (m *Mirror) combinedPatchID(base, tip string) (string, error) {
	diff, err := m.run(m.dir, "diff", base+".."+tip)
	if err != nil {
		return "", err
	}
	return m.patchIDOf(diff)
}

func (m *Mirror) commitPatchID(sha string) (string, error) {
	diff, err := m.run(m.dir, "show", sha)
	if err != nil {
		return "", err
	}
	return m.patchIDOf(diff)
}

func (m *Mirror) patchIDOf(diff string) (string, error) {
	if strings.TrimSpace(diff) == "" {
		return "", nil
	}
	cmd := m.command(m.dir, "patch-id", "--stable")
	cmd.Stdin = strings.NewReader(diff)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return "", nil
	}
	return fields[0], nil
}

// run executes a LOCAL git plumbing command in dir and returns trimmed stdout.
func (m *Mirror) run(dir string, args ...string) (string, error) {
	return capture(m.command(dir, args...), args)
}

// runNetwork executes a network-touching git command (clone/fetch) with the
// optional https credential helper attached.
func (m *Mirror) runNetwork(dir string, args ...string) (string, error) {
	cmd := m.command(dir, args...)
	if m.auth.Token != "" && strings.HasPrefix(m.url, "https://") {
		askpass, cleanup, err := writeAskpass(m.auth)
		if err != nil {
			return "", fmt.Errorf("git credentials: %w", err)
		}
		defer cleanup()
		cmd.Env = append(cmd.Env, "GIT_ASKPASS="+askpass)
	}
	return capture(cmd, args)
}

func capture(cmd *exec.Cmd, args []string) (string, error) {
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("git %v: %w: %s", args, err, ee.Stderr)
		}
		return "", fmt.Errorf("git %v: %w", args, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// command builds a git exec.Cmd with prompts disabled; credentials are only
// ever attached by runNetwork, via a temp GIT_ASKPASS helper (never argv),
// matching harvest §8.3.
func (m *Mirror) command(dir string, args ...string) *exec.Cmd {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	return cmd
}

// writeAskpass materializes a GIT_ASKPASS helper answering username /
// password prompts from the configured Auth (https-only; the token never
// appears on any argv). Same pattern as the harvest runner's copy — reused
// by pattern, not by import (that one is push-scoped and in-pod).
func writeAskpass(auth Auth) (path string, cleanup func(), err error) {
	username := auth.Username
	if username == "" {
		username = "x-access-token"
	}
	dir, err := os.MkdirTemp("", "gitcheck-askpass")
	if err != nil {
		return "", nil, err
	}
	script := fmt.Sprintf("#!/bin/sh\ncase \"$1\" in\n  Username*) printf '%%s' '%s';;\n  *) printf '%%s' '%s';;\nesac\n",
		strings.ReplaceAll(username, "'", ""), strings.ReplaceAll(strings.TrimSpace(auth.Token), "'", ""))
	path = filepath.Join(dir, "askpass.sh")
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		os.RemoveAll(dir)
		return "", nil, err
	}
	return path, func() { os.RemoveAll(dir) }, nil
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, strings.TrimSpace(l))
		}
	}
	return out
}
