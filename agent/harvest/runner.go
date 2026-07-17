package harvest

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Results is the §2.8.1 results.json payload (v0 subset: no gates, no
// judge — those configs are refused, never skipped).
type Results struct {
	Status   string          `json:"status"` // pass | fail | error
	Metadata ResultsMetadata `json:"metadata"`
}

type ResultsMetadata struct {
	PushedBranch string `json:"pushed_branch,omitempty"`
	HeadSHA      string `json:"head_sha,omitempty"`
	Detail       string `json:"detail,omitempty"`
}

// Run executes the harvest v0 flow against workspaceDir: refuse
// unenforceable config (exit 2), verify the workspace is a committed
// git tree (F33: dirty ⇒ fail, exit 1, nothing pushed, nothing
// auto-discarded), then push-by-sha with --force-with-lease to the
// stable ticket branch (exit taxonomy per §2.8.1: 0 pass, 1 fail,
// 2 platform error). credsDir holds the mounted agent-harvest-git-<slug>
// secret files (`token`, optional `username`); empty credsDir pushes
// with the remote's own auth (file:// remotes in tests).
func Run(cfg Config, workspaceDir, credsDir string, out io.Writer) int {
	emit := func(status, detail string, meta ResultsMetadata) int {
		meta.Detail = detail
		json.NewEncoder(out).Encode(Results{Status: status, Metadata: meta})
		switch status {
		case "pass":
			return 0
		case "fail":
			return 1
		default:
			return 2
		}
	}

	// v0 refusals — these blocks validated at import and MUST NOT be
	// silently skipped (the dogfood ticket #5 pattern).
	if len(cfg.GatePolicy.Gates) > 0 || cfg.GatePolicy.OnGateFailure != "" {
		return emit("error", "gate_policy declared but harvest v0 has no gate engine (full harvest-step workstream)", ResultsMetadata{})
	}
	if cfg.Judge != nil {
		return emit("error", "judge declared but harvest v0 has no judge (full harvest-step workstream)", ResultsMetadata{})
	}
	if cfg.Push && cfg.Branch == "" {
		return emit("error", "push requires a branch", ResultsMetadata{})
	}

	git := func(args ...string) (string, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir = workspaceDir
		var buf strings.Builder
		cmd.Stdout = &buf
		cmd.Stderr = &buf
		err := cmd.Run()
		return strings.TrimSpace(buf.String()), err
	}

	// The workspace must BE a committed git tree: gates verify the
	// working tree while push delivers committed HEAD, so anything less
	// is the agent's failure, never auto-repaired.
	if _, err := git("rev-parse", "--git-dir"); err != nil {
		return emit("fail", "workspace is not a git repository — the agent must commit its work into the workspace checkout", ResultsMetadata{})
	}
	status, err := git("status", "--porcelain")
	if err != nil {
		return emit("error", "git status: "+status, ResultsMetadata{})
	}
	if status != "" {
		return emit("fail", "workspace-dirty: uncommitted changes present (F33) — commit or clean up; nothing was pushed:\n"+status, ResultsMetadata{})
	}
	head, err := git("rev-parse", "HEAD")
	if err != nil {
		return emit("fail", "workspace has no commits: "+head, ResultsMetadata{})
	}

	meta := ResultsMetadata{HeadSHA: head}
	if !cfg.Push {
		return emit("pass", "", meta)
	}

	if _, err := git("remote", "get-url", "origin"); err != nil {
		return emit("fail", "workspace has no origin remote to push to", ResultsMetadata{HeadSHA: head})
	}

	pushArgs := []string{
		"push",
		"--force-with-lease=refs/heads/" + cfg.Branch,
		"origin",
		head + ":refs/heads/" + cfg.Branch,
	}
	cmd := exec.Command("git", pushArgs...)
	cmd.Dir = workspaceDir
	cmd.Env = os.Environ()
	if credsDir != "" {
		askpass, cleanup, err := writeAskpass(credsDir)
		if err != nil {
			return emit("error", "git credentials: "+err.Error(), ResultsMetadata{HeadSHA: head})
		}
		defer cleanup()
		cmd.Env = append(cmd.Env, "GIT_ASKPASS="+askpass, "GIT_TERMINAL_PROMPT=0")
	}
	var pushOut strings.Builder
	cmd.Stdout = &pushOut
	cmd.Stderr = &pushOut
	if err := cmd.Run(); err != nil {
		// Auth/network/lease failures are platform faults (the lease only
		// trips on a concurrent harvest, which correctly errors).
		return emit("error", "git push failed: "+pushOut.String(), ResultsMetadata{HeadSHA: head})
	}

	meta.PushedBranch = cfg.Branch
	return emit("pass", "", meta)
}

// writeAskpass materializes a GIT_ASKPASS helper answering username /
// password prompts from the mounted secret files. The token never
// touches argv or logs; it flows only through the helper's stdout into
// git.
func writeAskpass(credsDir string) (path string, cleanup func(), err error) {
	token, err := os.ReadFile(filepath.Join(credsDir, "token"))
	if err != nil {
		return "", nil, fmt.Errorf("read token: %w", err)
	}
	username := "x-access-token"
	if u, err := os.ReadFile(filepath.Join(credsDir, "username")); err == nil && len(strings.TrimSpace(string(u))) > 0 {
		username = strings.TrimSpace(string(u))
	}

	dir, err := os.MkdirTemp("", "harvest-askpass")
	if err != nil {
		return "", nil, err
	}
	script := fmt.Sprintf("#!/bin/sh\ncase \"$1\" in\n  Username*) printf '%%s' '%s';;\n  *) printf '%%s' '%s';;\nesac\n",
		strings.ReplaceAll(username, "'", ""), strings.ReplaceAll(strings.TrimSpace(string(token)), "'", ""))
	path = filepath.Join(dir, "askpass.sh")
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		os.RemoveAll(dir)
		return "", nil, err
	}
	return path, func() { os.RemoveAll(dir) }, nil
}
