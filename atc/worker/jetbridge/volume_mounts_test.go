package jetbridge

import (
	"testing"

	"github.com/concourse/concourse/atc/runtime"
)

func TestVolumeMountLayout(t *testing.T) {
	type expectedMount struct{ handle, path string }
	cases := []struct {
		name, handle string
		spec         runtime.ContainerSpec
		executor     bool
		want         []expectedMount
	}{
		{name: "directory", handle: "h", spec: runtime.ContainerSpec{Dir: "/workdir"}, executor: false, want: []expectedMount{{"h-dir", "/workdir"}}},
		{name: "inputs", handle: "h", spec: runtime.ContainerSpec{Dir: "/workdir", Inputs: []runtime.Input{{DestinationPath: "/workdir/input-a"}, {DestinationPath: "/workdir/input-b"}}}, executor: false, want: []expectedMount{{"h-dir", "/workdir"}, {"h-input-0", "/workdir/input-a"}, {"h-input-1", "/workdir/input-b"}}},
		{name: "output", handle: "h", spec: runtime.ContainerSpec{Dir: "/workdir", Outputs: runtime.OutputPaths{"result": "/workdir/result"}}, executor: false, want: []expectedMount{{"h-dir", "/workdir"}, {"h-output-result", "/workdir/result"}}},
		{name: "overlap", handle: "h", spec: runtime.ContainerSpec{Dir: "/workdir", Inputs: []runtime.Input{{DestinationPath: "/workdir/shared"}}, Outputs: runtime.OutputPaths{"shared": "/workdir/shared"}}, executor: false, want: []expectedMount{{"h-dir", "/workdir"}, {"h-input-0", "/workdir/shared"}}},
		{name: "separate-input-output", handle: "h", spec: runtime.ContainerSpec{Dir: "/workdir", Inputs: []runtime.Input{{DestinationPath: "/workdir/input"}}, Outputs: runtime.OutputPaths{"output": "/workdir/output"}}, executor: false, want: []expectedMount{{"h-dir", "/workdir"}, {"h-input-0", "/workdir/input"}, {"h-output-output", "/workdir/output"}}},
		{name: "relative-cache", handle: "h", spec: runtime.ContainerSpec{Dir: "/workdir", Caches: []string{"cache-a"}}, executor: false, want: []expectedMount{{"h-dir", "/workdir"}, {"h-cache-0", "/workdir/cache-a"}}},
		{name: "volume-names", handle: "abc", spec: runtime.ContainerSpec{Dir: "/workdir", Inputs: []runtime.Input{{DestinationPath: "/workdir/my-input"}}, Outputs: runtime.OutputPaths{"my-output": "/workdir/my-output"}, Caches: []string{"my-cache"}}, executor: false, want: []expectedMount{{"abc-dir", "/workdir"}, {"abc-input-0", "/workdir/my-input"}, {"abc-output-my-output", "/workdir/my-output"}, {"abc-cache-0", "/workdir/my-cache"}}},
		{name: "relative-cache-name", handle: "h", spec: runtime.ContainerSpec{Dir: "/workdir", Caches: []string{"my-cache"}}, executor: false, want: []expectedMount{{"h-dir", "/workdir"}, {"h-cache-0", "/workdir/my-cache"}}},
		{name: "absolute-cache", handle: "h", spec: runtime.ContainerSpec{Dir: "/workdir", Caches: []string{"/absolute/cache"}}, executor: false, want: []expectedMount{{"h-dir", "/workdir"}, {"h-cache-0", "/absolute/cache"}}},
		{name: "executor", handle: "h", spec: runtime.ContainerSpec{Dir: "/workdir"}, executor: true, want: []expectedMount{{"h-dir", "/workdir"}}},
		{name: "overlap-trailing-slash", handle: "h", spec: runtime.ContainerSpec{Dir: "/workdir", Inputs: []runtime.Input{{DestinationPath: "/workdir/shared"}}, Outputs: runtime.OutputPaths{"shared": "/workdir/shared/"}}, executor: false, want: []expectedMount{{"h-dir", "/workdir"}, {"h-input-0", "/workdir/shared"}}},
		{name: "output-without-dir", handle: "h", spec: runtime.ContainerSpec{Outputs: runtime.OutputPaths{"out": "/workdir/out"}}, executor: false, want: []expectedMount{{"h-output-out", "/workdir/out"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var executor PodExecutor
			if tc.executor {
				executor = volumeConstructionExecutor()
			}
			mounts, volumes := buildVolumeMounts("test-worker", "test-ns", executor, tc.handle, tc.spec)
			if len(mounts) != len(tc.want) || len(volumes) != len(tc.want) {
				t.Fatalf("mount/volume count: got %d/%d, want %d", len(mounts), len(volumes), len(tc.want))
			}
			for i, want := range tc.want {
				volume := volumes[i]
				if mounts[i].Volume != volume || mounts[i].MountPath != want.path || volume.MountPath() != want.path || volume.Handle() != want.handle {
					t.Errorf("mount %d: mount=%+v volume=%+v; want handle=%q path=%q", i, mounts[i], volume, want.handle, want.path)
				}
				if volume.Source() != "test-worker" || volume.HasExecutor() != tc.executor || volume.executor != executor {
					t.Errorf("mount %d lost worker or executor identity: %+v", i, volume)
				}
				if tc.executor && (volume.namespace != "test-ns" || volume.containerName != mainContainerName) {
					t.Errorf("mount %d lost exec destination: %+v", i, volume)
				}
			}
		})
	}
}
