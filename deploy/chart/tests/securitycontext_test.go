// Package tests renders the Helm chart with `helm template` and asserts on the
// produced manifests. These tests require the `helm` binary on PATH; when it is
// absent (e.g. in a Go-only unit-test environment) they skip rather than fail,
// so they never break `make test-unit`. Run explicitly with:
//
//	go test ./deploy/chart/tests/
package tests

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// deployment is a minimal projection of a rendered Deployment, just enough to
// assert on pod- and container-level securityContext.
type deployment struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		Template struct {
			Spec struct {
				SecurityContext struct {
					RunAsNonRoot   *bool  `json:"runAsNonRoot"`
					RunAsUser      *int64 `json:"runAsUser"`
					FSGroup        *int64 `json:"fsGroup"`
					SeccompProfile struct {
						Type string `json:"type"`
					} `json:"seccompProfile"`
				} `json:"securityContext"`
				Containers []struct {
					Name            string   `json:"name"`
					Args            []string `json:"args"`
					SecurityContext struct {
						AllowPrivilegeEscalation *bool  `json:"allowPrivilegeEscalation"`
						Privileged               *bool  `json:"privileged"`
						RunAsUser                *int64 `json:"runAsUser"`
						ReadOnlyRootFilesystem   *bool  `json:"readOnlyRootFilesystem"`
						Capabilities             struct {
							Drop []string `json:"drop"`
							Add  []string `json:"add"`
						} `json:"capabilities"`
						SeccompProfile struct {
							Type string `json:"type"`
						} `json:"seccompProfile"`
					} `json:"securityContext"`
				} `json:"containers"`
			} `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
}

func findDaemonSet(t *testing.T, manifests, nameSuffix string) deployment {
	t.Helper()
	for _, doc := range strings.Split(manifests, "\n---") {
		var d deployment
		if yaml.Unmarshal([]byte(doc), &d) == nil && d.Kind == "DaemonSet" && strings.HasSuffix(d.Metadata.Name, nameSuffix) {
			return d
		}
	}
	t.Fatalf("no DaemonSet with name ending %q found", nameSuffix)
	return deployment{}
}

// renderChart runs `helm template` against the chart (the parent dir of this
// test package) with the given --set overrides, skipping if helm is missing.
func renderChart(t *testing.T, sets ...string) string {
	t.Helper()
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm not on PATH; skipping chart render test")
	}
	chartDir, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("resolve chart dir: %v", err)
	}
	args := []string{"template", "test-release", chartDir}
	for _, s := range sets {
		args = append(args, "--set", s)
	}
	out, err := exec.Command("helm", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("helm template failed: %v\n%s", err, out)
	}
	return string(out)
}

// findDeployment parses the multi-document manifest stream and returns the
// Deployment whose name ends with nameSuffix.
func findDeployment(t *testing.T, manifests, nameSuffix string) deployment {
	t.Helper()
	for _, doc := range strings.Split(manifests, "\n---") {
		if strings.TrimSpace(doc) == "" {
			continue
		}
		var d deployment
		if err := yaml.Unmarshal([]byte(doc), &d); err != nil {
			continue
		}
		if d.Kind == "Deployment" && strings.HasSuffix(d.Metadata.Name, nameSuffix) {
			return d
		}
	}
	t.Fatalf("no Deployment with name ending %q found in rendered chart", nameSuffix)
	return deployment{}
}

func boolVal(p *bool) bool { return p != nil && *p }

func TestWebContainerSecurityContext(t *testing.T) {
	web := findDeployment(t, renderChart(t), "-web")

	podSC := web.Spec.Template.Spec.SecurityContext
	if !boolVal(podSC.RunAsNonRoot) {
		t.Error("web pod securityContext.runAsNonRoot should be true")
	}
	if podSC.RunAsUser == nil {
		t.Error("web pod securityContext.runAsUser should be set")
	}

	if len(web.Spec.Template.Spec.Containers) == 0 {
		t.Fatal("web Deployment has no containers")
	}
	c := web.Spec.Template.Spec.Containers[0]
	if boolVal(c.SecurityContext.AllowPrivilegeEscalation) {
		t.Error("web container allowPrivilegeEscalation should be false")
	}
	if !boolVal(c.SecurityContext.ReadOnlyRootFilesystem) {
		t.Error("web container readOnlyRootFilesystem should be true")
	}
	if !containsStr(c.SecurityContext.Capabilities.Drop, "ALL") {
		t.Errorf("web container should drop ALL capabilities, got %v", c.SecurityContext.Capabilities.Drop)
	}
}

func TestPostgresContainerSecurityContext(t *testing.T) {
	db := findDeployment(t, renderChart(t), "-db")

	podSC := db.Spec.Template.Spec.SecurityContext
	if podSC.RunAsUser == nil || *podSC.RunAsUser != 999 {
		t.Errorf("postgres pod securityContext.runAsUser should be 999, got %v", podSC.RunAsUser)
	}
	if podSC.FSGroup == nil || *podSC.FSGroup != 999 {
		t.Errorf("postgres pod securityContext.fsGroup should be 999, got %v", podSC.FSGroup)
	}

	if len(db.Spec.Template.Spec.Containers) == 0 {
		t.Fatal("postgres Deployment has no containers")
	}
	c := db.Spec.Template.Spec.Containers[0]
	if boolVal(c.SecurityContext.AllowPrivilegeEscalation) {
		t.Error("postgres container allowPrivilegeEscalation should be false")
	}
	if !containsStr(c.SecurityContext.Capabilities.Drop, "ALL") {
		t.Errorf("postgres container should drop ALL capabilities, got %v", c.SecurityContext.Capabilities.Drop)
	}
}

func TestHangarKeepsArtifactDaemonAtItsMinimalSecurityContext(t *testing.T) {
	daemon := findDaemonSet(t, renderChart(t,
		"artifactDaemon.hangar.enabled=true",
		"artifactDaemon.tls.enabled=true",
		"artifactDaemon.tls.existingSecret=operator-daemon-tls",
		"artifactDaemon.durable.store=gcs",
		"artifactDaemon.durable.bucket=b",
	), "-artifact-daemon")
	if boolVal(daemon.Spec.Template.Spec.SecurityContext.RunAsNonRoot) {
		t.Fatal("artifact-daemon must remain root for arbitrary-UID hostPath content")
	}
	if user := daemon.Spec.Template.Spec.SecurityContext.RunAsUser; user != nil {
		t.Fatalf("artifact-daemon pod runAsUser=%d; root must be scoped to the daemon container", *user)
	}
	if daemon.Spec.Template.Spec.SecurityContext.SeccompProfile.Type != "RuntimeDefault" {
		t.Fatalf("pod seccomp=%q, want RuntimeDefault", daemon.Spec.Template.Spec.SecurityContext.SeccompProfile.Type)
	}
	if len(daemon.Spec.Template.Spec.Containers) != 1 {
		t.Fatalf("artifact-daemon containers=%d, want 1", len(daemon.Spec.Template.Spec.Containers))
	}
	c := daemon.Spec.Template.Spec.Containers[0]
	if user := c.SecurityContext.RunAsUser; user == nil || *user != 0 {
		t.Fatalf("artifact-daemon container runAsUser=%v, want explicit root", user)
	}
	if c.SecurityContext.Privileged == nil || *c.SecurityContext.Privileged {
		t.Fatalf("artifact-daemon privileged=%v, want explicit false", c.SecurityContext.Privileged)
	}
	if c.SecurityContext.AllowPrivilegeEscalation == nil || *c.SecurityContext.AllowPrivilegeEscalation {
		t.Fatalf("artifact-daemon allowPrivilegeEscalation=%v, want explicit false", c.SecurityContext.AllowPrivilegeEscalation)
	}
	if !containsStr(c.SecurityContext.Capabilities.Drop, "ALL") {
		t.Fatalf("artifact-daemon capability drops=%v, want ALL", c.SecurityContext.Capabilities.Drop)
	}
	if len(c.SecurityContext.Capabilities.Add) != 1 || c.SecurityContext.Capabilities.Add[0] != "DAC_OVERRIDE" {
		t.Fatalf("artifact-daemon added capabilities=%v, want only DAC_OVERRIDE", c.SecurityContext.Capabilities.Add)
	}
	if c.SecurityContext.SeccompProfile.Type != "RuntimeDefault" {
		t.Fatalf("container seccomp=%q, want RuntimeDefault", c.SecurityContext.SeccompProfile.Type)
	}
}

// renderChartSetString is like renderChart but uses `--set-string` so values
// containing commas (escaped as `\,`) survive helm's --set list parsing.
func renderChartSetString(t *testing.T, sets ...string) string {
	t.Helper()
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm not on PATH; skipping chart render test")
	}
	chartDir, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("resolve chart dir: %v", err)
	}
	args := []string{"template", "test-release", chartDir}
	for _, s := range sets {
		args = append(args, "--set-string", s)
	}
	out, err := exec.Command("helm", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("helm template failed: %v\n%s", err, out)
	}
	return string(out)
}

// TestWebLocalUsersSplitIntoSeparateFlags guards against the flag-parsing bug
// where a comma-separated localUsers string was emitted as a single
// `--add-local-user` argument. Concourse's CLI map parser splits only on the
// first colon and does not split on commas, so a value like
// "admin:pw,test:test" collapsed into one user "admin" whose password was the
// entire remainder ("pw,test:test"). The chart must emit one flag per user.
func TestWebLocalUsersSplitIntoSeparateFlags(t *testing.T) {
	manifests := renderChartSetString(t, `web.localUsers=admin:Fish25\,test:test\,guest:guest`)
	web := findDeployment(t, manifests, "-web")
	if len(web.Spec.Template.Spec.Containers) == 0 {
		t.Fatal("web Deployment has no containers")
	}
	args := web.Spec.Template.Spec.Containers[0].Args

	want := []string{
		"--add-local-user=admin:Fish25",
		"--add-local-user=test:test",
		"--add-local-user=guest:guest",
	}
	for _, w := range want {
		if !containsStr(args, w) {
			t.Errorf("web args missing %q; got %v", w, args)
		}
	}
	// The whole-string single-flag form must NOT be present.
	if containsStr(args, "--add-local-user=admin:Fish25,test:test,guest:guest") {
		t.Error("web args still contains the un-split single --add-local-user flag")
	}
}

func containsStr(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
