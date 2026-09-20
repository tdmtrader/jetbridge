package steps

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

func liveArtifactRecordingDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		liveMirrorRecordingDefinition(),
		brine.DefineMapUsing[brine.Empty, ArtifactCluster](
			"a jetbridge worker with a real node's artifact daemon",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, _ brine.Params, rec *brine.Recorder, res brine.Resources) (ArtifactCluster, error) {
				database, ok := res.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return ArtifactCluster{}, fmt.Errorf("jetbridge-db resource is %T", res.Get("jetbridge-db"))
				}
				return newLiveArtifactRecording(database, rec, false)
			},
		),
		Transform[ArtifactCluster, ArtifactCluster](
			"the {string} step {string} ran on the artifact node",
			func(in ArtifactCluster, a Args) (ArtifactCluster, error) {
				if in.live == nil {
					return in, fmt.Errorf("a real artifact node is required")
				}
				in.Handle, in.NodeName = a.String(1), in.live.pod.Spec.NodeName
				switch a.String(0) {
				case "task":
					in.ProducerType = db.ContainerTypeTask
				case "get":
					in.ProducerType, in.ProducerDir = db.ContainerTypeGet, getStepWorkDir
				default:
					return in, fmt.Errorf("expected task or get step")
				}
				return in, nil
			},
		),
	}
}

func newLiveArtifactRecording(database JetbridgeDB, rec *brine.Recorder, mirror bool) (ArtifactCluster, error) {
	ctx, cancel := context.WithTimeout(execLogger("live-artifact-recording"), 3*time.Minute)
	rec.RegisterDisposer(cancel)
	d, err := startLiveArtifactDaemon(ctx, rec, mirror)
	if err != nil {
		return ArtifactCluster{}, err
	}
	// Producer cleanup precedes daemon/anchor cleanup, including after a
	// partially failed Run. Only this owned namespace's worker pods match.
	TrackDisposer(rec, "the recording's producer pods", func() error {
		clean, stop := context.WithTimeout(context.Background(), 45*time.Second)
		defer stop()
		pods, err := d.store.cluster.Clientset.CoreV1().Pods(d.store.cluster.Namespace).List(clean,
			metav1.ListOptions{LabelSelector: "concourse.ci/handle"})
		if err != nil {
			return err
		}
		// One pod that will not go is not a reason to leave the rest.
		var failures []error
		for i := range pods.Items {
			if err := deleteLiveStoragePod(clean, d.store, &pods.Items[i]); err != nil {
				failures = append(failures, err)
			}
		}
		return errors.Join(failures...)
	})
	trace := new(execObservation)
	cs, err := kubernetes.NewForConfig(trace.config(d.store.cluster.Config))
	if err != nil {
		return ArtifactCluster{}, err
	}
	row, err := database.PersistNamedWorker("artifact-recording-worker")
	if err != nil {
		return ArtifactCluster{}, err
	}
	var mirrorRecorder *brine.Recorder
	if mirror {
		mirrorRecorder = rec
	}
	return wireArtifactCluster(ArtifactCluster{
		mirrorRecorder: mirrorRecorder,
		Ctx:            ctx, Namespace: d.store.cluster.Namespace, Clientset: cs,
		DB: database, WorkerRow: row, StoreRoot: d.store.root,
		NodeName: d.pod.Spec.NodeName, live: d, nodeReads: trace,
		Node: &realNode{Root: d.store.root, URL: d.url(), host: d.nodeIP, port: int(d.port),
			store: d.store, ctx: ctx},
	}, d.store.executor)
}

func (in ArtifactCluster) daemonService() string {
	if in.live != nil {
		return liveArtifactDaemonService
	}
	return artifactDaemonService
}

// Removing actual owned discovery makes the recorded node the only route.
// Both the local and live variants use selectorless, fixture-owned slices.
func (in ArtifactCluster) unpublishDaemons() error {
	slices := in.Clientset.DiscoveryV1().EndpointSlices(in.Namespace)
	options := metav1.ListOptions{LabelSelector: "kubernetes.io/service-name=" + in.daemonService()}
	published, err := slices.List(in.Ctx, options)
	if err != nil {
		return err
	}
	if len(published.Items) == 0 {
		return fmt.Errorf("no published daemons to remove")
	}
	for _, slice := range published.Items {
		uid := slice.UID
		if err := slices.Delete(in.Ctx, slice.Name,
			metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); err != nil {
			return err
		}
	}
	remaining, err := slices.List(in.Ctx, options)
	if err != nil {
		return err
	}
	if len(remaining.Items) != 0 {
		return fmt.Errorf("daemon discovery was not empty")
	}
	return nil
}
