package steps

import (
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
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
// Checks inspect the stored pod. The migrated fixtures use the real API;
// legacy fake-backed configuration and execution fixtures remain separate.
// QoS is read from API-assigned status, never reimplemented by the fixture.

func ContainerPodDefinitions() []brine.StepDefinition {
	return append([]brine.StepDefinition{

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

		Transform[ContainerDraft, ContainerDraft]("it produces output {string} at {string}",
			func(in ContainerDraft, a Args) (ContainerDraft, error) {
				name := a.String(0)
				if name == "" {
					return ContainerDraft{}, fmt.Errorf("expected a nonempty output name")
				}
				if in.NamedOutputs == nil {
					in.NamedOutputs = map[string]string{}
				}
				if _, exists := in.NamedOutputs[name]; exists {
					return ContainerDraft{}, fmt.Errorf("duplicate output name %q", name)
				}
				in.NamedOutputs[name] = a.String(1)
				return in, nil
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

		refineSidecar("the sidecar {string} declares its working directory as {string}",
			func(sc *atc.SidecarConfig, a Args) { sc.WorkingDir = a.String(1) }),

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

		ephemeralMountDefinition(),

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

		// PE-07: Kubernetes assigns QoS when it admits the pod, accounting
		// for every container. Include the main envelope in failure diagnostics.
		CheckString[PodCreated]("the API assigns the pod QoS class {string}",
			"the pod's QoS class",
			func(in PodCreated) (string, error) {
				if in.Pod == nil || in.Pod.UID == "" || in.Pod.ResourceVersion == "" {
					return "", fmt.Errorf("QoS observation requires an API-persisted pod")
				}
				if in.Pod.Status.QOSClass == "" {
					return "", fmt.Errorf("API reported no QoS class for pod %q", in.Pod.Name)
				}
				return string(in.Pod.Status.QOSClass), nil
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
		CheckThat[PodCreated]("the step has no resource limits",
			func(in PodCreated) error {
				main, err := mainContainer(in.Pod)
				if err != nil {
					return err
				}
				if len(main.Resources.Limits) != 0 {
					return fmt.Errorf("expected no resource limits, got %v", main.Resources.Limits)
				}
				return nil
			}),

		// PE-04: both privilege modes retain the pod-level hardening policy.
		brine.DefineCheck[PodCreated]("the step uses the {string} security policy",
			func(in PodCreated, p brine.Params, _ *brine.Recorder) error {
				mode, ok := p.GetString(0)
				if !ok || (mode != "privileged" && mode != "unprivileged") {
					return fmt.Errorf("expected privileged or unprivileged security policy, got %q", mode)
				}
				main, err := mainContainer(in.Pod)
				if err != nil {
					return err
				}
				podSC := in.Pod.Spec.SecurityContext
				if podSC == nil {
					return fmt.Errorf("expected a pod security context")
				}
				if podSC.RunAsNonRoot != nil {
					return fmt.Errorf("expected RunAsNonRoot to remain unset, got %t", *podSC.RunAsNonRoot)
				}
				if podSC.SeccompProfile == nil || podSC.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
					return fmt.Errorf("expected RuntimeDefault seccomp, got %+v", podSC.SeccompProfile)
				}
				sc := main.SecurityContext
				if mode == "privileged" {
					if sc == nil || sc.Privileged == nil || !*sc.Privileged {
						return fmt.Errorf("expected a privileged container, got %+v", sc)
					}
				} else if sc == nil || sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
					return fmt.Errorf("expected privilege escalation to be denied, got %+v", sc)
				}
				return nil
			}),

		// --- Sidecars ---

		CheckCount[PodCreated]("the pod has {int} init containers",
			"init containers",
			func(in PodCreated) ([]string, error) {
				names := make([]string, 0, len(in.Pod.Spec.InitContainers))
				for _, c := range in.Pod.Spec.InitContainers {
					names = append(names, c.Name)
				}
				return names, nil
			}),

		CheckCount[PodCreated]("the pod runs {int} containers",
			"containers",
			func(in PodCreated) ([]string, error) {
				names := make([]string, 0, len(in.Pod.Spec.Containers))
				for _, c := range in.Pod.Spec.Containers {
					names = append(names, c.Name)
				}
				return names, nil
			}),

		containerRosterDefinition(),

		CheckStringFor[PodCreated]("the container {string} works in {string}",
			"the container's working directory",
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

		// SC-02: preserve the complete mount list, including permissions and order.
		Assert[PodCreated]("the sidecar {string} sees the same volumes as the step",
			func(in PodCreated, args Args) error {
				main, err := mainContainer(in.Pod)
				if err != nil {
					return err
				}
				side, err := containerNamed(in.Pod, args.String(0))
				if err != nil {
					return err
				}
				if !reflect.DeepEqual(side.VolumeMounts, main.VolumeMounts) {
					return fmt.Errorf("expected sidecar %q mounts %+v to equal main mounts %+v", side.Name, side.VolumeMounts, main.VolumeMounts)
				}
				return validatePodMounts(in.Pod)
			}),

		// CF-05: compare the complete list, including duplicate multiplicity.
		// Credential names cannot contain commas; use the configuration vocabulary.
		Assert[PodCreated]("the pod pulls images using exactly {string}",
			func(in PodCreated, args Args) error {
				want := splitList(args.String(0))
				got := make([]string, len(in.Pod.Spec.ImagePullSecrets))
				for i, secret := range in.Pod.Spec.ImagePullSecrets {
					got[i] = secret.Name
				}
				slices.Sort(want)
				slices.Sort(got)
				if !slices.Equal(want, got) {
					return fmt.Errorf("expected exactly image pull secrets %v, got %v", want, got)
				}
				return nil
			}),

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
	}, sidecarSpecDefinitions()...)
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
		Refine[WorkerReady]("the worker pulls with secrets {string} as service account {string}",
			func(in WorkerReady, a Args) WorkerReady {
				in.Config.ImagePullSecrets = splitList(a.String(0))
				in.Config.ServiceAccount = a.String(1)
				return in.rebuild()
			}),
		Refine[WorkerReady]("the worker uses private registry secret {string}, alongside {string}",
			func(in WorkerReady, a Args) WorkerReady {
				in.Config.ImagePullSecrets = splitList(a.String(1))
				in.Config.ImageRegistry = &jetbridge.ImageRegistryConfig{Prefix: "gcr.io/my-project/concourse", SecretName: a.String(0)}
				return in.rebuild()
			}),
	}
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

		Refine[WorkerReady]("the worker keeps standalone caches under {string}",
			func(in WorkerReady, a Args) WorkerReady {
				in.Config.CacheHostPath = a.String(0)
				return in.rebuild()
			}),
		Refine[WorkerReady]("the worker uses {string} cache storage",
			func(in WorkerReady, a Args) WorkerReady {
				in.Config.CacheStore = a.String(0)
				return in.rebuild()
			}),

		Refine[ContainerDraft]("it has no reusable cache identity",
			func(in ContainerDraft, _ Args) ContainerDraft {
				in.CacheIdentityOmitted = true
				return in
			}),

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

func ephemeralMountDefinition() brine.StepDefinition {
	return brine.DefineCheck[PodCreated]("the step has exactly these ephemeral mounts",
		func(in PodCreated, p brine.Params, _ *brine.Recorder) error {
			rows := p.RequireDataTable()
			if len(rows) < 2 || (len(rows[0]) != 1 && len(rows[0]) != 2) || rows[0][0] != "mount path" ||
				(len(rows[0]) == 2 && rows[0][1] != "volume prefix") {
				return fmt.Errorf("expected a nonempty table headed 'mount path', optionally followed by 'volume prefix'")
			}
			expected := map[string]bool{}
			prefixes := map[string]string{}
			for _, row := range rows[1:] {
				if len(row) != len(rows[0]) || !filepath.IsAbs(row[0]) || expected[row[0]] {
					return fmt.Errorf("expected distinct absolute mount paths, got %v", row)
				}
				expected[row[0]] = true
				if len(row) == 2 {
					prefixes[row[0]] = row[1]
				}
			}
			if len(in.Pod.Spec.Volumes) != len(expected) {
				return fmt.Errorf("expected %d volumes, got %d", len(expected), len(in.Pod.Spec.Volumes))
			}
			for _, volume := range in.Pod.Spec.Volumes {
				if volume.EmptyDir == nil {
					return fmt.Errorf("expected volume %q to be ephemeral", volume.Name)
				}
			}
			main, err := mainContainer(in.Pod)
			if err != nil {
				return err
			}
			if len(main.VolumeMounts) != len(expected) {
				return fmt.Errorf("expected %d step mounts, got %d", len(expected), len(main.VolumeMounts))
			}
			if err := validatePodMounts(in.Pod); err != nil {
				return err
			}
			mounted := map[string]bool{}
			for _, mount := range main.VolumeMounts {
				if mount.SubPath != "" || mount.SubPathExpr != "" {
					return fmt.Errorf("expected the whole volume at %q, got subPath=%q subPathExpr=%q", mount.MountPath, mount.SubPath, mount.SubPathExpr)
				}
				if !expected[mount.MountPath] {
					return fmt.Errorf("unexpected step mount at %q", mount.MountPath)
				}
				if prefix := prefixes[mount.MountPath]; prefix != "" && !strings.HasPrefix(mount.Name, prefix) {
					return fmt.Errorf("mount at %q uses volume %q, want prefix %q", mount.MountPath, mount.Name, prefix)
				}
				if mounted[mount.Name] {
					return fmt.Errorf("independent directories share volume %q", mount.Name)
				}
				mounted[mount.Name] = true
				delete(expected, mount.MountPath)
			}
			if len(expected) != 0 {
				return fmt.Errorf("missing step mounts: %v", expected)
			}
			return nil
		})
}
