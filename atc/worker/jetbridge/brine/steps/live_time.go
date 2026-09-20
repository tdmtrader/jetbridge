package steps

import (
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/event"
	"github.com/concourse/concourse/atc/exec"
	execbuild "github.com/concourse/concourse/atc/exec/build"
	atcworker "github.com/concourse/concourse/atc/worker"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const timeResourceImage = "concourse/time-resource@sha256:5ee278f0e12aada4734b1a56a4ac6f04980236841d9e0b7e0d619e80d4167dc1"

type execLiveTime struct {
	worker            WorkerReady
	pool              atcworker.Pool
	workflow          string
	started, finished time.Time
}

func LiveTimeDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, ExecBuild]("a build using the actual time resource", []string{"jetbridge-db"},
			func(_ brine.Empty, _ brine.Params, rec *brine.Recorder, res brine.Resources) (ExecBuild, error) {
				core, err := newExecCore(res, "some-team", "some-pipeline", atc.Config{Resources: atc.ResourceConfigs{{Name: "some-resource", Type: "time", Source: atc.Source{"location": "UTC"}}}, Jobs: atc.JobConfigs{{Name: "some-job"}}})
				if err != nil {
					return ExecBuild{}, err
				}
				rec.RegisterDisposer(core.Cancel)
				in := ExecBuild{core: core}
				in.core.liveTime, err = in.prepareLiveTime(rec)
				return in, err
			}),
		Transform[ExecBuild, ExecRun]("the time resource runs the {string} workflow", func(in ExecBuild, a Args) (ExecRun, error) { return in.runTimeWorkflow(a.String(0)) }),
		CheckThat[ExecRun]("the time-resource workflow preserves its results", checkTimeWorkflow),
	}
}

func (in ExecBuild) prepareLiveTime(rec *brine.Recorder) (*execLiveTime, error) {
	w, err := newLiveRuntimeWorker(in.core.DB, rec, 3)
	if err != nil {
		return nil, err
	}
	// Three pods let the armed third retry execute under a fault, within
	// the unchanged aggregate CPU, memory and storage caps.

	_, err = in.core.DB.WorkerFactory.SaveWorker(atc.Worker{Name: w.DBWorker.Name(), Platform: "linux", Version: "1.2.3", State: string(db.WorkerStateRunning),
		ResourceTypes: []atc.WorkerResourceType{{Type: "time", Image: timeResourceImage, Version: "pinned-time"}}}, 0)
	if err != nil {
		return nil, err
	}
	workerDB := newExecWorkerDB(in.core.DB)
	factory := atcworker.DefaultFactory{DB: workerDB, K8sClientset: w.Clientset, K8sConfig: &w.Config, K8sExecutor: w.Executor}
	return &execLiveTime{worker: w, pool: atcworker.NewPool(factory, workerDB)}, nil
}

func (in ExecBuild) timePut(id atc.PlanID, valid bool) exec.Step {
	plan := execPutPlan(string(id), "")
	plan.Type, plan.TypeImage, plan.Source, plan.Params = "time", atc.TypeImage{ImageRef: "docker:///" + timeResourceImage}, atc.Source{"location": "UTC"}, nil
	if !valid {
		plan.Source = atc.Source{"interval": "brine-invalid-duration"}
	}
	return in.putStepWithPool(id, plan, in.core.liveTime.pool)
}

func (in ExecBuild) runTimeWorkflow(kind string) (ExecRun, error) {
	g := in.core.liveTime
	if g == nil {
		return ExecRun{}, fmt.Errorf("time workflow must use real resources and pool")
	}
	g.workflow, g.started = kind, time.Now().UTC()
	defer func() { g.finished = time.Now().UTC() }()
	var ok bool
	var err error
	switch kind {
	case "put-get":
		const putID = atc.PlanID("publish")
		ok, err = in.timePut(putID, true).Run(g.worker.Ctx, in.core.State)
		if err == nil && ok {
			plan := execGetPlan("some-resource")
			plan.Type, plan.TypeImage, plan.Source = "time", atc.TypeImage{ImageRef: "docker:///" + timeResourceImage}, atc.Source{"location": "UTC"}
			from := putID
			plan.VersionFrom = &from
			ok, err = in.getStepWithPool("fetch", plan, g.pool).Run(g.worker.Ctx, in.core.State)
		}
	case "retry":
		ok, err = exec.Retry(in.timePut("attempt-1", false), in.timePut("attempt-2", true), in.timePut("attempt-3", true)).Run(g.worker.Ctx, in.core.State)
	default:
		return ExecRun{}, fmt.Errorf("unknown time-resource workflow %q", kind)
	}
	fmt.Printf("actual time-resource workflow %s: ok=%t error=%v\n", kind, ok, err)
	return ExecRun{core: in.core, Ok: ok, Err: err}, nil
}

