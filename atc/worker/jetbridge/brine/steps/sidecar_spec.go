package steps

import (
	"fmt"
	"slices"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	corev1 "k8s.io/api/core/v1"
)

func sidecarSpecDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		Transform[ContainerDraft, ContainerDraft]("the worker provides its exec transport to the task",
			func(in ContainerDraft, _ Args) (ContainerDraft, error) {
				if in.MountExecutor == nil {
					return ContainerDraft{}, fmt.Errorf("draft has no production execution transport")
				}
				in.Worker.SetExecutor(in.MountExecutor)
				return in, nil
			}),
		refineSidecar("the sidecar {string} declares environment {string} as {string}",
			func(sc *atc.SidecarConfig, a Args) {
				sc.Env = append(sc.Env, atc.SidecarEnvVar{Name: a.String(1), Value: a.String(2)})
			}),
		checkContainer("the container {string} has environment {string} set to {string}",
			func(sc corev1.Container, a Args) error {
				want := corev1.EnvVar{Name: a.String(1), Value: a.String(2)}
				if !slices.Contains(sc.Env, want) {
					return fmt.Errorf("expected container environment %+v, got %+v", want, sc.Env)
				}
				return nil
			}),
		refineSidecar("the sidecar {string} declares port {int}",
			func(sc *atc.SidecarConfig, a Args) {
				sc.Ports = append(sc.Ports, atc.SidecarPort{ContainerPort: a.Int(1)})
			}),
		checkContainer("the sidecar {string} exposes TCP port {int}",
			func(sc corev1.Container, a Args) error {
				want := corev1.ContainerPort{ContainerPort: int32(a.Int(1)), Protocol: corev1.ProtocolTCP}
				if !slices.Contains(sc.Ports, want) {
					return fmt.Errorf("expected sidecar port %+v, got %+v", want, sc.Ports)
				}
				return nil
			}),
		refineSidecar("the sidecar {string} declares command {string} and arguments {string}",
			func(sc *atc.SidecarConfig, a Args) {
				sc.Command, sc.Args = splitList(a.String(1)), splitList(a.String(2))
			}),
		checkContainer("the container {string} runs command {string} with arguments {string}",
			func(sc corev1.Container, a Args) error {
				command, args := splitList(a.String(1)), splitList(a.String(2))
				if !slices.Equal(sc.Command, command) || !slices.Equal(sc.Args, args) {
					return fmt.Errorf("expected container command %v and arguments %v, got %v and %v", command, args, sc.Command, sc.Args)
				}
				return nil
			}),
		refineSidecar("the sidecar {string} requests {string} CPU and {string} memory with limits {string} CPU and {string} memory",
			func(sc *atc.SidecarConfig, a Args) {
				sc.Resources = &atc.SidecarResources{
					Requests: atc.SidecarResourceList{CPU: a.String(1), Memory: a.String(2)},
					Limits:   atc.SidecarResourceList{CPU: a.String(3), Memory: a.String(4)},
				}
			}),
		checkContainer("the sidecar {string} has requests {string} CPU and {string} memory and limits {string} CPU and {string} memory",
			func(sc corev1.Container, a Args) error {
				values := []string{sc.Resources.Requests.Cpu().String(), sc.Resources.Requests.Memory().String(), sc.Resources.Limits.Cpu().String(), sc.Resources.Limits.Memory().String()}
				labels := []string{"CPU request", "memory request", "CPU limit", "memory limit"}
				for i, got := range values {
					if want := a.String(i + 1); got != want {
						return fmt.Errorf("expected sidecar %s %s, got %s", labels[i], want, got)
					}
				}
				return nil
			}),
		checkContainer("the sidecar {string} is pulled only when absent",
			func(sc corev1.Container, _ Args) error {
				if sc.ImagePullPolicy != corev1.PullIfNotPresent {
					return fmt.Errorf("expected sidecar pull policy IfNotPresent, got %s", sc.ImagePullPolicy)
				}
				return nil
			}),
		CheckThat[PodCreated]("the main container waits for exec",
			func(in PodCreated) error {
				main, err := mainContainer(in.Pod)
				if err != nil {
					return err
				}
				want := []string{"sh", "-c", "trap 'exit 0' TERM; sleep 86400 & wait"}
				if !slices.Equal(main.Command, want) {
					return fmt.Errorf("expected pause command %q, got %q", want, main.Command)
				}
				return nil
			}),
	}
}

// The shared lookup fails when a scenario names a sidecar it never declared.
func refineSidecar(pattern string, refine func(*atc.SidecarConfig, Args)) brine.StepDefinition {
	return Transform[ContainerDraft, ContainerDraft](pattern, func(in ContainerDraft, a Args) (ContainerDraft, error) {
		name := a.String(0)
		for i := range in.Sidecars {
			if in.Sidecars[i].Name == name {
				refine(&in.Sidecars[i], a)
				return in, nil
			}
		}
		return ContainerDraft{}, fmt.Errorf("no sidecar named %q", name)
	})
}

func checkContainer(pattern string, check func(corev1.Container, Args) error) brine.StepDefinition {
	return Assert[PodCreated](pattern, func(in PodCreated, a Args) error {
		sc, err := containerNamed(in.Pod, a.String(0))
		if err != nil {
			return err
		}
		return check(sc, a)
	})
}

func containerRosterDefinition() brine.StepDefinition {
	return brine.DefineCheck[PodCreated]("the pod runs these containers in order", func(in PodCreated, p brine.Params, _ *brine.Recorder) error {
		rows := p.RequireDataTable()
		if len(rows) < 2 || !slices.Equal(rows[0], []string{"name", "image"}) {
			return fmt.Errorf("expected a nonempty table headed name and image")
		}
		if len(in.Pod.Spec.Containers) != len(rows)-1 {
			return fmt.Errorf("expected %d containers, got %d", len(rows)-1, len(in.Pod.Spec.Containers))
		}
		seen := map[string]bool{}
		for i, row := range rows[1:] {
			if len(row) != 2 || row[0] == "" || row[1] == "" || seen[row[0]] {
				return fmt.Errorf("expected distinct named containers with images, got %v", row)
			}
			seen[row[0]] = true
			got := in.Pod.Spec.Containers[i]
			if got.Name != row[0] || got.Image != row[1] {
				return fmt.Errorf("expected container %d to be %s using %s, got %s using %s", i, row[0], row[1], got.Name, got.Image)
			}
		}
		return nil
	})
}
