package jetbridge

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/concourse/concourse/artifactcap"
	"github.com/concourse/concourse/artifactwire"
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
func capabilitiesFrom(t *testing.T, inits []corev1.Container) []artifactwire.ResolveRequest {
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
		Items []artifactwire.ResolveRequest `json:"items"`
	}
	if err := json.Unmarshal([]byte(rest[:end+3]), &payload); err != nil {
		t.Fatalf("payload is not valid JSON (%v): %s", err, rest[:end+3])
	}
	return payload.Items
}

func backendWith(t *testing.T, key []byte) *DaemonSetBackend {
	t.Helper()
	return NewDaemonSetBackend(Config{
		ArtifactDaemonHostPath:             "/var/concourse/artifacts",
		ArtifactDaemonResolveCapabilityKey: key,
		PodSchedulingTimeout:               DefaultPodSchedulingTimeout,
		PodStartupTimeout:                  DefaultPodStartupTimeout,
	}, nil, nil, nil)
}

// oneInput returns an input plus the volume and mount that must accompany it;
// without the matching pair BuildFetchInitContainers produces no items at all
// and every assertion below would pass on an empty payload.
func oneInput(b *DaemonSetBackend) ([]runtime.Input, []corev1.Volume, []corev1.VolumeMount) {
	return []runtime.Input{{Artifact: constructionArtifact("vol-1", "worker-1"), DestinationPath: "/tmp/build/in"}},
		[]corev1.Volume{b.StepVolume("input-0", "handle-1", "input-0")},
		[]corev1.VolumeMount{{Name: "input-0", MountPath: "/tmp/build/in"}}
}

// The ATC signs, and the daemon's verifier accepts what it signed. Neither half
// is worth anything without the other, and they live in different packages, so
// nothing else checks that they agree on the wire format.
func TestResolveCapability_ATCSignsWhatTheDaemonVerifies(t *testing.T) {
	key := capKey()
	b := backendWith(t, key)

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
	b := backendWith(t, nil)

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

// Startup keeps key validation even though expiry needs no configuration.
func TestValidateResolveCapabilityConfig(t *testing.T) {
	for _, key := range [][]byte{nil, capKey()} {
		if err := ValidateResolveCapabilityConfig(Config{ArtifactDaemonResolveCapabilityKey: key}); err != nil {
			t.Fatal(err)
		}
	}
	if err := ValidateResolveCapabilityConfig(Config{ArtifactDaemonResolveCapabilityKey: []byte("short")}); err == nil {
		t.Fatal("malformed signing key accepted")
	}
}

func TestResolveCapabilityCoversEffectivePodWait(t *testing.T) {
	for _, tc := range []struct {
		name                string
		scheduling, startup time.Duration
		want                time.Duration
	}{
		{"defaults", DefaultPodSchedulingTimeout, DefaultPodStartupTimeout, 56 * time.Minute},
		{"zero", 0, 0, 56 * time.Minute},
		{"negative", -time.Second, -time.Second, 56 * time.Minute},
		{"deployed scheduling", 60 * time.Minute, 5 * time.Minute, 101 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := backendWith(t, capKey())
			b.config.PodSchedulingTimeout = tc.scheduling
			b.config.PodStartupTimeout = tc.startup
			before := time.Now()
			inputs, volumes, mounts := oneInput(b)
			inits, err := b.BuildFetchInitContainers("handle-1", inputs, volumes, mounts)
			if err != nil {
				t.Fatal(err)
			}
			after := time.Now()
			items := capabilitiesFrom(t, inits)
			if len(items) != 1 {
				t.Fatalf("want one signed item, got %d", len(items))
			}
			item := items[0]
			verifier, err := artifactcap.NewVerifier(capKey())
			if err != nil {
				t.Fatal(err)
			}
			// The whole wait fits with one five-minute margin, including the
			// signed wire timestamp's second precision.
			if err := verifier.VerifyResolve(item.Capability, item.Key, item.Dest, before.Add(tc.want-time.Second)); err != nil {
				t.Fatalf("capability expired before intended wait: %v", err)
			}
			if err := verifier.VerifyResolve(item.Capability, item.Key, item.Dest, after.Add(tc.want)); err == nil {
				t.Fatal("capability outlived its derived expiry")
			}
		})
	}
}

func TestResolveCapabilityExpiryDoesNotOverflowDuration(t *testing.T) {
	large := time.Duration(1<<63 - 1)
	cfg := Config{PodSchedulingTimeout: large, PodStartupTimeout: large}
	before := time.Now().Add(large).Add(large).Add(31 * time.Minute).Add(5 * time.Minute)
	got := resolveCapabilityExpiry(cfg)
	after := time.Now().Add(large).Add(large).Add(31 * time.Minute).Add(5 * time.Minute)
	if got.Before(before) || got.After(after) {
		t.Fatalf("expiry wrapped: %v", got)
	}
}
