package tests

import (
	"os/exec"
	"strings"
	"testing"
)

// Each kubernetes.stepPodGrants entry is one --kubernetes-step-pod-grant on
// web, and only web: a grant is web configuration and nothing else.
func TestStepPodGrantsRenderOneFlagEachOnWeb(t *testing.T) {
	out := render(t,
		"kubernetes.stepPodGrants[0].name=brine-live",
		"kubernetes.stepPodGrants[0].owner=main/one-off",
		"kubernetes.stepPodGrants[0].serviceAccount=concourse-brine-live",
		"kubernetes.stepPodGrants[1].name=release",
		"kubernetes.stepPodGrants[1].owner=main/jetbridge/release",
		"kubernetes.stepPodGrants[1].serviceAccount=jetbridge-releaser",
	)
	for _, want := range []string{
		"--kubernetes-step-pod-grant=name=brine-live,owner=main/one-off,service-account=concourse-brine-live",
		"--kubernetes-step-pod-grant=name=release,owner=main/jetbridge/release,service-account=jetbridge-releaser",
	} {
		if strings.Count(out, want) != 1 {
			t.Errorf("render has %d copies of %q, want exactly one (on web)", strings.Count(out, want), want)
		}
	}
	if !strings.Contains(webDeployment(t, out), "--kubernetes-step-pod-grant=name=release") {
		t.Error("the grants are not on the web Deployment")
	}
}

func TestNoStepPodGrantRendersNoFlag(t *testing.T) {
	if strings.Contains(render(t), "--kubernetes-step-pod-grant") {
		t.Error("the default render passes a step pod grant")
	}
}

// A field carrying the flag's own separators would let one value smuggle a
// different owner or ServiceAccount into the grant web parses.
func TestAStepPodGrantFieldCannotCarryASeparator(t *testing.T) {
	for _, field := range []string{"name", "owner", "serviceAccount"} {
		sets := []string{
			"kubernetes.stepPodGrants[0].name=release",
			"kubernetes.stepPodGrants[0].owner=main/jetbridge/release",
			"kubernetes.stepPodGrants[0].serviceAccount=jetbridge-releaser",
		}
		for i, set := range sets {
			if strings.HasPrefix(set, "kubernetes.stepPodGrants[0]."+field+"=") {
				sets[i] = set + `\,owner=main/one-off`
			}
		}
		args := []string{"template", "jb", "deploy/chart", "-f", "deploy/chart/tests/testdata/required-values.yaml"}
		for _, s := range sets {
			args = append(args, "--set", s)
		}
		cmd := exec.Command("helm", args...)
		cmd.Dir = repoRoot(t)
		out, err := cmd.CombinedOutput()
		if err == nil || !strings.Contains(string(out), "stepPodGrants") {
			t.Errorf("a %s with a separator rendered (err %v): %s", field, err, firstLines(string(out), 3))
		}
	}
}

func webDeployment(t *testing.T, rendered string) string {
	t.Helper()
	for _, chunk := range strings.Split(rendered, "\n---") {
		if strings.Contains(chunk, "kind: Deployment") && strings.Contains(chunk, "name: jb-concourse-jetbridge-web") {
			return chunk
		}
	}
	t.Fatal("render has no web Deployment")
	return ""
}

func firstLines(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
