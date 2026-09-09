package steps

import (
	"fmt"
	"strings"

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

		// One input, carrying a real artifact volume — the only kind of input
		// there is. This sentence used to have a longer twin, "... produced by
		// an earlier step", for the input that carried an artifact, leaving
		// the plain form to mean an input with none. That distinction is gone:
		// in JetBridge every input is produced by an earlier step, a get or a
		// task, so once the artifact-less form ceased to exist the qualifier
		// named the only case there was and carried no information.
		Refine[ContainerDraft]("it takes an input at {string}",
			func(in ContainerDraft, a Args) ContainerDraft {
				in.Inputs = append(in.Inputs, a.String(0))
				return in
			}),

		Refine[ContainerDraft]("it produces an output at {string}",
			func(in ContainerDraft, a Args) ContainerDraft {
				in.Outputs = append(in.Outputs, a.String(0))
				return in
			}),

		Refine[ContainerDraft]("it caches {string}",
			func(in ContainerDraft, a Args) ContainerDraft {
				in.Caches = append(in.Caches, a.String(0))
				return in
			}),

		Refine[ContainerDraft]("it uses scratch space at {string}",
			func(in ContainerDraft, a Args) ContainerDraft {
				in.Scratch = append(in.Scratch, a.String(0))
				return in
			}),

		Refine[ContainerDraft]("it works in {string}",
			func(in ContainerDraft, a Args) ContainerDraft {
				in.Dir = a.String(0)
				return in
			}),

		// PE-07: the resource envelope decides the pod's QoS class, which
		// decides which pods the kubelet evicts first under pressure.
		// CPU is CPU shares, memory is bytes — the units ContainerLimits
		// actually uses. The runtime maps shares to millicores.
		Refine[ContainerDraft]("it is limited to {int} CPU shares and {int} bytes of memory",
			func(in ContainerDraft, a Args) ContainerDraft {
				c, m := uint64(a.Int(0)), uint64(a.Int(1))
				in.LimitCPU, in.LimitMemory = &c, &m
				return in
			}),

		Refine[ContainerDraft]("it requests {int} CPU shares and {int} bytes of memory",
			func(in ContainerDraft, a Args) ContainerDraft {
				c, m := uint64(a.Int(0)), uint64(a.Int(1))
				in.RequestCPU, in.RequestMemory = &c, &m
				return in
			}),

		// PE-07's ephemeral-storage clause: a step that writes a large
		// artifact to local disk is evicted without this, and the eviction
		// looks like an unexplained failure.
		Refine[ContainerDraft]("it is limited to {int} bytes of local disk, requesting {int}",
			func(in ContainerDraft, a Args) ContainerDraft {
				l, r := uint64(a.Int(0)), uint64(a.Int(1))
				in.LimitEphemeral, in.RequestEphemeral = &l, &r
				return in
			}),

		// Both disk fields are required; quantity comparison accepts equivalent units.
		resourceCheck("the step may use at most {string} of local disk, reserving {string}",
			resourceExpectation{corev1.ResourceEphemeralStorage, "limit", false},
			resourceExpectation{corev1.ResourceEphemeralStorage, "request", false},
		),
		// PE-04
		Refine[ContainerDraft]("it runs privileged",
			func(in ContainerDraft, _ Args) ContainerDraft {
				in.Privileged = true
				return in
			}),

		// SC-01 to SC-06
		Refine[ContainerDraft]("a sidecar {string} runs {string} alongside it",
			func(in ContainerDraft, a Args) ContainerDraft {
				in.Sidecars = append(in.Sidecars,
					atc.SidecarConfig{Name: a.String(0), Image: a.String(1)})
				return in
			}),

		Transform[ContainerDraft, ContainerDraft](
			"the sidecar {string} declares its working directory as {string}",
			func(in ContainerDraft, a Args) (ContainerDraft, error) {
				name := a.String(0)

				for i := range in.Sidecars {
					if in.Sidecars[i].Name == name {
						in.Sidecars[i].WorkingDir = a.String(1)
						return in, nil
					}
				}
				return ContainerDraft{}, fmt.Errorf("no sidecar named %q", name)
			},
		),

		// --- Checks over the resulting pod ---

		CheckCount[PodCreated]("the pod has {int} volumes",
			"volumes",
			func(in PodCreated) ([]string, error) {
				names := make([]string, 0, len(in.Pod.Spec.Volumes))
				for _, v := range in.Pod.Spec.Volumes {
					names = append(names, v.Name)
				}
				return names, nil
			}),

		CheckMember[PodCreated]("the step sees a volume mounted at {string}",
			"the step's mounts",
			func(in PodCreated) ([]string, error) {
				main, err := mainContainer(in.Pod)
				if err != nil {
					return nil, err
				}
				var paths []string
				for _, vm := range main.VolumeMounts {
					paths = append(paths, vm.MountPath)
				}
				return paths, nil
			}),

		// CO-06/CO-07/CF-04: which storage backs a volume decides whether its
		// contents survive the pod.
		CheckThat[PodCreated]("every volume is ephemeral",
			func(in PodCreated) error {
				for _, v := range in.Pod.Spec.Volumes {
					if v.EmptyDir == nil {
						return fmt.Errorf("expected volume %q to be ephemeral, it is not (hostPath=%v)",
							v.Name, v.HostPath != nil)
					}
				}
				return nil
			}),

		// Every VolumeMount in the pod — the step's own container and every
		// init container before it — must name exactly one of the volumes the
		// pod declares.
		//
		// This is not a restatement of the mount assertions around it. Those
		// ask where the step SEES a directory, and they answer by finding the
		// FIRST volume of that name. This asks whether the pod is admissible
		// at all: the API server rejects a mount naming a volume the pod does
		// not declare, and it rejects a volume name declared twice, so either
		// one turns a pod spec that satisfies every other check in this file
		// into a step that never starts — with the error on the pod object
		// rather than in the build log, which is where anyone would look.
		//
		// Nothing else here counts volumes BY NAME, which is what makes the
		// duplicate half of this reachable only from here.
		CheckThat[PodCreated]("every mount in the pod names exactly one of its volumes",
			func(in PodCreated) error {
				return validatePodMounts(in.Pod)
			}),

		// Resolve the actual mount first so an absent mount is distinct from
		// one backed by the wrong storage. Storage kind is a data variation,
		// not a separate step definition.
		CheckStringFor[PodCreated]("the volume mounted at {string} uses {string} storage",
			"the mounted volume's storage",
			func(in PodCreated, path string) (string, error) {
				v, err := volumeAt(in.Pod, path)
				if err != nil {
					return "", err
				}
				switch {
				case v.HostPath != nil && v.EmptyDir == nil:
					return "node-local", nil
				case v.EmptyDir != nil && v.HostPath == nil:
					return "ephemeral", nil
				default:
					return "", fmt.Errorf("volume at %q has neither a unique hostPath nor emptyDir source", path)
				}
			}),

		// PE-07: the QoS class is the observable consequence of the envelope,
		// so the failure carries the limits and requests it was derived from —
		// the evidence the rule is actually about.
		CheckString[PodCreated]("the pod is scheduled as {string}",
			"the pod's QoS class",
			func(in PodCreated) (string, error) {
				return qosClassOf(in.Pod), nil
			},
			func(in PodCreated) string {
				main, _ := mainContainer(in.Pod)
				return fmt.Sprintf("limits=%v requests=%v",
					main.Resources.Limits, main.Resources.Requests)
			}),

		// CPU/memory retain the existing allowance for an unchecked blank field.
		resourceCheck("the step may use at most {string} CPU and {string} memory",
			resourceExpectation{corev1.ResourceCPU, "limit", true},
			resourceExpectation{corev1.ResourceMemory, "limit", true},
		),
		resourceCheck("the step is reserved {string} CPU and {string} memory",
			resourceExpectation{corev1.ResourceCPU, "request", true},
			resourceExpectation{corev1.ResourceMemory, "request", true},
		),
		// PE-04
		CheckThat[PodCreated]("the step can escalate its privileges",
			func(in PodCreated) error {
				main, err := mainContainer(in.Pod)
				if err != nil {
					return err
				}
				sc := main.SecurityContext
				if sc == nil || sc.Privileged == nil || !*sc.Privileged {
					return fmt.Errorf("expected a privileged container, got %+v", sc)
				}
				return nil
			}),

		CheckThat[PodCreated]("the step cannot escalate its privileges",
			func(in PodCreated) error {
				main, err := mainContainer(in.Pod)
				if err != nil {
					return err
				}
				sc := main.SecurityContext
				if sc == nil || sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
					return fmt.Errorf("expected privilege escalation to be denied, got %+v", sc)
				}
				return nil
			}),

		// --- Sidecars ---

		CheckCount[PodCreated]("the pod runs {int} containers",
			"containers",
			func(in PodCreated) ([]string, error) {
				names := make([]string, 0, len(in.Pod.Spec.Containers))
				for _, c := range in.Pod.Spec.Containers {
					names = append(names, c.Name)
				}
				return names, nil
			}),

		// containerNamed's error is how "there is no such sidecar" is reported;
		// it already names the containers the pod does run.
		CheckStringFor[PodCreated]("the sidecar {string} runs image {string}",
			"the sidecar image",
			func(in PodCreated, name string) (string, error) {
				c, err := containerNamed(in.Pod, name)
				return c.Image, err
			}),

		CheckStringFor[PodCreated]("the sidecar {string} works in {string}",
			"the sidecar's working directory",
			func(in PodCreated, name string) (string, error) {
				c, err := containerNamed(in.Pod, name)
				return c.WorkingDir, err
			}),

		// SC-04: a sidecar is unprivileged regardless of the main container.
		// Keeps its own body: the parameter names WHICH container to look at
		// rather than a value to compare, and a sidecar the pod does not run
		// has to stay an error — under a membership check it would pass by
		// being absent.
		Assert[PodCreated](
			"the sidecar {string} cannot escalate its privileges",
			func(in PodCreated, args Args) error {
				name := args.String(0)

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
		// Keeps its own body: the parameter names the sidecar, and the
		// assertion is that one set of mounts covers another — a subset, not
		// membership of the string the sentence carries.
		Assert[PodCreated](
			"the sidecar {string} sees the same volumes as the step",
			func(in PodCreated, args Args) error {
				name := args.String(0)

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
		CheckMember[PodCreated]("the pod pulls images using the secret {string}",
			"the pod's image pull secrets",
			func(in PodCreated) ([]string, error) {
				var names []string
				for _, s := range in.Pod.Spec.ImagePullSecrets {
					names = append(names, s.Name)
				}
				return names, nil
			}),

		// Keeps its own body: it counts occurrences, and "exactly once" is not
		// membership — the duplicate this check exists to catch would satisfy
		// a member check.
		Assert[PodCreated](
			"the pod names the secret {string} exactly once",
			func(in PodCreated, args Args) error {
				secret := args.String(0)

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

		CheckString[PodCreated]("the pod runs as the service account {string}",
			"the pod's service account",
			func(in PodCreated) (string, error) {
				return in.Pod.Spec.ServiceAccountName, nil
			}),

		// Two fields, so no comparison combinator fits; CheckThat carries the
		// body unchanged and each arm keeps saying which half failed.
		CheckThat[PodCreated]("the pod names no image pull secret and no service account",
			func(in PodCreated) error {
				if len(in.Pod.Spec.ImagePullSecrets) != 0 {
					return fmt.Errorf("expected no image pull secrets, got %d", len(in.Pod.Spec.ImagePullSecrets))
				}
				if in.Pod.Spec.ServiceAccountName != "" {
					return fmt.Errorf("expected no service account, got %q", in.Pod.Spec.ServiceAccountName)
				}
				return nil
			}),

		// PE-03: a step must never be restarted behind the scheduler's back.
		CheckThat[PodCreated]("the pod is never restarted",
			func(in PodCreated) error {
				if in.Pod.Spec.RestartPolicy != corev1.RestartPolicyNever {
					return fmt.Errorf("expected RestartPolicy Never, got %q", in.Pod.Spec.RestartPolicy)
				}
				return nil
			}),
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

type resourceExpectation struct {
	name       corev1.ResourceName
	kind       string
	allowBlank bool
}

// Every resource sentence compares two declared fields. Keeping them explicit
// preserves limit/request selection and the CPU/memory-only blank allowance.
func resourceCheck(pattern string, first, second resourceExpectation) brine.StepDefinition {
	return brine.DefineCheck[PodCreated](pattern, func(in PodCreated, p brine.Params, _ *brine.Recorder) error {
		main, err := mainContainer(in.Pod)
		if err != nil {
			return err
		}
		for i, want := range []resourceExpectation{first, second} {
			raw, ok := p.GetString(i)
			if !ok {
				return fmt.Errorf("step %q requires resource parameter %d", pattern, i)
			}
			if raw == "" && want.allowBlank {
				continue
			}
			list := main.Resources.Limits
			if want.kind == "request" {
				list = main.Resources.Requests
			}
			if err := matchResourceQuantity(list, want.name, raw, want.kind); err != nil {
				return err
			}
		}
		return nil
	})
}

func matchResourceQuantity(list corev1.ResourceList, name corev1.ResourceName, raw, kind string) error {
	want, err := resource.ParseQuantity(raw)
	if err != nil {
		return fmt.Errorf("bad %s %s %q: %w", name, kind, raw, err)
	}
	got, ok := list[name]
	if !ok {
		return fmt.Errorf("expected %s %s of %s, none is set", name, kind, raw)
	}
	if got.Cmp(want) != 0 {
		return fmt.Errorf("expected %s %s of %s, got %s", name, kind, raw, got.String())
	}
	return nil
}

// ClusterConfigDefinitions builds workers whose CONFIG differs — image pull
// secrets, a service account, a private registry. These are operator settings,
// so they belong to the worker rather than to any one container spec.
func ClusterConfigDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		TransformUsing[brine.Empty, ClusterReady](
			"a jetbridge worker that pulls with the secrets {string} as the service account {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, a Args, res brine.Resources) (ClusterReady, error) {
				return newConfiguredWorker(res, func(cfg *jetbridge.Config) {
					cfg.ImagePullSecrets = splitList(a.String(0))
					cfg.ServiceAccount = a.String(1)
				})
			},
		),

		// CF-05: a private registry's credentials are added to every pod, and
		// must not be added twice when the operator already listed them.
		TransformUsing[brine.Empty, ClusterReady](
			"a jetbridge worker pulling from a private registry with secret {string}, already pulling with {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, a Args, res brine.Resources) (ClusterReady, error) {
				return newConfiguredWorker(res, func(cfg *jetbridge.Config) {
					cfg.ImagePullSecrets = splitList(a.String(1))
					cfg.ImageRegistry = &jetbridge.ImageRegistryConfig{
						Prefix:     "gcr.io/my-project/concourse",
						SecretName: a.String(0),
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

		TransformUsing[brine.Empty, ClusterReady](
			"a jetbridge worker keeping caches on the node under {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, a Args, res brine.Resources) (ClusterReady, error) {
				return newConfiguredWorker(res, func(cfg *jetbridge.Config) {
					cfg.CacheHostPath = a.String(0)
				})
			},
		),

		TransformUsing[brine.Empty, ClusterReady](
			"a jetbridge worker with an artifact store, told to keep caches {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, a Args, res brine.Resources) (ClusterReady, error) {
				return newConfiguredWorker(res, func(cfg *jetbridge.Config) {
					cfg.ArtifactDaemonHostPath = "/var/concourse/artifacts"
					cfg.CacheStore = a.String(0)
				})
			},
		),

		// The job and step identify a cache across builds. Without them the
		// key varies per build and the cache never hits.
		Refine[ContainerDraft]("it belongs to job {int} step {string}",
			func(in ContainerDraft, a Args) ContainerDraft {
				in.JobID, in.StepName = a.Int(0), a.String(1)
				return in
			}),

		// The same sentence for a step inside a materialized run. A run's
		// pipeline is created for the run and destroyed with it, so its JobID
		// is a different number every time and names nothing that outlives the
		// build. The team, the template it was instanced from and the job's
		// name within that template are what two runs of the same job share.
		Refine[ContainerDraft]("it belongs to run job {string} of template pipeline {int} in team {int}, step {string}",
			func(in ContainerDraft, a Args) ContainerDraft {
				in.RunJobName = a.String(0)
				in.RunTemplatePipelineID = a.Int(1)
				in.RunTeamID = a.Int(2)
				in.StepName = a.String(3)
				return in
			}),

		// Keeps its own body: it pins three separate properties, and each
		// failure explains the rule it broke — survives the pod, filed under a
		// stable key, created when absent.
		Assert[PodCreated](
			"the cache at {string} is kept on the node under {string}",
			func(in PodCreated, args Args) error {
				mountPath := args.String(0)
				prefix := args.String(1)

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
