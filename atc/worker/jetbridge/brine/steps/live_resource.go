package steps

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type ResourceProtocolOutcome struct {
	Cluster                WorkerReady
	Stdout, Stderr         string
	Status, ExpectedStatus int
	Err                    error
}

const resourceInput = "{\"source\":{\"key\":\"value\"}}\n"
const resourceArgument = "space ' quote \" dollar $HOME ; literal"

func ResourceProtocolDefinitions() []brine.StepDefinition {
	const command = "a real {string} resource command exits with status {int}"
	return []brine.StepDefinition{
		brine.DefineMap[WorkerReady, ResourceProtocolOutcome](command,
			func(in WorkerReady, p brine.Params, rec *brine.Recorder) (ResourceProtocolOutcome, error) {
				return applyAction(command, in, p, func(in WorkerReady, a Args) (ResourceProtocolOutcome, error) {
					return runResourceProtocol(in, a.String(0), a.Int(1))
				})
			}),
		CheckThat[ResourceProtocolOutcome]("the resource protocol preserves its streams and exit status",
			func(in ResourceProtocolOutcome) error {
				if in.Err != nil {
					return in.Err
				}
				if in.Status != in.ExpectedStatus || in.Stdout != resourceInput+resourceArgument || in.Stderr != "resource diagnostic\n" {
					return fmt.Errorf("resource protocol: status=%d want=%d stdout=%q stderr=%q", in.Status, in.ExpectedStatus, in.Stdout, in.Stderr)
				}
				return nil
			}),
	}
}

func runResourceProtocol(w WorkerReady, kind string, status int) (ResourceProtocolOutcome, error) {
	out := ResourceProtocolOutcome{Cluster: w, ExpectedStatus: status}
	if kind != "get" && kind != "put" && kind != "check" {
		return out, fmt.Errorf("unknown resource kind %q", kind)
	}
	cpu, memory := uint64(250), uint64(64*1024*1024)
	container, _, err := w.Worker.FindOrCreateContainer(w.Ctx, db.NewFixedHandleContainerOwner("resource-protocol"),
		db.ContainerMetadata{Type: db.ContainerType(kind)}, runtime.ContainerSpec{
			Type: db.ContainerType(kind), Dir: "/tmp", ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox:1.37.0"},
			Limits: runtime.ContainerLimits{CPU: &cpu, Memory: &memory}}, nil)
	if err != nil {
		return out, err
	}
	var stdout, stderr bytes.Buffer
	process, err := container.Run(w.Ctx, runtime.ProcessSpec{Path: "sh", Args: []string{"-c",
		`cat; printf '%s' "$1"; printf 'resource diagnostic\n' >&2; exit "$2"`, "resource-protocol", resourceArgument, strconv.Itoa(status)}},
		runtime.ProcessIO{Stdin: strings.NewReader(resourceInput), Stdout: &stdout, Stderr: &stderr})
	if err != nil {
		return out, err
	}
	pod, err := w.Clientset.CoreV1().Pods(w.Namespace).Get(w.Ctx, "resource-protocol", metav1.GetOptions{})
	if err != nil {
		return out, err
	}
	result, err := process.Wait(w.Ctx)
	out.Status, out.Err, out.Stdout, out.Stderr = result.ExitStatus, err, stdout.String(), stderr.String()
	actual, err := awaitLivePod(w.Ctx, liveKubernetes{Clientset: w.Clientset, Namespace: w.Namespace}, "resource-protocol")
	if err != nil {
		return out, err
	}
	if actual.UID != pod.UID {
		return out, fmt.Errorf("resource command replaced its pod")
	}
	fmt.Printf("real resource protocol %s pod %s/%s UID %s exit %d\n", kind, w.Namespace, pod.Name, pod.UID, out.Status)
	return out, nil
}
