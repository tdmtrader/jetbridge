package steps

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runs"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type RunOutputRuntime struct {
	Start           RunOutputStart
	Client          kubernetes.Interface
	Node            *corev1.Node
	Config          jetbridge.Config
	Spec            runtime.ContainerSpec
	Control, Replay *runtime.ExecutionControl
	Err             error
	OutcomeReader   jetbridge.PodExecutor
}

func RunOutputRuntimeDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, RunOutputRuntime]("a Run producer and a ready output node", []string{"jetbridge-db", "real-cluster"}, func(_ brine.Empty, _ brine.Params, rec *brine.Recorder, res brine.Resources) (RunOutputRuntime, error) {
			return newRunOutputRuntime(rec, res, false)
		}),
		brine.DefineMap[RunOutputRuntime, RunOutputRuntime]("its Run runtime prepares the producer", func(in RunOutputRuntime, _ brine.Params, _ *brine.Recorder) (RunOutputRuntime, error) {
			in.Control, in.Err = in.prepare()
			return in, in.Err
		}),
		brine.DefineMap[RunOutputRuntime, RunOutputRuntime]("its Run runtime tries to prepare the producer", func(in RunOutputRuntime, _ brine.Params, _ *brine.Recorder) (RunOutputRuntime, error) {
			in.Replay, in.Err = in.prepare()
			return in, nil
		}),
		brine.DefineMap[RunOutputRuntime, RunOutputRuntime]("a new Run runtime prepares the producer again", func(in RunOutputRuntime, _ brine.Params, _ *brine.Recorder) (RunOutputRuntime, error) {
			in.Replay, in.Err = in.prepare()
			return in, in.Err
		}),
		brine.DefineMap[RunOutputRuntime, RunOutputRuntime]("two Run runtimes prepare the producer concurrently", func(in RunOutputRuntime, _ brine.Params, _ *brine.Recorder) (RunOutputRuntime, error) {
			in.Start.DB.Conn.SetMaxOpenConns(4)
			var controls [2]*runtime.ExecutionControl
			var errs [2]error
			var wg sync.WaitGroup
			start := make(chan struct{})
			for i := range controls {
				wg.Add(1)
				go func(i int) { defer wg.Done(); <-start; controls[i], errs[i] = in.prepare() }(i)
			}
			close(start)
			wg.Wait()
			for _, err := range errs {
				if err != nil {
					return in, err
				}
			}
			in.Control, in.Replay = controls[0], controls[1]
			return in, nil
		}),
		CheckThat[RunOutputRuntime]("its runtime control names the retained capture", checkRuntimeSource),
		brine.DefineCheck[RunOutputRuntime]("its runtime grant holds the capture's step directory", func(in RunOutputRuntime, _ brine.Params, rec *brine.Recorder) error {
			return checkRuntimeGrant(in, rec)
		}),
		brine.DefineMap[RunOutputRuntime, RunOutputRuntime]("its runtime input overlaps the selected output", func(in RunOutputRuntime, _ brine.Params, _ *brine.Recorder) (RunOutputRuntime, error) {
			in.Spec.Inputs = []runtime.Input{{DestinationPath: "/workspace/result/input", HangarTree: &hangar.TreeRef{Scope: "brine", Digest: hangar.Digest("sha256:" + strings.Repeat("a", 64)), Generation: 1}}}
			return in, nil
		}),
		CheckThat[RunOutputRuntime]("the repeated runtime control retains identity with fresh grants", func(in RunOutputRuntime) error {
			if err := checkRuntimeSource(in); err != nil {
				return err
			}
			a, b := in.Control, in.Replay
			if b == nil || a.Identity != b.Identity || a.Capture.Key() != b.Capture.Key() || a.Capture.Node != b.Capture.Node || a.Capture.NodeUID != b.Capture.NodeUID {
				return fmt.Errorf("reconnect changed the execution or its capture")
			}
			if a.Capability == b.Capability || a.Capture.SourceControlGrant == b.Capture.SourceControlGrant {
				return fmt.Errorf("reconnect reused one-shot grants")
			}
			return b.Validate(in.Spec)
		}),
		brine.DefineMap[RunOutputRuntime, RunOutputRuntime]("its output node becomes unready", func(in RunOutputRuntime, _ brine.Params, _ *brine.Recorder) (RunOutputRuntime, error) {
			in.Node.Status.Conditions[0].Status = corev1.ConditionFalse
			var err error
			in.Node, err = in.Client.CoreV1().Nodes().UpdateStatus(context.Background(), in.Node, metav1.UpdateOptions{})
			return in, err
		}),
		brine.DefineMap[RunOutputRuntime, RunOutputRuntime]("its runtime declares no selected output", func(in RunOutputRuntime, _ brine.Params, _ *brine.Recorder) (RunOutputRuntime, error) {
			in.Spec.Outputs = nil
			return in, nil
		}),
		brine.DefineMap[RunOutputRuntime, RunOutputRuntime]("its runtime producer is aborted", func(in RunOutputRuntime, _ brine.Params, _ *brine.Recorder) (RunOutputRuntime, error) {
			_, err := in.Start.DB.Conn.Exec(`UPDATE builds SET aborted=true WHERE id=$1`, in.Start.Creation.EntryBuilds[0].ID())
			return in, err
		}),
		CheckThat[RunOutputRuntime]("runtime preparation is refused without a capture", func(in RunOutputRuntime) error {
			if in.Err == nil || in.Replay != nil {
				return fmt.Errorf("unadmitted runtime got control")
			}
			return checkNoRunOutputStart(in.Start)
		}),
		brine.DefineMap[RunOutputRuntime, RunOutputRuntime]("its output node is replaced", func(in RunOutputRuntime, _ brine.Params, _ *brine.Recorder) (RunOutputRuntime, error) {
			ctx := context.Background()
			if err := in.Client.CoreV1().Nodes().Delete(ctx, in.Node.Name, metav1.DeleteOptions{}); err != nil {
				return in, err
			}
			node := in.Node.DeepCopy()
			node.ObjectMeta = metav1.ObjectMeta{Name: in.Node.Name, Labels: in.Node.Labels}
			_, err := in.Client.CoreV1().Nodes().Create(ctx, node, metav1.CreateOptions{})
			return in, err
		}),
		CheckThat[RunOutputRuntime]("the replacement node receives no runtime control", func(in RunOutputRuntime) error {
			if in.Err == nil || in.Replay != nil {
				return fmt.Errorf("replacement node inherited runtime authority")
			}
			r, err := in.readSource()
			if err != nil {
				return err
			}
			if r.Key != in.Control.Capture.Key() || string(r.NodeUID) != string(in.Control.Capture.NodeUID) {
				return fmt.Errorf("replacement node changed the retained capture")
			}
			return nil
		}),
	}
}

