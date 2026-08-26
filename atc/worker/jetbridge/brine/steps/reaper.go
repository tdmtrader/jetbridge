package steps

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/gc"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// ReaperDefinitions migrates reaper_test.go — garbage collection of pods and
// the container rows that track them.
//
// The reaper's consumer is the operator whose cluster fills up, and the build
// that wants to resume after a web restart. So the scenarios are written about
// what survives and what disappears, never about which repository method was
// called.

func ReaperDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMapUsing[brine.Empty, ReaperReady](
			"a Kubernetes worker whose reaper is running",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, _ brine.Params, _ *brine.Recorder, res brine.Resources) (ReaperReady, error) {
				database, ok := res.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return ReaperReady{}, fmt.Errorf("jetbridge-db resource is %T", res.Get("jetbridge-db"))
				}
				ctx := context.Background()
				clientset := fake.NewSimpleClientset()
				cfg := jetbridge.NewConfig("test-namespace", "")

				worker, err := database.PersistNamedWorker("k8s-test-namespace")
				if err != nil {
					return ReaperReady{}, err
				}

				logger := lagertest.NewTestLogger("reaper")
				destroyer := gc.NewDestroyer(logger, database.ContainerRepository, database.VolumeRepository)
				reaper := jetbridge.NewReaper(logger, clientset, cfg, database.ContainerRepository, destroyer)
				reaper.SetBuildLookup(database.BuildFactory)

				metric.Metrics.ContainersDeleted.Delta() // reset the counter

				return ReaperReady{
					DB: database, Worker: worker, Clientset: clientset,
					Config: cfg, Reaper: reaper, Ctx: ctx, BuildLookup: true,
				}, nil
			},
		),

		brine.DefineMap[ReaperReady, ReaperReady](
			"the reaper cannot tell which builds are running",
			func(in ReaperReady, _ brine.Params, _ *brine.Recorder) (ReaperReady, error) {
				// A reaper with no build lookup must not guess. Retaining is
				// the safe answer: deleting a pod whose build is still running
				// loses the build.
				logger := lagertest.NewTestLogger("reaper")
				destroyer := gc.NewDestroyer(logger, in.DB.ContainerRepository, in.DB.VolumeRepository)
				in.Reaper = jetbridge.NewReaper(logger, in.Clientset, in.Config, in.DB.ContainerRepository, destroyer)
				in.BuildLookup = false
				return in, nil
			},
		),

		brine.DefineMap[ReaperReady, ReaperReady](
			"a container {string} exists on this worker",
			func(in ReaperReady, p brine.Params, _ *brine.Recorder) (ReaperReady, error) {
				handle, ok := p.GetString(0)
				if !ok {
					return ReaperReady{}, fmt.Errorf("expected a handle parameter")
				}
				_, err := in.createContainer(in.Worker, handle, false)
				return in, err
			},
		),

		brine.DefineMap[ReaperReady, ReaperReady](
			"a container {string} on this worker is being destroyed",
			func(in ReaperReady, p brine.Params, _ *brine.Recorder) (ReaperReady, error) {
				handle, ok := p.GetString(0)
				if !ok {
					return ReaperReady{}, fmt.Errorf("expected a handle parameter")
				}
				_, err := in.createContainer(in.Worker, handle, true)
				return in, err
			},
		),

		brine.DefineMap[ReaperReady, ReaperReady](
			"a container {string} on another worker is being destroyed",
			func(in ReaperReady, p brine.Params, _ *brine.Recorder) (ReaperReady, error) {
				handle, ok := p.GetString(0)
				if !ok {
					return ReaperReady{}, fmt.Errorf("expected a handle parameter")
				}
				other, err := in.DB.PersistNamedWorker("k8s-other-namespace")
				if err != nil {
					return ReaperReady{}, err
				}
				_, err = in.createContainer(other, handle, true)
				return in, err
			},
		),

		brine.DefineMap[ReaperReady, ReaperReady](
			"a pod {string} is running for it",
			func(in ReaperReady, p brine.Params, _ *brine.Recorder) (ReaperReady, error) {
				name, ok := p.GetString(0)
				if !ok {
					return ReaperReady{}, fmt.Errorf("expected a pod name parameter")
				}
				return in, in.createPod(name, name, "", "")
			},
		),

		brine.DefineMap[ReaperReady, ReaperReady](
			"a pod {string} is running, labelled with the handle {string}",
			func(in ReaperReady, p brine.Params, _ *brine.Recorder) (ReaperReady, error) {
				name, _ := p.GetString(0)
				handle, ok := p.GetString(1)
				if !ok {
					return ReaperReady{}, fmt.Errorf("expected a pod name and a handle")
				}
				return in, in.createPod(name, handle, "", "")
			},
		),

		// GC-02's subject: a pod whose step has finished, carrying the
		// exit-status annotation the runtime writes for crash recovery.
		brine.DefineMap[ReaperReady, ReaperReady](
			"a finished step left a pod {string} behind, for a build that is {string}",
			func(in ReaperReady, p brine.Params, _ *brine.Recorder) (ReaperReady, error) {
				name, _ := p.GetString(0)
				state, ok := p.GetString(1)
				if !ok {
					return ReaperReady{}, fmt.Errorf("expected a pod name and a build state")
				}
				// A finished step's pod always has a container row behind it.
				// Without one the pod is an orphan and the reaper destroys it
				// through a different path entirely — which is what this
				// scenario looked like it was testing until the container was
				// added.
				buildID, handle, err := in.persistBuildAndContainer(name, state == "still running")
				if err != nil {
					return ReaperReady{}, err
				}
				return in, in.createPod(name, handle, buildID, string(db.ContainerTypeTask))
			},
		),

		brine.DefineMap[ReaperReady, ReaperReady](
			"a finished check left a pod {string} behind, with no build to resume",
			func(in ReaperReady, p brine.Params, _ *brine.Recorder) (ReaperReady, error) {
				name, ok := p.GetString(0)
				if !ok {
					return ReaperReady{}, fmt.Errorf("expected a pod name parameter")
				}
				return in, in.createPod(name, name, "", string(db.ContainerTypeCheck))
			},
		),

		brine.DefineMap[ReaperReady, ReaperOutcome](
			"the reaper runs",
			func(in ReaperReady, _ brine.Params, _ *brine.Recorder) (ReaperOutcome, error) {
				err := in.Reaper.Run(in.Ctx)
				return ReaperOutcome{Ready: in, Err: err}, nil
			},
		),

		brine.DefineMap[ReaperOutcome, ReaperOutcome](
			"the reaper runs again",
			func(in ReaperOutcome, _ brine.Params, _ *brine.Recorder) (ReaperOutcome, error) {
				in.Err = in.Ready.Reaper.Run(in.Ready.Ctx)
				return in, nil
			},
		),

		brine.DefineCheck[ReaperOutcome](
			"the reaper completes without error",
			func(in ReaperOutcome, _ brine.Params, _ *brine.Recorder) error {
				if in.Err != nil {
					return fmt.Errorf("the reaper failed: %v", in.Err)
				}
				return nil
			},
		),

		brine.DefineCheck[ReaperOutcome](
			"the pod {string} is still there",
			func(in ReaperOutcome, p brine.Params, _ *brine.Recorder) error {
				name, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a pod name parameter")
				}
				exists, err := in.Ready.podExists(name)
				if err != nil {
					return err
				}
				if !exists {
					return fmt.Errorf("expected pod %q to survive the reaper, it was deleted", name)
				}
				return nil
			},
		),

		brine.DefineCheck[ReaperOutcome](
			"the pod {string} is gone",
			func(in ReaperOutcome, p brine.Params, _ *brine.Recorder) error {
				name, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a pod name parameter")
				}
				exists, err := in.Ready.podExists(name)
				if err != nil {
					return err
				}
				if exists {
					return fmt.Errorf("expected pod %q to be reaped, it is still there", name)
				}
				return nil
			},
		),

		brine.DefineCheck[ReaperOutcome](
			"the container {string} is still tracked as {string}",
			func(in ReaperOutcome, p brine.Params, _ *brine.Recorder) error {
				handle, _ := p.GetString(0)
				want, ok := p.GetString(1)
				if !ok {
					return fmt.Errorf("expected a handle and a state")
				}
				row, found, err := in.Ready.containerRow(handle)
				if err != nil {
					return err
				}
				if !found {
					return fmt.Errorf("expected container %q to still be tracked, its row is gone", handle)
				}
				if row.State != want {
					return fmt.Errorf("expected container %q to be %q, it is %q", handle, want, row.State)
				}
				return nil
			},
		),

		brine.DefineCheck[ReaperOutcome](
			"the container {string} is no longer tracked",
			func(in ReaperOutcome, p brine.Params, _ *brine.Recorder) error {
				handle, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a handle parameter")
				}
				_, found, err := in.Ready.containerRow(handle)
				if err != nil {
					return err
				}
				if found {
					return fmt.Errorf("expected container %q to be forgotten, its row is still there", handle)
				}
				return nil
			},
		),

		// The signal that a container's pod has vanished: the scheduler uses
		// missing_since to decide when to give up on it.
		brine.DefineCheck[ReaperOutcome](
			"the container {string} is marked as missing",
			func(in ReaperOutcome, p brine.Params, _ *brine.Recorder) error {
				handle, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a handle parameter")
				}
				row, found, err := in.Ready.containerRow(handle)
				if err != nil {
					return err
				}
				if !found {
					return fmt.Errorf("expected container %q to exist", handle)
				}
				if !row.MissingSince.Valid {
					return fmt.Errorf("expected container %q to be marked missing, it is not", handle)
				}
				return nil
			},
		),

		brine.DefineCheck[ReaperOutcome](
			"the container {string} is not marked as missing",
			func(in ReaperOutcome, p brine.Params, _ *brine.Recorder) error {
				handle, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a handle parameter")
				}
				row, found, err := in.Ready.containerRow(handle)
				if err != nil {
					return err
				}
				if !found {
					return fmt.Errorf("expected container %q to exist", handle)
				}
				if row.MissingSince.Valid {
					return fmt.Errorf("expected container %q not to be marked missing, it is", handle)
				}
				return nil
			},
		),
	}
}

