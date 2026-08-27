package steps

import (
	"fmt"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// ContainerPodDefinitions extends the container-spec vocabulary to cover what
// container_test.go asserts: the volumes a step gets, where they are mounted,
// which storage backs them, the resource envelope the pod is scheduled under,
// its security posture, and the sidecars alongside it.
//
// All of these read `pod.Spec` from the fake clientset. That is NOT a spy
// assertion: the PodSpec is a real artifact submitted through a real client
// interface, and it is exactly what a consumer — the Kubernetes scheduler —
// receives. The double is not the subject of the assertion.

func ContainerPodDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		// --- Draft refinements. In and Out are the same type, so these
		// compose freely and in any order before the container runs. ---

		brine.DefineMap[ContainerDraft, ContainerDraft](
			"it takes an input at {string}",
			func(in ContainerDraft, p brine.Params, _ *brine.Recorder) (ContainerDraft, error) {
				path, ok := p.GetString(0)
				if !ok {
					return ContainerDraft{}, fmt.Errorf("expected a path parameter")
				}
				in.Inputs = append(in.Inputs, path)
				return in, nil
			},
		),

		brine.DefineMap[ContainerDraft, ContainerDraft](
			"it produces an output at {string}",
			func(in ContainerDraft, p brine.Params, _ *brine.Recorder) (ContainerDraft, error) {
				path, ok := p.GetString(0)
				if !ok {
					return ContainerDraft{}, fmt.Errorf("expected a path parameter")
				}
				in.Outputs = append(in.Outputs, path)
				return in, nil
			},
		),

		brine.DefineMap[ContainerDraft, ContainerDraft](
			"it caches {string}",
			func(in ContainerDraft, p brine.Params, _ *brine.Recorder) (ContainerDraft, error) {
				path, ok := p.GetString(0)
				if !ok {
					return ContainerDraft{}, fmt.Errorf("expected a path parameter")
				}
				in.Caches = append(in.Caches, path)
				return in, nil
			},
		),

		brine.DefineMap[ContainerDraft, ContainerDraft](
			"it uses scratch space at {string}",
			func(in ContainerDraft, p brine.Params, _ *brine.Recorder) (ContainerDraft, error) {
				path, ok := p.GetString(0)
				if !ok {
					return ContainerDraft{}, fmt.Errorf("expected a path parameter")
				}
				in.Scratch = append(in.Scratch, path)
				return in, nil
			},
		),

		brine.DefineMap[ContainerDraft, ContainerDraft](
			"it works in {string}",
			func(in ContainerDraft, p brine.Params, _ *brine.Recorder) (ContainerDraft, error) {
				dir, ok := p.GetString(0)
				if !ok {
					return ContainerDraft{}, fmt.Errorf("expected a directory parameter")
				}
				in.Dir = dir
				return in, nil
			},
		),

		// PE-07: the resource envelope decides the pod's QoS class, which
		// decides which pods the kubelet evicts first under pressure.
		// CPU is CPU shares, memory is bytes — the units ContainerLimits
		// actually uses. The runtime maps shares to millicores.
		brine.DefineMap[ContainerDraft, ContainerDraft](
			"it is limited to {int} CPU shares and {int} bytes of memory",
			func(in ContainerDraft, p brine.Params, _ *brine.Recorder) (ContainerDraft, error) {
				cpu, _ := p.GetInt(0)
				mem, ok := p.GetInt(1)
				if !ok {
					return ContainerDraft{}, fmt.Errorf("expected a cpu and a memory parameter")
				}
				c, m := uint64(cpu), uint64(mem)
				in.LimitCPU, in.LimitMemory = &c, &m
				return in, nil
			},
		),

		brine.DefineMap[ContainerDraft, ContainerDraft](
			"it requests {int} CPU shares and {int} bytes of memory",
			func(in ContainerDraft, p brine.Params, _ *brine.Recorder) (ContainerDraft, error) {
				cpu, _ := p.GetInt(0)
				mem, ok := p.GetInt(1)
				if !ok {
					return ContainerDraft{}, fmt.Errorf("expected a cpu and a memory parameter")
				}
				c, m := uint64(cpu), uint64(mem)
				in.RequestCPU, in.RequestMemory = &c, &m
				return in, nil
			},
		),

		// PE-07's ephemeral-storage clause: a step that writes a large
		// artifact to local disk is evicted without this, and the eviction
		// looks like an unexplained failure.
		brine.DefineMap[ContainerDraft, ContainerDraft](
			"it is limited to {int} bytes of local disk, requesting {int}",
			func(in ContainerDraft, p brine.Params, _ *brine.Recorder) (ContainerDraft, error) {
				lim, _ := p.GetInt(0)
				req, ok := p.GetInt(1)
				if !ok {
					return ContainerDraft{}, fmt.Errorf("expected a limit and a request")
				}
				l, r := uint64(lim), uint64(req)
				in.LimitEphemeral, in.RequestEphemeral = &l, &r
				return in, nil
			},
		),

		brine.DefineCheck[PodCreated](
			"the step may use at most {string} of local disk, reserving {string}",
			func(in PodCreated, p brine.Params, _ *brine.Recorder) error {
				lim, _ := p.GetString(0)
				req, ok := p.GetString(1)
				if !ok {
					return fmt.Errorf("expected a limit and a request")
				}
				main, err := mainContainer(in.Pod)
				if err != nil {
					return err
				}
				want, err := resource.ParseQuantity(lim)
				if err != nil {
					return fmt.Errorf("bad limit %q: %w", lim, err)
				}
				got, ok2 := main.Resources.Limits[corev1.ResourceEphemeralStorage]
				if !ok2 {
					return fmt.Errorf("expected an ephemeral-storage limit of %s, none is set", lim)
				}
				if got.Cmp(want) != 0 {
					return fmt.Errorf("expected an ephemeral-storage limit of %s, got %s", lim, got.String())
				}
				wantReq, err := resource.ParseQuantity(req)
				if err != nil {
					return fmt.Errorf("bad request %q: %w", req, err)
				}
				gotReq, ok3 := main.Resources.Requests[corev1.ResourceEphemeralStorage]
				if !ok3 {
					return fmt.Errorf("expected an ephemeral-storage request of %s, none is set", req)
				}
				if gotReq.Cmp(wantReq) != 0 {
					return fmt.Errorf("expected an ephemeral-storage request of %s, got %s", req, gotReq.String())
				}
				return nil
			},
		),

		// PE-04
		brine.DefineMap[ContainerDraft, ContainerDraft](
			"it runs privileged",
			func(in ContainerDraft, _ brine.Params, _ *brine.Recorder) (ContainerDraft, error) {
				in.Privileged = true
				return in, nil
			},
		),

		// SC-01 to SC-06
		brine.DefineMap[ContainerDraft, ContainerDraft](
			"a sidecar {string} runs {string} alongside it",
			func(in ContainerDraft, p brine.Params, _ *brine.Recorder) (ContainerDraft, error) {
				name, _ := p.GetString(0)
				image, ok := p.GetString(1)
				if !ok {
					return ContainerDraft{}, fmt.Errorf("expected a name and an image")
				}
				in.Sidecars = append(in.Sidecars, atc.SidecarConfig{Name: name, Image: image})
				return in, nil
			},
		),

		brine.DefineMap[ContainerDraft, ContainerDraft](
			"the sidecar {string} declares its working directory as {string}",
			func(in ContainerDraft, p brine.Params, _ *brine.Recorder) (ContainerDraft, error) {
				name, _ := p.GetString(0)
				dir, ok := p.GetString(1)
				if !ok {
					return ContainerDraft{}, fmt.Errorf("expected a name and a directory")
				}
				for i := range in.Sidecars {
					if in.Sidecars[i].Name == name {
						in.Sidecars[i].WorkingDir = dir
						return in, nil
					}
				}
				return ContainerDraft{}, fmt.Errorf("no sidecar named %q", name)
			},
		),

		// --- Checks over the resulting pod ---

		brine.DefineCheck[PodCreated](
			"the pod has {int} volumes",
			func(in PodCreated, p brine.Params, _ *brine.Recorder) error {
				want, ok := p.GetInt(0)
				if !ok {
					return fmt.Errorf("expected a count parameter")
				}
				if got := len(in.Pod.Spec.Volumes); got != want {
					names := make([]string, 0, got)
					for _, v := range in.Pod.Spec.Volumes {
						names = append(names, v.Name)
					}
					return fmt.Errorf("expected %d volumes, got %d (%s)", want, got, strings.Join(names, ", "))
				}
				return nil
			},
		),

		brine.DefineCheck[PodCreated](
			"the step sees a volume mounted at {string}",
			func(in PodCreated, p brine.Params, _ *brine.Recorder) error {
				path, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a path parameter")
				}
				main, err := mainContainer(in.Pod)
				if err != nil {
					return err
				}
				var paths []string
				for _, vm := range main.VolumeMounts {
					if vm.MountPath == path {
						return nil
					}
					paths = append(paths, vm.MountPath)
				}
				return fmt.Errorf("expected a volume mounted at %q, the step sees [%s]",
					path, strings.Join(paths, ", "))
			},
		),

		brine.DefineCheck[PodCreated](
			"the step sees nothing mounted at {string}",
			func(in PodCreated, p brine.Params, _ *brine.Recorder) error {
				path, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a path parameter")
				}
				main, err := mainContainer(in.Pod)
				if err != nil {
					return err
				}
				for _, vm := range main.VolumeMounts {
					if vm.MountPath == path {
						return fmt.Errorf("expected nothing mounted at %q, but %q is",
							path, vm.Name)
					}
				}
				return nil
			},
		),

		// CO-06/CO-07/CF-04: which storage backs a volume decides whether its
		// contents survive the pod.
		brine.DefineCheck[PodCreated](
			"every volume is ephemeral",
			func(in PodCreated, _ brine.Params, _ *brine.Recorder) error {
				for _, v := range in.Pod.Spec.Volumes {
					if v.EmptyDir == nil {
						return fmt.Errorf("expected volume %q to be ephemeral, it is not (hostPath=%v)",
							v.Name, v.HostPath != nil)
					}
				}
				return nil
			},
		),

		brine.DefineCheck[PodCreated](
			"the volume mounted at {string} survives the pod",
			func(in PodCreated, p brine.Params, _ *brine.Recorder) error {
				path, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a path parameter")
				}
				v, err := volumeAt(in.Pod, path)
				if err != nil {
					return err
				}
				if v.HostPath == nil {
					return fmt.Errorf("expected the volume at %q to be node-local storage, it is ephemeral", path)
				}
				return nil
			},
		),

		brine.DefineCheck[PodCreated](
			"the volume mounted at {string} is lost with the pod",
			func(in PodCreated, p brine.Params, _ *brine.Recorder) error {
				path, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a path parameter")
				}
				v, err := volumeAt(in.Pod, path)
				if err != nil {
					return err
				}
				if v.EmptyDir == nil {
					return fmt.Errorf("expected the volume at %q to be ephemeral, it is node-local storage", path)
				}
				return nil
			},
		),

		// PE-07: the QoS class is the observable consequence of the envelope.
		brine.DefineCheck[PodCreated](
			"the pod is scheduled as {string}",
			func(in PodCreated, p brine.Params, _ *brine.Recorder) error {
				want, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a QoS class parameter")
				}
				got := qosClassOf(in.Pod)
				if got != want {
					main, _ := mainContainer(in.Pod)
					return fmt.Errorf("expected QoS %q, got %q (limits=%v requests=%v)",
						want, got, main.Resources.Limits, main.Resources.Requests)
				}
				return nil
			},
		),

		brine.DefineCheck[PodCreated](
			"the step may use at most {string} CPU and {string} memory",
			func(in PodCreated, p brine.Params, _ *brine.Recorder) error {
				cpu, _ := p.GetString(0)
				mem, ok := p.GetString(1)
				if !ok {
					return fmt.Errorf("expected a cpu and a memory parameter")
				}
				main, err := mainContainer(in.Pod)
				if err != nil {
					return err
				}
				return matchQuantities(main.Resources.Limits, cpu, mem, "limit")
			},
		),

		brine.DefineCheck[PodCreated](
			"the step is reserved {string} CPU and {string} memory",
			func(in PodCreated, p brine.Params, _ *brine.Recorder) error {
				cpu, _ := p.GetString(0)
				mem, ok := p.GetString(1)
				if !ok {
					return fmt.Errorf("expected a cpu and a memory parameter")
				}
				main, err := mainContainer(in.Pod)
				if err != nil {
					return err
				}
				return matchQuantities(main.Resources.Requests, cpu, mem, "request")
			},
		),

		// PE-04
		brine.DefineCheck[PodCreated](
			"the step can escalate its privileges",
			func(in PodCreated, _ brine.Params, _ *brine.Recorder) error {
				main, err := mainContainer(in.Pod)
				if err != nil {
					return err
				}
				sc := main.SecurityContext
				if sc == nil || sc.Privileged == nil || !*sc.Privileged {
					return fmt.Errorf("expected a privileged container, got %+v", sc)
				}
				return nil
			},
		),

		brine.DefineCheck[PodCreated](
			"the step cannot escalate its privileges",
			func(in PodCreated, _ brine.Params, _ *brine.Recorder) error {
				main, err := mainContainer(in.Pod)
				if err != nil {
					return err
				}
				sc := main.SecurityContext
				if sc == nil || sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
					return fmt.Errorf("expected privilege escalation to be denied, got %+v", sc)
				}
				return nil
			},
		),

		// --- Sidecars ---

		brine.DefineCheck[PodCreated](
			"the pod runs {int} containers",
			func(in PodCreated, p brine.Params, _ *brine.Recorder) error {
				want, ok := p.GetInt(0)
				if !ok {
					return fmt.Errorf("expected a count parameter")
				}
				if got := len(in.Pod.Spec.Containers); got != want {
					var names []string
					for _, c := range in.Pod.Spec.Containers {
						names = append(names, c.Name)
					}
					return fmt.Errorf("expected %d containers, got %d (%s)", want, got, strings.Join(names, ", "))
				}
				return nil
			},
		),

		brine.DefineCheck[PodCreated](
			"the sidecar {string} runs image {string}",
			func(in PodCreated, p brine.Params, _ *brine.Recorder) error {
				name, _ := p.GetString(0)
				image, ok := p.GetString(1)
				if !ok {
					return fmt.Errorf("expected a name and an image")
				}
				c, err := containerNamed(in.Pod, name)
				if err != nil {
					return err
				}
				if c.Image != image {
					return fmt.Errorf("expected sidecar %q to run %q, got %q", name, image, c.Image)
				}
				return nil
			},
		),

		brine.DefineCheck[PodCreated](
			"the sidecar {string} works in {string}",
			func(in PodCreated, p brine.Params, _ *brine.Recorder) error {
				name, _ := p.GetString(0)
				dir, ok := p.GetString(1)
				if !ok {
					return fmt.Errorf("expected a name and a directory")
				}
				c, err := containerNamed(in.Pod, name)
				if err != nil {
					return err
				}
				if c.WorkingDir != dir {
					return fmt.Errorf("expected sidecar %q to work in %q, got %q", name, dir, c.WorkingDir)
				}
				return nil
			},
		),

		// SC-04: a sidecar is unprivileged regardless of the main container.
		brine.DefineCheck[PodCreated](
			"the sidecar {string} cannot escalate its privileges",
			func(in PodCreated, p brine.Params, _ *brine.Recorder) error {
				name, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a name parameter")
				}
				c, err := containerNamed(in.Pod, name)
				if err != nil {
					return err
				}
				sc := c.SecurityContext
				if sc == nil || sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
					return fmt.Errorf("expected sidecar %q to be denied escalation, got %+v", name, sc)
				}
				return nil
			},
		),

		// SC-02: a sidecar sees the same working set as the step, or it cannot
		// do its job (a log shipper with no log directory is useless).
		brine.DefineCheck[PodCreated](
			"the sidecar {string} sees the same volumes as the step",
			func(in PodCreated, p brine.Params, _ *brine.Recorder) error {
				name, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a name parameter")
				}
				main, err := mainContainer(in.Pod)
				if err != nil {
					return err
				}
				side, err := containerNamed(in.Pod, name)
				if err != nil {
					return err
				}
				mainPaths := map[string]bool{}
				for _, vm := range main.VolumeMounts {
					mainPaths[vm.MountPath] = true
				}
				sidePaths := map[string]bool{}
				for _, vm := range side.VolumeMounts {
					sidePaths[vm.MountPath] = true
				}
				for p := range mainPaths {
					if !sidePaths[p] {
						return fmt.Errorf("the step sees %q but sidecar %q does not", p, name)
					}
				}
				return nil
			},
		),

		// PE-03 / CF-05
		brine.DefineCheck[PodCreated](
			"the pod pulls images using the secret {string}",
			func(in PodCreated, p brine.Params, _ *brine.Recorder) error {
				secret, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a secret parameter")
				}
				var got []string
				for _, s := range in.Pod.Spec.ImagePullSecrets {
					if s.Name == secret {
						return nil
					}
					got = append(got, s.Name)
				}
				return fmt.Errorf("expected image pull secret %q, got [%s]", secret, strings.Join(got, ", "))
			},
		),

		brine.DefineCheck[PodCreated](
			"the pod names the secret {string} exactly once",
			func(in PodCreated, p brine.Params, _ *brine.Recorder) error {
				secret, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a secret parameter")
				}
				n := 0
				for _, s := range in.Pod.Spec.ImagePullSecrets {
					if s.Name == secret {
						n++
					}
				}
				if n != 1 {
					return fmt.Errorf("expected the secret %q exactly once, found it %d times", secret, n)
				}
				return nil
			},
		),

		brine.DefineCheck[PodCreated](
			"the pod runs as the service account {string}",
			func(in PodCreated, p brine.Params, _ *brine.Recorder) error {
				want, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a service account parameter")
				}
				if in.Pod.Spec.ServiceAccountName != want {
					return fmt.Errorf("expected service account %q, got %q", want, in.Pod.Spec.ServiceAccountName)
				}
				return nil
			},
		),

		brine.DefineCheck[PodCreated](
			"the pod names no image pull secret and no service account",
			func(in PodCreated, _ brine.Params, _ *brine.Recorder) error {
				if len(in.Pod.Spec.ImagePullSecrets) != 0 {
					return fmt.Errorf("expected no image pull secrets, got %d", len(in.Pod.Spec.ImagePullSecrets))
				}
				if in.Pod.Spec.ServiceAccountName != "" {
					return fmt.Errorf("expected no service account, got %q", in.Pod.Spec.ServiceAccountName)
				}
				return nil
			},
		),

		// PE-03: a step must never be restarted behind the scheduler's back.
		brine.DefineCheck[PodCreated](
			"the pod is never restarted",
			func(in PodCreated, _ brine.Params, _ *brine.Recorder) error {
				if in.Pod.Spec.RestartPolicy != corev1.RestartPolicyNever {
					return fmt.Errorf("expected RestartPolicy Never, got %q", in.Pod.Spec.RestartPolicy)
				}
				return nil
			},
		),
	}
}

