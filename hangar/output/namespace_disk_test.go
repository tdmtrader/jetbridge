package output

import (
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/hangar"
)

func TestTheScopeIsTheTenantAndTheStoreAndNeverTheEpoch(t *testing.T) {
	config := NamespaceConfig{Store: StoreDisk, StoreID: "volume-a", Bucket: "outputs", DeploymentPrefix: "deployment", TenantID: "tenant", ActivationEpoch: 1}
	a, err := DeriveNamespace(config)
	if err != nil {
		t.Fatal(err)
	}
	if a.BucketFingerprint() != "disk://volume-a/outputs" {
		t.Fatal(a.BucketFingerprint())
	}
	config.StoreID = "volume-b"
	b, err := DeriveNamespace(config)
	if err != nil {
		t.Fatal(err)
	}
	if a.Scope() == b.Scope() {
		t.Fatal("replacing storage identity reused scope")
	}
	config.Store = StoreGCS
	c, err := DeriveNamespace(config)
	if err != nil {
		t.Fatal(err)
	}
	if c.Scope() != deriveScope(config.TenantID, "gs://outputs") || c.BucketFingerprint() != "gs://outputs" {
		t.Fatal("a GCS scope is not H(tenant, bucket)")
	}
	config.ActivationEpoch = 2
	d, err := DeriveNamespace(config)
	if err != nil {
		t.Fatal(err)
	}
	if d.Scope() != c.Scope() {
		t.Fatal("a different epoch derived a different scope; there is no rotation")
	}
	config.ActivationEpoch = 1
	config.Store = StoreDisk
	config.StoreID = ""
	if _, err := DeriveNamespace(config); err == nil {
		t.Fatal("disk accepted missing identity")
	}
}

// Two installs sharing one bucket and prefix under different tenants never
// name one store, so neither's orphan sweep can take the other's objects.
func TestTwoTenantsInOneBucketAndPrefixAreTwoStores(t *testing.T) {
	config := NamespaceConfig{Store: StoreGCS, Bucket: "outputs", DeploymentPrefix: "deployment", TenantID: "tenant-a", ActivationEpoch: 1}
	a, err := DeriveNamespace(config)
	if err != nil {
		t.Fatal(err)
	}
	config.TenantID = "tenant-b"
	b, err := DeriveNamespace(config)
	if err != nil {
		t.Fatal(err)
	}
	if a.StoreIdentity() == b.StoreIdentity() {
		t.Fatalf("two tenants in one bucket and prefix share store identity %q", a.StoreIdentity())
	}
	if a.MarkerFor("44444444-4444-4444-8444-444444444444", "sha256:1111111111111111111111111111111111111111111111111111111111111111", NewTimestamp(time.Now())).Store != a.StoreIdentity() {
		t.Fatal("a marker does not name its namespace's store")
	}
}

// A ref published under scope v1 for this tenant, store and epoch stays
// readable; another tenant's v1 scope does not.
func TestARefUnderTheScopeV1DerivationIsStillOwned(t *testing.T) {
	config := NamespaceConfig{Store: StoreDisk, StoreID: "volume-a", Bucket: "outputs", DeploymentPrefix: "deployment", TenantID: "tenant", ActivationEpoch: 3}
	namespace, err := DeriveNamespace(config)
	if err != nil {
		t.Fatal(err)
	}
	digest := hangar.Digest("sha256:1111111111111111111111111111111111111111111111111111111111111111")
	legacy := hangar.TreeRef{Scope: deriveLegacyScope("disk\x00volume-a\x00outputs\x00tenant", 3), Digest: digest, Generation: 1}
	if !namespace.OwnsRef(legacy) {
		t.Fatal("a scope-v1 ref of this tenant and store is not owned")
	}
	key, err := namespace.RefKey(legacy)
	if err != nil || !strings.Contains(key, string(legacy.Scope)) {
		t.Fatalf("the v1 ref's key %q does not derive under its own scope: %v", key, err)
	}
	other := hangar.TreeRef{Scope: deriveLegacyScope("disk\x00volume-a\x00outputs\x00another", 3), Digest: digest, Generation: 1}
	if namespace.OwnsRef(other) {
		t.Fatal("another tenant's v1 scope is owned")
	}
	if _, err := namespace.RefKey(other); err == nil {
		t.Fatal("a key was derived for another tenant's ref")
	}
}