func (r ReaperReady) createContainer(worker db.Worker, handle string, destroying bool) (db.CreatedContainer, error) {
	creating, err := worker.CreateContainer(
		db.NewFixedHandleContainerOwner(handle),
		db.ContainerMetadata{Type: db.ContainerTypeTask},
	)
	if err != nil {
		return nil, fmt.Errorf("create container %q: %w", handle, err)
	}
	created, err := creating.Created()
	if err != nil {
		return nil, fmt.Errorf("mark %q created: %w", handle, err)
	}
	if destroying {
		if _, err := created.Destroying(); err != nil {
			return nil, fmt.Errorf("mark %q destroying: %w", handle, err)
		}
	}
	return created, nil
}

func (r ReaperReady) createPod(name, handle, buildID, ctype string) error {
	labels := map[string]string{"concourse.ci/worker": "k8s-" + r.Config.Namespace}
	if handle != "" {
		labels["concourse.ci/handle"] = handle
	}
	if ctype != "" {
		labels["concourse.ci/type"] = ctype
	}
	if buildID != "" {
		labels["concourse.ci/build-id"] = buildID
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: r.Config.Namespace, Labels: labels,
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	if ctype != "" || buildID != "" {
		pod.ObjectMeta.Annotations = map[string]string{"concourse.ci/exit-status": "0"}
	}
	_, err := r.Clientset.CoreV1().Pods(r.Config.Namespace).Create(r.Ctx, pod, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("create pod %q: %w", name, err)
	}
	return nil
}

// persistBuildAndContainer creates a started build, a container owned by one
// of its steps, and returns the build id and the container handle — the shape
// a real finished step leaves behind.
func (r ReaperReady) persistBuildAndContainer(name string, running bool) (string, string, error) {
	team, err := r.DB.TeamFactory.CreateTeam(atc.Team{Name: "reaper-" + name})
	if err != nil {
		return "", "", fmt.Errorf("create team: %w", err)
	}
	pipeline, _, err := team.SavePipeline(
		atc.PipelineRef{Name: "pipeline-" + name},
		atc.Config{Jobs: atc.JobConfigs{{Name: "some-job"}}}, 0, false)
	if err != nil {
		return "", "", fmt.Errorf("save pipeline: %w", err)
	}
	job, found, err := pipeline.Job("some-job")
	if err != nil || !found {
		return "", "", fmt.Errorf("find job: %v (found=%v)", err, found)
	}
	build, err := job.CreateBuild("reaper-test")
	if err != nil {
		return "", "", fmt.Errorf("create build: %w", err)
	}
	if _, err := build.Start(atc.Plan{ID: atc.PlanID("plan-" + name)}); err != nil {
		return "", "", fmt.Errorf("start build: %w", err)
	}

	creating, err := r.Worker.CreateContainer(
		db.NewBuildStepContainerOwner(build.ID(), atc.PlanID("step-"+name), team.ID()),
		db.ContainerMetadata{Type: db.ContainerTypeTask},
	)
	if err != nil {
		return "", "", fmt.Errorf("create build container: %w", err)
	}
	created, err := creating.Created()
	if err != nil {
		return "", "", fmt.Errorf("mark build container created: %w", err)
	}

	if !running {
		if err := build.Finish(db.BuildStatusSucceeded); err != nil {
			return "", "", fmt.Errorf("finish build: %w", err)
		}
	}
	return fmt.Sprintf("%d", build.ID()), created.Handle(), nil
}

func (r ReaperReady) podExists(name string) (bool, error) {
	_, err := r.Clientset.CoreV1().Pods(r.Config.Namespace).Get(r.Ctx, name, metav1.GetOptions{})
	if err == nil {
		return true, nil
	}
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	return false, fmt.Errorf("get pod %q: %w", name, err)
}

type containerRowState struct {
	State        string
	WorkerName   string
	MissingSince sql.NullTime
}

func (r ReaperReady) containerRow(handle string) (containerRowState, bool, error) {
	var row containerRowState
	err := r.DB.Conn.QueryRow(
		`SELECT state, worker_name, missing_since FROM containers WHERE handle = $1`, handle,
	).Scan(&row.State, &row.WorkerName, &row.MissingSince)
	if errors.Is(err, sql.ErrNoRows) {
		return row, false, nil
	}
	if err != nil {
		return row, false, fmt.Errorf("read container row %q: %w", handle, err)
	}
	return row, true, nil
}
