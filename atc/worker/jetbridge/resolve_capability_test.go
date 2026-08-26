package jetbridge

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/concourse/concourse/artifactcap"
	"github.com/concourse/concourse/atc/runtime"
)

func capKey() []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i + 1)
	}
	return k
}

// capabilitiesFrom pulls the signed tokens back out of the generated init
// container, which is the only place they exist — asserting on the pod spec is
// the only way to know the ATC actually signs.
func capabilitiesFrom(t *testing.T, inits []corev1.Container) []batchItem {
	t.Helper()
	if len(inits) != 1 {
		t.Fatalf("expected 1 init container, got %d", len(inits))
	}
	script := strings.Join(inits[0].Command, " ")
	start := strings.Index(script, `{"items":`)
	if start < 0 {
		t.Fatalf("no batch payload in the init command: %s", script)
	}
	rest := script[start:]
	end := strings.Index(rest, `}]}`)
	if end < 0 {
		t.Fatalf("could not delimit the batch payload: %s", rest)
	}
	var payload struct {
		Items []batchItem `json:"items"`
	}
	if err := json.Unmarshal([]byte(rest[:end+3]), &payload); err != nil {
		t.Fatalf("payload is not valid JSON (%v): %s", err, rest[:end+3])
	}
	return payload.Items
}

func backendWith(t *testing.T, key []byte, ttl time.Duration) *DaemonSetBackend {
	t.Helper()
	return NewDaemonSetBackend(Config{
		ArtifactDaemonHostPath:             "/var/concourse/artifacts",
		ArtifactDaemonResolveCapabilityKey: key,
		ArtifactDaemonResolveCapabilityTTL: ttl,
		PodSchedulingTimeout:               DefaultPodSchedulingTimeout,
		PodStartupTimeout:                  DefaultPodStartupTimeout,
	}, nil, nil)
}

// oneInput returns an input plus the volume and mount that must accompany it;
// without the matching pair BuildFetchInitContainers produces no items at all
// and every assertion below would pass on an empty payload.
func oneInput(b *DaemonSetBackend) ([]runtime.Input, []corev1.Volume, []corev1.VolumeMount) {
	return []runtime.Input{{Artifact: &testArtifact{handle: "vol-1"}, DestinationPath: "/tmp/build/in"}},
		[]corev1.Volume{b.StepVolume("input-0", "handle-1", "input-0")},
		[]corev1.VolumeMount{{Name: "input-0", MountPath: "/tmp/build/in"}}
}

// The ATC signs, and the daemon's verifier accepts what it signed. Neither half
// is worth anything without the other, and they live in different packages, so
// nothing else checks that they agree on the wire format.
func TestResolveCapability_ATCSignsWhatTheDaemonVerifies(t *testing.T) {
	key := capKey()
	b := backendWith(t, key, DefaultArtifactResolveCapabilityTTL)

	inputs, volumes, mounts := oneInput(b)
	inits, err := b.BuildFetchInitContainers("handle-1", inputs, volumes, mounts)
	if err != nil {
		t.Fatal(err)
	}
	items := capabilitiesFrom(t, inits)
	if len(items) == 0 {
		t.Fatal("no batch items were generated")
	}

	verifier, err := artifactcap.NewVerifier(key)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.Capability == "" {
			t.Fatalf("item %q carries no capability; the daemon will 403 it", item.Key)
		}
		if err := verifier.VerifyResolve(item.Capability, item.Key, item.Dest, time.Now()); err != nil {
			t.Errorf("the daemon would reject what the ATC signed for %q: %v", item.Key, err)
		}
	}
}

// No key configured means no capability, matching a daemon started without one.
func TestResolveCapability_UnsignedWhenNoKey(t *testing.T) {
	b := backendWith(t, nil, DefaultArtifactResolveCapabilityTTL)

	inputs, volumes, mounts := oneInput(b)
	inits, err := b.BuildFetchInitContainers("handle-1", inputs, volumes, mounts)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range capabilitiesFrom(t, inits) {
		if item.Capability != "" {
			t.Errorf("signed %q with no key configured", item.Key)
		}
	}
}

// A TTL below the floor disables signing rather than minting capabilities that
// expire before a slow pod can use them.
//
// The failure it prevents is nasty: the request is legitimate, the pod simply
// waited too long to be scheduled, and the operator sees a 403.
func TestResolveCapability_TooShortATTLDisablesSigning(t *testing.T) {
	minimum, err := MinimumArtifactResolveCapabilityTTL(DefaultPodSchedulingTimeout, DefaultPodStartupTimeout)
	if err != nil {
		t.Fatal(err)
	}
	if DefaultArtifactResolveCapabilityTTL <= minimum {
		t.Fatalf("the DEFAULT TTL %v does not clear its own floor %v — every deployment would 403",
			DefaultArtifactResolveCapabilityTTL, minimum)
	}

	b := backendWith(t, capKey(), minimum-time.Second)
	inputs, volumes, mounts := oneInput(b)
	inits, err := b.BuildFetchInitContainers("handle-1", inputs, volumes, mounts)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range capabilitiesFrom(t, inits) {
		if item.Capability != "" {
			t.Error("signed with a TTL below the floor; a slow pod would 403 on a legitimate request")
		}
	}
}

// The floor must cover scheduling, startup AND the init container's own retry
// budget — a capability signed at spec time is verified after all three.
func TestMinimumArtifactResolveCapabilityTTL_CoversTheWholeWait(t *testing.T) {
	got, err := MinimumArtifactResolveCapabilityTTL(15*time.Minute, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if want := 15*time.Minute + 5*time.Minute + ArtifactResolveInitRetryBudget + 5*time.Minute; got != want {
		t.Errorf("floor = %v, want %v", got, want)
	}
	if _, err := MinimumArtifactResolveCapabilityTTL(-time.Second, 0); err == nil {
		t.Error("a negative timeout was accepted")
	}
}
