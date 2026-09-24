package output

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/hangar"
)

// fixedInstant is the one clock reading these tables use. Nothing here is about
// elapsed time, and a wall clock would make the marker round-trip depend on how
// long the test took.
var fixedInstant = time.Date(2026, 9, 9, 4, 5, 6, 123456789, time.UTC)

// The derivation is a pure function over authenticated configuration, and a
// table is the honest shape for it: Req 20 is a closed statement about which
// inputs may reach the bucket, the prefix and the scope, so the test that
// proves it has to enumerate the inputs that must not.

func validNamespaceConfig() NamespaceConfig {
	return NamespaceConfig{
		Store:             StoreGCS,
		Bucket:            "deployment-output",
		DeploymentPrefix:  "deployments/blue",
		TenantID:          "tenant-a",
		CacheBucket:       "deployment-durable-cache",
		StrictInputBucket: "deployment-strict-input",
		ActivationEpoch:   7,
	}
}

func TestDeriveNamespaceRefusesEveryConfigurationRequirement20Forbids(t *testing.T) {
	// The positive control, first: the valid configuration derives. Without
	// it, every refusal below would also pass on a function that refused
	// everything.
	namespace, err := DeriveNamespace(validNamespaceConfig())
	if err != nil {
		t.Fatalf("the valid configuration did not derive: %v", err)
	}
	if namespace.IsZero() {
		t.Fatal("the valid configuration derived a zero namespace")
	}

	for name, testCase := range map[string]struct {
		mutate   func(*NamespaceConfig)
		sentinel error
		says     string
	}{
		"the cache bucket": {
			mutate:   func(c *NamespaceConfig) { c.Bucket = c.CacheBucket },
			sentinel: ErrConflict,
			says:     "durable cache bucket",
		},
		"the strict-input bucket": {
			mutate:   func(c *NamespaceConfig) { c.Bucket = c.StrictInputBucket },
			sentinel: ErrConflict,
			says:     "strict-input bucket",
		},
		"a shared bucket with prefix-only isolation": {
			mutate:   func(c *NamespaceConfig) { c.SharedBucketPrefixOnlyIsolation = true },
			sentinel: ErrUnauthorized,
			says:     "not an activation-compatible substitute",
		},
		"a store that is not native GCS": {
			mutate:   func(c *NamespaceConfig) { c.Store = "filesystem" },
			sentinel: ErrUnsupportedProtocol,
			says:     "unsupported output store",
		},
		"an S3-compatible store": {
			mutate:   func(c *NamespaceConfig) { c.Store = "s3" },
			sentinel: ErrUnsupportedProtocol,
			says:     "unsupported output store",
		},
		"an absolute prefix": {
			mutate:   func(c *NamespaceConfig) { c.DeploymentPrefix = "/deployments/blue" },
			sentinel: ErrIncomplete,
			says:     "prefix",
		},
		"a prefix that climbs": {
			mutate:   func(c *NamespaceConfig) { c.DeploymentPrefix = "deployments/../../etc" },
			sentinel: ErrIncomplete,
			says:     "prefix",
		},
		"a prefix with a backslash": {
			mutate:   func(c *NamespaceConfig) { c.DeploymentPrefix = `deployments\blue` },
			sentinel: ErrIncomplete,
			says:     "prefix",
		},
		"a prefix with an uppercase segment": {
			mutate:   func(c *NamespaceConfig) { c.DeploymentPrefix = "Deployments/blue" },
			sentinel: ErrIncomplete,
			says:     "prefix",
		},
		"no bucket at all": {
			mutate:   func(c *NamespaceConfig) { c.Bucket = "  " },
			sentinel: ErrIncomplete,
			says:     "no output bucket",
		},
		"no authenticated tenant": {
			mutate:   func(c *NamespaceConfig) { c.TenantID = "" },
			sentinel: ErrIncomplete,
			says:     "opaque scope has nothing to be derived from",
		},
		"no active epoch": {
			mutate:   func(c *NamespaceConfig) { c.ActivationEpoch = 0 },
			sentinel: ErrIncomplete,
			says:     "activation epoch",
		},
	} {
		config := validNamespaceConfig()
		testCase.mutate(&config)

		derived, err := DeriveNamespace(config)
		if err == nil {
			t.Errorf("%s derived the namespace %q/%q instead of being refused",
				name, derived.Bucket(), derived.Scope())

			continue
		}
		if !errors.Is(err, testCase.sentinel) {
			t.Errorf("%s was refused as %v, expected %v", name, err, testCase.sentinel)
		}
		if !strings.Contains(err.Error(), testCase.says) {
			t.Errorf("%s was refused with %q, which does not say %q", name, err, testCase.says)
		}
		if !derived.IsZero() {
			t.Errorf("%s was refused and still returned a usable namespace", name)
		}
	}
}

