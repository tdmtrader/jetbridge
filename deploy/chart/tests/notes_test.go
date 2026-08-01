package tests

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// renderNotes runs `helm install --dry-run=client`, which is the only mode in
// the Helm version this repo targets that actually renders NOTES.txt to
// stdout (unlike `helm template`, which never prints notes). It returns just
// the NOTES: section so assertions don't have to wade through the manifest
// stream.
func renderNotes(t *testing.T, sets ...string) string {
	t.Helper()
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm not on PATH; skipping chart render test")
	}
	chartDir, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("resolve chart dir: %v", err)
	}
	args := []string{
		"install", "test-release", chartDir,
		"--dry-run=client",
		"--set-string", "kubernetes.artifactHelperImage=" + testArtifactHelperImage,
	}
	for _, s := range sets {
		args = append(args, "--set", s)
	}
	out, err := exec.Command("helm", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("helm install --dry-run=client failed: %v\n%s", err, out)
	}
	idx := strings.Index(string(out), "NOTES:")
	if idx == -1 {
		t.Fatalf("rendered output has no NOTES: section:\n%s", out)
	}
	return string(out)[idx:]
}

func TestNotesWarnsWhenAgentSnapshotsEnabledAndHermeticEgressEmpty(t *testing.T) {
	notes := renderNotes(t,
		"agentSnapshots.enabled=true",
		"artifactDaemon.enabled=true",
		"artifactDaemon.tls.enabled=true",
		"artifactDaemon.hangar.bucket=test-hangar",
	)
	if !strings.Contains(notes, "WARNING: agentSnapshots is enabled and networkPolicy.hermeticEgressTo is empty.") {
		t.Fatalf("expected fail-closed-egress warning, got:\n%s", notes)
	}
}

func TestNotesSilentWhenHermeticEgressConfigured(t *testing.T) {
	notes := renderNotes(t,
		"agentSnapshots.enabled=true",
		"artifactDaemon.enabled=true",
		"artifactDaemon.tls.enabled=true",
		"artifactDaemon.hangar.bucket=test-hangar",
		"networkPolicy.hermeticEgressTo[0].to[0].ipBlock.cidr=0.0.0.0/0",
	)
	if strings.Contains(notes, "WARNING") {
		t.Fatalf("expected no warning once hermeticEgressTo is configured, got:\n%s", notes)
	}
}

func TestNotesSilentWhenAgentSnapshotsDisabledAndHermeticEgressEmpty(t *testing.T) {
	notes := renderNotes(t)
	if strings.Contains(notes, "WARNING") {
		t.Fatalf("expected no warning for a task-only deployment, got:\n%s", notes)
	}
}

func TestNotesSilentWhenAgentSnapshotsDisabledAndHermeticEgressConfigured(t *testing.T) {
	notes := renderNotes(t,
		"networkPolicy.hermeticEgressTo[0].to[0].ipBlock.cidr=0.0.0.0/0",
	)
	if strings.Contains(notes, "WARNING") {
		t.Fatalf("expected no warning for a task-only deployment, got:\n%s", notes)
	}
}
