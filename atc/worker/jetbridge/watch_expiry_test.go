package jetbridge

// An expired watch is terminal: the apiserver has thrown away the history the
// watch was resuming from, and reconnecting at the same resourceVersion can
// only ever be told the same thing again. The recovery is to re-read the pod
// and watch from its resourceVersion if it advanced, preserving that checkpoint.
// If it is unchanged, omit resourceVersion to resume from most recent.
//
// The apiserver says "expired" in two different places, so both are driven
// here against a real HTTP server speaking the watch wire format, through a
// real client-go clientset:
//
//   - as an ERROR event on an established stream (a 410 Status frame), which
//     is what a stream outliving the etcd history window gets, and
//   - as a 410 response to the watch request itself, which is what resuming
//     from an already-expired resourceVersion gets.
//
// A stub apiserver rather than client-go's fake clientset: the fake has no
// resourceVersion semantics and cannot answer a watch request with a status
// code at all, so neither branch would be reachable through it. Both branches
// also turn on client-go's own decoding of the 410 (apierrors.IsResourceExpired
// over what Watch() returned, apierrors.FromObject over the event payload),
// which a hand-built watch.Event would bypass.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const (
	expiryTestNamespace = "watch-expiry-ns"
	expiryTestPod       = "watch-expiry-pod"
)

// watchStubAPIServer is the pod subset of the core/v1 REST surface: GET on a
// named pod, and a watch over the collection. Each watch request is handed to
// respond along with its zero-based attempt number, so a test can script what
// the apiserver does the first time and what it does afterwards.
type watchStubAPIServer struct {
	t       *testing.T
	respond func(attempt int, w http.ResponseWriter, r *http.Request)

	mu sync.Mutex
	// resourceVersion is what the next GET reports. Bumping it lets a test
	// tell "re-watched from the version the recovery read" apart from
	// "re-watched from the version that just expired".
	resourceVersion string
	getCalls        int
	watchedVersions []string
}

func (s *watchStubAPIServer) snapshot() (getCalls int, watched []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getCalls, append([]string(nil), s.watchedVersions...)
}

func (s *watchStubAPIServer) setResourceVersion(rv string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resourceVersion = rv
}

