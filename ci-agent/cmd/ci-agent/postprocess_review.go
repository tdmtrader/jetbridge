package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/concourse/ci-agent/phaseconfig"
	"github.com/concourse/concourse/agent/schema"
)

// fillReviewMetadata patches the review artifact's metadata (repo, commit,
// branch, timestamp, agent_cli, agent_model, duration_seconds) when the
// review prompt itself doesn't report them — the LLM is only asked to
// produce findings, not identify the repo it's reviewing. Downstream
// publishing requires repo/commit to key stored reviews, so this backfills
// them from git rather than trusting the model to echo them accurately.
// No-ops for fields the review.json already sets.
func fillReviewMetadata(cfg *phaseconfig.Config, outputDir, repoDir, agentCLI, agentModel string, duration time.Duration) error {
	artifactPath := findReviewArtifactPath(cfg, outputDir)
	if artifactPath == "" {
		return nil
	}

	data, err := os.ReadFile(artifactPath)
	if err != nil {
		return nil // no artifact written (e.g. step failed before writing) — nothing to patch
	}

	var review schema.ReviewOutput
	if err := json.Unmarshal(data, &review); err != nil {
		return nil // not valid JSON for this shape — leave it for downstream validation to reject
	}

	if review.Metadata.Repo == "" {
		review.Metadata.Repo = repoNameFromGitRemote(repoDir)
	}
	if review.Metadata.Commit == "" {
		review.Metadata.Commit = gitOutput(repoDir, "rev-parse", "HEAD")
	}
	if review.Metadata.Branch == "" {
		review.Metadata.Branch = gitOutput(repoDir, "rev-parse", "--abbrev-ref", "HEAD")
	}
	if review.Metadata.Timestamp == "" {
		review.Metadata.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}
	if review.Metadata.AgentCLI == "" {
		review.Metadata.AgentCLI = agentCLI
	}
	if review.Metadata.AgentModel == "" {
		review.Metadata.AgentModel = agentModel
	}
	if review.Metadata.DurationSec == 0 {
		review.Metadata.DurationSec = int(duration.Seconds())
	}

	synthesizeFindingIDs(&review)

	patched, err := json.MarshalIndent(&review, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(artifactPath, patched, 0644)
}

// synthesizeFindingIDs assigns a stable, non-empty id to any proven issue or
// observation the model emitted without one. The review UI keys per-finding
// triage state (expansion, note box, and the recorded verdict) on the id, so
// two id-less findings would collide onto "" — expanding one expands both, and
// a verdict click posts findingId="" that can't disambiguate them, collapsing
// two distinct human verdicts into one last-write-wins record. Guaranteeing an
// id here, at generation, keeps every finding independently triageable rather
// than merely hidden. Existing ids are never touched.
func synthesizeFindingIDs(review *schema.ReviewOutput) {
	seen := map[string]bool{}
	for _, p := range review.ProvenIssues {
		if p.ID != "" {
			seen[p.ID] = true
		}
	}
	for _, o := range review.Observations {
		if o.ID != "" {
			seen[o.ID] = true
		}
	}

	// unique returns candidate, or candidate with a suffix bumped until it no
	// longer collides with a real or already-synthesized id.
	unique := func(candidate string) string {
		id := candidate
		for i := 2; seen[id]; i++ {
			id = fmt.Sprintf("%s-%d", candidate, i)
		}
		seen[id] = true
		return id
	}

	for i := range review.ProvenIssues {
		if review.ProvenIssues[i].ID == "" {
			review.ProvenIssues[i].ID = unique(fmt.Sprintf("proven-%d", i+1))
		}
	}
	for i := range review.Observations {
		if review.Observations[i].ID == "" {
			review.Observations[i].ID = unique(fmt.Sprintf("observation-%d", i+1))
		}
	}
}

// findReviewArtifactPath locates the "review" artifact declared by the
// phase config, matching how phaserunner writes it (outputDir/art.Path).
func findReviewArtifactPath(cfg *phaseconfig.Config, outputDir string) string {
	for _, step := range cfg.Steps {
		for _, art := range step.Artifacts {
			if art.Name == "review" {
				return filepath.Join(outputDir, art.Path)
			}
		}
	}
	return ""
}

// repoNameFromGitRemote derives a short repo identifier (e.g. "jetbridge")
// from the origin remote URL, falling back to the directory's base name.
func repoNameFromGitRemote(repoDir string) string {
	url := gitOutput(repoDir, "config", "--get", "remote.origin.url")
	if url == "" {
		return filepath.Base(repoDir)
	}
	url = strings.TrimSuffix(url, ".git")
	url = strings.TrimSuffix(url, "/")
	if idx := strings.LastIndexAny(url, "/:"); idx != -1 && idx+1 < len(url) {
		return url[idx+1:]
	}
	return url
}

// gitOutput runs a git command in repoDir and returns trimmed stdout, or
// "" if the command fails (e.g. not a git repo — best-effort only).
func gitOutput(repoDir string, args ...string) string {
	if repoDir == "" {
		return ""
	}
	cmd := exec.Command("git", append([]string{"-C", repoDir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
