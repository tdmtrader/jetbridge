package steps

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/event"
	execbuild "github.com/concourse/concourse/atc/exec/build"
	atcworker "github.com/concourse/concourse/atc/worker"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// The server and resource run in different real pods. Git's HTTP transport serves
// actual commits over its normal protocol; no resource replies are synthesized.
type execLiveGit struct {
	worker                WorkerReady
	pool                  atcworker.Pool
	source                atc.Source
	first, last, selected string
	timeout               time.Duration
	serverPID, httpPID    string
	timedPod              *corev1.Pod
	stopErr               error
}

func LiveGetStepDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, ExecBuild]("a build fetching from a real Git repository", []string{"jetbridge-db"},
			func(_ brine.Empty, _ brine.Params, rec *brine.Recorder, res brine.Resources) (ExecBuild, error) {
				core, err := newExecCore(res, "some-team", "some-pipeline", execPlainPipeline())
				if err != nil {
					return ExecBuild{}, err
				}
				rec.RegisterDisposer(core.Cancel)
				in := ExecBuild{core: core}
				in.core.liveGit, err = in.prepareLiveGit(rec)
				return in, err
			}),
		Transform[ExecBuild, ExecBuild]("the Git server stops responding", func(in ExecBuild, _ Args) (ExecBuild, error) { return in.pauseGitServer() }),
		Transform[ExecBuild, ExecRun]("the Git get selects the {string} commit",
			func(in ExecBuild, a Args) (ExecRun, error) { return in.runLiveGitGet(a.String(0)) }),
		CheckThat[ExecRun]("the build preserves the Git get outcome", checkLiveGitGet),
	}
}

func (in ExecBuild) prepareLiveGit(rec *brine.Recorder, podLimits ...int64) (*execLiveGit, error) {
	if in.core.cached != nil {
		return nil, fmt.Errorf("live get must not use runtime substitutes or a preloaded cache")
	}
	w, err := newLiveRuntimeWorker(in.core.DB, rec, podLimits...)
	if err != nil {
		return nil, err
	}
	// Ordinary gets need two pods; callers may reserve room for another real step.
	script := "git init -q -b main /tmp/source; git -C /tmp/source config user.name Brine; git -C /tmp/source config user.email brine@example.invalid; printf 'first\\n' > /tmp/source/payload.txt; git -C /tmp/source add payload.txt; git -C /tmp/source -c commit.gpgsign=false commit -qm first; printf 'second\\n' > /tmp/source/payload.txt; git -C /tmp/source -c commit.gpgsign=false commit -qam second; git clone -q --bare /tmp/source /srv/repository.git; git --git-dir=/srv/repository.git update-server-info; git --git-dir=/srv/repository.git rev-list --reverse main > /srv/commits"
	grace := int64(1)
	_, err = w.Clientset.CoreV1().Pods(w.Namespace).Create(w.Ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "git-server"},
		Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever, TerminationGracePeriodSeconds: &grace,
			Volumes: []corev1.Volume{{Name: "repository", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}},
			InitContainers: []corev1.Container{{Name: "seed", Image: gitResourceImage, Command: []string{"sh", "-ec", script},
				VolumeMounts: []corev1.VolumeMount{{Name: "repository", MountPath: "/srv"}}}},
			Containers: []corev1.Container{{
				Name: "main", Image: "busybox:1.37.0", Command: []string{"sh", "-ec", "httpd -f -p 8080 -h /srv & child=$!; printf '%s\\n' \"$child\" > /tmp/git-httpd.pid; trap 'kill -TERM \"$child\"; exit 0' TERM INT; wait \"$child\""},
				VolumeMounts:   []corev1.VolumeMount{{Name: "repository", MountPath: "/srv", ReadOnly: true}},
				ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/repository.git/info/refs", Port: intstr.FromInt32(8080)}}, PeriodSeconds: 1},
			}},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}
	pod, err := awaitLivePod(w.Ctx, liveKubernetes{Clientset: w.Clientset, Namespace: w.Namespace}, "git-server")
	if err != nil {
		logs, logErr := w.Clientset.CoreV1().Pods(w.Namespace).GetLogs("git-server", &corev1.PodLogOptions{Container: "main"}).DoRaw(w.Ctx)
		return nil, fmt.Errorf("Git server startup: %w; logs=%s logError=%v", err, logs, logErr)
	}
	if net.ParseIP(pod.Status.PodIP) == nil {
		return nil, fmt.Errorf("Git server has no real pod IP")
	}
	refs, err := gitFixtureExec(w, pod.Name, []string{"cat", "/srv/commits"})
	if err != nil {
		return nil, err
	}
	commits := strings.Fields(refs)
	if len(commits) != 2 || len(commits[0]) != 40 || len(commits[1]) != 40 || commits[0] == commits[1] {
		return nil, fmt.Errorf("expected two distinct actual Git commits: %q", refs)
	}
	source := atc.Source{"uri": "http://" + net.JoinHostPort(pod.Status.PodIP, "8080") + "/repository.git", "branch": "main"}
	// Read the protocol from a real client before handing the URL to the SUT.
	remote, err := gitFixtureExec(w, pod.Name, []string{"wget", "-qO-", source["uri"].(string) + "/info/refs"})
	if err != nil {
		return nil, err
	}
	remoteFields := strings.Fields(remote)
	if len(remoteFields) != 2 || remoteFields[0] != commits[1] {
		return nil, fmt.Errorf("Git HTTP server did not serve its actual main ref: %q", remote)
	}
	pipeline := atc.Config{Resources: atc.ResourceConfigs{{Name: "some-resource", Type: "git", Source: source}}, Jobs: atc.JobConfigs{{Name: "some-job"}}}
	in.core.Pipeline, _, err = in.core.Team.SavePipeline(atc.PipelineRef{Name: in.core.Pipeline.Name()}, pipeline, in.core.Pipeline.ConfigVersion(), false)
	if err != nil {
		return nil, err
	}
	_, err = in.core.DB.WorkerFactory.SaveWorker(atc.Worker{Name: w.DBWorker.Name(), Platform: "linux", Version: "1.2.3", State: string(db.WorkerStateRunning),
		ResourceTypes: []atc.WorkerResourceType{{Type: "git", Image: gitResourceImage, Version: "pinned-git"}}}, 0)
	if err != nil {
		return nil, err
	}
	database := in.core.DB
	workerDB := newExecWorkerDB(database)
	factory := atcworker.DefaultFactory{DB: workerDB, K8sClientset: w.Clientset, K8sConfig: &w.Config, K8sExecutor: w.Executor}
	fmt.Printf("actual Git server %s/%s UID %s serves commits %s %s\n", w.Namespace, pod.Name, pod.UID, commits[0], commits[1])
	return &execLiveGit{worker: w, pool: atcworker.NewPool(factory, workerDB), source: source, first: commits[0], last: commits[1]}, nil
}

