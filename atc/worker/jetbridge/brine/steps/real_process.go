package steps

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// watchHandshakeTransport forwards requests and responses unchanged. It exposes
// only the successful real-API watch handshake, so the fixture can delete after
// Process.Wait has established its watch. Assertions inspect the process result,
// not request counts. No response, pod event or error is synthesized here.
type watchHandshakeTransport struct {
	next           http.RoundTripper
	path, selector string
	ready          chan struct{}
	once           sync.Once
}

func (t *watchHandshakeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.next.RoundTrip(request)
	query := request.URL.Query()
	if err == nil && response != nil && response.StatusCode == http.StatusOK &&
		request.URL.Path == t.path && query.Get("watch") == "true" &&
		query.Get("fieldSelector") == t.selector && query.Get("resourceVersion") != "" {
		t.once.Do(func() { close(t.ready) })
	}
	return response, err
}

// observeProcessDeletion retains the direct compatibility deletion contract.
func observeProcessDeletion(in WorkerReady, handle string, cluster *realCluster) (StepOutcome, error) {
	in.Executor = nil
	out, err := observeDiagnosticDeletion(in, handle, "", cluster)
	return StepOutcome{Err: out.Err, Message: out.Message, ExitStatus: out.ExitStatus, Stderr: out.Stderr}, err
}

// observeDiagnosticDeletion drives actual API deletion through either runtime.
// The API pod remains Pending; binding and node configuration do not supply status.
func observeDiagnosticDeletion(in WorkerReady, handle, nodeName string, cluster *realCluster) (out ProcessOutcome, err error) {
	ctx, cancel := context.WithTimeout(in.Ctx, 20*time.Second)
	defer cancel()
	ready := make(chan struct{})
	config := rest.CopyConfig(cluster.RESTConfig)
	config.Wrap(func(next http.RoundTripper) http.RoundTripper {
		return &watchHandshakeTransport{next: next, path: "/api/v1/namespaces/" + in.Namespace + "/pods", selector: "metadata.name=" + handle, ready: ready}
	})
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return ProcessOutcome{}, fmt.Errorf("build real watch client: %w", err)
	}
	in.Clientset = client
	in = in.rebuild()
	container, _, err := in.Worker.FindOrCreateContainer(ctx, db.NewFixedHandleContainerOwner(handle),
		db.ContainerMetadata{Type: db.ContainerTypeTask},
		runtime.ContainerSpec{TeamID: in.TeamID, ImageSpec: runtime.ImageSpec{ImageURL: "busybox"}}, nil)
	if err != nil {
		return ProcessOutcome{}, fmt.Errorf("create watched container: %w", err)
	}
	stderr := new(bytes.Buffer)
	process, err := container.Run(ctx, runtime.ProcessSpec{Path: "/bin/sh"}, runtime.ProcessIO{Stderr: stderr})
	if err != nil {
		return ProcessOutcome{}, fmt.Errorf("create watched pod: %w", err)
	}
	pods := client.CoreV1().Pods(in.Namespace)
	pod, err := pods.Get(ctx, handle, metav1.GetOptions{})
	if err != nil {
		return ProcessOutcome{}, fmt.Errorf("read watched pod: %w", err)
	}
	if pod.UID == "" || pod.ResourceVersion == "" {
		return ProcessOutcome{}, fmt.Errorf("watched pod has no API identity")
	}
	if nodeName != "" {
		if err := pods.Bind(ctx, &corev1.Binding{
			ObjectMeta: metav1.ObjectMeta{Name: pod.Name, Namespace: pod.Namespace, UID: pod.UID},
			Target:     corev1.ObjectReference{Kind: "Node", Name: nodeName},
		}, metav1.CreateOptions{}); err != nil {
			return ProcessOutcome{}, fmt.Errorf("bind diagnostic pod: %w", err)
		}
		bound, err := pods.Get(ctx, handle, metav1.GetOptions{})
		if err != nil {
			return ProcessOutcome{}, err
		}
		if bound.UID != pod.UID || bound.Spec.NodeName != nodeName || bound.Status.Phase != corev1.PodPending {
			return ProcessOutcome{}, fmt.Errorf("binding did not preserve actual Pending pod identity")
		}
		pod = bound
	}
	if pod.Status.Phase != corev1.PodPending {
		return ProcessOutcome{}, fmt.Errorf("deletion requires the actual API-default Pending state")
	}
	type completion struct {
		result runtime.ProcessResult
		err    error
	}
	done := make(chan completion, 1)
	joined := false
	go func() { result, waitErr := process.Wait(ctx); done <- completion{result: result, err: waitErr} }()
	defer func() {
		cancel()
		if !joined {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				err = fmt.Errorf("watched process did not stop after fixture cancellation")
			}
		}
	}()
	select {
	case <-ready:
	case result := <-done:
		joined = true
		return ProcessOutcome{}, fmt.Errorf("process returned before establishing its real watch: %v", result.err)
	case <-ctx.Done():
		return ProcessOutcome{}, fmt.Errorf("wait for real watch handshake: %w", ctx.Err())
	}
	zero := int64(0)
	if err := pods.Delete(ctx, handle, metav1.DeleteOptions{GracePeriodSeconds: &zero, Preconditions: &metav1.Preconditions{UID: &pod.UID}}); err != nil {
		return ProcessOutcome{}, fmt.Errorf("delete watched pod: %w", err)
	}
	if _, err := pods.Get(ctx, handle, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		return ProcessOutcome{}, fmt.Errorf("watched pod was not removed: %v", err)
	}
	select {
	case result := <-done:
		joined = true
		return ProcessOutcome{Err: result.err, Message: errorMessage(result.err), ExitStatus: result.result.ExitStatus, Stderr: stderr.String(), NodeName: nodeName}, nil
	case <-ctx.Done():
		return ProcessOutcome{}, fmt.Errorf("wait for deletion result: %w", ctx.Err())
	}
}
