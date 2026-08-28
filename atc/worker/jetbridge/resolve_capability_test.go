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

// A key that is configured MUST sign. The daemon behind the chart requires a
// capability unconditionally, so an ATC that quietly stops signing fails every
// build with no diagnostic — the exact fail-open/fail-closed mismatch this
// test pins down. A TTL below the floor is a misconfiguration that startup
// validation refuses; if an unvalidated config gets here anyway, signing with
// a short TTL 403s only pods slower than the TTL, while not signing 403s all
// of them.
func TestResolveCapability_TooShortTTLStillSigns(t *testing.T) {
	minimum, err := MinimumArtifactResolveCapabilityTTL(DefaultPodSchedulingTimeout, DefaultPodStartupTimeout)
	if err != nil {
		t.Fatal(err)
	}
	if DefaultArtifactResolveCapabilityTTL <= minimum {
		t.Fatalf("the DEFAULT TTL %v does not clear its own floor %v — every deployment would 403",
			DefaultArtifactResolveCapabilityTTL, minimum)
	}

	key := capKey()
	b := backendWith(t, key, minimum-time.Second)
	inputs, volumes, mounts := oneInput(b)
	inits, err := b.BuildFetchInitContainers("handle-1", inputs, volumes, mounts)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := artifactcap.NewVerifier(key)
	if err != nil {
		t.Fatal(err)
	}
	items := capabilitiesFrom(t, inits)
	if len(items) == 0 {
		t.Fatal("no batch items were generated")
	}
	for _, item := range items {
		if item.Capability == "" {
			t.Error("stopped signing on a TTL below the floor; an enforcing daemon now 403s every request instead of only slow pods")
			continue
		}
		if err := verifier.VerifyResolve(item.Capability, item.Key, item.Dest, time.Now()); err != nil {
			t.Errorf("the daemon would reject what the ATC signed for %q: %v", item.Key, err)
		}
	}
}

// Startup refuses the configurations that used to disable signing silently.
// The ATC either signs, or says exactly why it will not start — never neither.
func TestValidateResolveCapabilityConfig(t *testing.T) {
	minimum, err := MinimumArtifactResolveCapabilityTTL(DefaultPodSchedulingTimeout, DefaultPodStartupTimeout)
	if err != nil {
		t.Fatal(err)
	}
	base := func() Config {
		return Config{
			ArtifactDaemonHostPath:             "/var/concourse/artifacts",
			ArtifactDaemonResolveCapabilityKey: capKey(),
			ArtifactDaemonResolveCapabilityTTL: DefaultArtifactResolveCapabilityTTL,
			PodSchedulingTimeout:               DefaultPodSchedulingTimeout,
			PodStartupTimeout:                  DefaultPodStartupTimeout,
		}
	}

	t.Run("valid key and TTL pass", func(t *testing.T) {
		if err := ValidateResolveCapabilityConfig(base()); err != nil {
			t.Fatalf("valid configuration refused: %v", err)
		}
	})

	t.Run("no key is signing deliberately off", func(t *testing.T) {
		cfg := base()
		cfg.ArtifactDaemonResolveCapabilityKey = nil
		if err := ValidateResolveCapabilityConfig(cfg); err != nil {
			t.Fatalf("keyless configuration refused: %v", err)
		}
	})

	t.Run("TTL at or below the floor is refused with the floor named", func(t *testing.T) {
		cfg := base()
		cfg.ArtifactDaemonResolveCapabilityTTL = minimum
		err := ValidateResolveCapabilityConfig(cfg)
		if err == nil {
			t.Fatal("a TTL at the floor was accepted; it used to disable signing silently")
		}
		if !strings.Contains(err.Error(), minimum.String()) {
			t.Errorf("error %q does not name the floor %v the operator must clear", err, minimum)
		}
	})

	t.Run("raised pod timeouts move the floor and are still refused", func(t *testing.T) {
		// The live shape of the bug: values.yaml exposes podStartupTimeout, an
		// operator raises it for slow image pulls, and the default 2h TTL sinks
		// below the recomputed floor without any flag changing.
		cfg := base()
		cfg.PodStartupTimeout = 3 * time.Hour
		if err := ValidateResolveCapabilityConfig(cfg); err == nil {
			t.Fatal("a floor raised past the TTL by pod timeouts was accepted")
		}
	})

	t.Run("malformed key is refused", func(t *testing.T) {
		cfg := base()
		cfg.ArtifactDaemonResolveCapabilityKey = []byte("short")
		if err := ValidateResolveCapabilityConfig(cfg); err == nil {
			t.Fatal("a malformed key was accepted; it used to disable signing silently")
		}
	})

	t.Run("non-positive pod timeout is normalised to the default, like the pod code", func(t *testing.T) {
		// podSchedulingTimeout/podStartupTimeout treat <=0 as "use the default",
		// and validation computes the floor from those same effective values —
		// so a zero or negative Config timeout is not fatal, it just contributes
		// the default. With the default 2h TTL that still clears the floor.
		for _, d := range []time.Duration{0, -time.Second} {
			cfg := base()
			cfg.PodSchedulingTimeout = d
			cfg.PodStartupTimeout = d
			if err := ValidateResolveCapabilityConfig(cfg); err != nil {
				t.Errorf("timeout %v (effective = default) was refused: %v", d, err)
			}
		}
	})

	t.Run("floor uses the EFFECTIVE timeout, not the raw zero", func(t *testing.T) {
		// Zero scheduling + startup means 15m + 5m at runtime, so the floor is
		// the full default floor — a TTL just under it must still be refused,
		// proving validation did not compute the floor from the raw zeros.
		cfg := base()
		cfg.PodSchedulingTimeout = 0
		cfg.PodStartupTimeout = 0
		cfg.ArtifactDaemonResolveCapabilityTTL = minimum - time.Second
		if err := ValidateResolveCapabilityConfig(cfg); err == nil {
			t.Fatal("a TTL below the effective (default) floor was accepted; the floor was computed from raw zeros")
		}
	})
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