func volumeAt(pod *corev1.Pod, path string) (corev1.Volume, error) {
	main, err := mainContainer(pod)
	if err != nil {
		return corev1.Volume{}, err
	}
	name := ""
	for _, vm := range main.VolumeMounts {
		if vm.MountPath == path {
			name = vm.Name
			break
		}
	}
	if name == "" {
		return corev1.Volume{}, fmt.Errorf("the step sees nothing mounted at %q", path)
	}
	for _, v := range pod.Spec.Volumes {
		if v.Name == name {
			return v, nil
		}
	}
	return corev1.Volume{}, fmt.Errorf("mount %q names volume %q, which the pod does not define", path, name)
}

func containerNamed(pod *corev1.Pod, name string) (corev1.Container, error) {
	for _, c := range pod.Spec.Containers {
		if c.Name == name {
			return c, nil
		}
	}
	var names []string
	for _, c := range pod.Spec.Containers {
		names = append(names, c.Name)
	}
	return corev1.Container{}, fmt.Errorf("the pod has no container %q (it runs %s)",
		name, strings.Join(names, ", "))
}

// qosClassOf derives the class Kubernetes would assign, from the main
// container's envelope. Guaranteed: limits == requests on every resource.
// BestEffort: neither set. Burstable: anything else.
func qosClassOf(pod *corev1.Pod) string {
	main, err := mainContainer(pod)
	if err != nil {
		return "unknown"
	}
	lim, req := main.Resources.Limits, main.Resources.Requests
	if len(lim) == 0 && len(req) == 0 {
		return "BestEffort"
	}
	if len(lim) > 0 && len(lim) == len(req) {
		same := true
		for k, lv := range lim {
			rv, ok := req[k]
			if !ok || lv.Cmp(rv) != 0 {
				same = false
				break
			}
		}
		if same {
			return "Guaranteed"
		}
	}
	return "Burstable"
}