func (in RunOutputRuntime) source() *jetbridge.OutputSource {
	source := jetbridge.NewOutputSource(in.Client, in.Config, in.Start.Daemon.Minter, executioncontrol.ActivationEpoch(hangarEpoch))
	source.SetExecutor(in.OutcomeReader)
	return source
}
func (in RunOutputRuntime) starter() *runs.OutputStarter {
	return runs.NewOutputStarter(in.Start.DB.Conn, db.NewPipelineRunFactory(in.Start.DB.Conn, in.Start.DB.LockFactory), in.source(), int64(hangarEpoch), time.Hour)
}
func (in RunOutputRuntime) prepare() (*runtime.ExecutionControl, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return in.starter().Prepare(ctx, in.Start.Creation.EntryBuilds[0].ID(), in.Start.Plan, in.Spec)
}

// readSource is the producer's capture as the Run retains it.
func (in RunOutputRuntime) readSource() (runCaptureRecord, error) {
	return in.Start.readCapture()
}

func checkRuntimeSource(in RunOutputRuntime) error {
	if in.Err != nil {
		return in.Err
	}
	if in.Control == nil || in.Control.Capture == nil {
		return fmt.Errorf("runtime has no capture control")
	}
	if err := in.Control.Validate(in.Spec); err != nil {
		return err
	}
	r, err := in.readSource()
	if err != nil {
		return err
	}
	c := in.Control.Capture
	if r.State != output.CapturePending || r.Execution != in.Control.Identity || r.Key != c.Key() ||
		r.Node != c.Node || r.Node != in.Node.Name || string(r.NodeUID) != string(c.NodeUID) ||
		string(r.NodeUID) != string(in.Node.UID) {
		return fmt.Errorf("runtime control differs from the retained capture")
	}
	client := jetbridge.NewOutputControlClient(in.Start.Daemon.Output.URL, in.Start.Daemon.HTTP, in.Start.Daemon.Minter, executioncontrol.ActivationEpoch(hangarEpoch))
	_, err = client.Classify(context.Background(), r.Execution)
	return err
}

// checkRuntimeGrant plays the capture control init: from a real Pod on the
// capture's node, it presents the runtime's own hold grant at the hold route
// and requires the held marker to name exactly that capture and that Pod.
func checkRuntimeGrant(in RunOutputRuntime, rec *brine.Recorder) error {
	_, err := holdRuntimeCapture(in, rec)
	return err
}

