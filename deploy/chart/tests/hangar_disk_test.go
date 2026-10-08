package tests

import (
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"
)

var diskSets = []string{
	"hangarStorage.disk.enabled=true",
	"hangarStorage.disk.storeID=store-1",
	"hangarStorage.disk.tls.existingSecret=storage-tls",
	"hangarStorage.disk.credentials.existingSecret=storage-credentials",
	"hangarOutput.store=disk",
	`artifactDaemon.serviceAccount.annotations.iam\.gke\.io/gcp-service-account=`,
	`serviceAccount.annotations.iam\.gke\.io/gcp-service-account=`,
}

func TestDiskStorageRendersWithoutGCSAndProjectsOnlyEachRolesCredential(t *testing.T) {
	out := renderOutput(t, diskSets...)
	if strings.Contains(out, "policy-attestor") || strings.Contains(out, "--output-store=gcs") {
		t.Fatal("disk render requires GCS or retired attestation")
	}
	// The artifact daemon holds one role's credential, the publisher's, for
	// its output plane; it publishes captures and Run input publications and
	// reads them back with it. The web holds the other two, one volume each:
	// the inventory token its orphan sweep lists with, and the reclaimer token
	// it deletes with.
	roles := map[string]map[string]string{
		"artifact-daemon": {"hangar-output-disk-client": "publisher"},
		webComponent:      {"hangar-output-list": "inventory", "hangar-output-delete": "reclaimer"},
	}
	found := 0
	for _, doc := range documentsIn(t, out) {
		var pod corev1.PodSpec
		switch doc.kind {
		case "Deployment":
			var d appsv1.Deployment
			if err := yaml.UnmarshalStrict([]byte(doc.body), &d); err != nil {
				t.Fatal(err)
			}
			pod = d.Spec.Template.Spec
		case "DaemonSet":
			var d appsv1.DaemonSet
			if err := yaml.UnmarshalStrict([]byte(doc.body), &d); err != nil {
				t.Fatal(err)
			}
			pod = d.Spec.Template.Spec
		default:
			continue
		}
		var volumes map[string]string
		for component, r := range roles {
			if strings.HasSuffix(doc.name, "-"+component) {
				volumes = r
			}
		}
		for _, c := range pod.Containers {
			for _, m := range c.VolumeMounts {
				exists := false
				for _, v := range pod.Volumes {
					if m.Name == v.Name {
						exists = true
					}
				}
				if !exists {
					t.Errorf("%s mount %s has no volume", doc.name, m.Name)
				}
			}
		}
		if volumes == nil {
			continue
		}
		found++
		for volume, role := range volumes {
			hasCredential := false
			for _, v := range pod.Volumes {
				if v.Name != volume {
					continue
				}
				if v.Projected == nil {
					t.Fatalf("%s credential volume %s is not projected", doc.name, volume)
				}
				for _, source := range v.Projected.Sources {
					if source.Secret == nil || source.Secret.Name != "storage-credentials" {
						continue
					}
					hasCredential = true
					if len(source.Secret.Items) != 1 || source.Secret.Items[0].Key != role {
						t.Errorf("%s's %s can access other roles: %+v", doc.name, volume, source.Secret.Items)
					}
				}
			}
			if !hasCredential {
				t.Errorf("%s missing %s credential", doc.name, role)
			}
		}
	}
	if found != len(roles) {
		t.Fatalf("found %d role workloads, expected %d", found, len(roles))
	}
}

func TestDiskInitializationIsExplicitAndStopsTheOwnerDuringProvisioning(t *testing.T) {
	for _, initialize := range []bool{false, true} {
		sets := append([]string{}, diskSets...)
		if initialize {
			sets = append(sets, "hangarStorage.disk.initialize=true")
		}
		out := renderOutput(t, sets...)
		d := objectNamed(t, out, "Deployment", "-hangar-store")
		var deployment appsv1.Deployment
		if err := yaml.UnmarshalStrict([]byte(d.body), &deployment); err != nil {
			t.Fatal(err)
		}
		want := int32(1)
		if initialize {
			want = 0
		}
		if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != want {
			t.Fatalf("initialize=%v: owner replicas must be %d", initialize, want)
		}
		if strings.Contains(out, "--initialize-only") != initialize {
			t.Fatal("initialization is not explicitly gated")
		}
	}
}

// The store serves two namespaces, cache and output. It is handed the output
// namespace and no other: Run inputs are input publications in it.
func TestTheDiskStoreIsHandedOnlyTheOutputNamespace(t *testing.T) {
	out := renderOutput(t, diskSets...)
	d := objectNamed(t, out, "Deployment", "-hangar-store")
	var deployment appsv1.Deployment
	if err := yaml.UnmarshalStrict([]byte(d.body), &deployment); err != nil {
		t.Fatal(err)
	}
	if len(deployment.Spec.Template.Spec.Containers) != 1 {
		t.Fatalf("hangar-store containers=%d, want 1", len(deployment.Spec.Template.Spec.Containers))
	}
	c := deployment.Spec.Template.Spec.Containers[0]
	namespaces := []string{}
	for _, arg := range append(append([]string{}, c.Command...), c.Args...) {
		if strings.Contains(arg, "-namespace=") {
			namespaces = append(namespaces, arg)
		}
	}
	if len(namespaces) != 1 || namespaces[0] != "--output-namespace=jb-output" {
		t.Fatalf("the store is handed namespaces %v, want only --output-namespace=jb-output", namespaces)
	}
}