func matchQuantities(list corev1.ResourceList, cpu, mem, kind string) error {
	if cpu != "" {
		want, err := resource.ParseQuantity(cpu)
		if err != nil {
			return fmt.Errorf("bad cpu %q: %w", cpu, err)
		}
		got, ok := list[corev1.ResourceCPU]
		if !ok {
			return fmt.Errorf("expected a cpu %s of %s, none is set", kind, cpu)
		}
		if got.Cmp(want) != 0 {
			return fmt.Errorf("expected a cpu %s of %s, got %s", kind, cpu, got.String())
		}
	}
	if mem != "" {
		want, err := resource.ParseQuantity(mem)
		if err != nil {
			return fmt.Errorf("bad memory %q: %w", mem, err)
		}
		got, ok := list[corev1.ResourceMemory]
		if !ok {
			return fmt.Errorf("expected a memory %s of %s, none is set", kind, mem)
		}
		if got.Cmp(want) != 0 {
			return fmt.Errorf("expected a memory %s of %s, got %s", kind, mem, got.String())
		}
	}
	return nil
}

// ClusterConfigDefinitions builds workers whose CONFIG differs — image pull
// secrets, a service account, a private registry. These are operator settings,
// so they belong to the worker rather than to any one container spec.
func ClusterConfigDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMapUsing[brine.Empty, ClusterReady](
			"a jetbridge worker that pulls with the secrets {string} as the service account {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, res brine.Resources) (ClusterReady, error) {
				secrets, _ := p.GetString(0)
				account, ok := p.GetString(1)
				if !ok {
					return ClusterReady{}, fmt.Errorf("expected secrets and a service account")
				}
				return newConfiguredWorker(res, func(cfg *jetbridge.Config) {
					cfg.ImagePullSecrets = splitList(secrets)
					cfg.ServiceAccount = account
				})
			},
		),

		// An unschedulable pod is only reported once the scheduling deadline
		// passes. With the 5-minute default a scenario would simply hang, so
		// this worker is impatient — the same move process_test.go makes.
		brine.DefineMapUsing[brine.Empty, ClusterReady](
			"a jetbridge worker that waits only seconds for a pod to be scheduled",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, _ brine.Params, _ *brine.Recorder, res brine.Resources) (ClusterReady, error) {
				return newConfiguredWorker(res, func(cfg *jetbridge.Config) {
					cfg.PodSchedulingTimeout = 3 * time.Second
					cfg.PodStartupTimeout = 2 * time.Second
				})
			},
		),

		// CF-05: a private registry's credentials are added to every pod, and
		// must not be added twice when the operator already listed them.
		brine.DefineMapUsing[brine.Empty, ClusterReady](
			"a jetbridge worker pulling from a private registry with secret {string}, already pulling with {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, res brine.Resources) (ClusterReady, error) {
				registrySecret, _ := p.GetString(0)
				existing, ok := p.GetString(1)
				if !ok {
					return ClusterReady{}, fmt.Errorf("expected a registry secret and existing secrets")
				}
				return newConfiguredWorker(res, func(cfg *jetbridge.Config) {
					cfg.ImagePullSecrets = splitList(existing)
					cfg.ImageRegistry = &jetbridge.ImageRegistryConfig{
						Prefix:     "gcr.io/my-project/concourse",
						SecretName: registrySecret,
					}
				})
			},
		),
	}
}