func holdRuntimeCapture(in RunOutputRuntime, rec *brine.Recorder) (output.CaptureHoldAcknowledgement, error) {
	ctx := context.Background()
	pod, err := in.Client.CoreV1().Pods("default").Create(ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "run-hold-"},
		Spec:       corev1.PodSpec{NodeName: in.Node.Name, RestartPolicy: corev1.RestartPolicyNever, Containers: []corev1.Container{{Name: "main", Image: "busybox"}}},
	}, metav1.CreateOptions{})
	if err != nil {
		return output.CaptureHoldAcknowledgement{}, err
	}
	TrackDisposer(rec, "the run hold pod "+pod.Name, func() error {
		zero := int64(0)
		return releasedIfGone(in.Client.CoreV1().Pods("default").Delete(ctx, pod.Name, metav1.DeleteOptions{GracePeriodSeconds: &zero}))
	})
	c := in.Control.Capture
	body, err := json.Marshal(holdBody(c.Identity, output.OutputName(c.Output), executioncontrol.PodUID(pod.UID)))
	if err != nil {
		return output.CaptureHoldAcknowledgement{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, in.Control.Endpoint+"/capture/v1/hold", bytes.NewReader(body))
	if err != nil {
		return output.CaptureHoldAcknowledgement{}, err
	}
	request.Header.Set(jetbridge.CapabilityHeaderName, string(c.SourceControlGrant))
	response, err := in.Start.Daemon.HTTP.Do(request)
	if err != nil {
		return output.CaptureHoldAcknowledgement{}, err
	}
	defer response.Body.Close()
	answer, err := io.ReadAll(response.Body)
	ack, err := decodeControl[output.CaptureHoldAcknowledgement](controlAnswer{Status: response.StatusCode, Body: answer, Err: err})
	if err != nil {
		return ack, err
	}
	if err := ack.Validate(); err != nil {
		return ack, err
	}
	if ack.Marker.Key() != c.Key() || string(ack.Marker.PodUID) != string(pod.UID) ||
		string(ack.Marker.Node) != string(in.Node.UID) || ack.Marker.State != output.StepHeld {
		return ack, fmt.Errorf("runtime grant held another producer: %+v", ack.Marker)
	}
	return ack, nil
}

func newRunOutputRuntime(rec *brine.Recorder, res brine.Resources, checks bool) (RunOutputRuntime, error) {
	ctx := context.Background()
	cluster, err := getRealCluster(res)
	if err != nil {
		return RunOutputRuntime{}, err
	}
	in := RunOutputRuntime{Client: cluster.Clientset, Spec: runtime.ContainerSpec{Outputs: runtime.OutputPaths{"result": "/workspace/result"}}}
	node, err := in.Client.CoreV1().Nodes().Create(ctx, &corev1.Node{ObjectMeta: metav1.ObjectMeta{GenerateName: "run-output-", Labels: map[string]string{
		"concourse.dev/artifact-cache": "ready", "concourse.dev/hangar-v1": "ready", executioncontrol.ReadyLabel: "ready", output.ReadyLabel: "ready",
	}}}, metav1.CreateOptions{})
	if err != nil {
		return in, err
	}
	name := node.Name
	TrackDisposer(rec, "the run output node "+name, func() error {
		return releasedIfGone(in.Client.CoreV1().Nodes().Delete(ctx, name, metav1.DeleteOptions{}))
	})
	node.Labels[corev1.LabelHostname] = name
	node, err = in.Client.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
	if err != nil {
		return in, err
	}
	node.Status.Addresses = []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "127.0.0.1"}}
	node.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}
	in.Node, err = in.Client.CoreV1().Nodes().UpdateStatus(ctx, node, metav1.UpdateOptions{})
	if err != nil {
		return in, err
	}
	// envtest has no node-lifecycle controller to remove the bootstrap
	// not-ready taint when the supplied kubelet status becomes ready.
	in.Node.Spec.Taints = nil
	in.Node, err = in.Client.CoreV1().Nodes().Update(ctx, in.Node, metav1.UpdateOptions{})
	if err != nil {
		return in, err
	}
	in.Start, err = runOutputFixtureConfig(rec, res, "current", string(in.Node.UID), checks)
	if err != nil {
		return in, err
	}
	if in.Start.Err != nil {
		return in, in.Start.Err
	}
	in.Start.DB.Conn.SetMaxOpenConns(1)
	in.Config = jetbridge.NewConfig("default", "")
	err = outputPlaneConfig(&in.Config, in.Start.Daemon.Output.URL, in.Start.Daemon.CertDir)
	return in, err
}
