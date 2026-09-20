package steps

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
)

type TerminalOutcome struct {
	Output    string
	Err       error
	taskTrace *execObservation
	namespace string
	withTTY   bool
}

func TTYDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[LiveTaskPlan, TerminalOutcome](
			"resource and supervised task steps run with {string} terminal attached",
			func(in LiveTaskPlan, p brine.Params, rec *brine.Recorder) (TerminalOutcome, error) {
				attached, ok := p.GetString(0)
				if !ok || (attached != "one" && attached != "none") {
					return TerminalOutcome{}, fmt.Errorf("expected one or none attached")
				}
				return runTerminalProbe(in, rec, attached == "one")
			}),
		CheckContains[TerminalOutcome]("the resource reports {string} and the task retains its terminal choice", "the command's output",
			func(in TerminalOutcome) (string, error) {
				if in.Err != nil {
					return "", fmt.Errorf("the step failed: %v", in.Err)
				}
				if err := in.taskTrace.requireSupervisedExec(in.namespace, "terminal-task", "'/bin/sh' '-c' '"+terminalProbeCommand+"'", in.withTTY); err != nil {
					return "", err
				}
				return in.Output, nil
			},
			func(TerminalOutcome) string {
				return "interactive tools need a terminal; resource JSON needs an unmodified pipe"
			}),
	}
}

// Exercise both exec branches with the same declared terminal choice. Resource
// stdin stays open for the remote PTY; supervised tasks deliberately have nil
// stdin, so their actual request (not redirected command output) is the oracle.
func runTerminalProbe(in LiveTaskPlan, rec *brine.Recorder, withTTY bool) (TerminalOutcome, error) {
	ctx, cancel := context.WithTimeout(execLogger("live-terminal"), 90*time.Second)
	defer cancel()
	cluster, err := newLiveKubernetes(ctx, rec)
	if err != nil {
		return TerminalOutcome{}, err
	}
	dw, err := in.Database.PersistNamedWorker("live-terminal-worker")
	if err != nil {
		return TerminalOutcome{}, err
	}
	config := jetbridge.NewConfig(cluster.Namespace, "")
	config.PodStartupTimeout, config.PodSchedulingTimeout = time.Minute, time.Minute
	worker := jetbridge.NewWorker(dw, cluster.Clientset, config)
	cpu, memory := uint64(250), uint64(64*1024*1024)
	out := TerminalOutcome{namespace: cluster.Namespace, withTTY: withTTY}
	for _, kind := range []db.ContainerType{db.ContainerTypeGet, db.ContainerTypeTask} {
		handle := "terminal-resource"
		executorConfig := cluster.Config
		if kind == db.ContainerTypeTask {
			handle = "terminal-task"
			out.taskTrace = &execObservation{}
			executorConfig = out.taskTrace.config(cluster.Config)
		}
		worker.SetExecutor(jetbridge.NewSPDYExecutor(cluster.Clientset, executorConfig))
		container, _, err := worker.FindOrCreateContainer(ctx, db.NewFixedHandleContainerOwner(handle),
			db.ContainerMetadata{Type: kind},
			runtime.ContainerSpec{Dir: "/tmp/build/probe", Type: kind,
				ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox:1.37.0"},
				Limits:    runtime.ContainerLimits{CPU: &cpu, Memory: &memory}}, nil)
		if err != nil {
			return TerminalOutcome{}, err
		}
		spec := runtime.ProcessSpec{Path: "/bin/sh", Args: []string{"-c", terminalProbeCommand}}
		if withTTY {
			spec.TTY = &runtime.TTYSpec{WindowSize: runtime.WindowSize{Columns: 80, Rows: 24}}
		}
		stdout, stderr := new(liveLogBuffer), new(liveLogBuffer)
		processIO := runtime.ProcessIO{Stdout: stdout, Stderr: stderr}
		var stdin *io.PipeReader
		var input *io.PipeWriter
		if kind == db.ContainerTypeGet {
			stdin, input = io.Pipe()
			defer stdin.Close()
			defer input.Close()
			processIO.Stdin = stdin
		}
		process, err := container.Run(ctx, spec, processIO)
		if err != nil {
			return TerminalOutcome{}, err
		}
		if process == nil {
			return TerminalOutcome{}, fmt.Errorf("terminal probe returned no process")
		}
		result, waitErr := process.Wait(ctx)
		if stdin != nil {
			_ = stdin.Close()
			_ = input.Close()
		}
		if waitErr != nil {
			out.Err = waitErr
			return out, nil
		}
		if result.ExitStatus != 0 || stderr.String() != "" {
			out.Err = fmt.Errorf("%s probe exit=%d stderr=%q", handle, result.ExitStatus, stderr.String())
			return out, nil
		}
		pod, err := awaitLivePod(ctx, cluster, handle)
		if err != nil {
			return TerminalOutcome{}, err
		}
		fmt.Printf("live terminal kind=%s requested=%t observed=%q pod %s/%s UID %s node %s\n",
			kind, withTTY, strings.TrimSpace(stdout.String()), pod.Namespace, pod.Name, pod.UID, pod.Spec.NodeName)
		if kind == db.ContainerTypeGet {
			out.Output = stdout.String()
		} else if strings.TrimSpace(stdout.String()) != "pipe" {
			out.Err = fmt.Errorf("supervised probe did not run with redirected output: %q", stdout.String())
			return out, nil
		}
	}
	return out, nil
}

const terminalProbeCommand = "test -t 1 && echo terminal || echo pipe"
