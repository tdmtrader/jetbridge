package jetbridge

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/atc/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

func TestExecProcessWaitForCheckpointPreemptionUsesItsExactScheduledNode(t *testing.T) {
	pod := checkpointTestPod("agent-42", "uid-42", "main")
	process := checkpointTestProcess(fake.NewClientset(pod), &checkpointTestExecutor{})
	backend := process.container.storageBackend.(*DaemonSetBackend)
	observed := time.Date(2026, time.July, 29, 14, 0, 0, 0, time.UTC)
	var requestedHost string
	backend.SetDaemonClient(preemptionDaemonClient(
		[]DaemonEndpoint{
			{NodeName: "node-b", Address: "10.0.0.2"},
			{NodeName: "node-a", Address: "10.0.0.1"},
		},
		func(request *http.Request) (*http.Response, error) {
			requestedHost = request.URL.Host
			return &http.Response{
				StatusCode: http.StatusOK,
				Body: io.NopCloser(strings.NewReader(
					`{"sequence":1,"observed_at":"2026-07-29T14:00:00Z"}`,
				)),
			}, nil
		},
	))

	var source runtime.CheckpointPreemptionProcess = process
	got, err := source.WaitForCheckpointPreemption(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(observed) || requestedHost != "10.0.0.1:7780" {
		t.Fatalf("notice = %v host = %q", got, requestedHost)
	}
}

func TestExecProcessWaitForCheckpointPreemptionFailsClosedWithoutAuthenticatedDaemon(t *testing.T) {
	process := checkpointTestProcess(
		fake.NewClientset(checkpointTestPod("agent-42", "uid-42", "main")),
		&checkpointTestExecutor{},
	)
	if _, err := process.WaitForCheckpointPreemption(context.Background()); err == nil {
		t.Fatal("preemption watch succeeded without a daemon client")
	}

	backend := process.container.storageBackend.(*DaemonSetBackend)
	backend.SetDaemonClient(&DaemonClient{scheme: "http"})
	if _, err := process.WaitForCheckpointPreemption(context.Background()); err == nil {
		t.Fatal("preemption watch accepted an unauthenticated daemon client")
	}
}
