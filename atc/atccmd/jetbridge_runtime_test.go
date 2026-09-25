package atccmd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/hangar"
	"github.com/jessevdk/go-flags"
)

// A kubeconfig naming a server nothing listens on. Building a clientset from it
// never dials, so the runtime's components can be assembled without a cluster.
const unreachableKubeconfig = `apiVersion: v1
kind: Config
clusters:
- name: unreachable
  cluster:
    server: https://127.0.0.1:1
contexts:
- name: unreachable
  context:
    cluster: unreachable
    user: nobody
current-context: unreachable
users:
- name: nobody
  user:
    token: not-a-token
`

func writeKubeconfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(path, []byte(unreachableKubeconfig), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// jetbridgeCommand is the smallest command that turns the JetBridge runtime on.
func jetbridgeCommand(t *testing.T) *RunCommand {
	t.Helper()
	cmd := &RunCommand{}
	cmd.Kubernetes.Namespace = "concourse"
	cmd.Kubernetes.Kubeconfig = writeKubeconfig(t)
	cmd.Kubernetes.ArtifactDaemonHostPath = "/var/lib/artifacts"
	cmd.Kubernetes.ArtifactDaemonService = "artifact-daemon"
	cmd.Kubernetes.ArtifactDaemonPort = 7780
	return cmd
}

// reaperArtifactLocator reads the artifact locator a Reaper was handed. The
// field is unexported and the Reaper has no reason to expose it, so the test
// reads it by reflection; a rename fails here, loudly, rather than passing.
func reaperArtifactLocator(t *testing.T, reaper *jetbridge.Reaper) *jetbridge.ArtifactLocator {
	t.Helper()
	field := reflect.ValueOf(reaper).Elem().FieldByName("artifactLocator")
	if !field.IsValid() {
		t.Fatal("jetbridge.Reaper has no artifactLocator field; update reaperArtifactLocator")
	}
	return (*jetbridge.ArtifactLocator)(field.UnsafePointer())
}

func findReaper(t *testing.T, components []RunnableComponent) *jetbridge.Reaper {
	t.Helper()
	for _, c := range components {
		if c.Component.Name == atc.ComponentK8sWorkerReaper {
			reaper, ok := c.Runnable.(*jetbridge.Reaper)
			if !ok {
				t.Fatalf("%s component is a %T, not a *jetbridge.Reaper", c.Component.Name, c.Runnable)
			}
			return reaper
		}
	}
	t.Fatalf("no %s component among %d", atc.ComponentK8sWorkerReaper, len(components))
	return nil
}

// The Reaper can only drop an artifact the workers recorded, and only forgets a
// key (ArtifactLocator.Remove has no other caller) in the same map. Given a
// locator of its own it finds nothing, deletes nothing on the daemon, and the
// workers' map grows until the web restarts.
//
// Startup order is reproduced: both pools (API, then backend) are built before
// the backend components.
func TestTheReaperSharesTheWorkersArtifactLocator(t *testing.T) {
	cmd := jetbridgeCommand(t)

	apiFactory, _, err := cmd.workerFactory(nil, nil, nil)
	if err != nil {
		t.Fatalf("API worker factory: %v", err)
	}
	backendFactory, _, err := cmd.workerFactory(nil, nil, nil)
	if err != nil {
		t.Fatalf("backend worker factory: %v", err)
	}
	components, err := cmd.jetbridgeComponents(lagertest.NewTestLogger("test"), nil, nil, nil)
	if err != nil {
		t.Fatalf("runtime components: %v", err)
	}
	reaperLocator := reaperArtifactLocator(t, findReaper(t, components))

	if apiFactory.K8sArtifactLocator == nil || reaperLocator == nil {
		t.Fatalf("a nil locator: API pool %p, Reaper %p", apiFactory.K8sArtifactLocator, reaperLocator)
	}
	if apiFactory.K8sArtifactLocator != backendFactory.K8sArtifactLocator {
		t.Fatalf("the two pools hold different locators: API %p, backend %p",
			apiFactory.K8sArtifactLocator, backendFactory.K8sArtifactLocator)
	}
	if reaperLocator != backendFactory.K8sArtifactLocator {
		t.Fatalf("the Reaper holds its own locator %p, not the workers' %p",
			reaperLocator, backendFactory.K8sArtifactLocator)
	}

	// And the sharing is the point: what a worker records, the Reaper finds.
	// A worker records a step's outputs under their VOLUME handles; the Reaper
	// is handed the CONTAINER handle and finds the step through the index.
	backendFactory.K8sArtifactLocator.RecordStepOutput("step-handle", "step-handle-output-result", "node-a", "step-handle/result")
	step := reaperLocator.Step("step-handle")
	if len(step.Keys) != 1 || step.Keys[0] != "step-handle-output-result" ||
		len(step.Nodes) != 1 || step.Nodes[0] != "node-a" {
		t.Fatalf("the Reaper cannot find, by container handle, what a worker recorded: %+v", step)
	}
}

// Config fields no flag reaches, each with the reason. A field added to
// jetbridge.Config without a flag mapping fails the completeness check below
// until it is mapped or listed here.
var jetbridgeConfigFieldsWithoutAFlag = map[string]string{}

// Every flag the runtime reads reaches the one assembled Config, and every
// Config field is either set from a flag or listed as flagless.
func TestTheJetbridgeConfigIsAssembledFromTheFlags(t *testing.T) {
	dir := t.TempDir()
	resolveKey := filepath.Join(dir, "resolve.key")
	if err := os.WriteFile(resolveKey, bytes.Repeat([]byte{0x52}, 32), 0600); err != nil {
		t.Fatal(err)
	}
	kubeconfig := writeKubeconfig(t)

	cmd := &RunCommand{}
	parser := flags.NewParser(cmd, flags.None)
	if _, err := parser.ParseArgs([]string{
		"--kubernetes-namespace", "ci-steps",
		"--kubernetes-kubeconfig", kubeconfig,
		"--kubernetes-pod-startup-timeout", "7m",
		"--kubernetes-pod-scheduling-timeout", "11m",
		"--kubernetes-image-pull-secret", "pull-a",
		"--kubernetes-image-pull-secret", "pull-b",
		"--kubernetes-service-account", "step-runner",
		"--kubernetes-cache-store", "hostpath",
		"--kubernetes-cache-host-path", "/var/cache/steps",
		"--kubernetes-artifact-helper-image", "registry.example/helper:1",
		"--kubernetes-artifact-daemon-port", "7790",
		"--kubernetes-artifact-daemon-host-path", "/var/lib/jb-artifacts",
		"--kubernetes-artifact-daemon-resolve-capability-key", resolveKey,
		"--kubernetes-artifact-daemon-resolve-capability-ttl", "3h",
		"--kubernetes-artifact-daemon-service", "artifacts",
		"--kubernetes-artifact-daemon-namespace", "ci-daemons",
		"--kubernetes-artifact-daemon-warm-timeout", "45s",
		"--kubernetes-artifact-daemon-tls-cert", "/tls/artifact/cert.pem",
		"--kubernetes-artifact-daemon-tls-key", "/tls/artifact/key.pem",
		"--kubernetes-artifact-daemon-tls-ca-cert", "/tls/artifact/ca.pem",
		"--kubernetes-hangar-enabled",
		"--kubernetes-hangar-output-enabled",
		"--kubernetes-hangar-output-activation-epoch", "9",
		"--kubernetes-hangar-output-operation-timeout", "2m",
		"--kubernetes-hangar-output-daemon-port", "7791",
		"--kubernetes-hangar-output-tls-cert", "/tls/output/cert.pem",
		"--kubernetes-hangar-output-tls-key", "/tls/output/key.pem",
		"--kubernetes-hangar-output-tls-ca-cert", "/tls/output/ca.pem",
		"--kubernetes-hangar-output-tls-server-name", "output-daemon.ci-steps",
		"--kubernetes-image-registry-prefix", "registry.example/types",
		"--kubernetes-image-registry-secret", "registry-auth",
		"--kubernetes-base-resource-type", "git=registry.example/git:2",
		"--kubernetes-base-resource-type", "custom=registry.example/custom:1",
	}); err != nil {
		// Required flags unrelated to the runtime (the session signing key and
		// the like) are checked after every given flag has been applied.
		var flagsErr *flags.Error
		if !errors.As(err, &flagsErr) || flagsErr.Type != flags.ErrRequired {
			t.Fatalf("parse flags: %v", err)
		}
	}
	// Startup validation builds the warrant signer before anything is
	// assembled; stand in for it.
	signer, err := hangar.NewWarrantSigner(bytes.Repeat([]byte{0x57}, 32), 15*time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	cmd.k8sHangarWarrantSigner = signer

	rt, err := cmd.jetbridgeConfig()
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}

	resourceTypes := map[string]string{}
	for name, image := range jetbridge.DefaultResourceTypeImages {
		resourceTypes[name] = image
	}
	resourceTypes["git"] = "registry.example/git:2"
	resourceTypes["custom"] = "registry.example/custom:1"

	want := jetbridge.Config{
		Namespace:                          "ci-steps",
		KubeconfigPath:                     kubeconfig,
		PodStartupTimeout:                  7 * time.Minute,
		PodSchedulingTimeout:               11 * time.Minute,
		ResourceTypeImages:                 resourceTypes,
		ImagePullSecrets:                   []string{"pull-a", "pull-b"},
		ServiceAccount:                     "step-runner",
		CacheStore:                         "hostpath",
		CacheHostPath:                      "/var/cache/steps",
		ArtifactHelperImage:                "registry.example/helper:1",
		ImageRegistry:                      &jetbridge.ImageRegistryConfig{Prefix: "registry.example/types", SecretName: "registry-auth"},
		ArtifactDaemonPort:                 7790,
		ArtifactDaemonWarmTimeout:          45 * time.Second,
		ArtifactDaemonHostPath:             "/var/lib/jb-artifacts",
		ArtifactDaemonService:              "artifacts",
		ArtifactDaemonNamespace:            "ci-daemons",
		ArtifactDaemonTLSCert:              "/tls/artifact/cert.pem",
		ArtifactDaemonTLSKey:               "/tls/artifact/key.pem",
		ArtifactDaemonTLSCACert:            "/tls/artifact/ca.pem",
		ArtifactDaemonResolveCapabilityKey: bytes.Repeat([]byte{0x52}, 32),
		ArtifactDaemonResolveCapabilityTTL: 3 * time.Hour,
		ArtifactDaemonTLSEnabled:           true,
		OutputPlaneEnabled:                 true,
		OutputDaemonPort:                   7791,
		OutputOperationTimeout:             2 * time.Minute,
		OutputDaemonTLSCert:                "/tls/output/cert.pem",
		OutputDaemonTLSKey:                 "/tls/output/key.pem",
		OutputDaemonTLSCACert:              "/tls/output/ca.pem",
		OutputDaemonTLSServerName:          "output-daemon.ci-steps",
		OutputActivationEpoch:              9,
		HangarEnabled:                      true,
		HangarWarrantSigner:                signer,
	}
	if !reflect.DeepEqual(rt.config, want) {
		got, wanted := reflect.ValueOf(rt.config), reflect.ValueOf(want)
		for i := 0; i < got.NumField(); i++ {
			if !reflect.DeepEqual(got.Field(i).Interface(), wanted.Field(i).Interface()) {
				t.Errorf("Config.%s = %#v, want %#v", got.Type().Field(i).Name, got.Field(i).Interface(), wanted.Field(i).Interface())
			}
		}
	}

	fields := reflect.ValueOf(rt.config)
	for i := 0; i < fields.NumField(); i++ {
		name := fields.Type().Field(i).Name
		_, flagless := jetbridgeConfigFieldsWithoutAFlag[name]
		if fields.Field(i).IsZero() && !flagless {
			t.Errorf("Config.%s is not set by any flag: map it in assembleJetbridgeConfig or list it in jetbridgeConfigFieldsWithoutAFlag", name)
		}
	}

	if rt.clientset == nil || rt.restConfig == nil {
		t.Fatalf("no clientset (%v) or rest config (%v)", rt.clientset, rt.restConfig)
	}
	if rt.restConfig.Host != "https://127.0.0.1:1" {
		t.Errorf("rest config host %q is not the kubeconfig's server", rt.restConfig.Host)
	}
	// One clientset replaced three at client-go's default 5 QPS / burst 10;
	// it keeps their combined budget.
	if rt.restConfig.QPS != 15 || rt.restConfig.Burst != 30 {
		t.Errorf("shared clientset rate limit is %v QPS / burst %d, want 15 / 30", rt.restConfig.QPS, rt.restConfig.Burst)
	}
	if limiter := rt.clientset.CoreV1().RESTClient().GetRateLimiter(); limiter == nil || limiter.QPS() != 15 {
		t.Errorf("the clientset was not built with the shared rate limit: %v", limiter)
	}
}

// The configuration and its clientset are built once: both pools, the
// registrar and the Reaper share them, and a failure is not retried into a
// different answer.
func TestTheJetbridgeConfigIsAssembledOnce(t *testing.T) {
	cmd := jetbridgeCommand(t)

	apiFactory, _, err := cmd.workerFactory(nil, nil, nil)
	if err != nil {
		t.Fatalf("API worker factory: %v", err)
	}
	backendFactory, _, err := cmd.workerFactory(nil, nil, nil)
	if err != nil {
		t.Fatalf("backend worker factory: %v", err)
	}
	rt, err := cmd.jetbridgeConfig()
	if err != nil {
		t.Fatal(err)
	}
	if apiFactory.K8sClientset != rt.clientset || backendFactory.K8sClientset != rt.clientset {
		t.Fatalf("the pools built their own clientsets: API %p, backend %p, assembled %p",
			apiFactory.K8sClientset, backendFactory.K8sClientset, rt.clientset)
	}
	if apiFactory.K8sConfig == backendFactory.K8sConfig {
		t.Fatal("the pools share one *Config; each must hold its own copy")
	}

	broken := jetbridgeCommand(t)
	broken.Kubernetes.CacheStore = "tmpfs"
	if _, _, err := broken.workerFactory(nil, nil, nil); err == nil || !strings.Contains(err.Error(), `invalid --kubernetes-cache-store value "tmpfs"`) {
		t.Fatalf("an unknown cache store reached the worker factory: %v", err)
	}
	if _, err := broken.jetbridgeComponents(lagertest.NewTestLogger("test"), nil, nil, nil); err == nil {
		t.Fatal("an unknown cache store reached the Reaper")
	}
	broken.Kubernetes.CacheStore = jetbridge.CacheStoreHostPath
	if _, err := broken.jetbridgeConfig(); err == nil {
		t.Fatal("a failed assembly was retried: the configuration must be assembled exactly once")
	}
}
