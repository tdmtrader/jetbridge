package steps

import (
	"context"
	"errors"
	"fmt"
	"net"
	"reflect"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/event"
	"github.com/concourse/concourse/atc/exec"
	atcworker "github.com/concourse/concourse/atc/worker"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type execLiveHook struct {
	fate              string
	started, finished time.Time
	transportCut      bool
}

func LiveHookDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[ExecBuild, ExecRun]("the on_abort hook guards a real step that {string}",
			func(in ExecBuild, p brine.Params, rec *brine.Recorder) (ExecRun, error) {
				fate, err := paramAt("the on_abort hook guards a real step that {string}", p, 0)
				if err != nil {
					return ExecRun{}, err
				}
				return in.runLiveHook(rec, fate)
			}),
		CheckThat[ExecRun]("the real hook runs only for cancellation", checkLiveHook),
	}
}

func (in ExecBuild) runLiveHook(rec *brine.Recorder, fate string) (out ExecRun, err error) {
	g := in.core.liveGit
	if g == nil {
		return out, fmt.Errorf("hook requires real Git/time resources")
	}
	if fate != "aborts" && fate != "disconnects" && fate != "fails" && fate != "succeeds" {
		return out, fmt.Errorf("unknown hook fate %q", fate)
	}
	h := &execLiveHook{fate: fate, started: time.Now().UTC()}
	in.core.liveHook = h
	defer func() { h.finished = time.Now().UTC() }()
	ctx, cancel := context.WithCancel(g.worker.Ctx)
	defer cancel()
	pool := g.pool
	var route net.Listener
	if fate == "disconnects" {
		config, configErr := liveKubernetesConfig()
		if configErr != nil {
			return out, configErr
		}
		executor, relay, routeErr := liveExecutionRoute(g.worker.Ctx, rec, config)
		if routeErr != nil {
			return out, routeErr
		}
		route = relay
		database := newExecWorkerDB(in.core.DB)
		// Only guarded execution uses the relay. Hook execution and observations
		// keep the verified direct route, so a faulty hook can really publish.
		factory := atcworker.DefaultFactory{DB: database, K8sClientset: g.worker.Clientset, K8sConfig: &g.worker.Config, K8sExecutor: executor}
		pool = atcworker.NewPool(factory, database)
	}
	interrupt := fate == "aborts" || fate == "disconnects"
	if interrupt {
		if _, err := in.pauseGitServer(); err != nil {
			return out, err
		}
		defer func() { err = errors.Join(err, g.resumeHTTPServer()) }()
	}
	g.selected = "first"
	ref := g.first
	if fate == "fails" {
		ref, g.selected = strings.Repeat("0", 40), "missing"
	}
	plan := execGetPlan("some-resource")
	plan.Type, plan.TypeImage, plan.Source = "git", atc.TypeImage{ImageRef: "docker:///" + gitResourceImage}, g.source
	version := execVersionOf(ref)
	plan.Version = &version
	guarded := in.getStepWithPool("guarded", plan, pool)
	hook := in.liveOutputPut("hook")
	done := make(chan ExecRun, 1)
	joined := false
	go func() {
		ok, runErr := exec.OnAbort(guarded, hook).Run(ctx, in.core.State)
		done <- ExecRun{core: in.core, Ok: ok, Err: runErr}
	}()
	defer func() {
		cancel()
		if !joined {
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				panic("real hook failed to drain")
			}
		}
	}()
	if interrupt {
		if err := g.awaitHTTPFetch(ctx, done, &joined); err != nil {
			return out, err
		}
		if fate == "aborts" {
			cancel()
			fmt.Printf("cancelled guarded Git step after observing HTTP child PID %s pod UID %s\n", g.httpPID, g.timedPod.UID)
		} else {
			if err := route.Close(); err != nil {
				return out, err
			}
			state, stateErr := childState(g.worker.Ctx, g.worker, g.timedPod.Name, g.httpPID)
			if stateErr != nil || state == "gone" || state == "Z" || state == "X" {
				return out, fmt.Errorf("guarded child did not survive actual connection cut: state=%s error=%v", state, stateErr)
			}
			h.transportCut = true
			fmt.Printf("cut actual guarded exec relay; direct probe sees HTTP child PID %s state %s pod UID %s\n", g.httpPID, state, g.timedPod.UID)
		}
	}
	select {
	case out = <-done:
		joined = true
	case <-g.worker.Ctx.Done():
		return out, g.worker.Ctx.Err()
	}
	if fate == "aborts" {
		g.stopErr = observeChildStop(g.worker, g.timedPod.Name, g.httpPID)
	}
	if fate == "disconnects" {
		// The lost exec connection is not an abort. Resume the owned peer so the
		// real resource process can finish before namespace disposal.
		if err := g.resumeHTTPServer(); err != nil {
			return out, err
		}
		g.stopErr = observeChildStop(g.worker, g.timedPod.Name, g.httpPID)
	}
	fmt.Printf("actual hook fate %s: ok=%t error=%v childStop=%v\n", fate, out.Ok, out.Err, g.stopErr)
	return out, nil
}

