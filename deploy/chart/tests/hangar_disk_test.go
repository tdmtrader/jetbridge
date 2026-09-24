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
	"artifactDaemon.hangar.enabled=true",
	"artifactDaemon.hangar.store=disk",
	"artifactDaemon.hangar.bucket=inputs",
	`hangarOutput.daemon.serviceAccount.annotations.iam\.gke\.io/gcp-service-account=`,
	`hangarOutput.inventory.serviceAccount.annotations.iam\.gke\.io/gcp-service-account=`,
	`hangarOutput.reclaimer.serviceAccount.annotations.iam\.gke\.io/gcp-service-account=`,
}

func TestDiskStorageRendersWithoutGCSAndProjectsOnlyEachRolesCredential(t *testing.T) {
	out := renderOutput(t, diskSets...)
	if strings.Contains(out, "policy-attestor") || strings.Contains(out, "--output-store=gcs") || strings.Contains(out, "--durable-store=gcs") {
		t.Fatal("disk render requires GCS or retired attestation")
	}
	roles := map[string]string{"artifact-daemon": "input", outputDaemonComponent: "publisher", outputInventoryComponent: "inventory", outputReclaimerComponent: "reclaimer"}
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
		role := ""
		for component, r := range roles {
			if strings.HasSuffix(doc.name, "-"+component) {
				role = r
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
		if role == "" {
			continue
		}
		found++
		hasCredential := false
		for _, v := range pod.Volumes {
			if v.Name != "hangar-disk-client" {
				continue
			}
			if v.Projected == nil {
				t.Fatalf("%s credential volume is not projected", doc.name)
			}
			for _, source := range v.Projected.Sources {
				if source.Secret == nil || source.Secret.Name != "storage-credentials" {
					continue
				}
				hasCredential = true
				if len(source.Secret.Items) != 1 || source.Secret.Items[0].Key != role {
					t.Errorf("%s can access other roles: %+v", doc.name, source.Secret.Items)
				}
			}
		}
		if !hasCredential {
			t.Errorf("%s missing %s credential", doc.name, role)
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

func TestExplicitStrictInputStorageCannotSilentlyInheritTheCacheBucket(t *testing.T) {
	message := renderHangarError(t,
		"artifactDaemon.hangar.enabled=true",
		"artifactDaemon.hangar.store=gcs",
		"artifactDaemon.durable.store=gcs",
		"artifactDaemon.durable.bucket=resource-cache")
	if !strings.Contains(message, "hangar.bucket") {
		t.Fatal(message)
	}
}

func TestDiskRefusesSharedInputAndOutputNamespace(t *testing.T) {
	sets := append(append([]string{}, diskSets...), "hangarOutput.bucket=inputs")
	message := renderOutputError(t, sets...)
	if !strings.Contains(message, "namespaces must be distinct") {
		t.Fatal(message)
	}
}

func TestStrictInputPrefixIsIndependentOfResourceCacheStorage(t *testing.T) {
	for _, profile := range []string{"disk", "gcs"} {
		for _, prefix := range []string{"", "strict-trees"} {
			for _, cachePrefix := range []string{"cache-v1", "cache-v2"} {
				sets := append(append([]string{}, diskSets...),
					"artifactDaemon.hangar.store="+profile,
					"artifactDaemon.hangar.prefix="+prefix,
					"artifactDaemon.durable.store=gcs",
					"artifactDaemon.durable.bucket=resource-cache",
					"artifactDaemon.durable.prefix="+cachePrefix)
				out := renderOutput(t, sets...)
				doc := objectNamed(t, out, "DaemonSet", "-artifact-daemon")
				var daemon appsv1.DaemonSet
				if err := yaml.UnmarshalStrict([]byte(doc.body), &daemon); err != nil {
					t.Fatal(err)
				}
				found := false
				for _, container := range daemon.Spec.Template.Spec.Containers {
					for _, arg := range container.Command {
						if strings.HasPrefix(arg, "--hangar-prefix=") {
							found = true
							if arg != "--hangar-prefix="+prefix {
								t.Fatalf("%s storage follows cache prefix: %s", profile, arg)
							}
						}
					}
				}
				if !found {
					t.Fatal("explicit strict-input storage has no prefix flag")
				}
			}
		}
	}
	out := render(t, "artifactDaemon.hangar.enabled=true", "artifactDaemon.durable.store=gcs", "artifactDaemon.durable.bucket=legacy", "artifactDaemon.durable.prefix=legacy-prefix")
	if strings.Contains(out, "--hangar-prefix=") || !strings.Contains(out, "--durable-prefix=legacy-prefix") {
		t.Fatal("legacy prefix inheritance changed")
	}
}
