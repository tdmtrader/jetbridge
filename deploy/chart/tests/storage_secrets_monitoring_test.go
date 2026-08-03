package tests

import (
	"encoding/base64"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/concourse/concourse/atc/atccmd"
	"github.com/jessevdk/go-flags"
	"sigs.k8s.io/yaml"
)

func mustBase64(t *testing.T, encoded string) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("Secret value is not base64: %v", err)
	}
	return string(raw)
}

// TestDefaultWebArgsMatchCurrentBinaryFlags guards the release seam between
// the chart and the web binary. Helm accepts arbitrary strings in a container
// args list, so a removed Concourse flag otherwise passes helm lint/template
// and only surfaces as a CrashLoop after deployment.
func TestDefaultWebArgsMatchCurrentBinaryFlags(t *testing.T) {
	web := findDeployment(t, renderChart(t), "-web")
	if len(web.Spec.Template.Spec.Containers) == 0 {
		t.Fatal("web Deployment has no containers")
	}

	// ATCCommand's "run" subcommand is the same atccmd.RunCommand embedded by
	// cmd/concourse's "web" subcommand. Default chart values do not enable any
	// dynamically registered credential-manager flags, so its static option
	// set is the authoritative schema for every rendered --flag here.
	cmd := &atccmd.ATCCommand{}
	parser := flags.NewParser(cmd, flags.Default)
	parser.NamespaceDelimiter = "-"
	runCmd := parser.Find("run")
	if runCmd == nil {
		t.Fatal("ATC run command is missing")
	}

	var unknown []string
	for _, arg := range web.Spec.Template.Spec.Containers[0].Args {
		if !strings.HasPrefix(arg, "--") {
			continue
		}
		name := strings.TrimPrefix(strings.SplitN(arg, "=", 2)[0], "--")
		if runCmd.FindOptionByLongName(name) == nil {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		t.Fatalf("default chart renders flags removed from the current web binary: %v", unknown)
	}
}

// retiredStorageConfig is the configuration deleted along with the PVC-based
// artifact store. Both halves fail silently: `helm --set` accepts unknown keys,
// so a leftover override reads as a fix while doing nothing, and the web flag
// parser rejects unknown flags, so a leftover container arg is a CrashLoop that
// no render-time check catches.
//
// Only the *applied* forms are listed. Naming a removed flag in prose is fine
// and sometimes necessary ("these flags no longer exist"); what must not
// survive is anything that actually passes one to a container or sets one of
// the deleted values.
var retiredStorageConfig = []string{
	"cachePvc.enabled",
	"artifactStorePvc.enabled",
	"artifactStorePvc.gcsFuse",
	"artifactStorePvc.accessModes",
	"--kubernetes-cache-pvc=",
	"--kubernetes-artifact-store-claim=",
	"- --kubernetes-artifact-store-gcs-fuse",
}

// TestActiveDeployPathsDoNotReferenceRetiredPVCConfig covers the deploy paths
// the chart tests cannot render: raw manifests, the local-dev script, and the
// topgun helm invocations. TestDefaultWebArgsMatchCurrentBinaryFlags only sees
// what the chart itself emits.
func TestActiveDeployPathsDoNotReferenceRetiredPVCConfig(t *testing.T) {
	assertNoRetiredStorageConfig(t, "deploy", "hack", "topgun")
}

// TestActiveGuidesDoNotRecommendRetiredStorageConfiguration keeps the docs from
// telling an operator to set values that no longer exist — the failure mode is
// worse than a stale sentence, because following the instruction produces a
// no-op the operator believes is a fix.
func TestActiveGuidesDoNotRecommendRetiredStorageConfiguration(t *testing.T) {
	assertNoRetiredStorageConfig(t, "docs", "README.md", "JETBRIDGE.md")
}

func assertNoRetiredStorageConfig(t *testing.T, roots ...string) {
	t.Helper()
	repoRoot, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve this test file's path")
	}

	scannable := map[string]bool{
		".yaml": true, ".yml": true, ".sh": true,
		".go": true, ".md": true, ".html": true, ".tpl": true,
	}

	for _, root := range roots {
		walkErr := filepath.WalkDir(filepath.Join(repoRoot, root), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				switch entry.Name() {
				case ".git", "node_modules", "vendor":
					return fs.SkipDir
				}
				return nil
			}
			if path == self || !scannable[filepath.Ext(path)] {
				return nil
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, retired := range retiredStorageConfig {
				if strings.Contains(string(content), retired) {
					rel, relErr := filepath.Rel(repoRoot, path)
					if relErr != nil {
						rel = path
					}
					t.Errorf("%s still applies retired storage config %q; the PVC artifact store and cache PVC were removed from both the chart and the binary", rel, retired)
				}
			}
			return nil
		})
		if walkErr != nil {
			t.Fatalf("scan %s: %v", root, walkErr)
		}
	}
}

