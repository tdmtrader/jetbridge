package steps

import (
	"fmt"
	"strings"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

type HijackOptionsOutcome struct {
	Ready      WorkerReady
	Trace      *execObservation
	Pod        string
	UID        types.UID
	ExitStatus int
	Err        error
}

func HijackOptionDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		Transform[WorkerReady, WorkerReady]("a Bash-capable pod {string} is ready for interception",
			func(in WorkerReady, a Args) (WorkerReady, error) {
				name := a.String(0)
				// Official Debian bookworm-slim index; the premise verifies /bin/bash itself.
				const image = "debian@sha256:88200866dfff7ea7f5cbcb6ec7c8a701889efe6fe859fe64d6990e4b07ea4171"
				if err := createInterceptPodWithImage(in, name, map[string]string{"concourse.ci/worker": in.DBWorker.Name()}, image); err != nil {
					return in, err
				}
				var version strings.Builder
				if err := in.Executor.ExecInPod(in.Ctx, in.Namespace, name, "main", []string{"/bin/bash", "-c", `printf "%s" "$BASH_VERSION"`}, nil, &version, nil, false, jetbridge.ExecAttrs{Purpose: "hijack-bash-premise"}); err != nil {
					return in, fmt.Errorf("verify real Bash: %w", err)
				}
				if version.Len() == 0 {
					return in, fmt.Errorf("hijack image reported no Bash version")
				}
				creating, err := in.DBWorker.CreateContainer(db.NewFixedHandleContainerOwner(name), db.ContainerMetadata{Type: db.ContainerTypeTask})
				if err != nil {
					return in, err
				}
				if _, err = creating.Created(); err != nil {
					return in, err
				}
				in.StepHandle, in.StepPodName = name, name
				fmt.Printf("real Bash %s is ready in %s/%s\n", version.String(), in.Namespace, name)
				return in, nil
			}),
		Transform[WorkerReady, HijackOptionsOutcome]("the operator hijacks {string} using {string} with arguments {string} and terminal {string}",
			func(in WorkerReady, a Args) (HijackOptionsOutcome, error) {
				handle, path, arguments, terminal := a.String(0), a.String(1), a.String(2), a.String(3)
				if handle != in.StepHandle || (terminal != "one" && terminal != "none") {
					return HijackOptionsOutcome{}, fmt.Errorf("expected prepared handle and terminal one or none")
				}
				pod, err := in.Clientset.CoreV1().Pods(in.Namespace).Get(in.Ctx, in.StepPodName, metav1.GetOptions{})
				if err != nil {
					return HijackOptionsOutcome{}, err
				}
				trace := new(execObservation)
				cfg, err := liveKubernetesConfig()
				if err != nil {
					return HijackOptionsOutcome{}, err
				}
				// Only the SUT's transport is observed. Premise and cleanup exec remain
				// on the original production transport and cannot inflate the count.
				in = in.rebuildWith(jetbridge.NewSPDYExecutor(in.Clientset, trace.config(cfg)))
				container, found, err := in.Worker.LookupContainer(in.Ctx, handle)
				if err != nil {
					return HijackOptionsOutcome{}, err
				}
				if !found || container == nil {
					return HijackOptionsOutcome{}, fmt.Errorf("prepared container %q is absent", handle)
				}
				spec := runtime.ProcessSpec{Path: path, Args: splitList(arguments)}
				if terminal == "one" {
					spec.TTY = &runtime.TTYSpec{WindowSize: runtime.WindowSize{Columns: 80, Rows: 24}}
				}
				out := HijackOptionsOutcome{Ready: in, Trace: trace, Pod: pod.Name, UID: pod.UID}
				// Nil stdin is essential: these are supervised task hijacks, unlike the
				// existing resource terminal probe, whose stdin is deliberately open.
				process, err := container.Run(in.Ctx, spec, runtime.ProcessIO{})
				if err != nil {
					out.Err = err
					return out, nil
				}
				if process == nil {
					return out, fmt.Errorf("hijack Run returned no process")
				}
				result, err := process.Wait(in.Ctx)
				out.ExitStatus, out.Err = result.ExitStatus, err
				return out, nil
			}),
		Assert[HijackOptionsOutcome]("the hijack preserves its pod and forwards {string} once with terminal {string}",
			func(in HijackOptionsOutcome, a Args) error {
				command, terminal := a.String(0), a.String(1)
				if command == "" || (terminal != "one" && terminal != "none") {
					return fmt.Errorf("expected a command and terminal one or none")
				}
				if in.Err != nil || in.ExitStatus != 0 {
					return fmt.Errorf("hijack exit=%d error=%v", in.ExitStatus, in.Err)
				}
				pods, err := in.Ready.Clientset.CoreV1().Pods(in.Ready.Namespace).List(in.Ready.Ctx, metav1.ListOptions{})
				if err != nil {
					return err
				}
				if len(pods.Items) != 1 || pods.Items[0].Name != in.Pod || pods.Items[0].UID != in.UID {
					return fmt.Errorf("hijack did not preserve its sole original pod %s (%s)", in.Pod, in.UID)
				}
				if err := in.Trace.requireSupervisedExec(in.Ready.Namespace, in.Pod, command, terminal == "one"); err != nil {
					return fmt.Errorf("hijack: %w", err)
				}
				fmt.Printf("observed one hijack exec in original pod UID %s\n", in.UID)
				return nil
			}),
	}
}
