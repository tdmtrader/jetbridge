package steps

import (
	"context"
	"fmt"
	"io"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	atcworker "github.com/concourse/concourse/atc/worker"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type execCachedResource struct {
	fixture execPoolFixture
	pool    atcworker.Pool
	ref     string
}

func (in ExecBuild) prepareCachedResource(rec *brine.Recorder, res brine.Resources, ref string) (*execCachedResource, error) {
	if in.core.cached != nil {
		return nil, fmt.Errorf("cached get must not use resource runtime substitutes")
	}
	ctx, cancel := context.WithTimeout(in.core.Ctx, 20*time.Second)
	defer cancel()
	f, err := newExecPoolFixture(ctx, rec, in.core.DB, res)
	if err != nil {
		return nil, err
	}
	_, err = in.core.DB.WorkerFactory.SaveWorker(atc.Worker{
		Name: execTaskWorkerName, Platform: "linux", Version: "1.2.3", State: string(db.WorkerStateRunning),
		ResourceTypes: []atc.WorkerResourceType{{Type: "some-base-type", Image: "busybox:1.37.0", Version: "cache-source-v1"}},
	}, 0)
	if err != nil {
		return nil, err
	}
	producer, err := in.core.Team.CreateOneOffBuild()
	if err != nil {
		return nil, err
	}
	plan := execGetPlan("some-resource")
	cache, err := in.core.Caches.FindOrCreateResourceCache(db.ForBuild(producer.ID()), plan.Type, execVersionOf(ref), plan.Source, plan.Params, nil)
	if err != nil {
		return nil, err
	}
	if err := in.core.Caches.UpdateResourceCacheMetadata(cache, atc.Metadata{{Name: "ref", Value: ref}}); err != nil {
		return nil, err
	}
	creating, err := in.core.DB.VolumeRepository.CreateVolume(in.core.Team.ID(), execTaskWorkerName, db.VolumeTypeResource)
	if err != nil {
		return nil, err
	}
	volume, err := creating.Created()
	if err != nil {
		return nil, err
	}
	association, err := volume.InitializeResourceCache(cache)
	if err != nil {
		return nil, err
	}
	if association == nil {
		return nil, fmt.Errorf("real cache volume was not associated with its worker")
	}
	daemon, err := startExecArtifactDaemon(rec)
	if err != nil {
		return nil, err
	}
	if err := writeExecArtifact(ctx, daemon, volume.Handle(), map[string]string{"version": ref}); err != nil {
		return nil, err
	}
	_, port, err := hostPortOfURL(daemon.URL)
	if err != nil {
		return nil, err
	}
	address, err := discoveryIPv4()
	if err != nil {
		return nil, err
	}
	f.Config.ArtifactDaemonHostPath = daemon.Root
	f.Config.ArtifactDaemonPort = port
	f.Config.ArtifactDaemonService = "artifact-daemon"
	ready, tcp, endpointPort := true, corev1.ProtocolTCP, int32(port)
	slice, err := f.Cluster.Clientset.DiscoveryV1().EndpointSlices(f.Config.Namespace).Create(ctx, &discoveryv1.EndpointSlice{
		ObjectMeta:  metav1.ObjectMeta{Name: "exec-cache", Labels: map[string]string{discoveryv1.LabelServiceName: f.Config.ArtifactDaemonService}},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints:   []discoveryv1.Endpoint{{Addresses: []string{address}, Conditions: discoveryv1.EndpointConditions{Ready: &ready}}},
		Ports:       []discoveryv1.EndpointPort{{Port: &endpointPort, Protocol: &tcp}},
	}, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}
	registerAPICleanup(rec, "exec cache EndpointSlice", func(ctx context.Context) error {
		return f.Cluster.Clientset.DiscoveryV1().EndpointSlices(f.Config.Namespace).Delete(ctx, slice.Name,
			metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &slice.UID}})
	})
	client := jetbridge.NewDaemonClient(lagertest.NewTestLogger("exec-cache"), f.Cluster.Clientset, f.Config.Namespace, f.Config.ArtifactDaemonService, port, nil)
	pool, err := f.pool(nil, client)
	if err != nil {
		return nil, err
	}
	// Witness production lookup and actual bytes before the get runs.
	actual, found, err := pool.FindResourceCacheVolumeOnWorker(ctx, cache, atcworker.Spec{TeamID: in.core.Team.ID()}, execTaskWorkerName, time.Now())
	if err != nil || !found || actual == nil || actual.Handle() != volume.Handle() {
		return nil, fmt.Errorf("real cache lookup: found=%t volume=%v error=%v", found, actual, err)
	}
	result := &execCachedResource{fixture: f, pool: pool, ref: ref}
	if err := result.verify(ctx, actual); err != nil {
		return nil, err
	}
	fmt.Printf("real cached get holds version %s in volume %s from build %d\n", ref, actual.Handle(), producer.ID())
	return result, nil
}

func (in ExecBuild) runCachedGet(plan atc.GetPlan) (ExecRun, error) {
	ctx, cancel := context.WithTimeout(in.core.Ctx, 8*time.Second)
	defer cancel()
	ok, err := in.getStepWithPool("get-1", plan, in.core.cached.pool).Run(ctx, in.core.State)
	return ExecRun{core: in.core, Ok: ok, Err: err}, nil
}

func (c *execCachedResource) verify(parent context.Context, artifact runtime.Artifact) error {
	if artifact == nil {
		return fmt.Errorf("cached get registered no readable artifact")
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	stream, err := atcworker.NewStreamer(compression.NewGzipCompression()).StreamFile(ctx, artifact, "version")
	if err != nil {
		return fmt.Errorf("read actual cached version: %w", err)
	}
	body, readErr := io.ReadAll(stream)
	closeErr := stream.Close()
	if readErr != nil || closeErr != nil || string(body) != c.ref {
		return fmt.Errorf("cached bytes=%q want=%q read=%v close=%v", body, c.ref, readErr, closeErr)
	}
	pods, err := c.fixture.Cluster.Clientset.CoreV1().Pods(c.fixture.Config.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	if len(pods.Items) != 0 {
		return fmt.Errorf("cached get created %d resource pods", len(pods.Items))
	}
	fmt.Printf("read cached version %s through the real daemon without a resource pod\n", c.ref)
	return nil
}