func (in ExecBuild) runLiveGitGet(selection string) (ExecRun, error) {
	g := in.core.liveGit
	if g == nil {
		return ExecRun{}, fmt.Errorf("live Git build is not prepared")
	}
	ref := g.first
	if selection == "missing" {
		ref = strings.Repeat("0", 40)
	} else if selection != "first" {
		return ExecRun{}, fmt.Errorf("unknown Git commit selection %q", selection)
	}
	g.selected = selection
	plan := execGetPlan("some-resource")
	plan.Type, plan.TypeImage, plan.Source = "git", atc.TypeImage{ImageRef: "docker:///" + gitResourceImage}, g.source
	version := execVersionOf(ref)
	plan.Version = &version
	if g.timeout > 0 {
		return in.runTimedGitGet(plan)
	}
	ok, err := in.getStepWithPool("get-1", plan, g.pool).Run(g.worker.Ctx, in.core.State)
	fmt.Printf("actual Git build get selected %s ref %s: ok=%t error=%v\n", selection, ref, ok, err)
	return ExecRun{core: in.core, Ok: ok, Err: err}, nil
}

func checkLiveGitGet(in ExecRun) error {
	g := in.core.liveGit
	if g == nil {
		return fmt.Errorf("no actual Git build")
	}
	if in.Err != nil {
		return fmt.Errorf("Git get errored: %w", in.Err)
	}
	finishes, err := in.core.finishes(event.EventTypeFinishGet)
	if err != nil {
		return err
	}
	if g.timeout > 0 {
		return checkTimedGitGet(in, finishes)
	}
	if len(finishes) != 1 {
		return fmt.Errorf("expected one real get finish, got %d", len(finishes))
	}
	pods, err := g.worker.Clientset.CoreV1().Pods(g.worker.Namespace).List(g.worker.Ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	var resourcePods []corev1.Pod
	for _, pod := range pods.Items {
		if pod.Name != "git-server" {
			resourcePods = append(resourcePods, pod)
		}
	}
	if len(resourcePods) != 1 {
		return fmt.Errorf("expected one executed Git resource pod, got %d", len(resourcePods))
	}
	pod := resourcePods[0]
	if pod.UID == "" || pod.Spec.NodeName == "" || len(pod.Spec.Containers) != 1 || pod.Spec.Containers[0].Image != gitResourceImage {
		return fmt.Errorf("Git get did not use the actual pinned resource pod")
	}
	status, err := strconv.Atoi(pod.Annotations[exitStatusAnnotation])
	if err != nil || status != finishes[0].ExitStatus {
		return fmt.Errorf("get finish exit %d differs from actual resource pod %s status %q: %v", finishes[0].ExitStatus, pod.Name, pod.Annotations[exitStatusAnnotation], err)
	}
	fmt.Printf("actual Git get pod %s/%s UID %s recorded exit %d; build finish agrees\n", g.worker.Namespace, pod.Name, pod.UID, status)
	artifact, fromCache, found := in.core.State.ArtifactRepository().ArtifactFor(execbuild.ArtifactName("some-resource"))
	if g.selected == "missing" {
		if in.Ok || finishes[0].ExitStatus == 0 || found {
			return fmt.Errorf("missing Git commit must fail, finish nonzero and publish no artifact: ok=%t finish=%+v artifact=%t", in.Ok, finishes[0], found)
		}
		log, err := in.core.log()
		if err != nil {
			return err
		}
		if !strings.Contains(log, strings.Repeat("0", 40)) {
			return fmt.Errorf("missing-commit diagnostic does not name the requested ref: %q", log)
		}
		return nil
	}
	if !in.Ok || finishes[0].ExitStatus != 0 || finishes[0].Version["ref"] != g.first {
		return fmt.Errorf("get finish did not preserve pinned Git commit %s: ok=%t finish=%+v", g.first, in.Ok, finishes[0])
	}
	refs, err := in.core.cachedVersions()
	if err != nil {
		return err
	}
	if len(refs) != 1 || refs[0] != g.first {
		return fmt.Errorf("build cache refs %v, want only %s", refs, g.first)
	}
	if !found || artifact == nil || fromCache {
		return fmt.Errorf("Git artifact missing, nil or falsely cached: found=%t nil=%t fromCache=%t", found, artifact == nil, fromCache)
	}
	// Read the artifact through the same production streamer the next step uses.
	stream, err := atcworker.NewStreamer(compression.NewGzipCompression()).StreamFile(g.worker.Ctx, artifact, "payload.txt")
	if err != nil {
		return err
	}
	body, readErr := io.ReadAll(stream)
	closeErr := stream.Close()
	if readErr != nil || closeErr != nil || string(body) != "first\n" {
		return fmt.Errorf("pinned artifact bytes=%q read=%v close=%v", body, readErr, closeErr)
	}
	fmt.Printf("actual Git artifact streamed first commit %s (not branch head %s)\n", g.first, g.last)
	return nil
}

func (in ExecBuild) pauseGitServer() (ExecBuild, error) {
	g := in.core.liveGit
	if g == nil {
		return in, fmt.Errorf("Git server must be real before it is paused")
	}
	pid, err := gitFixtureExec(g.worker, "git-server", []string{"sh", "-ec", "pid=$(cat /tmp/git-httpd.pid)\n[ \"$pid\" -gt 1 ]\n[ \"$(cat \"/proc/$pid/comm\")\" = httpd ]\nkill -STOP \"$pid\"\nprintf '%s' \"$pid\""})
	if err != nil {
		return in, err
	}
	g.serverPID = strings.TrimSpace(pid)
	state, err := childState(g.worker.Ctx, g.worker, "git-server", g.serverPID)
	if err != nil || state != "T" {
		return in, fmt.Errorf("Git HTTP server was not physically stopped: PID=%s state=%s error=%v", g.serverPID, state, err)
	}
	g.timeout = 15 * time.Second
	fmt.Printf("paused actual Git HTTP server %s/git-server PID %s state %s\n", g.worker.Namespace, g.serverPID, state)
	return in, nil
}

func (in ExecBuild) runTimedGitGet(plan atc.GetPlan) (out ExecRun, err error) {
	g := in.core.liveGit
	plan.Timeout = g.timeout.String()
	ctx, cancel := context.WithCancel(g.worker.Ctx)
	defer cancel()
	defer func() { err = errors.Join(err, g.resumeHTTPServer()) }()
	done := make(chan ExecRun, 1)
	waited := false
	defer func() {
		cancel()
		if !waited {
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				panic("timed Git get failed to drain")
			}
		}
	}()
	go func() {
		ok, runErr := in.getStepWithPool("get-1", plan, g.pool).Run(ctx, in.core.State)
		done <- ExecRun{core: in.core, Ok: ok, Err: runErr}
	}()
	if observeErr := g.awaitHTTPFetch(ctx, done, &waited); observeErr != nil {
		return out, observeErr
	}

	select {
	case out = <-done:
		waited = true
	case <-ctx.Done():
		return out, ctx.Err()
	}
	g.stopErr = observeChildStop(g.worker, g.timedPod.Name, g.httpPID)
	fmt.Printf("actual timed Git get: ok=%t error=%v childStop=%v\n", out.Ok, out.Err, g.stopErr)
	return out, nil
}

