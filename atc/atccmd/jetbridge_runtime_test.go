package atccmd

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/worker/jetbridge"
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
	backendFactory.K8sArtifactLocator.Record("step-handle", "node-a", "/var/lib/artifacts/steps/step-handle")
	if node, found := reaperLocator.LocateNode("step-handle"); !found || node != "node-a" {
		t.Fatalf("the Reaper cannot locate a key a worker recorded: node %q, found %v", node, found)
	}
}
