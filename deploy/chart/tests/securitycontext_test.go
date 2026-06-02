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
					RunAsNonRoot *bool  `json:"runAsNonRoot"`
					RunAsUser    *int64 `json:"runAsUser"`
					FSGroup      *int64 `json:"fsGroup"`
				} `json:"securityContext"`
				Containers []struct {
					Name            string `json:"name"`
					SecurityContext struct {
						AllowPrivilegeEscalation *bool `json:"allowPrivilegeEscalation"`
						ReadOnlyRootFilesystem   *bool `json:"readOnlyRootFilesystem"`
						Capabilities             struct {
							Drop []string `json:"drop"`
						} `json:"capabilities"`
					} `json:"securityContext"`
				} `json:"containers"`
			} `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
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

func containsStr(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
