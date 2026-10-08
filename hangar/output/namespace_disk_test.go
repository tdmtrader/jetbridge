package output

import (
	"testing"
	"time"
)

func TestTheScopeIsTheTenantAndTheStore(t *testing.T) {
	config := NamespaceConfig{Store: StoreDisk, StoreID: "volume-a", Bucket: "outputs", DeploymentPrefix: "deployment", TenantID: "tenant"}
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
	config.Store = StoreDisk
	config.StoreID = ""
	if _, err := DeriveNamespace(config); err == nil {
		t.Fatal("disk accepted missing identity")
	}
}

// Two installs sharing one bucket and prefix under different tenants never
// name one store, so neither's orphan sweep can take the other's objects.
func TestTwoTenantsInOneBucketAndPrefixAreTwoStores(t *testing.T) {
	config := NamespaceConfig{Store: StoreGCS, Bucket: "outputs", DeploymentPrefix: "deployment", TenantID: "tenant-a"}
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
