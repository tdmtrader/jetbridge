package steps

import (
	"context"
	"fmt"
	"strings"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// ContainerLifecycleDefinitions migrates container_test.go's reuse, property
// and re-attach blocks — what happens to a step's container across a web
// restart, a repeated check, and a pod that has already finished.

// LeftoverPod is the state where a pod from a previous run is already on the
// cluster before the step starts.
type LeftoverPod struct {
	Namespace string
	Worker    *jetbridge.Worker
	Clientset *fake.Clientset
	Ctx       context.Context
	Handle    string
	Metadata  db.ContainerMetadata
	PodName   string
}

// ReusedPod is the state after the step has run against that cluster.
type ReusedPod struct {
	Namespace string
	Clientset *fake.Clientset
	Ctx       context.Context
	PodName   string
	Pod       *corev1.Pod
	Err       error
}

// ContainerProperties is the state for the property store.
type ContainerProperties struct {
	Container runtime.Container
	Props     map[string]string
	Err       error
}

func ContainerLifecycleDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		// PE-01. A check container's pause pod finishes when its sleep
		// expires. The next check must get a fresh pod — exec-ing into a dead
		// one fails in a way that looks like the resource is broken.
		brine.DefineMapUsing[brine.Empty, LeftoverPod](
			"a check step whose previous pod is {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, res brine.Resources) (LeftoverPod, error) {
				phase, ok := p.GetString(0)
				if !ok {
					return LeftoverPod{}, fmt.Errorf("expected a phase parameter")
				}
				database, ok := res.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return LeftoverPod{}, fmt.Errorf("jetbridge-db resource is %T", res.Get("jetbridge-db"))
				}
				dbWorker, err := database.PersistNamedWorker("k8s-worker-1")
				if err != nil {
					return LeftoverPod{}, err
				}

				ctx := context.Background()
				namespace := "test-namespace"
				clientset := fake.NewSimpleClientset()
				worker := jetbridge.NewWorker(dbWorker, clientset, jetbridge.NewConfig(namespace, ""))
				worker.SetExecutor(execStub{})

				handle := "aaaa1111-bbbb-cccc-dddd-eeee2222ffff"
				metadata := db.ContainerMetadata{Type: db.ContainerTypeCheck, StepName: "my-time"}
				podName := jetbridge.GeneratePodName(metadata, handle)

				leftover := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: podName, Namespace: namespace,
						Labels: map[string]string{"concourse.ci/worker": "k8s-worker-1"},
					},
					Status: corev1.PodStatus{Phase: corev1.PodPhase(phase)},
				}
				if _, err := clientset.CoreV1().Pods(namespace).Create(ctx, leftover, metav1.CreateOptions{}); err != nil {
					return LeftoverPod{}, fmt.Errorf("create leftover pod: %w", err)
				}

				return LeftoverPod{
					Namespace: namespace, Worker: worker, Clientset: clientset,
					Ctx: ctx, Handle: handle, Metadata: metadata, PodName: podName,
				}, nil
			},
		),

		brine.DefineMap[LeftoverPod, ReusedPod](
			"the check runs again",
			func(in LeftoverPod, _ brine.Params, _ *brine.Recorder) (ReusedPod, error) {
				container, _, err := in.Worker.FindOrCreateContainer(
					in.Ctx,
					db.NewFixedHandleContainerOwner(in.Handle),
					in.Metadata,
					runtime.ContainerSpec{
						TeamID:    1,
						Dir:       "/tmp/build/workdir",
						ImageSpec: runtime.ImageSpec{ImageURL: "docker:///concourse/time-resource"},
					},
					&noopDelegate{},
				)
				if err != nil {
					return ReusedPod{}, fmt.Errorf("find or create container: %w", err)
				}

				out := ReusedPod{
					Namespace: in.Namespace, Clientset: in.Clientset,
					Ctx: in.Ctx, PodName: in.PodName,
				}
				if _, err := container.Run(in.Ctx,
					runtime.ProcessSpec{Path: "/opt/resource/check"},
					runtime.ProcessIO{Stdin: strings.NewReader("{}")},
				); err != nil {
					out.Err = err
					return out, nil
				}

				pod, err := in.Clientset.CoreV1().Pods(in.Namespace).Get(in.Ctx, in.PodName, metav1.GetOptions{})
				if err != nil {
					return ReusedPod{}, fmt.Errorf("get pod after run: %w", err)
				}
				out.Pod = pod
				return out, nil
			},
		),

		brine.DefineCheck[ReusedPod](
			"the step gets a live pod, not the dead one",
			func(in ReusedPod, _ brine.Params, _ *brine.Recorder) error {
				if in.Err != nil {
					return fmt.Errorf("the step failed instead of replacing the pod: %v", in.Err)
				}
				if in.Pod == nil {
					return fmt.Errorf("no pod named %q exists after the run", in.PodName)
				}
				switch in.Pod.Status.Phase {
				case corev1.PodSucceeded, corev1.PodFailed:
					return fmt.Errorf("expected a live pod, %q is still %s — the dead pod was reused",
						in.PodName, in.Pod.Status.Phase)
				}
				return nil
			},
		),

		// A container's properties are how the runtime remembers a step's
		// result in-process, which is what Attach reads before it asks
		// Kubernetes anything.
		brine.DefineMapUsing[brine.Empty, ContainerProperties](
			"a container that has recorded {string} as {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, res brine.Resources) (ContainerProperties, error) {
				key, _ := p.GetString(0)
				value, ok := p.GetString(1)
				if !ok {
					return ContainerProperties{}, fmt.Errorf("expected a key and a value")
				}
				database, ok := res.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return ContainerProperties{}, fmt.Errorf("jetbridge-db resource is %T", res.Get("jetbridge-db"))
				}
				dbWorker, err := database.PersistNamedWorker("k8s-worker-1")
				if err != nil {
					return ContainerProperties{}, err
				}
				ctx := context.Background()
				clientset := fake.NewSimpleClientset()
				worker := jetbridge.NewWorker(dbWorker, clientset, jetbridge.NewConfig("test-namespace", ""))

				container, _, err := worker.FindOrCreateContainer(
					ctx,
					db.NewFixedHandleContainerOwner("props-handle"),
					db.ContainerMetadata{},
					runtime.ContainerSpec{ImageSpec: runtime.ImageSpec{ImageURL: "docker:///alpine"}},
					&noopDelegate{},
				)
				if err != nil {
					return ContainerProperties{}, fmt.Errorf("find or create container: %w", err)
				}
				if err := container.SetProperty(key, value); err != nil {
					return ContainerProperties{}, fmt.Errorf("set property: %w", err)
				}
				props, err := container.Properties()
				return ContainerProperties{Container: container, Props: props, Err: err}, nil
			},
		),

		brine.DefineCheck[ContainerProperties](
			"reading it back yields {string} as {string}",
			func(in ContainerProperties, p brine.Params, _ *brine.Recorder) error {
				key, _ := p.GetString(0)
				want, ok := p.GetString(1)
				if !ok {
					return fmt.Errorf("expected a key and a value")
				}
				if in.Err != nil {
					return fmt.Errorf("reading properties failed: %v", in.Err)
				}
				got, found := in.Props[key]
				if !found {
					return fmt.Errorf("expected property %q, the container has %d properties", key, len(in.Props))
				}
				if got != want {
					return fmt.Errorf("expected %q to be %q, got %q", key, want, got)
				}
				return nil
			},
		),
	}
}
