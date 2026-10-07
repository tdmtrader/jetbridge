package output

import "testing"

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
