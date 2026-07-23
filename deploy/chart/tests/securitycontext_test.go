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
				AutomountServiceAccountToken *bool `json:"automountServiceAccountToken"`
				SecurityContext              struct {
					RunAsNonRoot *bool  `json:"runAsNonRoot"`
					RunAsUser    *int64 `json:"runAsUser"`
					FSGroup      *int64 `json:"fsGroup"`
				} `json:"securityContext"`
				InitContainers []struct {
					Name         string        `json:"name"`
					Command      []string      `json:"command"`
					VolumeMounts []volumeMount `json:"volumeMounts"`
				} `json:"initContainers"`
				Containers []struct {
					Name string   `json:"name"`
					Args []string `json:"args"`
					Env  []struct {
						Name  string `json:"name"`
						Value string `json:"value"`
					} `json:"env"`
					VolumeMounts    []volumeMount `json:"volumeMounts"`
					SecurityContext struct {
						AllowPrivilegeEscalation *bool  `json:"allowPrivilegeEscalation"`
						ReadOnlyRootFilesystem   *bool  `json:"readOnlyRootFilesystem"`
						RunAsNonRoot             *bool  `json:"runAsNonRoot"`
						RunAsUser                *int64 `json:"runAsUser"`
						Capabilities             struct {
							Drop []string `json:"drop"`
							Add  []string `json:"add"`
						} `json:"capabilities"`
					} `json:"securityContext"`
				} `json:"containers"`
				Volumes []podVolume `json:"volumes"`
			} `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
}

type daemonSet struct {
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
				} `json:"securityContext"`
				Containers []struct {
					Name            string `json:"name"`
					SecurityContext struct {
						RunAsNonRoot             *bool  `json:"runAsNonRoot"`
						RunAsUser                *int64 `json:"runAsUser"`
						AllowPrivilegeEscalation *bool  `json:"allowPrivilegeEscalation"`
						Capabilities             struct {
							Drop []string `json:"drop"`
							Add  []string `json:"add"`
						} `json:"capabilities"`
					} `json:"securityContext"`
				} `json:"containers"`
			} `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
}

type podVolume struct {
	Name     string `json:"name"`
	EmptyDir *struct {
		Medium    string `json:"medium"`
		SizeLimit string `json:"sizeLimit"`
	} `json:"emptyDir"`
	PersistentVolumeClaim *struct {
		ClaimName string `json:"claimName"`
	} `json:"persistentVolumeClaim"`
	Projected *struct {
		Sources []struct {
			ServiceAccountToken *struct {
				ExpirationSeconds int64  `json:"expirationSeconds"`
				Path              string `json:"path"`
			} `json:"serviceAccountToken"`
			ConfigMap *struct {
				Name  string `json:"name"`
				Items []struct {
					Key  string `json:"key"`
					Path string `json:"path"`
				} `json:"items"`
			} `json:"configMap"`
			DownwardAPI *struct {
				Items []struct {
					Path     string `json:"path"`
					FieldRef struct {
						FieldPath string `json:"fieldPath"`
					} `json:"fieldRef"`
				} `json:"items"`
			} `json:"downwardAPI"`
		} `json:"sources"`
	} `json:"projected"`
}

type volumeMount struct {
	Name      string `json:"name"`
	MountPath string `json:"mountPath"`
	ReadOnly  bool   `json:"readOnly"`
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
	args := []string{
		"template", "test-release", chartDir,
		"--set-string", "kubernetes.artifactHelperImage=" + testArtifactHelperImage,
	}
	for _, s := range sets {
		args = append(args, "--set", s)
	}
	out, err := exec.Command("helm", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("helm template failed: %v\n%s", err, out)
	}
	return string(out)
}

const testArtifactHelperImage = "registry.example/jetbridge/artifact-helper@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

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