// newConfiguredWorker is now a thin alias over the shared fixture. It keeps
// its own name because six steps read better with it.
func newConfiguredWorker(res brine.Resources, apply func(*jetbridge.Config)) (ClusterReady, error) {
	cluster, err := NewCluster(res, WithConfig(apply), WithVolumeRepo(), WithTeam())
	if err != nil {
		return ClusterReady{}, err
	}
	return cluster.Ready(), nil
}

func splitList(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// CacheStorageDefinitions covers where a step's caches actually live.
//
// A cache exists to survive between builds. Whether it does depends entirely
// on which storage backs it and on the key it is filed under: a key that
// varies per build gives a directory that is always empty, which looks exactly
// like a working cache and is never a hit.
func CacheStorageDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMapUsing[brine.Empty, ClusterReady](
			"a jetbridge worker keeping caches on the node under {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, res brine.Resources) (ClusterReady, error) {
				path, ok := p.GetString(0)
				if !ok {
					return ClusterReady{}, fmt.Errorf("expected a host path parameter")
				}
				return newConfiguredWorker(res, func(cfg *jetbridge.Config) {
					cfg.CacheHostPath = path
				})
			},
		),

		brine.DefineMapUsing[brine.Empty, ClusterReady](
			"a jetbridge worker with an artifact store, told to keep caches {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, res brine.Resources) (ClusterReady, error) {
				store, ok := p.GetString(0)
				if !ok {
					return ClusterReady{}, fmt.Errorf("expected a cache store parameter")
				}
				return newConfiguredWorker(res, func(cfg *jetbridge.Config) {
					cfg.ArtifactDaemonHostPath = "/var/concourse/artifacts"
					cfg.CacheStore = store
				})
			},
		),

		// The job and step identify a cache across builds. Without them the
		// key varies per build and the cache never hits.
		brine.DefineMap[ContainerDraft, ContainerDraft](
			"it belongs to job {int} step {string}",
			func(in ContainerDraft, p brine.Params, _ *brine.Recorder) (ContainerDraft, error) {
				jobID, _ := p.GetInt(0)
				step, ok := p.GetString(1)
				if !ok {
					return ContainerDraft{}, fmt.Errorf("expected a job id and a step name")
				}
				in.JobID, in.StepName = jobID, step
				return in, nil
			},
		),

		brine.DefineCheck[PodCreated](
			"the cache at {string} is kept on the node under {string}",
			func(in PodCreated, p brine.Params, _ *brine.Recorder) error {
				mountPath, _ := p.GetString(0)
				prefix, ok := p.GetString(1)
				if !ok {
					return fmt.Errorf("expected a mount path and a host prefix")
				}
				v, err := volumeAt(in.Pod, mountPath)
				if err != nil {
					return err
				}
				if v.HostPath == nil {
					return fmt.Errorf(
						"expected the cache at %q to live on the node so it survives the pod; it is ephemeral",
						mountPath)
				}
				if !strings.HasPrefix(v.HostPath.Path, prefix) {
					return fmt.Errorf(
						"expected the cache filed under %q so the next build finds it; it is at %q",
						prefix, v.HostPath.Path)
				}
				if v.HostPath.Type == nil || *v.HostPath.Type != corev1.HostPathDirectoryOrCreate {
					return fmt.Errorf(
						"expected the cache directory to be created when absent, or the first build fails (type=%v)",
						v.HostPath.Type)
				}
				return nil
			},
		),
	}
}