func checkTimedGitGet(in ExecRun, finishes []execFinish) error {
	g := in.core.liveGit
	if g.timedPod == nil || g.httpPID == "" {
		return fmt.Errorf("no real Git HTTP fetch observed before timeout")
	}
	if in.Ok || len(finishes) != 0 || len(in.core.artifactNames()) != 0 {
		return fmt.Errorf("timed-out Git get succeeded, finished or exposed artifacts")
	}
	if g.stopErr != nil {
		return g.stopErr
	}
	pod, err := g.worker.Clientset.CoreV1().Pods(g.worker.Namespace).Get(g.worker.Ctx, g.timedPod.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if pod.UID != g.timedPod.UID {
		return fmt.Errorf("timed-out Git resource pod was replaced")
	}
	return nil
}

// awaitHTTPFetch observes the real resource process; it never fabricates a step result.
func (g *execLiveGit) awaitHTTPFetch(ctx context.Context, done <-chan ExecRun, waited *bool) error {
	// An actual git-remote-http process is the premise: pod startup alone
	// cannot pass this observation. The peer is known to be paused above.
	for g.timedPod == nil {
		select {
		case out := <-done:
			*waited = true
			return fmt.Errorf("get finished before a real HTTP fetch was observed: ok=%t error=%v", out.Ok, out.Err)
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		pods, listErr := g.worker.Clientset.CoreV1().Pods(g.worker.Namespace).List(ctx, metav1.ListOptions{})
		if listErr != nil {
			return listErr
		}
		for _, pod := range pods.Items {
			if pod.Name == "git-server" || pod.Status.Phase != corev1.PodRunning {
				continue
			}
			probeCtx, stop := context.WithTimeout(ctx, time.Second)
			var stdout bytes.Buffer
			probeErr := g.worker.Executor.ExecInPod(probeCtx, g.worker.Namespace, pod.Name, "main", []string{"sh", "-c", "for d in /proc/[0-9]*; do\n [ \"$(cat \"$d/comm\" 2>/dev/null)\" = git-remote-http ] || continue\n args=$(tr '\\000' ' ' < \"$d/cmdline\") || continue\n case \"$args\" in *\"$1\"*) basename \"$d\"; exit 0;; esac\ndone", "observe-git-http", g.source["uri"].(string)}, nil, &stdout, io.Discard, false, jetbridge.ExecAttrs{Purpose: "observe-real-git-fetch"})
			stop()
			pid := strings.TrimSpace(stdout.String())
			if probeErr != nil || pid == "" {
				continue
			}
			number, parseErr := strconv.Atoi(pid)
			if parseErr != nil || number <= 1 {
				return fmt.Errorf("invalid observed Git HTTP PID %q", pid)
			}
			state, stateErr := childState(ctx, g.worker, pod.Name, pid)
			if stateErr != nil || state == "gone" || state == "Z" || state == "X" {
				return fmt.Errorf("Git HTTP child not live before interruption: state=%s error=%v", state, stateErr)
			}
			g.timedPod, g.httpPID = &pod, pid
			fmt.Printf("actual Git HTTP fetch pod %s/%s UID %s PID %s state %s observed running\n", g.worker.Namespace, pod.Name, pod.UID, pid, state)
			break
		}
		if g.timedPod == nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	return nil
}

func (g *execLiveGit) resumeHTTPServer() error {
	_, err := gitFixtureExec(g.worker, "git-server", []string{"kill", "-CONT", g.serverPID})
	if err != nil {
		return err
	}
	response, err := gitFixtureExec(g.worker, "git-server", []string{"wget", "-qO-", g.source["uri"].(string) + "/info/refs"})
	if err != nil || !strings.Contains(response, g.last) {
		return fmt.Errorf("resumed Git server failed its real HTTP probe: %v %q", err, response)
	}
	return nil
}