func checkTimeWorkflow(in ExecRun) error {
	g := in.core.liveTime
	if g == nil || !in.Ok || in.Err != nil {
		return fmt.Errorf("time-resource workflow failed: ok=%t error=%v", in.Ok, in.Err)
	}
	puts, err := in.core.finishes(event.EventTypeFinishPut)
	if err != nil {
		return err
	}
	_, outputs, err := in.core.Build.Resources()
	if err != nil {
		return err
	}
	name, wantPuts := "publish", 1
	if g.workflow == "retry" {
		name, wantPuts = "attempt-2", 2
	}
	if len(outputs) != 1 || outputs[0].Name != name {
		return fmt.Errorf("expected only %s publication, got %v", name, outputs)
	}
	if len(puts) != wantPuts {
		return fmt.Errorf("expected %d actual put finishes, got %v", wantPuts, puts)
	}
	result := puts[len(puts)-1]
	if result.ExitStatus != 0 || string(result.Origin.ID) != name || !reflect.DeepEqual(outputs[0].Version, result.Version) {
		return fmt.Errorf("published version and successful put finish disagree: output=%v finish=%+v", outputs, result)
	}
	stamp, err := time.Parse(time.RFC3339Nano, result.Version["time"])
	if err != nil || len(result.Version) != 1 || stamp.Before(g.started.Add(-5*time.Second)) || stamp.After(g.finished.Add(5*time.Second)) {
		return fmt.Errorf("resource did not generate a current timestamp: version=%v error=%v window=%s..%s", result.Version, err, g.started, g.finished)
	}
	if g.workflow == "retry" {
		if puts[0].ExitStatus != 1 || string(puts[0].Origin.ID) != "attempt-1" {
			return fmt.Errorf("first real put was not refused: %+v", puts[0])
		}
		log, err := in.core.log()
		if err != nil {
			return err
		}
		if !strings.Contains(log, "brine-invalid-duration") || !strings.Contains(log, "parse error:") {
			return fmt.Errorf("first attempt has no actual resource rejection diagnostic: %q", log)
		}
	} else {
		gets, err := in.core.finishes(event.EventTypeFinishGet)
		if err != nil {
			return err
		}
		if len(gets) != 1 || gets[0].ExitStatus != 0 || !reflect.DeepEqual(gets[0].Version, result.Version) {
			return fmt.Errorf("get did not select the put's actual timestamp: put=%v get=%v", result.Version, gets)
		}
		caches, err := in.core.resourceCacheVersions()
		if err != nil {
			return err
		}
		if len(caches) != 1 || !reflect.DeepEqual(caches[0], result.Version) {
			return fmt.Errorf("build cache did not retain the put's actual timestamp: %v", caches)
		}
		artifact, cached, found := in.core.State.ArtifactRepository().ArtifactFor(execbuild.ArtifactName("some-resource"))
		if !found || artifact == nil || cached {
			return fmt.Errorf("actual time artifact missing or unexpectedly cached")
		}
		readFile := func(path string) ([]byte, error) {
			stream, err := atcworker.NewStreamer(compression.NewGzipCompression()).StreamFile(g.worker.Ctx, artifact, path)
			if err != nil {
				return nil, err
			}
			data, err := io.ReadAll(stream)
			closeErr := stream.Close()
			if err != nil {
				return nil, err
			}
			return data, closeErr
		}
		request, err := readFile("input")
		if err != nil {
			return err
		}
		var input struct{ Version atc.Version }
		if err := json.Unmarshal(request, &input); err != nil {
			return err
		}
		if !reflect.DeepEqual(input.Version, result.Version) {
			return fmt.Errorf("actual resource input file lost the dynamic timestamp: %s", request)
		}
		epoch, err := readFile("epoch")
		if err != nil {
			return err
		}
		if string(epoch) != strconv.FormatInt(stamp.Unix(), 10) {
			return fmt.Errorf("time artifact epoch %q disagrees with published timestamp %s", epoch, stamp)
		}
		fmt.Printf("actual time artifact contains dynamic version %s and epoch %s\n", result.Version["time"], epoch)
	}
	pods, err := g.worker.Clientset.CoreV1().Pods(g.worker.Namespace).List(g.worker.Ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	if len(pods.Items) != 2 {
		return fmt.Errorf("workflow created %d pods, expected its two executed steps", len(pods.Items))
	}
	for _, pod := range pods.Items {
		if pod.UID == "" || pod.Spec.NodeName == "" || len(pod.Spec.Containers) != 1 || pod.Spec.Containers[0].Image != timeResourceImage {
			return fmt.Errorf("workflow did not use real pinned time-resource pods")
		}
		status, err := strconv.Atoi(pod.Annotations[exitStatusAnnotation])
		if err != nil {
			return err
		}
		fmt.Printf("actual time-resource pod %s/%s UID %s exit %d\n", g.worker.Namespace, pod.Name, pod.UID, status)
	}
	fmt.Printf("actual time-resource %s published only %s version %v\n", g.workflow, name, result.Version)
	return nil
}