func checkLiveHook(in ExecRun) error {
	g, h := in.core.liveGit, in.core.liveHook
	if g == nil || h == nil {
		return fmt.Errorf("no real hook workflow")
	}
	switch h.fate {
	case "aborts":
		if in.Ok || !errors.Is(in.Err, context.Canceled) {
			return fmt.Errorf("guarded abort lost cancellation: ok=%t error=%v", in.Ok, in.Err)
		}
	case "disconnects":
		if in.Ok || in.Err == nil || errors.Is(in.Err, context.Canceled) || errors.Is(in.Err, context.DeadlineExceeded) || !h.transportCut || !strings.Contains(in.Err.Error(), "exec in pod") {
			return fmt.Errorf("guarded disconnect did not produce a real non-cancellation exec error: ok=%t error=%v cut=%t", in.Ok, in.Err, h.transportCut)
		}
	case "fails":
		if in.Ok || in.Err != nil {
			return fmt.Errorf("guarded resource refusal was not an ordinary failure: ok=%t error=%v", in.Ok, in.Err)
		}
	case "succeeds":
		if !in.Ok || in.Err != nil {
			return fmt.Errorf("guarded success changed: ok=%t error=%v", in.Ok, in.Err)
		}
	}
	wantHook := h.fate == "aborts"
	_, outputs, err := in.core.Build.Resources()
	if err != nil {
		return err
	}
	expected := 0
	if wantHook {
		expected = 1
	}
	if len(outputs) != expected || wantHook && outputs[0].Name != "hook" {
		return fmt.Errorf("hook publication for %s: expected %d hook outputs, got %v", h.fate, expected, outputs)
	}
	puts, err := in.core.finishes(event.EventTypeFinishPut)
	if err != nil {
		return err
	}
	if len(puts) != expected {
		return fmt.Errorf("hook finish count for %s: got %v", h.fate, puts)
	}
	origins, err := in.core.finishes(event.EventTypeInitializeGet)
	if err != nil {
		return err
	}
	if len(origins) != 1 || origins[0].Origin.ID != "guarded" {
		return fmt.Errorf("guarded real step never initialized: %v", origins)
	}
	putOrigins, err := in.core.finishes(event.EventTypeInitializePut)
	if err != nil {
		return err
	}
	if len(putOrigins) != expected || wantHook && putOrigins[0].Origin.ID != "hook" {
		return fmt.Errorf("unexpected hook initialization for %s: %v", h.fate, putOrigins)
	}
	if wantHook {
		result := puts[0]
		stamp, parseErr := time.Parse(time.RFC3339Nano, result.Version["time"])
		if result.Origin.ID != "hook" || result.ExitStatus != 0 || !reflect.DeepEqual(outputs[0].Version, result.Version) || len(result.Version) != 1 || parseErr != nil || stamp.Before(h.started.Add(-5*time.Second)) || stamp.After(h.finished.Add(5*time.Second)) {
			return fmt.Errorf("hook did not publish its actual current timestamp: finish=%+v output=%v parse=%v", result, outputs, parseErr)
		}
		fmt.Printf("actual on_abort hook published generated timestamp %s\n", result.Version["time"])
	}
	if h.fate == "fails" || h.fate == "succeeds" {
		if err := checkLiveGitGet(in); err != nil {
			return err
		}
	} else {
		if g.stopErr != nil {
			return g.stopErr
		}
		gets, err := in.core.finishes(event.EventTypeFinishGet)
		if err != nil {
			return err
		}
		if len(gets) != 0 || len(in.core.artifactNames()) != 0 {
			return fmt.Errorf("interrupted guarded get finished or exposed an artifact")
		}
		if g.timedPod == nil || g.httpPID == "" {
			return fmt.Errorf("interrupted guarded process was not observed")
		}
	}
	pods, err := g.worker.Clientset.CoreV1().Pods(g.worker.Namespace).List(g.worker.Ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	if len(pods.Items) != 2+expected {
		return fmt.Errorf("hook workflow left unexpected pods: %d", len(pods.Items))
	}
	hookPods := 0
	guardedFound := g.timedPod == nil
	for _, pod := range pods.Items {
		if pod.Name == "git-server" {
			continue
		}
		if pod.UID == "" || pod.Spec.NodeName == "" || len(pod.Spec.Containers) != 1 {
			return fmt.Errorf("missing real resource identity")
		}
		if pod.Spec.Containers[0].Image == timeResourceImage {
			hookPods++
			if pod.Annotations[exitStatusAnnotation] != "0" {
				return fmt.Errorf("hook pod did not exit successfully")
			}
		} else if pod.Spec.Containers[0].Image != gitResourceImage {
			return fmt.Errorf("unexpected resource image")
		}
		if g.timedPod != nil && pod.Name == g.timedPod.Name {
			if pod.UID != g.timedPod.UID {
				return fmt.Errorf("guarded resource pod was replaced")
			}
			guardedFound = true
		}
		fmt.Printf("actual hook resource pod %s/%s UID %s image %s\n", g.worker.Namespace, pod.Name, pod.UID, pod.Spec.Containers[0].Image)
	}
	if !guardedFound {
		return fmt.Errorf("original guarded resource pod is missing")
	}
	if hookPods != expected {
		return fmt.Errorf("unexpected time-resource hook pod count %d", hookPods)
	}
	fmt.Printf("actual hook contract holds for %s: hook publications=%d\n", h.fate, len(outputs))
	return nil
}
