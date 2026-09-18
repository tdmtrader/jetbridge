package steps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/event"
	"github.com/concourse/concourse/atc/exec"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The first attempt really executes Git; cancellation is armed only after its
// HTTP child is observed. The remaining attempt is an ordinary time-resource
// put. Its initialization event detects entry even if the worker itself
// correctly rejects the already-cancelled context.
func LiveRetryAbortDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, ExecBuild]("a build using actual Git and time resources", []string{"jetbridge-db"},
			func(_ brine.Empty, _ brine.Params, rec *brine.Recorder, res brine.Resources) (ExecBuild, error) {
				return newLiveResourceBuild(rec, res)
			}),
		Transform[ExecBuild, ExecRun]("the build aborts during its first real attempt", func(in ExecBuild, _ Args) (ExecRun, error) { return in.abortRealRetry() }),
		CheckThat[ExecRun]("the aborted retry never enters its remaining attempt", checkAbortedRetry),
	}
}

func (in ExecBuild) abortRealRetry() (out ExecRun, err error) {
	g := in.core.liveGit
	if g == nil || g.serverPID == "" {
		return out, fmt.Errorf("aborted retry requires a real paused Git server")
	}
	// Keep the independent worker context alive for observations and cleanup.
	ctx, cancel := context.WithCancel(g.worker.Ctx)
	defer cancel()
	defer func() { err = errors.Join(err, g.resumeHTTPServer()) }()
	plan := execGetPlan("some-resource")
	plan.Type, plan.TypeImage, plan.Source = "git", atc.TypeImage{ImageRef: "docker:///" + gitResourceImage}, g.source
	version := execVersionOf(g.first)
	plan.Version = &version
	first := in.getStepWithPool("attempt-1", plan, g.pool)
	next := in.liveOutputPut("attempt-2")
	done := make(chan ExecRun, 1)
	joined := false
	go func() {
		ok, runErr := exec.Retry(first, next).Run(ctx, in.core.State)
		done <- ExecRun{core: in.core, Ok: ok, Err: runErr}
	}()
	defer func() {
		cancel()
		if !joined {
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				panic("aborted retry failed to drain")
			}
		}
	}()
	if err := g.awaitHTTPFetch(ctx, done, &joined); err != nil {
		return out, err
	}
	cancel()
	fmt.Printf("aborted actual retry after observing attempt-1 HTTP child PID %s in pod UID %s\n", g.httpPID, g.timedPod.UID)
	select {
	case out = <-done:
		joined = true
	case <-g.worker.Ctx.Done():
		return out, g.worker.Ctx.Err()
	}
	g.stopErr = observeChildStop(g.worker, g.timedPod.Name, g.httpPID)
	fmt.Printf("actual aborted retry: ok=%t error=%v childStop=%v\n", out.Ok, out.Err, g.stopErr)
	return out, nil
}

func checkAbortedRetry(in ExecRun) error {
	g := in.core.liveGit
	if g == nil || g.timedPod == nil || g.httpPID == "" {
		return fmt.Errorf("first real attempt was not observed")
	}
	if in.Ok || !errors.Is(in.Err, context.Canceled) {
		return fmt.Errorf("retry lost the actual cancellation: ok=%t error=%v", in.Ok, in.Err)
	}
	if g.stopErr != nil {
		return g.stopErr
	}
	// Durable engine events witness both positive entry and forbidden entry.
	for _, item := range []struct {
		kind atc.EventType
		want []string
	}{
		{event.EventTypeInitializeGet, []string{"attempt-1"}},
		{event.EventTypeInitializePut, nil},
	} {
		payloads, err := in.core.payloadsOfType(item.kind)
		if err != nil {
			return err
		}
		var ids []string
		for _, payload := range payloads {
			var e struct{ Origin event.Origin }
			if err := json.Unmarshal(payload, &e); err != nil {
				return err
			}
			ids = append(ids, string(e.Origin.ID))
		}
		if len(ids) != len(item.want) || len(ids) == 1 && ids[0] != item.want[0] {
			return fmt.Errorf("aborted retry initialized unexpected %s steps: got %v want %v", item.kind, ids, item.want)
		}
		fmt.Printf("actual aborted retry %s origins: %v\n", item.kind, ids)
	}
	_, outputs, err := in.core.Build.Resources()
	if err != nil {
		return err
	}
	if len(outputs) != 0 || len(in.core.artifactNames()) != 0 {
		return fmt.Errorf("aborted retry published outputs or artifacts: %v", outputs)
	}
	for _, kind := range []atc.EventType{event.EventTypeFinishGet, event.EventTypeFinishPut} {
		finishes, err := in.core.finishes(kind)
		if err != nil {
			return err
		}
		if len(finishes) != 0 {
			return fmt.Errorf("aborted retry reported %s: %v", kind, finishes)
		}
	}
	pods, err := g.worker.Clientset.CoreV1().Pods(g.worker.Namespace).List(g.worker.Ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	if len(pods.Items) != 2 {
		return fmt.Errorf("aborted retry left %d pods, expected server and original resource", len(pods.Items))
	}
	found := false
	for _, pod := range pods.Items {
		if pod.Name == g.timedPod.Name {
			found = pod.UID == g.timedPod.UID && pod.Spec.NodeName != "" && len(pod.Spec.Containers) == 1 && pod.Spec.Containers[0].Image == gitResourceImage
		}
	}
	if !found {
		return fmt.Errorf("aborted retry did not preserve its actual resource pod")
	}
	fmt.Println("actual aborted retry retained attempt-1 pod and published no output; attempt-2 never initialized")
	return nil
}

const liveOutputResource = "step-output"

func newLiveResourceBuild(rec *brine.Recorder, res brine.Resources) (ExecBuild, error) {
	core, err := newExecCore(res, "some-team", "some-pipeline", execPlainPipeline())
	if err != nil {
		return ExecBuild{}, err
	}
	rec.RegisterDisposer(core.Cancel)
	in := ExecBuild{core: core}
	core.liveGit, err = in.prepareLiveGit(rec, 3)
	if err != nil {
		return in, err
	}
	g := core.liveGit
	pipeline := atc.Config{Resources: atc.ResourceConfigs{
		{Name: "some-resource", Type: "git", Source: g.source},
		{Name: liveOutputResource, Type: "time", Source: atc.Source{"location": "UTC"}},
	}, Jobs: atc.JobConfigs{{Name: "some-job"}}}
	core.Pipeline, _, err = core.Team.SavePipeline(atc.PipelineRef{Name: core.Pipeline.Name()}, pipeline, core.Pipeline.ConfigVersion(), false)
	if err != nil {
		return in, err
	}
	_, err = core.DB.WorkerFactory.SaveWorker(atc.Worker{Name: g.worker.DBWorker.Name(), Platform: "linux", Version: "1.2.3", State: string(db.WorkerStateRunning),
		ResourceTypes: []atc.WorkerResourceType{{Type: "git", Image: gitResourceImage, Version: "pinned-git"}, {Type: "time", Image: timeResourceImage, Version: "pinned-time"}}}, 0)
	return in, err
}

func (in ExecBuild) liveOutputPut(id atc.PlanID) exec.Step {
	put := execPutPlan(string(id), "")
	put.Resource, put.Type, put.TypeImage, put.Source, put.Params = liveOutputResource, "time", atc.TypeImage{ImageRef: "docker:///" + timeResourceImage}, atc.Source{"location": "UTC"}, nil
	return in.putStepWithPool(id, put, in.core.liveGit.pool)
}
