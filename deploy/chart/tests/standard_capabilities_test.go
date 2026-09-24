package tests

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"
)

func TestStandardCapabilitiesAreWiredTogether(t *testing.T) {
	out := render(t, "mcp.disabledOperations[0]=build_abort")
	web := standardWeb(t, out)
	container := web.Spec.Template.Spec.Containers[0]
	args := strings.Join(container.Args, "\n")
	for _, flag := range []string{"--enable-mcp", "--mcp-client-config=/etc/concourse/mcp-clients/clients.json", "--mcp-disable-operation=build_abort", "--kubernetes-artifact-daemon-tls-ca-cert=", "--kubernetes-artifact-daemon-resolve-capability-key="} {
		if !strings.Contains(args, flag) {
			t.Errorf("web missing %s", flag)
		}
	}
	// A mounted path must resolve to the ConfigMap that actually contains the registration.
	mounted := false
	for _, m := range container.VolumeMounts {
		if m.MountPath != "/etc/concourse/mcp-clients" {
			continue
		}
		for _, v := range web.Spec.Template.Spec.Volumes {
			if v.Name != m.Name || v.ConfigMap == nil {
				continue
			}
			for _, doc := range splitYAMLDocs(out) {
				var cm corev1.ConfigMap
				if yaml.Unmarshal([]byte(doc), &cm) != nil || cm.Kind != "ConfigMap" || cm.Name != v.ConfigMap.Name {
					continue
				}
				var clients []struct {
					ID        string   `json:"client_id"`
					Redirects []string `json:"redirect_uris"`
				}
				if err := json.Unmarshal([]byte(cm.Data["clients.json"]), &clients); err != nil {
					t.Fatal(err)
				}
				if len(clients) != 1 || clients[0].ID != "test-client" || len(clients[0].Redirects) != 1 || clients[0].Redirects[0] != "http://127.0.0.1:8964/callback" {
					t.Fatalf("registration changed: %+v", clients)
				}
				mounted = true
			}
		}
	}
	if !mounted {
		t.Fatal("MCP registration is not mounted in web")
	}
	var ds struct {
		Spec struct{ Template struct{ Spec corev1.PodSpec } }
	}
	if err := yaml.Unmarshal([]byte(daemonSetDocument(t, out)), &ds); err != nil {
		t.Fatal(err)
	}
	daemon := ds.Spec.Template.Spec.Containers[0]
	if !strings.Contains(strings.Join(daemon.Command, "\n"), "--resolve-capability-key=") || daemon.ReadinessProbe.HTTPGet.Scheme != corev1.URISchemeHTTPS {
		t.Fatal("daemon security missing")
	}
	for _, pod := range []corev1.PodSpec{web.Spec.Template.Spec, ds.Spec.Template.Spec} {
		volumes := map[string]bool{}
		for _, v := range pod.Volumes {
			volumes[v.Name] = true
		}
		for _, c := range append(pod.InitContainers, pod.Containers...) {
			for _, m := range c.VolumeMounts {
				if !volumes[m.Name] {
					t.Errorf("%s mount %s has no volume", c.Name, m.Name)
				}
			}
		}
	}
}

func TestStandardCapabilityConfigurationFailsClearly(t *testing.T) {
	for _, tc := range []struct{ setting, want string }{
		{"artifactDaemon.enabled=true", "artifactDaemon.enabled has been removed"},
		{"artifactDaemon.enabled=false", "artifactDaemon.enabled has been removed"},
		{"artifactDaemon.tls.enabled=false", "artifactDaemon.tls.enabled has been removed"},
		{"artifactDaemon.tls.existingSecret=", "tls.existingSecret is required"},
		{"artifactDaemon.tls.source=generated", "must be empty"},
		{"artifactDaemon.tls.source=guess", "artifactDaemon.tls.source"},
		{"artifactDaemon.resolveCapability.existingSecret=", "resolveCapability.existingSecret is required"},
		{"mcp.clients=[]", "mcp.clients"},
		{"web.externalUrl=https://example.com/subpath", "origin URL"},
		{"web.extraArgs[0]=--enable-mcp=false", "chart-owned --enable-mcp"},
		{"web.extraArgs[0]=--mcp-client-config=/other", "chart-owned --mcp-client-config"},
		{"web.env[0].name=CONCOURSE_ENABLE_MCP", "chart-owned CONCOURSE_ENABLE_MCP"},
		{"web.env[0].name=CONCOURSE_KUBERNETES_ARTIFACT_DAEMON_TLS_KEY", "chart-owned CONCOURSE_KUBERNETES_ARTIFACT_DAEMON_TLS_KEY"},
	} {
		t.Run(tc.setting, func(t *testing.T) {
			out := renderHangarError(t, tc.setting)
			if !strings.Contains(strings.ReplaceAll(out, "/", "."), tc.want) {
				t.Fatalf("want %q: %s", tc.want, out)
			}
		})
	}
}

func TestBareChartDoesNotInventMCPClients(t *testing.T) {
	cmd := exec.Command("helm", "template", "jb", "deploy/chart")
	cmd.Dir = repoRoot(t)
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(strings.ReplaceAll(string(out), "/", "."), "mcp.clients") {
		t.Fatalf("expected registration prerequisite: %v %s", err, out)
	}
}

func TestExplicitDatabaseTopologyRemainsIndependent(t *testing.T) {
	for _, bundled := range []bool{true, false} {
		setting := "postgresql.enabled=true"
		if !bundled {
			setting = "postgresql.enabled=false"
		}
		out := render(t, setting, "postgresql.host=external.example")
		args := strings.Join(webContainerArgs(t, out), "\n")
		if strings.Contains(args, "--postgres-host=external.example") == bundled {
			t.Fatalf("explicit database mode ignored: %s", setting)
		}
	}
}

func TestMCPRegistrationChangeRollsWebAndStableRenderPreservesSecrets(t *testing.T) {
	first := render(t)
	if second := render(t); first != second {
		t.Fatal("existing-secret rendering is not deterministic")
	}
	before := standardWeb(t, first).Spec.Template.Annotations["checksum/mcp-clients"]
	after := standardWeb(t, render(t, "mcp.clients[0].client_name=Renamed")).Spec.Template.Annotations["checksum/mcp-clients"]
	if before == "" || before == after {
		t.Fatal("registration change will not reload web's startup configuration")
	}
}

func standardWeb(t *testing.T, out string) appsv1.Deployment {
	t.Helper()
	for _, doc := range splitYAMLDocs(out) {
		var web appsv1.Deployment
		if yaml.Unmarshal([]byte(doc), &web) == nil && web.Kind == "Deployment" && strings.HasSuffix(web.Name, "-web") {
			return web
		}
	}
	t.Fatal("no web deployment")
	return appsv1.Deployment{}
}