// TestDefaultInstallSharesOneSessionSigningKey pins the multi-replica contract:
// with secrets.create=true the chart must mount a Secret that already carries
// the key, not an emptyDir that each pod mints into on start.
func TestDefaultInstallSharesOneSessionSigningKey(t *testing.T) {
	manifests := renderChart(t, "web.replicas=3")
	web := findDeployment(t, manifests, "-web")

	var keys *podVolume
	for index := range web.Spec.Template.Spec.Volumes {
		if web.Spec.Template.Spec.Volumes[index].Name == "keys" {
			keys = &web.Spec.Template.Spec.Volumes[index]
		}
	}
	if keys == nil {
		t.Fatal("web pod has no keys volume")
	}
	if keys.EmptyDir != nil {
		t.Fatal("session signing key is per-pod ephemeral: keys volume is an emptyDir")
	}
	if keys.Secret == nil || keys.Secret.SecretName == "" {
		t.Fatalf("keys volume does not mount a Secret: %+v", keys)
	}
	for _, init := range web.Spec.Template.Spec.InitContainers {
		if init.Name == "generate-keys" {
			t.Fatal("generate-keys init container renders alongside the mounted Secret; it would fight the shared key")
		}
	}

	secret := findSecret(t, manifests, keys.Secret.SecretName)
	pem, ok := secret.Data["session_signing_key"]
	if !ok || pem == "" {
		t.Fatalf("Secret %q carries no session_signing_key", keys.Secret.SecretName)
	}
	// go-flags decodes this file with jwt.ParseRSAPrivateKeyFromPEM, which
	// accepts PKCS#1 or PKCS#8 PEM. sprig's genPrivateKey "rsa" emits PKCS#1.
	decoded := mustBase64(t, pem)
	if !strings.HasPrefix(decoded, "-----BEGIN RSA PRIVATE KEY-----") &&
		!strings.HasPrefix(decoded, "-----BEGIN PRIVATE KEY-----") {
		t.Fatalf("session_signing_key is not a PEM private key the web loader accepts: %.40q", decoded)
	}
}

func TestExplicitSigningKeySecretWinsAndSuppressesChartSecret(t *testing.T) {
	manifests := renderChart(t, "secrets.signingKeySecret=operator-keys")
	web := findDeployment(t, manifests, "-web")

	var keys *podVolume
	for index := range web.Spec.Template.Spec.Volumes {
		if web.Spec.Template.Spec.Volumes[index].Name == "keys" {
			keys = &web.Spec.Template.Spec.Volumes[index]
		}
	}
	if keys == nil {
		t.Fatal("web pod has no keys volume")
	}
	if keys.Secret == nil || keys.Secret.SecretName != "operator-keys" {
		t.Fatalf("keys volume ignored the operator Secret: %+v", keys)
	}
	if strings.Contains(manifests, "test-release-concourse-jetbridge-keys") {
		t.Fatal("chart generated its own keys Secret despite an explicit signingKeySecret")
	}
}

// TestArtifactDaemonMetricsAreScraped covers the monitoring gap: the daemon
// serves /metrics on its Service port, but only the web Service was selected.
func TestArtifactDaemonMetricsAreScraped(t *testing.T) {
	for _, tc := range []struct {
		name       string
		sets       []string
		wantScheme string
		wantTLS    bool
	}{
		// serviceMonitor.web.enabled=false keeps these cases about the daemon:
		// the web monitor requires web.metrics.enabled and is covered on its
		// own in TestWebServiceMonitorScrapesAPortTheWebNodeActuallyServes.
		{name: "plaintext", sets: []string{"serviceMonitor.enabled=true", "serviceMonitor.web.enabled=false"}},
		{
			name:       "mtls",
			sets:       []string{"serviceMonitor.enabled=true", "serviceMonitor.web.enabled=false", "artifactDaemon.tls.enabled=true"},
			wantScheme: "https",
			wantTLS:    true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifests := renderChart(t, tc.sets...)
			monitor := findServiceMonitor(t, manifests, "-artifact-daemon")
			service := findService(t, manifests, "-artifact-daemon")

			for key, value := range monitor.Spec.Selector.MatchLabels {
				if service.Metadata.Labels[key] != value {
					t.Fatalf("daemon ServiceMonitor selector %s=%s does not match the daemon Service label %q",
						key, value, service.Metadata.Labels[key])
				}
			}
			if len(monitor.Spec.Endpoints) != 1 {
				t.Fatalf("daemon ServiceMonitor endpoints = %d", len(monitor.Spec.Endpoints))
			}
			endpoint := monitor.Spec.Endpoints[0]
			if endpoint.Port != "http" || endpoint.Path != "/metrics" || endpoint.Interval == "" {
				t.Fatalf("daemon endpoint is not wired to the metrics port: %+v", endpoint)
			}
			if endpoint.Scheme != tc.wantScheme {
				t.Fatalf("daemon endpoint scheme = %q, want %q", endpoint.Scheme, tc.wantScheme)
			}
			// Under mTLS the daemon serves HTTPS on the same port, so a scrape
			// with no tlsConfig at all silently fails at the transport layer.
			if tc.wantTLS && len(endpoint.TLSConfig) == 0 {
				t.Fatal("daemon endpoint has no tlsConfig while the daemon serves HTTPS")
			}
		})
	}
}

