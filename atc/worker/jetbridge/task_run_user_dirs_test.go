package jetbridge

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// A task whose run.user is not root writes its own working directory, its
// declared outputs and its caches. The kubelet creates a hostPath directory
// as root, and fsGroup does not reach hostPath, so the pod hands those
// directories to the task's uid before the task starts: one init, after
// every other writer, mounting only the main container's own writable
// mounts. A root task, and a task with no run.user, get no such init.
func runUserDirSpec() runtime.ContainerSpec {
	return runtime.ContainerSpec{
		Dir:               "/tmp/build/task",
		Outputs:           map[string]string{"built": "/tmp/build/task/built"},
		Caches:            []string{"cache"},
		TaskCacheIdentity: &atc.TaskCacheIdentity{JobID: 7},
		ImageSpec:         runtime.ImageSpec{ImageURL: "docker:///busybox"},
	}
}

// runUserPath is one runtime's way of building a task's pod.
type runUserPath struct {
	name  string
	build func(*testing.T, runtime.ProcessSpec, bool) (*corev1.PodSpec, *fake.Clientset, error)
}

func runUserDirPaths() []runUserPath {
	hostPath := func(pause bool) func(*testing.T, runtime.ProcessSpec, bool) (*corev1.PodSpec, *fake.Clientset, error) {
		return func(t *testing.T, spec runtime.ProcessSpec, _ bool) (*corev1.PodSpec, *fake.Clientset, error) {
			cfg := Config{Namespace: "test-ns", ArtifactDaemonHostPath: "/var/concourse/artifacts"}
			container := makeContainer("user-dirs-handle", taskMetadata(), runUserDirSpec(), cfg, nil, false)
			build := func() (*corev1.Pod, error) {
				return container.buildPod(spec, []string{spec.Path}, spec.Args)
			}
			if pause {
				build = func() (*corev1.Pod, error) {
					return container.buildPod(spec, []string{"sh", "-c"}, []string{pauseCommand})
				}
			}
			pod, err := build()
			if pod == nil {
				return nil, nil, err
			}
			return &pod.Spec, nil, err
		}
	}
	return []runUserPath{
		{"exec direct, hostPath", hostPath(false)},
		{"exec pause, hostPath", hostPath(true)},
		{"job", func(t *testing.T, spec runtime.ProcessSpec, _ bool) (*corev1.PodSpec, *fake.Clientset, error) {
			clientset := fake.NewSimpleClientset()
			container := jobRuntimeContainer(t, clientset, db.ContainerTypeTask, runUserDirSpec())
			job, err := container.buildStepJob(spec, false)
			if job == nil {
				return nil, clientset, err
			}
			return &job.Spec.Template.Spec, clientset, err
		}},
	}
}

func findInit(pod *corev1.PodSpec, name string) (int, *corev1.Container) {
	for i := range pod.InitContainers {
		if pod.InitContainers[i].Name == name {
			return i, &pod.InitContainers[i]
		}
	}
	return -1, nil
}

func TestANonRootTaskRunUserOwnsItsOwnWritableDirectories(t *testing.T) {
	for _, path := range runUserDirPaths() {
		t.Run(path.name, func(t *testing.T) {
			pod, _, err := path.build(t, runtime.ProcessSpec{Path: "/bin/true", User: "1001"}, false)
			if err != nil {
				t.Fatal(err)
			}
			index, prepare := findInit(pod, runUserInitContainerName)
			if prepare == nil {
				t.Fatal("no init gives uid 1001 its directories; a hostPath output stays root-owned and the task cannot write it")
			}

			var want []corev1.VolumeMount
			var targets string
			for _, mount := range pod.Containers[0].VolumeMounts {
				if mount.ReadOnly {
					continue
				}
				want = append(want, corev1.VolumeMount{Name: mount.Name, MountPath: mount.MountPath})
				targets += " " + shellQuote(mount.MountPath)
			}
			if len(want) < 3 {
				t.Fatalf("the main container has %d writable mounts, want the dir, the output and the cache", len(want))
			}
			if fmt.Sprint(prepare.VolumeMounts) != fmt.Sprint(want) {
				t.Errorf("the init mounts %v, want exactly the main container's writable mounts %v", prepare.VolumeMounts, want)
			}
			if got, wantCommand := fmt.Sprint(prepare.Command), fmt.Sprint([]string{"sh", "-c", "chown -hR 1001" + targets}); got != wantCommand {
				t.Errorf("command = %s, want %s", got, wantCommand)
			}

			sc := prepare.SecurityContext
			if sc == nil || sc.RunAsUser == nil || *sc.RunAsUser != 0 {
				t.Fatal("the init does not run as root, so it cannot chown")
			}
			if sc.Privileged != nil && *sc.Privileged {
				t.Error("the init is privileged")
			}
			if sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
				t.Error("the init allows privilege escalation")
			}
			if sc.Capabilities != nil && len(sc.Capabilities.Add) > 0 {
				t.Errorf("the init adds capabilities %v; a step pod never adds one", sc.Capabilities.Add)
			}

			// Every other init that writes one of these volumes runs first,
			// so what it staged is the task's too.
			mounted := map[string]bool{}
			for _, mount := range want {
				mounted[mount.Name] = true
			}
			for i, other := range pod.InitContainers {
				for _, mount := range other.VolumeMounts {
					if i > index && mounted[mount.Name] {
						t.Errorf("init %q writes %s after the ownership is set", other.Name, mount.Name)
					}
				}
			}
		})
	}
}

func TestARootOrUnsetTaskRunUserGetsNoOwnershipInit(t *testing.T) {
	for _, path := range runUserDirPaths() {
		for _, user := range []string{"", "root", "0"} {
			t.Run(fmt.Sprintf("%s/%q", path.name, user), func(t *testing.T) {
				pod, _, err := path.build(t, runtime.ProcessSpec{Path: "/bin/true", User: user}, false)
				if err != nil {
					t.Fatal(err)
				}
				if _, prepare := findInit(pod, runUserInitContainerName); prepare != nil {
					t.Error("a root or unset run.user got an ownership init")
				}
			})
		}
	}
}

// The init's script is run here, as this user, against real directories:
// it must parse, recurse, and leave the directories writable by the uid it
// names, including one holding a symlink a previous run left behind.
func TestTheOwnershipInitScriptRuns(t *testing.T) {
	root := t.TempDir()
	output := filepath.Join(root, "built")
	cache := filepath.Join(root, "cache")
	for _, dir := range []string{output, filepath.Join(cache, "nested")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("/etc/hosts", filepath.Join(cache, "planted")); err != nil {
		t.Fatal(err)
	}
	uid := int64(os.Getuid())
	c := &Container{}
	prepare := c.buildRunUserInitContainer(&uid, []corev1.VolumeMount{
		{Name: "output-0", MountPath: output},
		{Name: "cache-1", MountPath: cache},
	})
	if prepare == nil {
		if uid == 0 {
			t.Skip("running as root: no ownership init is built")
		}
		t.Fatal("no ownership init for a non-root uid")
	}
	out, err := exec.CommandContext(context.Background(), prepare.Command[0], prepare.Command[1:]...).CombinedOutput()
	if err != nil {
		t.Fatalf("the init's script failed: %v: %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(output, "artifact"), []byte("built"), 0o644); err != nil {
		t.Fatalf("the output is not writable after the init: %v", err)
	}
	read, err := os.ReadFile(filepath.Join(output, "artifact"))
	if err != nil || string(read) != "built" {
		t.Fatalf("the next reader got %q, %v", read, err)
	}
}