func (s *watchStubAPIServer) podJSON(resourceVersion string) []byte {
	s.t.Helper()
	pod := &corev1.Pod{
		TypeMeta: metav1.TypeMeta{Kind: "Pod", APIVersion: "v1"},
		ObjectMeta: metav1.ObjectMeta{
			Name:            expiryTestPod,
			Namespace:       expiryTestNamespace,
			ResourceVersion: resourceVersion,
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	raw, err := json.Marshal(pod)
	if err != nil {
		s.t.Fatalf("marshal pod: %v", err)
	}
	return raw
}

func (s *watchStubAPIServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	collection := "/api/v1/namespaces/" + expiryTestNamespace + "/pods"
	switch {
	case r.URL.Path == collection+"/"+expiryTestPod:
		s.mu.Lock()
		s.getCalls++
		rv := s.resourceVersion
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(s.podJSON(rv))

	case r.URL.Path == collection && r.URL.Query().Get("watch") == "true":
		s.mu.Lock()
		attempt := len(s.watchedVersions)
		s.watchedVersions = append(s.watchedVersions, r.URL.Query().Get("resourceVersion"))
		s.mu.Unlock()
		s.respond(attempt, w, r)

	default:
		http.Error(w, "unexpected request "+r.Method+" "+r.URL.String(), http.StatusNotFound)
	}
}

// startWatchStream writes the 200 that opens a watch stream. The apiserver
// sends chunked JSON watch events on it, so the handler must flush each one.
func startWatchStream(t *testing.T, w http.ResponseWriter) func(eventType string, object []byte) {
	t.Helper()
	flusher, ok := w.(http.Flusher)
	if !ok {
		t.Fatal("the test server's ResponseWriter cannot flush, so no watch event can be delivered")
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	return func(eventType string, object []byte) {
		_, _ = fmt.Fprintf(w, `{"type":%q,"object":%s}`, eventType, object)
		flusher.Flush()
	}
}

// expiredStatusJSON is the 410 the apiserver sends when the resourceVersion a
// watch is resuming from has fallen out of the history window.
func expiredStatusJSON() []byte {
	return []byte(`{"kind":"Status","apiVersion":"v1","status":"Failure",` +
		`"message":"too old resource version: 100 (900)","reason":"Expired","code":410}`)
}

func newWatchStubClient(t *testing.T, server *watchStubAPIServer) kubernetes.Interface {
	t.Helper()
	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)

	clientset, err := kubernetes.NewForConfig(&rest.Config{Host: httpServer.URL})
	if err != nil {
		t.Fatalf("build a clientset against the stub apiserver: %v", err)
	}
	return clientset
}

// TestPodWatcherRecoversFromAnExpiredErrorEvent drives the 410 Gone/Expired
// Status frame that arrives as a watch.Error event on an established stream.
//
// Without watch.go's Error-event branch the Status is not a *corev1.Pod, so it
// falls through to the non-pod skip and the watcher goes back to reading the
// same dead stream forever: Next() never returns and the only thing the caller
// sees is its own deadline.
func TestPodWatcherRecoversFromAnExpiredErrorEvent(t *testing.T) {
	server := &watchStubAPIServer{t: t, resourceVersion: "100"}
	server.respond = func(attempt int, w http.ResponseWriter, r *http.Request) {
		if attempt == 0 {
			send := startWatchStream(t, w)
			// The history this stream was resuming from is gone.
			send("ERROR", expiredStatusJSON())
			// The real apiserver leaves the connection for the client to
			// drop; so does this, so that a watcher which ignores the error
			// blocks on it exactly as it would in production.
			<-r.Context().Done()
			return
		}
		if r.URL.Query().Get("resourceVersion") == "100" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusGone)
			_, _ = w.Write(expiredStatusJSON())
			return
		}
		send := startWatchStream(t, w)
		if r.URL.Query().Get("resourceVersion") == "200" {
			send("MODIFIED", server.podJSON("201"))
		}
		<-r.Context().Done()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	watcher := NewPodWatcher(newWatchStubClient(t, server), expiryTestNamespace, expiryTestPod)
	defer watcher.Stop()

	pod, err := watcher.Next(ctx)
	if err != nil {
		t.Fatalf("initial sync: Next() = %v, want the pod", err)
	}
	if pod.ResourceVersion != "100" {
		t.Fatalf("initial sync returned resourceVersion %q, want 100", pod.ResourceVersion)
	}

	// The pod moved on while the stream was expired; the recovery Get is the
	// only thing that can see it.
	server.setResourceVersion("200")

	pod, err = watcher.Next(ctx)
	if err != nil {
		t.Fatalf("after an Expired error event: Next() = %v, want the refreshed pod", err)
	}
	if pod.ResourceVersion != "200" {
		t.Errorf("after an Expired error event: resourceVersion %q, want 200 (the recovery Get's)", pod.ResourceVersion)
	}

	getCalls, watched := server.snapshot()
	if getCalls != 2 {
		t.Errorf("GET on the pod happened %d time(s), want 2 (initial sync + expiry recovery)", getCalls)
	}
	if len(watched) != 1 || watched[0] != "100" {
		t.Errorf("watch attempts asked for resourceVersions %v, want exactly [100]", watched)
	}

	// Recovery is not a one-shot exit: the watcher keeps going, and the
	// re-watch resumes from the advanced checkpoint the recovery Get observed.
	pod, err = watcher.Next(ctx)
	if err != nil {
		t.Fatalf("after recovery: Next() = %v, want the next watch event", err)
	}
	if pod.ResourceVersion != "201" {
		t.Errorf("after recovery: resourceVersion %q, want 201", pod.ResourceVersion)
	}
	_, watched = server.snapshot()
	if len(watched) != 2 || watched[1] != "200" {
		t.Errorf("watch attempts asked for resourceVersions %v, want the re-watch to resume from 200 (the recovery Get's)", watched)
	}
}

// TestPodWatcherRecoversFromAnExpiredWatchRequest drives the other place the
// apiserver says it: a 410 answer to the watch request itself, which is what
// resuming from an already-expired resourceVersion gets.
//
// Without watch.go's IsResourceExpired check on the WatchPod error, an expired
// version is treated as one more transient API error and retried at that same
// version — up to maxConsecutiveAPIErrors times — before the generic fallback
// reads the pod. The retry cannot succeed and delays the refreshed pod.
func TestPodWatcherRecoversFromAnExpiredWatchRequest(t *testing.T) {
	server := &watchStubAPIServer{t: t, resourceVersion: "100"}
	server.respond = func(_ int, w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("resourceVersion") == "100" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusGone)
			_, _ = w.Write(expiredStatusJSON())
			return
		}
		send := startWatchStream(t, w)
		if r.URL.Query().Get("resourceVersion") == "200" {
			send("MODIFIED", server.podJSON("201"))
		}
		<-r.Context().Done()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	watcher := NewPodWatcher(newWatchStubClient(t, server), expiryTestNamespace, expiryTestPod)
	defer watcher.Stop()

	if _, err := watcher.Next(ctx); err != nil {
		t.Fatalf("initial sync: Next() = %v, want the pod", err)
	}
	server.setResourceVersion("200")

	pod, err := watcher.Next(ctx)
	if err != nil {
		t.Fatalf("after an Expired watch request: Next() = %v, want the refreshed pod", err)
	}
	if pod.ResourceVersion != "200" {
		t.Errorf("after an Expired watch request: resourceVersion %q, want 200 (the recovery Get's)", pod.ResourceVersion)
	}

	getCalls, watched := server.snapshot()
	if len(watched) != 1 {
		t.Errorf("the expired watch was attempted %d times (%v), want exactly 1: retrying at an expired "+
			"resourceVersion can only be refused again", len(watched), watched)
	}
	if getCalls != 2 {
		t.Errorf("GET on the pod happened %d time(s), want 2 (initial sync + expiry recovery)", getCalls)
	}

	pod, err = watcher.Next(ctx)
	if err != nil {
		t.Fatalf("after recovery: Next() = %v, want the next watch event", err)
	}
	if pod.ResourceVersion != "201" {
		t.Errorf("after recovery: resourceVersion %q, want 201", pod.ResourceVersion)
	}
	_, watched = server.snapshot()
	if len(watched) != 2 || watched[1] != "200" {
		t.Errorf("watch attempts asked for resourceVersions %v, want the re-watch to resume from 200 (the recovery Get's)", watched)
	}
}

