package tests

import (
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/yaml"
)

// Run result downloads spool the archive and its canonical copy (about twice
// its size, up to 256Mi each) until the response is written. The web Pod's
// /tmp is a 64Mi in-memory emptyDir charged to its memory limit, so results
// above ~32Mi failed and concurrent reads contended for it. With capture on,
// web gets a disk-backed scratch volume sized for its bounded concurrency.

const (
	runResultScratchPath   = "/var/concourse/run-result-scratch"
	runResultScratchVolume = "run-result-scratch"
)

func strictWebDeployment(t *testing.T, manifests string) appsv1.Deployment {
	t.Helper()
	for _, chunk := range splitDocuments(manifests) {
		var head renderedObject
		if yaml.Unmarshal([]byte(chunk), &head) != nil || head.Kind != "Deployment" || !strings.HasSuffix(head.Metadata.Name, "-web") {
			continue
		}
		var web appsv1.Deployment
		if err := yaml.UnmarshalStrict([]byte(chunk), &web); err != nil {
			t.Fatalf("web Deployment does not decode: %v", err)
		}
		if len(web.Spec.Template.Spec.Containers) == 0 {
			t.Fatal("web Deployment has no containers")
		}
		return web
	}
	t.Fatal("render has no -web Deployment")
	return appsv1.Deployment{}
}

func TestRunResultScratchIsOffWithoutCapture(t *testing.T) {
	for name, manifests := range map[string]string{
		"default":      render(t),
		"output facet": renderOutput(t),
	} {
		t.Run(name, func(t *testing.T) {
			web := strictWebDeployment(t, manifests)
			for _, arg := range web.Spec.Template.Spec.Containers[0].Args {
				if strings.HasPrefix(arg, "--run-result-") {
					t.Errorf("render without web capture carries %s", arg)
				}
			}
			for _, volume := range web.Spec.Template.Spec.Volumes {
				if volume.Name == runResultScratchVolume {
					t.Error("render without web capture mounts result scratch")
				}
			}
		})
	}
}

func TestRunResultScratchIsDiskBackedAndSizedForConcurrency(t *testing.T) {
	cases := []struct {
		name        string
		sets        []string
		sizeLimit   string
		concurrency string
	}{
		{"defaults", nil, "1Gi", "2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			web := strictWebDeployment(t, renderOutput(t, append([]string{"hangarOutput.webEnabled=true"}, tc.sets...)...))
			container := web.Spec.Template.Spec.Containers[0]

			for _, arg := range container.Args {
				if strings.HasPrefix(arg, "--run-result-") {
					t.Errorf("removed setting rendered: %s", arg)
				}
			}
			var mount *corev1.VolumeMount
			for i := range container.VolumeMounts {
				if container.VolumeMounts[i].MountPath == runResultScratchPath {
					mount = &container.VolumeMounts[i]
				}
			}
			if mount == nil || mount.Name != runResultScratchVolume || mount.ReadOnly {
				t.Fatalf("no writable mount at %s: %+v", runResultScratchPath, container.VolumeMounts)
			}
			var volume *corev1.Volume
			for i := range web.Spec.Template.Spec.Volumes {
				if web.Spec.Template.Spec.Volumes[i].Name == mount.Name {
					volume = &web.Spec.Template.Spec.Volumes[i]
				}
			}
			if volume == nil || volume.EmptyDir == nil {
				t.Fatalf("mount %s resolves to no emptyDir volume: %+v", mount.Name, volume)
			}
			if volume.EmptyDir.Medium != corev1.StorageMediumDefault {
				t.Errorf("result scratch medium = %q; it must be disk, not memory charged to web", volume.EmptyDir.Medium)
			}
			want := resource.MustParse(tc.sizeLimit)
			if volume.EmptyDir.SizeLimit == nil || volume.EmptyDir.SizeLimit.Cmp(want) != 0 {
				t.Errorf("result scratch sizeLimit = %v, want %s", volume.EmptyDir.SizeLimit, tc.sizeLimit)
			}
		})
	}
}

func webContainerArgs(t *testing.T, manifests string) []string {
	t.Helper()

	web := findDeployment(t, manifests, "-web")
	if len(web.Spec.Template.Spec.Containers) == 0 {
		t.Fatal("the -web Deployment has no containers; this test is watching nothing")
	}

	args := web.Spec.Template.Spec.Containers[0].Args
	if len(args) == 0 {
		t.Fatal("the web container renders no args; this test is watching nothing")
	}
	return args
}
