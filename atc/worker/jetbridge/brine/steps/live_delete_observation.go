package steps

import (
	"fmt"
	"io"
	"net/http"
	"sync"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
)

// This observes the actual DELETE sent by the production client. GetBody reads
// an independent copy; the original request, response and transport error are
// forwarded unchanged. A later pod read cannot prove requested grace: the API
// and kubelet may already have reduced it to zero.
type podDeleteObservation struct {
	next     http.RoundTripper
	path     string
	mu       sync.Mutex
	requests []observedPodDelete
}

type observedPodDelete struct {
	options metav1.DeleteOptions
	status  int
	err     error
}

func (t *podDeleteObservation) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodDelete || req.URL.Path != t.path {
		return t.next.RoundTrip(req)
	}
	entry := observedPodDelete{}
	if req.GetBody == nil {
		entry.err = fmt.Errorf("DELETE has no independently readable body")
	} else if body, err := req.GetBody(); err != nil {
		entry.err = err
	} else {
		payload, err := io.ReadAll(body)
		entry.err = err
		if err == nil {
			_, _, entry.err = scheme.Codecs.UniversalDeserializer().Decode(payload, nil, &entry.options)
		}
		if err := body.Close(); entry.err == nil {
			entry.err = err
		}
	}
	response, err := t.next.RoundTrip(req)
	if entry.err == nil {
		entry.err = err
	}
	if response != nil {
		entry.status = response.StatusCode
	}
	t.mu.Lock()
	t.requests = append(t.requests, entry)
	t.mu.Unlock()
	return response, err
}

func observePodDeletes(w WorkerReady, handle string) (WorkerReady, *podDeleteObservation, error) {
	config, err := liveKubernetesConfig()
	if err != nil {
		return w, nil, err
	}
	trace := &podDeleteObservation{path: "/api/v1/namespaces/" + w.Namespace + "/pods/" + handle}
	config.Wrap(func(next http.RoundTripper) http.RoundTripper { trace.next = next; return trace })
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return w, nil, err
	}
	w.Clientset = client
	return w.rebuild(), trace, nil
}

func (t *podDeleteObservation) requireOneImmediateDelete() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.requests) != 1 {
		return fmt.Errorf("expected one task pod DELETE, observed %d", len(t.requests))
	}
	r := t.requests[0]
	if r.err != nil {
		return fmt.Errorf("observe actual task pod DELETE: %w", r.err)
	}
	if r.status < 200 || r.status >= 300 {
		return fmt.Errorf("task pod DELETE returned HTTP %d", r.status)
	}
	if r.options.GracePeriodSeconds == nil {
		return fmt.Errorf("task pod DELETE omitted its grace period")
	}
	if *r.options.GracePeriodSeconds != 0 {
		return fmt.Errorf("task pod DELETE requested grace %d, want zero", *r.options.GracePeriodSeconds)
	}
	fmt.Printf("actual task deletion request: DELETE %s count=1 grace=0 HTTP %d; original request and response unchanged\n", t.path, r.status)
	return nil
}
