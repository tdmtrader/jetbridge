package steps

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/hangar/executioncontrol"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func BoundSessionDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{brine.DefineMap[RunOutputRuntime, RunOutputRuntime]("its bound session transport encounters {string}", func(in RunOutputRuntime, p brine.Params, rec *brine.Recorder) (RunOutputRuntime, error) {
		mode, _ := p.GetString(0)
		return in, exerciseBoundSession(in, mode, rec)
	})}
}

func exerciseBoundSession(in RunOutputRuntime, mode string, rec *brine.Recorder) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	source := jetbridge.NewOutputSource(in.Client, in.Config, in.Start.Daemon.Minter, executioncontrol.ActivationEpoch(hangarEpoch))
	source.SetExecutor(localExecutor{client: in.Client})
	transport, ok := any(source).(interface {
		ExecBoundSession(context.Context, string, executioncontrol.Acknowledgement, time.Duration, []string, io.Reader, io.Writer) error
	})
	if !ok {
		return fmt.Errorf("node transport cannot bind a stdin session to its exact Pod with a bounded lifetime")
	}
	name := "session-" + freshUUID()
	makePod := func() (*corev1.Pod, error) {
		return in.Client.CoreV1().Pods(in.Config.Namespace).Create(ctx, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec:       corev1.PodSpec{NodeName: in.Node.Name, RestartPolicy: corev1.RestartPolicyNever, Containers: []corev1.Container{{Name: "main", Image: "busybox:1.37"}}},
		}, metav1.CreateOptions{})
	}
	pod, err := makePod()
	if err != nil {
		return err
	}
	TrackDisposer(rec, "the bound session pod "+name, func() error {
		current, err := in.Client.CoreV1().Pods(in.Config.Namespace).Get(context.Background(), name, metav1.GetOptions{})
		if err != nil {
			return releasedIfGone(err)
		}
		if len(current.Finalizers) > 0 {
			current.Finalizers = nil
			if _, err = in.Client.CoreV1().Pods(in.Config.Namespace).Update(context.Background(), current, metav1.UpdateOptions{}); err != nil {
				return err
			}
		}
		return releasedIfGone(in.Client.CoreV1().Pods(in.Config.Namespace).Delete(context.Background(), name, metav1.DeleteOptions{}))
	})
	if mode == "a tighter deadline" {
		seconds := int64(10)
		pod.Spec.ActiveDeadlineSeconds = &seconds
		pod, err = in.Client.CoreV1().Pods(in.Config.Namespace).Update(ctx, pod, metav1.UpdateOptions{})
		if err != nil {
			return err
		}
	}
	now := metav1.Now()
	pod.Status.Phase = corev1.PodRunning
	pod.Status.StartTime = &now
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "main", ContainerID: "brine://" + freshUUID(), Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: now}}}}
	if mode == "a restarted container" {
		pod.Status.ContainerStatuses[0].RestartCount = 1
	}
	if mode == "an expired lifetime" {
		old := metav1.NewTime(time.Now().Add(-2 * time.Minute))
		pod.Status.StartTime = &old
	}
	pod, err = in.Client.CoreV1().Pods(in.Config.Namespace).UpdateStatus(ctx, pod, metav1.UpdateOptions{})
	if err != nil {
		return err
	}
	id := executioncontrol.Identity{ExecutionID: executioncontrol.ExecutionID(freshUUID()), Fence: 1}
	if _, err := source.BaseRuntimeControl(ctx, in.Node.Name, string(in.Node.UID), executioncontrol.ActivationEpoch(hangarEpoch), id); err != nil {
		return err
	}
	client := jetbridge.NewOutputControlClient(in.Start.Daemon.Output.URL, in.Start.Daemon.HTTP, in.Start.Daemon.Minter, executioncontrol.ActivationEpoch(hangarEpoch))
	start, err := client.RecordStart(ctx, id, executioncontrol.PodUID(pod.UID), "bound-session-process")
	if err != nil {
		return err
	}
	lifetime := time.Minute
	switch mode {
	case "a foreign Pod witness":
		start.PodUID = executioncontrol.PodUID(freshUUID())
	case "a foreign node witness":
		start.NodeUID = executioncontrol.NodeUID(freshUUID())
	case "a replacement Pod":
		zero := int64(0)
		if err := in.Client.CoreV1().Pods(in.Config.Namespace).Delete(ctx, name, metav1.DeleteOptions{GracePeriodSeconds: &zero}); err != nil {
			return err
		}
		if _, err := makePod(); err != nil {
			return err
		}
	case "a completed execution":
		if _, err := client.RecordOutcome(ctx, id, executioncontrol.AcknowledgementFinish, executioncontrol.ExitOutcome{ExitCode: 0}); err != nil {
			return err
		}
	case "a terminating Pod":
		pod.Finalizers = []string{"brine.test/session"}
		if _, err := in.Client.CoreV1().Pods(in.Config.Namespace).Update(ctx, pod, metav1.UpdateOptions{}); err != nil {
			return err
		}
		if err := in.Client.CoreV1().Pods(in.Config.Namespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
			return err
		}
	case "a missing executor":
		source.SetExecutor(nil)
	case "a zero lifetime":
		lifetime = 0
	}
	// A real process consumes this non-secret payload; the fixture records API
	// status because envtest has no kubelet. This does not prove Pod expiry.
	if strings.HasSuffix(mode, "during stdin") {
		input, writer := io.Pipe()
		reader, stdout := io.Pipe()
		defer input.Close()
		defer writer.Close()
		defer reader.Close()
		defer stdout.Close()
		done := make(chan error, 1)
		go func() {
			err := transport.ExecBoundSession(ctx, in.Node.Name, start, lifetime, []string{"sh", "-c", "head -c 1; cat > /dev/null"}, input, stdout)
			stdout.Close()
			done <- err
		}()
		stop := context.AfterFunc(ctx, func() { input.Close(); writer.Close(); reader.Close(); stdout.Close() })
		defer stop()
		if _, err := writer.Write([]byte("x")); err != nil {
			return err
		}
		var first [1]byte
		if _, err := io.ReadFull(reader, first[:]); err != nil {
			return err
		}
		if mode == "a replacement container during stdin" {
			current, err := in.Client.CoreV1().Pods(in.Config.Namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return err
			}
			current.Status.ContainerStatuses[0].ContainerID = "brine://replacement"
			if _, err := in.Client.CoreV1().Pods(in.Config.Namespace).UpdateStatus(ctx, current, metav1.UpdateOptions{}); err != nil {
				return err
			}
		} else {
			zero := int64(0)
			if err := in.Client.CoreV1().Pods(in.Config.Namespace).Delete(ctx, name, metav1.DeleteOptions{GracePeriodSeconds: &zero}); err != nil {
				return err
			}
			if _, err := makePod(); err != nil {
				return err
			}
		}
		writer.Close()
		select {
		case err := <-done:
			if err == nil {
				return fmt.Errorf("%s was reported ready", mode)
			}
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	input := bytes.NewReader([]byte("bounded session stdin"))
	var stdout bytes.Buffer
	err = transport.ExecBoundSession(ctx, in.Node.Name, start, lifetime, []string{"wc", "-c"}, input, &stdout)
	allowed := mode == "a live execution" || mode == "a tighter deadline"
	if !allowed {
		if err == nil || input.Len() != len("bounded session stdin") || stdout.Len() != 0 {
			return fmt.Errorf("%s was not refused before stdin: %v", mode, err)
		}
		return nil
	}
	if err != nil {
		return err
	}
	count, err := strconv.Atoi(strings.TrimSpace(stdout.String()))
	if err != nil || count != len("bounded session stdin") {
		return fmt.Errorf("bound session did not reach its real process")
	}
	pod, err = in.Client.CoreV1().Pods(in.Config.Namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	want := int64(60)
	if mode == "a tighter deadline" {
		want = 10
	}
	if pod.Spec.ActiveDeadlineSeconds == nil || *pod.Spec.ActiveDeadlineSeconds != want {
		return fmt.Errorf("session lifetime was absent or extended")
	}
	return nil
}
