package steps

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
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
	Reserved        output.ReservedIncarnation
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
		CheckThat[RunOutputRuntime]("its runtime control names the retained daemon source", checkRuntimeSource),
		brine.DefineCheck[RunOutputRuntime]("its runtime grant establishes a signed source hold", func(in RunOutputRuntime, _ brine.Params, rec *brine.Recorder) error {
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
			if b == nil || a.Identity != b.Identity || a.Capture.HandoffID != b.Capture.HandoffID || a.Capture.ReservedIncarnation != b.Capture.ReservedIncarnation || a.Capture.ReservedDirectory != b.Capture.ReservedDirectory {
				return fmt.Errorf("reconnect changed the execution or source")
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
		CheckThat[RunOutputRuntime]("runtime preparation is refused without a start token", func(in RunOutputRuntime) error {
			if in.Err == nil || in.Replay != nil {
				return fmt.Errorf("unadmitted runtime got control")
			}
			return checkNoRunOutputStart(in.Start)
		}),
		CheckThat[RunOutputRuntime]("the cancelled producer receives no runtime control", func(in RunOutputRuntime) error {
			if in.Err == nil || in.Replay != nil {
				return fmt.Errorf("cancelled producer got runtime authority")
			}
			return nil
		}),
		brine.DefineMap[RunOutputRuntime, RunOutputRuntime]("its source dispatch stops before contacting the daemon", func(in RunOutputRuntime, _ brine.Params, _ *brine.Recorder) (RunOutputRuntime, error) {
			return in.dispatch(false)
		}),
		brine.DefineMap[RunOutputRuntime, RunOutputRuntime]("its source is dispatched but the database reply is lost", func(in RunOutputRuntime, _ brine.Params, _ *brine.Recorder) (RunOutputRuntime, error) {
			return in.dispatch(true)
		}),
		brine.DefineMap[RunOutputRuntime, RunOutputRuntime]("a new Run runtime reconciles pending sources", func(in RunOutputRuntime, _ brine.Params, _ *brine.Recorder) (RunOutputRuntime, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			return in, in.starter().Run(ctx)
		}),
		CheckThat[RunOutputRuntime]("the original source is recorded without starting the producer", func(in RunOutputRuntime) error {
			r, err := in.readSource()
			if err != nil {
				return err
			}
			if !r.Source.Reserved() || r.Execution != in.Start.Record.Execution || r.Source.Locator != in.Node.Name || string(r.Source.Incarnation.NodeUID) != string(in.Node.UID) {
				return fmt.Errorf("recovery lost the original source")
			}
			if in.Reserved.Directory != "" && (r.Source.Incarnation != in.Reserved.Incarnation || r.Source.Directory != in.Reserved.Directory) {
				return fmt.Errorf("recovery replaced the daemon's original reservation")
			}
			pods, err := in.Client.CoreV1().Pods("").List(context.Background(), metav1.ListOptions{})
			if err != nil {
				return err
			}
			for _, pod := range pods.Items {
				if pod.Spec.NodeName == in.Node.Name {
					return fmt.Errorf("recovery created a producing Pod")
				}
			}
			return nil
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
			if r.Source.Incarnation != in.Control.Capture.ReservedIncarnation {
				return fmt.Errorf("replacement node changed the retained source")
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
func (in RunOutputRuntime) readSource() (output.HandoffRecord, error) {
	tx, err := in.Start.DB.Conn.Begin()
	if err != nil {
		return output.HandoffRecord{}, err
	}
	defer db.Rollback(tx)
	inDB, found, err := db.NewPipelineRunFactory(in.Start.DB.Conn, in.Start.DB.LockFactory).OutputTask(context.Background(), tx, in.Start.Creation.EntryBuilds[0].ID(), in.Start.Plan.TaskID)
	if err == nil && !found {
		err = fmt.Errorf("runtime source is not retained")
	}
	return inDB.Record, err
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
	if r.Execution != in.Control.Identity || r.HandoffID != c.HandoffID || r.Source.Incarnation != c.ReservedIncarnation || r.Source.Directory != c.ReservedDirectory || r.Source.Locator != c.ReservingNode || string(r.Source.Incarnation.NodeUID) != string(in.Node.UID) {
		return fmt.Errorf("runtime differs from retained daemon source")
	}
	client := jetbridge.NewOutputControlClient(in.Start.Daemon.Output.URL, in.Start.Daemon.HTTP, in.Start.Daemon.Minter, r.ActivationEpoch)
	_, err = client.Classify(context.Background(), r.Execution)
	return err
}

func checkRuntimeGrant(in RunOutputRuntime, rec *brine.Recorder) error {
	ctx := context.Background()
	pod, err := in.Client.CoreV1().Pods("default").Create(ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "run-hold-"},
		Spec:       corev1.PodSpec{NodeName: in.Node.Name, RestartPolicy: corev1.RestartPolicyNever, Containers: []corev1.Container{{Name: "main", Image: "busybox"}}},
	}, metav1.CreateOptions{})
	if err != nil {
		return err
	}
	TrackDisposer(rec, "the run hold pod "+pod.Name, func() error {
		zero := int64(0)
		return releasedIfGone(in.Client.CoreV1().Pods("default").Delete(ctx, pod.Name, metav1.DeleteOptions{GracePeriodSeconds: &zero}))
	})
	c := in.Control.Capture
	body, err := json.Marshal(holdBody(output.CaptureAdmission{
		ProtocolVersion: output.ProtocolVersion, Execution: c.Identity, ActivationEpoch: c.ActivationEpoch,
		HandoffID: c.HandoffID, SourceHoldID: c.SourceHoldID, Output: output.OutputName(c.Output), CaptureDeadline: output.NewTimestamp(c.CaptureDeadline),
	}, c.ReservedIncarnation, executioncontrol.PodUID(pod.UID)))
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, in.Control.Endpoint+"/capture/v1/hold", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set(jetbridge.CapabilityHeaderName, string(c.SourceControlGrant))
	response, err := in.Start.Daemon.HTTP.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	answer, err := io.ReadAll(response.Body)
	ack, err := decodeControl[output.CaptureAcknowledgement](controlAnswer{Status: response.StatusCode, Body: answer, Err: err})
	if err != nil {
		return err
	}
	if ack.Execution != c.Identity || ack.Incarnation != c.ReservedIncarnation || string(ack.PodUID) != string(pod.UID) {
		return fmt.Errorf("runtime grant held another producer")
	}
	return output.VerifyCaptureAcknowledgement(ack, in.Start.Daemon.ControlPublic)
}

// Interrupt at the actual DB/network boundary. All operations are production
// operations; the fixture omits recording the reply to model controller loss.
func (in RunOutputRuntime) dispatch(contact bool) (RunOutputRuntime, error) {
	var err error
	in.Start.Record, err = in.Start.start(in.Start.Plan, int64(hangarEpoch), in.Node.Name, string(in.Node.UID), false)
	if err != nil {
		return in, err
	}
	if err := (RunOutputFinish{Start: in.Start}).requestSource(); err != nil {
		return in, err
	}
	if !contact {
		return in, nil
	}
	r := in.Start.Record
	in.Reserved, err = in.source().ReserveSource(context.Background(), in.Node.Name, string(in.Node.UID), output.CaptureAdmission{
		ProtocolVersion: output.ProtocolVersion, Execution: r.Execution, ActivationEpoch: r.ActivationEpoch, HandoffID: r.HandoffID, SourceHoldID: r.SourceHoldID, Output: r.Output, CaptureDeadline: r.CaptureDeadline,
	})
	return in, err
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
	in.Start, err = runOutputFixtureConfig(rec, res, "current", string(in.Node.UID), checks, in.Node.Name)
	if err != nil {
		return in, err
	}
	if in.Start.Err != nil {
		return in, in.Start.Err
	}
	in.Start.DB.Conn.SetMaxOpenConns(1)
	in.Config = jetbridge.NewConfig("default", "")
	in.Config.OutputDaemonPort, err = hangarDaemonPort(in.Start.Daemon.Output.URL)
	in.Config.OutputDaemonTLSCert = filepath.Join(in.Start.Daemon.CertDir, "client.crt")
	in.Config.OutputDaemonTLSKey = filepath.Join(in.Start.Daemon.CertDir, "client.key")
	in.Config.OutputDaemonTLSCACert = filepath.Join(in.Start.Daemon.CertDir, "ca.crt")
	in.Config.OutputDaemonTLSServerName = "artifact-daemon"
	return in, err
}