func TestTheDerivedNamespaceIsStableOpaqueAndPerTenantPerEpoch(t *testing.T) {
	first, err := DeriveNamespace(validNamespaceConfig())
	if err != nil {
		t.Fatalf("deriving: %v", err)
	}
	again, err := DeriveNamespace(validNamespaceConfig())
	if err != nil {
		t.Fatalf("deriving again: %v", err)
	}
	if first.Scope() != again.Scope() {
		t.Errorf("the same configuration derived two scopes, %q and %q; a namespace that moves "+
			"cannot be published into twice", first.Scope(), again.Scope())
	}
	if err := first.Scope().Validate(); err != nil {
		t.Errorf("the derived scope is not a valid hangar.Scope: %v", err)
	}

	otherTenant := validNamespaceConfig()
	otherTenant.TenantID = "tenant-b"
	other, err := DeriveNamespace(otherTenant)
	if err != nil {
		t.Fatalf("deriving for another tenant: %v", err)
	}
	if other.Scope() == first.Scope() {
		t.Error("two tenants derived one scope, so one tenant's capture would deduplicate " +
			"against another tenant's object")
	}

	nextEpoch := validNamespaceConfig()
	nextEpoch.ActivationEpoch = 8
	rotated, err := DeriveNamespace(nextEpoch)
	if err != nil {
		t.Fatalf("deriving for the next epoch: %v", err)
	}
	if rotated.Scope() == first.Scope() {
		t.Error("a rotation derived the same scope; the scope is derived from the active epoch " +
			"as well as the tenant, so that a rotated key ring publishes into its own namespace")
	}

	// Opaque means the tenant is not readable out of it. This is the property
	// that makes it safe in an object key that inventory lists.
	if strings.Contains(string(first.Scope()), "tenant") {
		t.Errorf("the derived scope %q carries the tenant identity in the clear", first.Scope())
	}
}

func TestTheObjectKeyComesOnlyFromTheDerivedNamespace(t *testing.T) {
	namespace, err := DeriveNamespace(validNamespaceConfig())
	if err != nil {
		t.Fatalf("deriving: %v", err)
	}

	digest := hangar.Digest("sha256:" + strings.Repeat("ab", 32))
	key, err := namespace.ObjectKey(digest)
	if err != nil {
		t.Fatalf("deriving the key: %v", err)
	}

	expected, err := hangar.TreeKey("deployments/blue", namespace.Scope(), digest)
	if err != nil {
		t.Fatalf("the foundation refused the same identity: %v", err)
	}
	if key != expected {
		t.Errorf("the output key is %q, the foundation's key for the same identity is %q. They "+
			"are deliberately the same shape in different buckets; a second key layout is a "+
			"second thing to keep in step", key, expected)
	}
	if !strings.HasPrefix(key, "deployments/blue/") {
		t.Errorf("the key %q does not start with the authenticated deployment prefix", key)
	}
	if !strings.HasPrefix(key, namespace.ListPrefix()) {
		t.Errorf("the key %q is not under the list prefix %q, so a bucket-wide sweep would not "+
			"find it", key, namespace.ListPrefix())
	}

	// The list prefix stops above the scope on purpose: a rotation derives a
	// new one, and a sweep under the current scope would call every previous
	// epoch's object an unmanaged stranger.
	if strings.Contains(namespace.ListPrefix(), string(namespace.Scope())) {
		t.Errorf("the list prefix %q descends into the scope", namespace.ListPrefix())
	}

	if _, err := namespace.ObjectKey("sha256:not-a-digest"); err == nil {
		t.Error("a malformed digest produced a key")
	}
	var underived OutputNamespace
	if _, err := underived.ObjectKey(digest); err == nil {
		t.Error("an underived namespace produced a key, which would publish into the empty bucket")
	}
}