func TestArtifactDaemonServiceMonitorIsOptional(t *testing.T) {
	manifests := renderChart(t, "serviceMonitor.enabled=true", "web.metrics.enabled=true", "serviceMonitor.artifactDaemon.enabled=false")
	// Asserted on the parsed objects, not on the "# Source:" template path, so
	// renaming the template file cannot make this pass while the daemon
	// ServiceMonitor still renders.
	if _, found := lookupServiceMonitor(manifests, "-artifact-daemon"); found {
		t.Fatal("daemon ServiceMonitor rendered despite serviceMonitor.artifactDaemon.enabled=false")
	}
	// The web monitor must survive the opt-out; without this the check above
	// would also pass if monitoring stopped rendering entirely.
	findServiceMonitor(t, manifests, "test-release-concourse-jetbridge")
}

// TestServiceMonitorsSelectTheReleaseNamespace pins the one thing a
// serviceMonitor.namespace override silently breaks: prometheus-operator
// defaults a ServiceMonitor's search space to the monitor's OWN namespace, so
// monitors relocated to a Prometheus namespace match zero Services — no error,
// no targets, no metrics.
func TestServiceMonitorsSelectTheReleaseNamespace(t *testing.T) {
	manifests := renderChart(t, "serviceMonitor.enabled=true", "web.metrics.enabled=true", "serviceMonitor.namespace=monitoring")
	for _, nameSuffix := range []string{"test-release-concourse-jetbridge", "-artifact-daemon"} {
		monitor := findServiceMonitor(t, manifests, nameSuffix)
		// renderChart does not pass -n, so the release namespace is "default"
		// while both monitors are pinned into "monitoring" above.
		if len(monitor.Spec.NamespaceSelector.MatchNames) != 1 ||
			monitor.Spec.NamespaceSelector.MatchNames[0] != "default" {
			t.Fatalf("ServiceMonitor %q does not select the release namespace: %+v",
				monitor.Metadata.Name, monitor.Spec.NamespaceSelector)
		}
	}
}

type secretDoc struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Data map[string]string `json:"data"`
}

type serviceDoc struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name   string            `json:"name"`
		Labels map[string]string `json:"labels"`
	} `json:"metadata"`
	Spec struct {
		Ports []struct {
			Name string `json:"name"`
			Port int    `json:"port"`
			// targetPort is an IntOrString: either a port number or a
			// container port name, so it cannot be typed more narrowly
			// without making findService skip the document entirely.
			TargetPort any `json:"targetPort"`
		} `json:"ports"`
	} `json:"spec"`
}

type serviceMonitorDoc struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		NamespaceSelector struct {
			MatchNames []string `json:"matchNames"`
		} `json:"namespaceSelector"`
		Selector struct {
			MatchLabels map[string]string `json:"matchLabels"`
		} `json:"selector"`
		Endpoints []struct {
			Port      string         `json:"port"`
			Path      string         `json:"path"`
			Interval  string         `json:"interval"`
			Scheme    string         `json:"scheme"`
			TLSConfig map[string]any `json:"tlsConfig"`
		} `json:"endpoints"`
	} `json:"spec"`
}

func findSecret(t *testing.T, manifests, name string) secretDoc {
	t.Helper()
	for _, doc := range strings.Split(manifests, "\n---") {
		var s secretDoc
		if err := yaml.Unmarshal([]byte(doc), &s); err != nil {
			continue
		}
		if s.Kind == "Secret" && s.Metadata.Name == name {
			return s
		}
	}
	t.Fatalf("no Secret named %q in rendered chart", name)
	return secretDoc{}
}

func findService(t *testing.T, manifests, nameSuffix string) serviceDoc {
	t.Helper()
	for _, doc := range strings.Split(manifests, "\n---") {
		var s serviceDoc
		if err := yaml.Unmarshal([]byte(doc), &s); err != nil {
			continue
		}
		if s.Kind == "Service" && strings.HasSuffix(s.Metadata.Name, nameSuffix) {
			return s
		}
	}
	t.Fatalf("no Service with name ending %q in rendered chart", nameSuffix)
	return serviceDoc{}
}

func findServiceMonitor(t *testing.T, manifests, nameSuffix string) serviceMonitorDoc {
	t.Helper()
	monitor, found := lookupServiceMonitor(manifests, nameSuffix)
	if !found {
		t.Fatalf("no ServiceMonitor with name ending %q in rendered chart", nameSuffix)
	}
	return monitor
}

func lookupServiceMonitor(manifests, nameSuffix string) (serviceMonitorDoc, bool) {
	for _, doc := range strings.Split(manifests, "\n---") {
		var m serviceMonitorDoc
		if err := yaml.Unmarshal([]byte(doc), &m); err != nil {
			continue
		}
		if m.Kind == "ServiceMonitor" && strings.HasSuffix(m.Metadata.Name, nameSuffix) {
			return m, true
		}
	}
	return serviceMonitorDoc{}, false
}