// A watch error that is NOT an expiry keeps its old meaning: it is not a pod,
// so it is skipped and the stream is read on. Without this arm the two tests
// above would be satisfied by a watcher that bailed out to a Get on any error
// event at all.
func TestPodWatcherKeepsReadingAfterANonExpiryErrorEvent(t *testing.T) {
	server := &watchStubAPIServer{t: t, resourceVersion: "100"}
	server.respond = func(attempt int, w http.ResponseWriter, r *http.Request) {
		send := startWatchStream(t, w)
		if attempt == 0 {
			send("ERROR", []byte(`{"kind":"Status","apiVersion":"v1","status":"Failure",`+
				`"message":"an internal error","reason":"InternalError","code":500}`))
			send("MODIFIED", server.podJSON("101"))
		}
		<-r.Context().Done()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	watcher := NewPodWatcher(newWatchStubClient(t, server), expiryTestNamespace, expiryTestPod)
	defer watcher.Stop()

	if _, err := watcher.Next(ctx); err != nil {
		t.Fatalf("initial sync: Next() = %v, want the pod", err)
	}

	pod, err := watcher.Next(ctx)
	if err != nil {
		t.Fatalf("after a non-expiry error event: Next() = %v, want the event that followed it", err)
	}
	if pod.ResourceVersion != "101" {
		t.Errorf("after a non-expiry error event: resourceVersion %q, want 101", pod.ResourceVersion)
	}

	getCalls, watched := server.snapshot()
	if getCalls != 1 {
		t.Errorf("GET on the pod happened %d time(s), want 1: a non-expiry error is not a reason to re-read", getCalls)
	}
	if len(watched) != 1 {
		t.Errorf("the watch was re-established %d time(s) (%v), want 1: the stream was still good",
			len(watched)-1, watched)
	}
}

// Guard against the stub drifting from the paths the production code calls:
// a typo in either would turn every assertion above into a 404.
func TestWatchStubAPIServerServesThePathsTheWatcherUses(t *testing.T) {
	server := &watchStubAPIServer{t: t, resourceVersion: "7"}
	server.respond = func(_ int, w http.ResponseWriter, r *http.Request) {
		send := startWatchStream(t, w)
		send("MODIFIED", server.podJSON("8"))
		<-r.Context().Done()
	}
	clientset := newWatchStubClient(t, server)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	pod, err := clientset.CoreV1().Pods(expiryTestNamespace).Get(ctx, expiryTestPod, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get against the stub: %v", err)
	}
	if pod.Name != expiryTestPod || pod.ResourceVersion != "7" {
		t.Fatalf("Get returned %s@%s, want %s@7", pod.Name, pod.ResourceVersion, expiryTestPod)
	}

	w, err := WatchPod(ctx, clientset, expiryTestNamespace, expiryTestPod, "7")
	if err != nil {
		t.Fatalf("WatchPod against the stub: %v", err)
	}
	defer w.Stop()
	select {
	case event := <-w.ResultChan():
		got, ok := event.Object.(*corev1.Pod)
		if !ok || got.ResourceVersion != "8" {
			t.Fatalf("watch event = %#v, want a pod at resourceVersion 8", event.Object)
		}
	case <-ctx.Done():
		t.Fatal("no watch event arrived from the stub")
	}

	_, watched := server.snapshot()
	if len(watched) != 1 || watched[0] != "7" {
		t.Fatalf("watch request carried resourceVersions %v, want [7]", watched)
	}
}

// TestPodWatcherDoesNotResumeFromAnUnchangedPodsExpiredVersion drives the
// case the two tests above cannot: the pod has NOT changed since the history
// expired. Its own resourceVersion is still the one the apiserver just called
// too old, so a watcher that resumes from what the recovery Get reports gets
// another 410, reads the same pod again, and hands it to the caller again,
// as fast as the loop turns. Resuming from most recent breaks the loop.
func TestPodWatcherDoesNotResumeFromAnUnchangedPodsExpiredVersion(t *testing.T) {
	server := &watchStubAPIServer{t: t, resourceVersion: "100"}
	server.respond = func(attempt int, w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("resourceVersion") == "100" {
			// Anything resuming from the pod's own version is too old.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusGone)
			_, _ = w.Write(expiredStatusJSON())
			return
		}
		send := startWatchStream(t, w)
		send("MODIFIED", server.podJSON("301"))
		<-r.Context().Done()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	watcher := NewPodWatcher(newWatchStubClient(t, server), expiryTestNamespace, expiryTestPod)
	defer watcher.Stop()

	pod, err := watcher.Next(ctx)
	if err != nil || pod.ResourceVersion != "100" {
		t.Fatalf("initial sync: Next() = (%v, %v), want the pod at 100", pod, err)
	}

	// The watch from 100 is refused; the recovery read returns the unchanged
	// pod, still at 100.
	pod, err = watcher.Next(ctx)
	if err != nil || pod.ResourceVersion != "100" {
		t.Fatalf("after the expired watch: Next() = (%v, %v), want the recovery read of the pod at 100", pod, err)
	}

	// The next watch must not ask for 100 again.
	pod, err = watcher.Next(ctx)
	if err != nil {
		t.Fatalf("after recovery: Next() = %v, want the next watch event", err)
	}
	if pod.ResourceVersion != "301" {
		t.Fatalf("Next() returned the pod at %q, want 301: the watch never resumed from a version the apiserver accepts", pod.ResourceVersion)
	}

	reads, watched := server.snapshot()
	if reads != 2 {
		t.Errorf("the pod was read %d time(s), want 2 (initial sync + one recovery)", reads)
	}
	if len(watched) != 2 || watched[0] != "100" || watched[1] != "" {
		t.Errorf("watch attempts asked for resourceVersions %v, want [100 \"\"]: once from the pod's version, then from most recent", watched)
	}
}
