package output

import "testing"

func TestDiskNamespacePinsStoreIdentityAndPreservesGCSScopes(t *testing.T) {
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
	if c.Scope() != deriveScope(config.TenantID, config.ActivationEpoch) || c.BucketFingerprint() != "gs://outputs" {
		t.Fatal("GCS compatibility changed")
	}
	config.Store = StoreDisk
	config.StoreID = ""
	if _, err := DeriveNamespace(config); err == nil {
		t.Fatal("disk accepted missing identity")
	}
}
