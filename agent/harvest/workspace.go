package harvest

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// DiffTruncatedMarker terminates a diff cut at maxBytes.
const DiffTruncatedMarker = "\n[diff truncated]"

func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// HeadSHA returns the workspace's HEAD commit.
func HeadSHA(dir string) (string, error) {
	return runGit(nil, dir, "rev-parse", "HEAD")
}

// BaseSHA returns the merge-base of HEAD and the target branch, preferring
// origin/<target> and falling back to a local <target> ref (§6.3: the gate
// diff base for affected_components).
func BaseSHA(dir, targetBranch string) (string, error) {
	if targetBranch == "" {
		targetBranch = "main"
	}
	if sha, err := runGit(nil, dir, "merge-base", "HEAD", "origin/"+targetBranch); err == nil {
		return sha, nil
	}
	return runGit(nil, dir, "merge-base", "HEAD", targetBranch)
}

// ChangedPaths lists paths changed between base and HEAD (base is already a
// merge-base, so two-dot equals the §6.3 three-dot semantics).
func ChangedPaths(dir, baseSHA string) ([]string, error) {
	out, err := runGit(nil, dir, "diff", "--name-only", baseSHA+"..HEAD")
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// Diff returns the base..HEAD patch text, truncated to maxBytes.
func Diff(dir, baseSHA string, maxBytes int) (string, error) {
	out, err := runGit(nil, dir, "diff", baseSHA+"..HEAD")
	if err != nil {
		return "", err
	}
	if maxBytes > 0 && len(out) > maxBytes {
		return out[:maxBytes] + DiffTruncatedMarker, nil
	}
	return out, nil
}

// Manifest is the patch manifest written to the flight dir (§2.8.1).
type Manifest struct {
	Repo    string           `json:"repo"`
	Branch  string           `json:"branch"`
	BaseSHA string           `json:"base_sha"`
	HeadSHA string           `json:"head_sha"`
	Commits []ManifestCommit `json:"commits"`
	Files   []ManifestFile   `json:"files"`
}

type ManifestCommit struct {
	SHA     string `json:"sha"`
	Author  string `json:"author"`
	Subject string `json:"subject"`
}

type ManifestFile struct {
	Path    string `json:"path"`
	Added   int    `json:"added"`
	Deleted int    `json:"deleted"`
}

// BuildManifest assembles the patch manifest for base..head.
func BuildManifest(dir, baseSHA, headSHA, repo, branch string) (*Manifest, error) {
	m := &Manifest{Repo: repo, Branch: branch, BaseSHA: baseSHA, HeadSHA: headSHA}

	logOut, err := runGit(nil, dir, "log", "--reverse", "--format=%H%x1f%an%x1f%s", baseSHA+".."+headSHA)
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(logOut, "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\x1f", 3)
		if len(parts) != 3 {
			continue
		}
		m.Commits = append(m.Commits, ManifestCommit{SHA: parts[0], Author: parts[1], Subject: parts[2]})
	}

	statOut, err := runGit(nil, dir, "diff", "--numstat", baseSHA+".."+headSHA)
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(statOut, "\n") {
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) != 3 {
			continue
		}
		added, _ := strconv.Atoi(parts[0]) // "-" (binary) parses to 0
		deleted, _ := strconv.Atoi(parts[1])
		m.Files = append(m.Files, ManifestFile{Path: parts[2], Added: added, Deleted: deleted})
	}

	return m, nil
}