func TestACallerChosenNamespaceFieldIsRefusedRatherThanIgnored(t *testing.T) {
	// The control, first: a request that names nothing is served.
	if err := (CallerNamespaceRequest{}).Validate(); err != nil {
		t.Fatalf("a request naming no namespace field was refused: %v", err)
	}

	for field, request := range map[string]CallerNamespaceRequest{
		"bucket": {Bucket: "somebody-elses-bucket"},
		"scope":  {Scope: "o0000000000000000000000000000000000000000"},
		"key":    {Key: "hangar/v1/scopes/other/trees/sha256/dead.tar.zst"},
		"prefix": {Prefix: "deployments/red"},
	} {
		err := request.Validate()
		if err == nil {
			t.Errorf("a request naming a %s was served", field)

			continue
		}
		if !errors.Is(err, ErrUnauthorized) {
			t.Errorf("a request naming a %s was refused as %v, expected ErrUnauthorized", field, err)
		}
		if !strings.Contains(err.Error(), field) {
			t.Errorf("the refusal for a caller-chosen %s does not name the field: %q", field, err)
		}
	}
}

func TestTheMarkerAndTheKeyAgreeByConstruction(t *testing.T) {
	namespace, err := DeriveNamespace(validNamespaceConfig())
	if err != nil {
		t.Fatalf("deriving: %v", err)
	}

	digest := hangar.Digest("sha256:" + strings.Repeat("cd", 32))
	reservation := ReservationID("44444444-4444-4444-8444-444444444444")
	marker := namespace.MarkerFor(reservation, digest, NewTimestamp(fixedInstant))

	if err := marker.Validate(); err != nil {
		t.Fatalf("the derived marker does not validate: %v", err)
	}
	if marker.Version != MarkerVersion {
		t.Errorf("the marker version is %q, this cohort writes %q", marker.Version, MarkerVersion)
	}
	if marker.Scope != namespace.Scope() {
		t.Errorf("the marker says scope %q, the namespace derives %q. An object whose marker "+
			"disagrees with its key is the corruption ParseObjectMarker exists to notice",
			marker.Scope, namespace.Scope())
	}
	if marker.ActivationEpoch != namespace.ActivationEpoch() {
		t.Errorf("the marker says epoch %d, the namespace was derived under %d",
			marker.ActivationEpoch, namespace.ActivationEpoch())
	}

	// Round-tripping through the wire form is what the publisher actually
	// writes, and a marker that parses back to a different value would be a
	// marker nothing could verify.
	parsed, err := ParseObjectMarker(marker.Metadata())
	if err != nil {
		t.Fatalf("the marker this cohort writes does not parse: %v", err)
	}
	if parsed != marker {
		t.Errorf("the marker did not round-trip: wrote %+v, read %+v", marker, parsed)
	}
	if !parsed.Matches(namespace.Ref(digest, 1725830823000001)) {
		t.Error("the parsed marker does not match the tree ref for the same identity")
	}

	// A wrong version is a typed collision, not a parse failure: it is a
	// deliberate statement by some other cohort and must never be overwritten.
	wrongVersion := marker.Metadata()
	wrongVersion[MarkerKeyVersion] = "hangar-output-v2"
	if _, err := ParseObjectMarker(wrongVersion); !errors.Is(err, ErrConflict) {
		t.Errorf("a wrong marker version parsed as %v, expected ErrConflict", err)
	}

	// And no marker at all is unmanaged, which is ErrNotFound and never a
	// cache miss the publisher fills in.
	unmarked := marker.Metadata()
	delete(unmarked, MarkerKeyVersion)
	if _, err := ParseObjectMarker(unmarked); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unmarked object parsed as %v, expected ErrNotFound", err)
	}
}