func findDaemonSet(t *testing.T, manifests, nameSuffix string) daemonSet {
	t.Helper()
	for _, doc := range strings.Split(manifests, "\n---") {
		if strings.TrimSpace(doc) == "" {
			continue
		}
		var d daemonSet
		if err := yaml.Unmarshal([]byte(doc), &d); err != nil {
			continue
		}
		if d.Kind == "DaemonSet" && strings.HasSuffix(d.Metadata.Name, nameSuffix) {
			return d
		}
	}
	t.Fatalf("no DaemonSet with name ending %q found in rendered chart", nameSuffix)
	return daemonSet{}
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

func TestWebUsesOnlyExplicitBoundedKubernetesAPICredentials(t *testing.T) {
	web := findDeployment(t, renderChart(t), "-web")
	spec := web.Spec.Template.Spec
	if spec.AutomountServiceAccountToken == nil || *spec.AutomountServiceAccountToken {
		t.Fatal("web pod must disable ambient ServiceAccount token automounting")
	}
	var projected *podVolume
	for index := range spec.Volumes {
		if spec.Volumes[index].Name == "web-kubernetes-api-access" {
			projected = &spec.Volumes[index]
		}
	}
	if projected == nil || projected.Projected == nil {
		t.Fatal("web pod is missing its explicit projected Kubernetes API credentials")
	}
	var token, ca, namespace bool
	for _, source := range projected.Projected.Sources {
		if source.ServiceAccountToken != nil {
			token = source.ServiceAccountToken.Path == "token" &&
				source.ServiceAccountToken.ExpirationSeconds > 0 &&
				source.ServiceAccountToken.ExpirationSeconds <= 3600
		}
		if source.ConfigMap != nil && source.ConfigMap.Name == "kube-root-ca.crt" {
			for _, item := range source.ConfigMap.Items {
				ca = ca || item.Key == "ca.crt" && item.Path == "ca.crt"
			}
		}
		if source.DownwardAPI != nil {
			for _, item := range source.DownwardAPI.Items {
				namespace = namespace || item.Path == "namespace" && item.FieldRef.FieldPath == "metadata.namespace"
			}
		}
	}
	if !token || !ca || !namespace {
		t.Fatalf("projected credential sources incomplete: token=%t ca=%t namespace=%t", token, ca, namespace)
	}
	for _, init := range spec.InitContainers {
		for _, mount := range init.VolumeMounts {
			if mount.Name == "web-kubernetes-api-access" {
				t.Fatalf("Kubernetes API credentials leaked into init container %q", init.Name)
			}
		}
	}
	foundWebMount := false
	for _, mount := range spec.Containers[0].VolumeMounts {
		if mount.Name == "web-kubernetes-api-access" &&
			mount.MountPath == "/var/run/secrets/kubernetes.io/serviceaccount" &&
			mount.ReadOnly {
			foundWebMount = true
		}
	}
	if !foundWebMount {
		t.Fatal("explicit Kubernetes API credentials are not mounted read-only at the standard web path")
	}
}

func TestArtifactDaemonRunsExplicitlyAsCapabilityBoundedRoot(t *testing.T) {
	daemon := findDaemonSet(t, renderChart(t), "-artifact-daemon")
	podSC := daemon.Spec.Template.Spec.SecurityContext
	if podSC.RunAsUser == nil || *podSC.RunAsUser != 0 ||
		podSC.RunAsNonRoot == nil || *podSC.RunAsNonRoot {
		t.Fatalf("daemon pod root identity is not explicit: uid=%v nonRoot=%v", podSC.RunAsUser, podSC.RunAsNonRoot)
	}
	if len(daemon.Spec.Template.Spec.Containers) != 1 {
		t.Fatalf("daemon containers = %d", len(daemon.Spec.Template.Spec.Containers))
	}
	sc := daemon.Spec.Template.Spec.Containers[0].SecurityContext
	if sc.RunAsUser == nil || *sc.RunAsUser != 0 || sc.RunAsNonRoot == nil || *sc.RunAsNonRoot {
		t.Fatalf("daemon container root identity is not explicit: uid=%v nonRoot=%v", sc.RunAsUser, sc.RunAsNonRoot)
	}
	if boolVal(sc.AllowPrivilegeEscalation) || !containsStr(sc.Capabilities.Drop, "ALL") ||
		!containsStr(sc.Capabilities.Add, "DAC_OVERRIDE") {
		t.Fatalf("daemon capabilities are not fail-closed: drop=%v add=%v escalation=%v",
			sc.Capabilities.Drop, sc.Capabilities.Add, sc.AllowPrivilegeEscalation)
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
	args := []string{
		"template", "test-release", chartDir,
		"--set-string", "kubernetes.artifactHelperImage=" + testArtifactHelperImage,
	}
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

func TestArtifactResolveCapabilitySecretIsPrivateAndWired(t *testing.T) {
	manifests := renderChart(t)
	for _, want := range []string{
		"artifact-daemon-resolve-capability",
		"resolve.key:",
		"--resolve-capability-key=/etc/concourse/resolve-capability/resolve.key",
		"--kubernetes-artifact-daemon-resolve-capability-key=/etc/concourse/resolve-capability/resolve.key",
		"--kubernetes-artifact-daemon-resolve-capability-ttl=2h",
		"--resolve-max-concurrent=32",
		"--resolve-timeout=30m",
		"mountPath: /etc/concourse/resolve-capability",
	} {
		if !strings.Contains(manifests, want) {
			t.Errorf("rendered chart missing capability wiring %q", want)
		}
	}
	// The scoped token is generated into an init request; task pods must never
	// receive the shared signing Secret as a general environment value.
	if strings.Contains(manifests, "CONCOURSE_ARTIFACT_RESOLVE_CAPABILITY_KEY") {
		t.Fatal("shared capability key was exposed as an environment variable")
	}
}

func TestArtifactResolveCapabilitySupportsExistingSecret(t *testing.T) {
	manifests := renderChart(t, "artifactDaemon.resolveCapability.existingSecret=operator-key")
	if !strings.Contains(manifests, "secretName: operator-key") {
		t.Fatal("existing capability Secret was not mounted")
	}
	if strings.Contains(manifests, "kind: Secret\nmetadata:\n  name: test-release-concourse-jetbridge-artifact-daemon-resolve-capability") {
		t.Fatal("chart generated a capability Secret despite existingSecret override")
	}
}
