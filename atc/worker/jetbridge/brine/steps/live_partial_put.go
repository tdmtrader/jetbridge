package steps

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	atcresource "github.com/concourse/concourse/atc/resource"
	"github.com/concourse/concourse/atc/runtime"
	atcworker "github.com/concourse/concourse/atc/worker"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Explicitly approved protocol fault injection, not an upstream time-resource
// behavior. BusyBox supplies the shell; only this owned pod's out
// executable is replaced. Worker selection, DB, pod, exec and exit are real.
const partialPutFaultImage = "busybox:1.37.0"

const partialPutFaultScript = `#!/bin/sh
set -eu
D=/tmp/brine-partial-put
cat > "$D/request.json"
cmp -s "$D/request.json" "$D/expected-request.json" || exit 125
printf '%s\n' "$1" > "$D/argument"
cat "$D/expected-reply.json" > "$D/reply.json"
cat "$D/reply.json"
printf 'the resource got halfway and then failed\n' >&2
exit 4
`

func (in ExecBuild) runPartialPut(rec *brine.Recorder, ref string) (ExecRun, error) {
	if !in.putGetsHalfway {
		return ExecRun{}, fmt.Errorf("partial-output put requires the explicit version-then-failure fixture")
	}
	w, err := newLiveRuntimeWorker(in.core.DB, rec, 1)
	if err != nil {
		return ExecRun{}, err
	}
	_, err = in.core.DB.WorkerFactory.SaveWorker(atc.Worker{Name: w.DBWorker.Name(), Platform: "linux", Version: "1.2.3", State: string(db.WorkerStateRunning),
		ResourceTypes: []atc.WorkerResourceType{{Type: "some-base-type", Image: partialPutFaultImage, Version: "partial-put-fault-v1"}}}, 0)
	if err != nil {
		return ExecRun{}, err
	}
	workerDB := newExecWorkerDB(in.core.DB)
	pool := atcworker.NewPool(atcworker.DefaultFactory{DB: workerDB, K8sClientset: w.Clientset, K8sConfig: &w.Config, K8sExecutor: w.Executor}, workerDB)
	plan := execPutPlan("some-resource", ref)
	plan.TypeImage = atc.TypeImage{ImageRef: "docker:///" + partialPutFaultImage}
	owner := db.NewBuildStepContainerOwner(in.core.Build.ID(), "put-1", in.core.Team.ID())
	spec := runtime.ContainerSpec{TeamID: in.core.Team.ID(), TeamName: in.core.Team.Name(), Type: db.ContainerTypePut,
		ImageSpec: runtime.ImageSpec{ImageURL: plan.TypeImage.ImageRef}, Dir: atcresource.ResourcesDir("put"),
		Env: in.stepMetadata().Env(), CertsBindMount: true}
	selected, err := pool.FindOrSelectWorker(w.Ctx, owner, spec, atcworker.Spec{TeamID: in.core.Team.ID()})
	if err != nil {
		return ExecRun{}, err
	}
	container, _, err := selected.FindOrCreateContainer(w.Ctx, owner, db.ContainerMetadata{Type: db.ContainerTypePut, WorkingDirectory: spec.Dir, StepName: plan.Name, PipelineID: in.core.Pipeline.ID()}, spec, nil)
	if err != nil {
		return ExecRun{}, err
	}
	// Run constructs the real pause pod; Wait is what executes a resource.
	// Do not execute a preparatory process or cache a completion annotation.
	_, err = container.Run(w.Ctx, runtime.ProcessSpec{ID: "resource", Path: "/opt/resource/out", Args: []string{spec.Dir}}, runtime.ProcessIO{})
	if err != nil {
		return ExecRun{}, err
	}
	pods := w.Clientset.CoreV1().Pods(w.Namespace)
	list, err := pods.List(w.Ctx, metav1.ListOptions{})
	if err != nil {
		return ExecRun{}, err
	}
	if len(list.Items) != 1 {
		return ExecRun{}, fmt.Errorf("expected one actual resource pod, got %d", len(list.Items))
	}
	original := list.Items[0]
	pod, err := awaitLivePod(w.Ctx, liveKubernetes{Clientset: w.Clientset, Namespace: w.Namespace}, original.Name)
	if err != nil {
		return ExecRun{}, err
	}
	if pod.UID == "" || pod.UID != original.UID || pod.Spec.NodeName == "" || len(pod.Spec.Containers) != 1 || pod.Spec.Containers[0].Image != partialPutFaultImage {
		return ExecRun{}, fmt.Errorf("partial-put fixture lost its actual pinned resource pod")
	}
	if err := validatePodMounts(pod); err != nil {
		return ExecRun{}, err
	}
	direct, ok := w.Executor.(*jetbridge.SPDYExecutor)
	if !ok {
		return ExecRun{}, fmt.Errorf("partial put requires the real SPDY executor, got %T", w.Executor)
	}
	run := func(command []string, input string) (string, string, error) {
		var stdout, stderr bytes.Buffer
		err := direct.ExecInPod(w.Ctx, w.Namespace, pod.Name, "main", command, strings.NewReader(input), &stdout, &stderr, false, jetbridge.ExecAttrs{})
		return stdout.String(), stderr.String(), err
	}
	request, err := (atcresource.Resource{Source: plan.Source, Params: plan.Params}).Signature()
	if err != nil {
		return ExecRun{}, err
	}
	expectedReply, err := json.Marshal(atcresource.VersionResult{Version: execVersionOf(ref)})
	if err != nil {
		return ExecRun{}, err
	}
	// The deliberately configured reply is emitted only after the executable
	// receives the exact intended request; a protocol mismatch exits 125.
	install := []string{"sh", "-ec", `mkdir -p /opt/resource /tmp/brine-partial-put; printf '%s' "$1" > /tmp/brine-partial-put/expected-request.json; printf '%s\n' "$2" > /tmp/brine-partial-put/expected-reply.json; cat > /opt/resource/out; chmod 0755 /opt/resource/out`, "install-partial-put", string(request), string(expectedReply)}
	if _, stderr, err := run(install, partialPutFaultScript); err != nil {
		return ExecRun{}, fmt.Errorf("install approved fault executable: %w: %s", err, stderr)
	}
	stdout, stderr, preflightErr := run([]string{"/opt/resource/out", spec.Dir}, string(request))
	var exit *jetbridge.ExecExitError
	var reply atcresource.VersionResult
	if !errors.As(preflightErr, &exit) || exit.ExitCode != 4 || json.Unmarshal([]byte(stdout), &reply) != nil || !reflect.DeepEqual(reply.Version, execVersionOf(ref)) || stderr != "the resource got halfway and then failed\n" {
		return ExecRun{}, fmt.Errorf("real fault executable did not emit valid version then exit 4: stdout=%q stderr=%q error=%v", stdout, stderr, preflightErr)
	}
	// Remove preflight receipts so only the production put can satisfy the
	// following observations; no runtime response or pod status is injected.
	if _, stderr, err := run([]string{"sh", "-ec", "rm /tmp/brine-partial-put/request.json /tmp/brine-partial-put/reply.json /tmp/brine-partial-put/argument"}, ""); err != nil {
		return ExecRun{}, fmt.Errorf("clear owned preflight receipts: %w: %s", err, stderr)
	}
	before, err := pods.Get(w.Ctx, pod.Name, metav1.GetOptions{})
	if err != nil {
		return ExecRun{}, err
	}
	if _, present := before.Annotations[exitStatusAnnotation]; present {
		return ExecRun{}, fmt.Errorf("fixture installed a cached process completion")
	}
	passed, runErr := in.putStepWithPool("put-1", plan, pool).Run(w.Ctx, in.core.State)
	for _, file := range []struct{ name, want string }{
		{"request.json", string(request)}, {"argument", spec.Dir + "\n"},
	} {
		actual, stderr, err := run([]string{"cat", "/tmp/brine-partial-put/" + file.name}, "")
		if err != nil || actual != file.want {
			return ExecRun{}, fmt.Errorf("production put lost actual %s receipt: got=%q want=%q read=%v stderr=%q step=%v", file.name, actual, file.want, err, stderr, runErr)
		}
	}
	emitted, stderr, err := run([]string{"cat", "/tmp/brine-partial-put/reply.json"}, "")
	if err != nil || json.Unmarshal([]byte(emitted), &reply) != nil || !reflect.DeepEqual(reply.Version, execVersionOf(ref)) {
		return ExecRun{}, fmt.Errorf("production put did not execute the version-emitting fixture: reply=%q read=%v stderr=%q step=%v", emitted, err, stderr, runErr)
	}
	final, err := pods.Get(w.Ctx, pod.Name, metav1.GetOptions{})
	if err != nil {
		return ExecRun{}, err
	}
	if final.UID != pod.UID || final.Annotations[exitStatusAnnotation] != "4" {
		return ExecRun{}, fmt.Errorf("production put did not persist real exit 4 on the original pod: uid=%s annotations=%v", final.UID, final.Annotations)
	}
	fmt.Printf("approved partial-put fault: real pod %s/%s UID %s node %s emitted version %s then exited 4; production put ok=%t error=%v\n", w.Namespace, pod.Name, pod.UID, pod.Spec.NodeName, strings.TrimSpace(emitted), passed, runErr)
	return ExecRun{core: in.core, Ok: passed, Err: runErr}, nil
}
