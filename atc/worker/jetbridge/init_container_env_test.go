package jetbridge

// Every init container's script expands only what its container defines.
//
// Init scripts run under `set -u`, so a variable the script expands and the
// container does not define is not an empty string: it is the shell exiting
// before the script's first command, and a pod that ends Failed before exec
// can run. The shell tests elsewhere in this package run each script with the
// variables they expect already exported, so a container that forgot one in
// its Env passed them; this one reads the variables off the composed Pod, the
// way the kubelet hands them over.

import (
	"regexp"
	"sort"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/hangar"
)

// shellProvided is what the shell itself defines, so a script may expand it
// without the container's Env.
var shellProvided = map[string]bool{
	"HOME": true, "IFS": true, "PATH": true, "PPID": true, "PWD": true, "RANDOM": true,
}

var (
	// $NAME and ${NAME...}: what the script expands.
	shellExpansion = regexp.MustCompile(`\$\{?([A-Za-z_][A-Za-z0-9_]*)(:?[-=+?])?`)
	// NAME=value at the start of a word: what the script assigns itself.
	shellAssignment = regexp.MustCompile(`(?m)(?:^|[\s;&|(])([A-Za-z_][A-Za-z0-9_]*)=`)
	// for NAME in, read [-r] NAME: the other ways a script binds a name.
	shellLoopOrRead = regexp.MustCompile(`(?:\bfor\s+|\bread\s+(?:-r\s+)?)([A-Za-z_][A-Za-z0-9_]*)`)
)

// undefinedExpansions is the names a script expands with no default that
// neither it, the shell nor its container's Env defines.
func undefinedExpansions(script string, env []corev1.EnvVar) []string {
	defined := map[string]bool{}
	for name := range shellProvided {
		defined[name] = true
	}
	for _, variable := range env {
		defined[variable.Name] = true
	}
	for _, match := range shellAssignment.FindAllStringSubmatch(script, -1) {
		defined[match[1]] = true
	}
	for _, match := range shellLoopOrRead.FindAllStringSubmatch(script, -1) {
		defined[match[1]] = true
	}

	missing := map[string]bool{}
	for _, match := range shellExpansion.FindAllStringSubmatch(script, -1) {
		// ${NAME:-default} and its kin say what to do when NAME is unset.
		if match[2] != "" {
			continue
		}
		if !defined[match[1]] {
			missing[match[1]] = true
		}
	}
	names := make([]string, 0, len(missing))
	for name := range missing {
		names = append(names, name)
	}
	sort.Strings(names)

	return names
}

func TestTheUndefinedExpansionCheckSeesWhatSetUWouldStopOn(t *testing.T) {
	hostIP := []corev1.EnvVar{{Name: "HOST_IP"}}
	for script, want := range map[string]string{
		`set -u` + "\n" + `DAEMON="https://${HOST_IP}:7780"`: "HOST_IP",
		`echo "$TARGET"`: "TARGET",
		`TARGET=/x` + "\n" + `rm -rf "${TARGET}"`: "",
		`TMP=$(mktemp -d "${TMPDIR:-/tmp}/x")`:    "",
		`for item in a b; do echo "$item"; done`:  "",
		`P=$((D + ${RANDOM:-$$} % 2))`:            "",
	} {
		got := strings.Join(undefinedExpansions(script, nil), ",")
		if got != want {
			t.Errorf("%q with no env: got %q, want %q", script, got, want)
		}
	}
	if got := undefinedExpansions(`DAEMON="https://${HOST_IP}:7780"`, hostIP); len(got) != 0 {
		t.Errorf("HOST_IP is in the container's env, yet reported undefined: %v", got)
	}
}

// initEnvContainer is one step's container in every shape that changes its
// init containers: the output plane, a reused handle (a re-driven build), a
// capture-selected output, an ordinary input and a managed Run input.
func initEnvContainer(t *testing.T, plane, reused, capture bool, containerType db.ContainerType) *Container {
	t.Helper()

	cfg := capturePodConfig(plane)
	cfg.LauncherImage = "registry.example/jetbridge:1"
	cfg.StepEnvelopeURL = "http://web.test-ns.svc:8080/runtime/v1/step-envelope"

	var control *runtime.ExecutionControl
	if capture && plane {
		control = admittedCapture()
	}
	container := capturingContainer(t, cfg, reused, control)
	container.metadata.Type = containerType
	container.containerSpec.Type = containerType

	inputs := []runtime.Input{
		{Artifact: constructionArtifact("ordinary", "worker-1"), DestinationPath: "/tmp/build/ordinary"},
	}
	ref := hangar.TreeRef{
		Scope:      "builds",
		Digest:     "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Generation: 7,
	}
	if plane {
		inputs = append(inputs, runtime.Input{HangarTree: &ref, DestinationPath: "/tmp/build/repo"})
	}
	container.containerSpec.Inputs = inputs
	if plane {
		_, mounts := container.buildVolumeMounts()
		volume := volumeNameForMountPath(mounts, "/tmp/build/repo")
		if volume == "" {
			t.Fatal("the managed Run input has no volume")
		}
		container.containerSpec.Inputs[1].HangarRead = runInputRead(ref, container.handle, volume)
	}

	return container
}

func TestEveryInitContainerDefinesEveryVariableItsScriptExpands(t *testing.T) {
	seen := map[string]bool{}
	for _, plane := range []bool{false, true} {
		for _, reused := range []bool{false, true} {
			for _, capture := range []bool{false, true} {
				for _, containerType := range []db.ContainerType{db.ContainerTypeTask, db.ContainerTypeGet} {
					shape := strings.Join([]string{
						map[bool]string{false: "no plane", true: "plane"}[plane],
						map[bool]string{false: "fresh", true: "reused"}[reused],
						map[bool]string{false: "no capture", true: "capture"}[capture],
						string(containerType),
					}, ", ")

					var pods []*corev1.Pod
					execPod, err := initEnvContainer(t, plane, reused, capture, containerType).
						buildPod(runtime.ProcessSpec{Path: "/bin/sh"}, []string{"sh"}, nil)
					if err != nil {
						t.Fatalf("%s: composing the exec runtime's pod: %v", shape, err)
					}
					pods = append(pods, execPod)
					job, _, err := initEnvContainer(t, plane, reused, capture, containerType).
						buildStepJob(runtime.ProcessSpec{Path: "/bin/sh"}, true)
					if err != nil {
						t.Fatalf("%s: composing the job runtime's pod: %v", shape, err)
					}
					pods = append(pods, &corev1.Pod{Spec: job.Spec.Template.Spec})

					for _, pod := range pods {
						for _, init := range pod.Spec.InitContainers {
							seen[init.Name] = true
							if len(init.Command) < 3 || init.Command[0] != "sh" {
								continue
							}
							script := init.Command[len(init.Command)-1]
							if missing := undefinedExpansions(script, init.Env); len(missing) > 0 {
								t.Errorf("%s: init container %q expands %v, which its env does not "+
									"define; under `set -u` the pod fails before its first command:\n%s",
									shape, init.Name, missing, script)
							}
						}
					}
				}
			}
		}
	}

	// The walk reached every init container that runs a script; a shape that
	// stopped emitting one would otherwise pass by checking nothing.
	for _, name := range []string{controlInitName, "cleanup-stale", "fetch-inputs", "materialize-run-input-0", launcherInitName} {
		if !seen[name] {
			t.Errorf("no composed pod had the init container %q; the walk does not cover it", name)
		}
	}
}
