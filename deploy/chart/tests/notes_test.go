package tests

import (
	"strings"
	"testing"
)

// warningHeadline and warningAction are substrings of the fail-closed-egress
// warning that both surfaces (NOTES.txt and the hermetic NetworkPolicy
// annotation) are expected to convey. Asserting on these specific strings,
// rather than the bare word "WARNING" or "warning", keeps the tests from
// spuriously breaking on an unrelated future NOTES/annotation addition, and
// from passing if the actionable remediation paragraph is ever deleted while
// the headline survives.
const (
	warningHeadline = "networkPolicy.hermeticEgressTo is empty"
	warningAction   = "set networkPolicy.hermeticEgressTo"
)

// renderNotes runs `helm install --dry-run=client`, which is the only mode in
// the Helm version this repo targets that actually renders NOTES.txt to
// stdout (unlike `helm template`, which never prints notes). It returns just
// the NOTES: section so assertions don't have to wade through the manifest
// stream.
func renderNotes(t *testing.T, sets ...string) string {
	t.Helper()
	out, err := runHelmChart(t, "install", []string{"--dry-run=client"}, sets...)
	if err != nil {
		t.Fatalf("helm install --dry-run=client failed: %v\n%s", err, out)
	}
	idx := strings.Index(out, "NOTES:")
	if idx == -1 {
		t.Fatalf("rendered output has no NOTES: section:\n%s", out)
	}
	return out[idx:]
}

// agentSnapshotsSettings are the minimum overrides required to satisfy the
// config-boundary chain agentSnapshots.enabled depends on (artifactDaemon
// enabled + TLS + a Hangar bucket; see web-deployment.yaml and
// artifact-daemon-daemonset.yaml).
func agentSnapshotsSettings() []string {
	return []string{
		"agentSnapshots.enabled=true",
		"artifactDaemon.enabled=true",
		"artifactDaemon.tls.enabled=true",
		"artifactDaemon.hangar.bucket=test-hangar",
	}
}

const agentStepImageSetting = "web.extraArgs[0]=--agent-step-image=registry.example/agent@sha256:" +
	"2222222222222222222222222222222222222222222222222222222222222222"

func TestNotesWarnsWhenAgentSnapshotsEnabledAndHermeticEgressEmpty(t *testing.T) {
	notes := renderNotes(t, agentSnapshotsSettings()...)
	if !strings.Contains(notes, warningHeadline) {
		t.Fatalf("expected fail-closed-egress warning headline, got:\n%s", notes)
	}
	if !strings.Contains(notes, warningAction) {
		t.Fatalf("expected the actionable remediation paragraph, got:\n%s", notes)
	}
}

// TestNotesWarnsWhenAgentStepImageSetWithoutAgentSnapshots pins the false-
// negative fix: hermetic: true applies to plain agent: steps too (see
// atc/steps.go and atc/exec/task_step.go), and those only need
// --agent-step-image (atc/exec/agent_step.go), not agentSnapshots, to run.
func TestNotesWarnsWhenAgentStepImageSetWithoutAgentSnapshots(t *testing.T) {
	notes := renderNotes(t, agentStepImageSetting)
	if !strings.Contains(notes, warningHeadline) {
		t.Fatalf("expected fail-closed-egress warning when --agent-step-image is set without agentSnapshots, got:\n%s", notes)
	}
}

func TestNotesSilentWhenHermeticEgressConfigured(t *testing.T) {
	sets := append(agentSnapshotsSettings(),
		"networkPolicy.hermeticEgressTo[0].to[0].ipBlock.cidr=0.0.0.0/0")
	notes := renderNotes(t, sets...)
	if strings.Contains(notes, warningHeadline) {
		t.Fatalf("expected no warning once hermeticEgressTo is configured, got:\n%s", notes)
	}
}

func TestNotesSilentWhenAgentSnapshotsDisabledAndHermeticEgressEmpty(t *testing.T) {
	notes := renderNotes(t)
	if strings.Contains(notes, warningHeadline) {
		t.Fatalf("expected no warning for a task-only deployment, got:\n%s", notes)
	}
}

func TestNotesSilentWhenAgentSnapshotsDisabledAndHermeticEgressConfigured(t *testing.T) {
	notes := renderNotes(t,
		"networkPolicy.hermeticEgressTo[0].to[0].ipBlock.cidr=0.0.0.0/0",
	)
	if strings.Contains(notes, warningHeadline) {
		t.Fatalf("expected no warning for a task-only deployment, got:\n%s", notes)
	}
}

// The NOTES warning is invisible to ArgoCD's `helm template` install path
// (deploy/chart/README.md's Quickstart (ArgoCD) section), so the same
// condition also annotates the always-emitted hermetic NetworkPolicy. That
// annotation is reachable through the existing `helm template`-based
// renderChart helper.

func TestHermeticEgressAnnotationPresentWhenAgentSnapshotsEnabledAndHermeticEgressEmpty(t *testing.T) {
	policy := findNetworkPolicy(t, renderChart(t, agentSnapshotsSettings()...), "-hermetic-egress")
	if _, ok := policy.Metadata.Annotations["concourse.ci/hermetic-egress-warning"]; !ok {
		t.Fatalf("expected concourse.ci/hermetic-egress-warning annotation on the hermetic NetworkPolicy, got annotations: %v", policy.Metadata.Annotations)
	}
}

func TestHermeticEgressAnnotationPresentWhenAgentStepImageSetWithoutAgentSnapshots(t *testing.T) {
	policy := findNetworkPolicy(t, renderChart(t, agentStepImageSetting), "-hermetic-egress")
	if _, ok := policy.Metadata.Annotations["concourse.ci/hermetic-egress-warning"]; !ok {
		t.Fatalf("expected concourse.ci/hermetic-egress-warning annotation when --agent-step-image is set without agentSnapshots, got annotations: %v", policy.Metadata.Annotations)
	}
}

func TestHermeticEgressAnnotationAbsentWhenHermeticEgressConfigured(t *testing.T) {
	sets := append(agentSnapshotsSettings(),
		"networkPolicy.hermeticEgressTo[0].to[0].ipBlock.cidr=0.0.0.0/0")
	policy := findNetworkPolicy(t, renderChart(t, sets...), "-hermetic-egress")
	if _, ok := policy.Metadata.Annotations["concourse.ci/hermetic-egress-warning"]; ok {
		t.Fatalf("expected no warning annotation once hermeticEgressTo is configured, got annotations: %v", policy.Metadata.Annotations)
	}
}

func TestHermeticEgressAnnotationAbsentWhenAgentSnapshotsDisabledAndHermeticEgressEmpty(t *testing.T) {
	policy := findNetworkPolicy(t, renderChart(t), "-hermetic-egress")
	if _, ok := policy.Metadata.Annotations["concourse.ci/hermetic-egress-warning"]; ok {
		t.Fatalf("expected no warning annotation for a task-only deployment, got annotations: %v", policy.Metadata.Annotations)
	}
}
