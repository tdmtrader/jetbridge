package steps

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
)

// CrossNodeHandoff is what a following task on another node received from a
// task whose pod -- and node -- produced it.
type CrossNodeHandoff struct {
	Expected, Received         map[string]string
	ProducerNode, ConsumerNode string
	ConsumerFetched            bool
}

func CrossNodeHandoffDefinitions() []brine.StepDefinition {
	const action = "a task on one approved node produces {string} containing {string} for a following task on another"
	const report = "the following task received exactly that artifact from the other node"
	return []brine.StepDefinition{
		brine.DefineMap[LiveTaskPlan, CrossNodeHandoff](action, func(in LiveTaskPlan, p brine.Params, rec *brine.Recorder) (CrossNodeHandoff, error) {
			return applyAction(action, in, p, func(in LiveTaskPlan, a Args) (CrossNodeHandoff, error) {
				return crossNodeHandoff(in, rec, a.String(0), a.String(1))
			})
		}),
		check[CrossNodeHandoff](report, func(in CrossNodeHandoff, p brine.Params) error {
			switch {
			case in.ProducerNode == "" || in.ProducerNode == in.ConsumerNode:
				return fmt.Errorf("producer on %q and consumer on %q did not cross nodes", in.ProducerNode, in.ConsumerNode)
			case !in.ConsumerFetched:
				return fmt.Errorf("the consumer's fetch-inputs init container did not complete on %s", in.ConsumerNode)
			case !reflect.DeepEqual(in.Expected, in.Received):
				return fmt.Errorf("expected exact artifact %v on %s, received %v", in.Expected, in.ConsumerNode, in.Received)
			}
			return nil
		}),
	}
}

// crossNodeHandoff runs the producer on the fixture's first node and the
// consumer on its second, each required there, deletes the producer's pod
// before the read, and reads the consumer's input volume on its own node.
// Neither the artifact's bytes nor its location are written by this step:
// the producer's own command writes them and the runtime's own init container
// moves them.
func crossNodeHandoff(in LiveTaskPlan, rec *brine.Recorder, name, content string) (CrossNodeHandoff, error) {
	out := CrossNodeHandoff{Expected: map[string]string{name: content}}
	ctx, cancel := context.WithTimeout(execLogger("live-crossnode-handoff"), 4*time.Minute)
	rec.RegisterDisposer(cancel)
	f, err := newLiveArtifactNodes(ctx, rec)
	if err != nil {
		return out, err
	}
	producerNode, consumerNode := f.stores[0].anchor.Spec.NodeName, f.stores[1].anchor.Spec.NodeName
	dbWorker, err := in.Database.PersistNamedWorker("k8s-worker-1")
	if err != nil {
		return out, err
	}
	team, err := in.Database.TeamFactory.CreateTeam(atc.Team{Name: "main"})
	if err != nil {
		return out, fmt.Errorf("create owned main team: %w", err)
	}
	// One locator, one discovery client: the same runtime state a single
	// worker carries, with only the placement differing per task.
	locator := jetbridge.NewArtifactLocator()
	discovery := jetbridge.NewDaemonClient(lagertest.NewTestLogger("crossnode-daemon"), f.cluster.Clientset, f.cluster.Namespace, liveArtifactDaemonService, f.port, nil)
	worker := func(node string) *jetbridge.Worker {
		cfg := f.runtimeConfig(node)
		cfg.PodStartupTimeout, cfg.PodSchedulingTimeout = 30*time.Second, 30*time.Second
		return jetbridge.NewWorker(dbWorker, f.cluster.Clientset, cfg, jetbridge.WorkerDeps{
			Executor: f.stores[0].executor, ArtifactLocator: locator, DaemonClient: discovery,
		})
	}
	producerStore, err := f.store(producerNode)
	if err != nil {
		return out, err
	}
	consumerStore, err := f.store(consumerNode)
	if err != nil {
		return out, err
	}

	spec := runtime.ContainerSpec{TeamID: team.ID(), Type: db.ContainerTypeTask,
		ImageSpec: runtime.ImageSpec{ImageURL: "busybox:1.37.0"}, Dir: "/work", Outputs: map[string]string{"result": "/work/result"}}
	var producerErr bytes.Buffer
	produce := runtime.ProcessSpec{Path: "sh", Args: []string{"-ec", `mkdir -p "$(dirname "$1/$3")"; printf '%s' "$2" > "$1/$3"`, "brine", "/work/result", content, name}}
	producerWorker := worker(producerNode)
	process, output, producer, err := liveHandoffContainer(ctx, rec, producerStore, producerWorker, "producer", spec, "/work/result", produce, runtime.ProcessIO{Stderr: &producerErr})
	if err != nil {
		return out, fmt.Errorf("producing task: %w", err)
	}
	out.ProducerNode = producer.Spec.NodeName
	result, err := process.Wait(ctx)
	if err != nil || result.ExitStatus != 0 {
		return out, fmt.Errorf("producing task exit %d: %v; stderr=%s", result.ExitStatus, err, producerErr.String())
	}
	artifact := producerWorker.ArtifactFromVolume(output)
	if err := deleteLiveStoragePod(ctx, producerStore, producer); err != nil {
		return out, err
	}
	fmt.Printf("cross-node producer UID %s on %s deleted before the read\n", producer.UID, out.ProducerNode)

	spec.Outputs = nil
	spec.Inputs = []runtime.Input{{Artifact: artifact, DestinationPath: "/work/input"}}
	var consumerOut, consumerErr bytes.Buffer
	consume := runtime.ProcessSpec{Path: "tar", Args: []string{"cf", "-", "-C", "/work/input", "."}}
	process, input, consumer, err := liveHandoffContainer(ctx, rec, consumerStore, worker(consumerNode), "consumer", spec, "/work/input", consume, runtime.ProcessIO{Stdout: &consumerOut, Stderr: &consumerErr})
	if err != nil {
		return out, fmt.Errorf("consuming task: %w", err)
	}
	out.ConsumerNode = consumer.Spec.NodeName
	for _, init := range consumer.Status.InitContainerStatuses {
		if init.Name == "fetch-inputs" {
			out.ConsumerFetched = init.ContainerID != "" && init.State.Terminated != nil && init.State.Terminated.ExitCode == 0
		}
	}
	// Read what the init container put on the consumer's node before the
	// task's own output, so a missing or wrong copy cannot hide behind it.
	fetched, err := input.StreamOut(ctx, ".", compression.NewGzipCompression())
	if err != nil {
		return out, err
	}
	onNode, readErr := filesInGzippedTar(fetched)
	closeErr := fetched.Close()
	if readErr != nil || closeErr != nil || !reflect.DeepEqual(onNode, out.Expected) {
		return out, fmt.Errorf("input on %s: expected %v, got %v; read=%v close=%v", out.ConsumerNode, out.Expected, onNode, readErr, closeErr)
	}
	result, err = process.Wait(ctx)
	if err != nil || result.ExitStatus != 0 {
		return out, fmt.Errorf("consuming task exit %d: %v; stderr=%s", result.ExitStatus, err, consumerErr.String())
	}
	out.Received, err = filesInTar(bytes.NewReader(consumerOut.Bytes()))
	if err == nil {
		fmt.Printf("cross-node handoff: %s on %s -> consumer UID %s on %s\n", name, out.ProducerNode, consumer.UID, out.ConsumerNode)
	}
	return out, err
}
