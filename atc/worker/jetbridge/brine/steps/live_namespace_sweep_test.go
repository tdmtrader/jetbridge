package steps

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// These tests exercise disposal ordering and polling, not Kubernetes behavior.
// Use the real client's HTTP boundary: the module guard prohibits fake clientsets.
type namespaceSweepTransport func(*http.Request) (*http.Response, error)

func (f namespaceSweepTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func namespaceSweepClient(t *testing.T, respond func(*http.Request) (int, any)) kubernetes.Interface {
	t.Helper()
	client, err := kubernetes.NewForConfig(&rest.Config{
		Host:          "https://namespace-cleanup.invalid",
		QPS:           -1, // Short test polling budgets must not exercise client throttling.
		ContentConfig: rest.ContentConfig{ContentType: "application/json"},
		Transport: namespaceSweepTransport(func(req *http.Request) (*http.Response, error) {
			code, body := respond(req)
			if status, ok := body.(*metav1.Status); ok {
				status.TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "Status"}
			}
			data, err := json.Marshal(body)
			if err != nil {
				return nil, err
			}
			return &http.Response{StatusCode: code, Header: http.Header{"Content-Type": {"application/json"}},
				Body: io.NopCloser(bytes.NewReader(data)), Request: req}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func namespaceSweepNotFound() (int, any) {
	return http.StatusNotFound, &metav1.Status{Status: metav1.StatusFailure, Reason: metav1.StatusReasonNotFound, Code: http.StatusNotFound}
}

func TestLiveNamespaceDisposerRequestsPodDeletionFirstWithoutWaiting(t *testing.T) {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "owned", UID: "owned-uid"}}
	var actions []string
	namespaceGets := 0
	var deletionDeadline time.Time
	client := namespaceSweepClient(t, func(req *http.Request) (int, any) {
		action := req.Method + " " + req.URL.Path
		actions = append(actions, action)
		if req.Method == http.MethodDelete {
			deadline, ok := req.Context().Deadline()
			if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 10*time.Second {
				t.Fatalf("deletion request missing bounded 10s context: %v", deadline)
			}
			if deletionDeadline.IsZero() {
				deletionDeadline = deadline
			} else if deadline.Equal(deletionDeadline) {
				t.Fatal("deletion requests share one deadline")
			}
		}
		switch action {
		case "DELETE /api/v1/namespaces/owned/pods":
			var opts metav1.DeleteOptions
			if err := json.NewDecoder(req.Body).Decode(&opts); err != nil {
				t.Fatal(err)
			}
			if opts.Preconditions != nil || opts.GracePeriodSeconds == nil || *opts.GracePeriodSeconds != 1 {
				t.Fatalf("pod collection delete options: %+v", opts)
			}
			return http.StatusOK, &metav1.Status{Status: metav1.StatusSuccess}
		case "DELETE /api/v1/namespaces/owned":
			var opts metav1.DeleteOptions
			if err := json.NewDecoder(req.Body).Decode(&opts); err != nil {
				t.Fatal(err)
			}
			if opts.Preconditions == nil || opts.Preconditions.UID == nil || *opts.Preconditions.UID != ns.UID {
				t.Fatalf("namespace delete lost UID precondition: %+v", opts)
			}
			return http.StatusOK, ns // deletion accepted; namespace still exists
		case "GET /api/v1/namespaces/owned":
			namespaceGets++
			if namespaceGets == 1 {
				return http.StatusOK, ns
			}
			return namespaceSweepNotFound()
		default:
			t.Fatalf("unexpected request: %s", action)
			return 0, nil
		}
	})
	var registry liveNamespaceRegistry
	if err := registry.dispose(client, ns); err != nil {
		t.Fatal(err)
	}
	want := []string{"DELETE /api/v1/namespaces/owned/pods", "DELETE /api/v1/namespaces/owned"}
	if !reflect.DeepEqual(actions, want) {
		t.Fatalf("disposer must request pod deletion then namespace deletion without polling: %v", actions)
	}
	if len(registry.namespaces) != 1 || registry.namespaces[0].uid != ns.UID {
		t.Fatalf("deleted namespace not recorded: %+v", registry.namespaces)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var out bytes.Buffer
	registry.sweep(ctx, time.Millisecond, &out)
	if namespaceGets != 2 || out.String() != "removed owned live namespace owned (owned-uid)\n" {
		t.Fatalf("sweep did not wait for NotFound: gets=%d output=%q", namespaceGets, out.String())
	}
	registry.sweep(ctx, time.Millisecond, &out)
	if namespaceGets != 2 {
		t.Fatal("swept a namespace twice")
	}
}

func TestLiveNamespaceSweepReportsSurvivorsWithinOneBudget(t *testing.T) {
	TakeDisposalFailures()
	t.Cleanup(func() { TakeDisposalFailures() })
	gets := make(map[string]int)
	client := namespaceSweepClient(t, func(req *http.Request) (int, any) {
		name := strings.TrimPrefix(req.URL.Path, "/api/v1/namespaces/")
		gets[name]++
		if name == "gone" {
			return namespaceSweepNotFound()
		}
		if name == "unreachable" {
			return http.StatusForbidden, &metav1.Status{Status: metav1.StatusFailure, Reason: metav1.StatusReasonForbidden,
				Code: http.StatusForbidden, Message: "namespace lookup forbidden"}
		}
		return http.StatusOK, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(name + "-uid")}}
	})
	registry := liveNamespaceRegistry{namespaces: []*ownedLiveNamespace{
		{name: "survivor", uid: "survivor-uid", client: client, deleteLanded: true},
		{name: "gone", uid: "gone-uid", client: client, deleteLanded: true},
		{name: "unreachable", uid: "unreachable-uid", client: client, deleteLanded: true},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	var out bytes.Buffer
	registry.sweep(ctx, time.Millisecond, &out)
	if gets["gone"] != 1 || gets["survivor"] < 2 || gets["unreachable"] < 2 {
		t.Fatalf("sweep did not poll all outstanding namespaces: %v", gets)
	}
	if !strings.Contains(out.String(), "removed owned live namespace gone (gone-uid)\n") {
		t.Fatalf("incorrect removal report: %q", out.String())
	}
	failures := TakeDisposalFailures()
	if len(failures) != 2 {
		t.Fatalf("want two disposal failures, got %v", failures)
	}
	for i, name := range []string{"survivor", "unreachable"} {
		if !strings.Contains(failures[i], fmt.Sprintf("%s (%s-uid)", name, name)) ||
			!strings.Contains(failures[i], "context deadline exceeded") {
			t.Errorf("failure lost namespace identity or deadline: %s", failures[i])
		}
	}
	if !strings.Contains(failures[1], "forbidden") {
		t.Errorf("failure lost lookup error: %s", failures[1])
	}
}

func TestLiveNamespacePodDeletionFailureStillDeletesNamespace(t *testing.T) {
	deleted := false
	client := namespaceSweepClient(t, func(req *http.Request) (int, any) {
		if strings.HasSuffix(req.URL.Path, "/pods") {
			return http.StatusForbidden, &metav1.Status{Status: metav1.StatusFailure, Reason: metav1.StatusReasonForbidden, Code: http.StatusForbidden}
		}
		deleted = req.Method == http.MethodDelete
		return namespaceSweepNotFound()
	})
	var registry liveNamespaceRegistry
	if err := registry.dispose(client, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "owned", UID: "uid"}}); err != nil {
		t.Fatal(err)
	}
	if !deleted || len(registry.namespaces) != 1 {
		t.Fatal("pod deletion failure prevented namespace deletion and registration")
	}
}

func TestLiveNamespacePendingDeleteRetriesAfterIndependentRequestDeadlines(t *testing.T) {
	var registry liveNamespaceRegistry
	deletes := 0
	client := namespaceSweepClient(t, func(req *http.Request) (int, any) {
		if len(registry.namespaces) != 1 {
			t.Fatal("namespace was not registered before network I/O")
		}
		if strings.HasSuffix(req.URL.Path, "/pods") {
			<-req.Context().Done()
			return http.StatusGatewayTimeout, &metav1.Status{Reason: metav1.StatusReasonTimeout, Code: http.StatusGatewayTimeout}
		}
		if req.Context().Err() != nil {
			t.Fatal("namespace request inherited exhausted pod deadline")
		}
		if req.Method == http.MethodDelete {
			deletes++
			var opts metav1.DeleteOptions
			if err := json.NewDecoder(req.Body).Decode(&opts); err != nil {
				t.Fatal(err)
			}
			if opts.Preconditions == nil || opts.Preconditions.UID == nil || *opts.Preconditions.UID != "uid" {
				t.Fatalf("missing UID: %+v", opts)
			}
			if deletes == 1 {
				return http.StatusServiceUnavailable, &metav1.Status{Reason: metav1.StatusReasonServiceUnavailable, Code: http.StatusServiceUnavailable}
			}
		}
		return namespaceSweepNotFound()
	})
	if err := registry.dispose(client, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "owned", UID: "uid"}}); err == nil {
		t.Fatal("expected initial delete failure")
	}
	if len(registry.namespaces) != 1 || registry.namespaces[0].deleteLanded {
		t.Fatal("failed deletion was lost or marked landed")
	}
	// The sweep takes the pending list out of the registry while working.
	original := registry.namespaces[0]
	original.client = namespaceSweepClient(t, func(req *http.Request) (int, any) {
		if req.Method != http.MethodDelete {
			t.Fatalf("NotFound delete must finish without GET: %s", req.Method)
		}
		deletes++
		var opts metav1.DeleteOptions
		if err := json.NewDecoder(req.Body).Decode(&opts); err != nil {
			t.Fatal(err)
		}
		if opts.Preconditions == nil || opts.Preconditions.UID == nil || *opts.Preconditions.UID != "uid" {
			t.Fatalf("retry missing UID: %+v", opts)
		}
		return namespaceSweepNotFound()
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := registry.sweep(ctx, time.Millisecond, io.Discard); err != nil {
		t.Fatal(err)
	}
	if deletes != 2 || len(registry.namespaces) != 0 {
		t.Fatalf("pending delete not recovered: deletes=%d pending=%d", deletes, len(registry.namespaces))
	}
}

func TestStartNamespaceSweepRequiresAttributionAndAgeWithoutWaiting(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	makeNS := func(name string, age time.Duration, owned bool) corev1.Namespace {
		ns := corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(name + "-uid"), CreationTimestamp: metav1.NewTime(now.Add(-age))}}
		if owned {
			ns.Labels = map[string]string{"app.kubernetes.io/managed-by": "brine-runtime-tests"}
		}
		return ns
	}
	var deleted []string
	client := namespaceSweepClient(t, func(req *http.Request) (int, any) {
		if req.Method == http.MethodGet {
			if req.URL.Path != "/api/v1/namespaces" {
				t.Fatalf("startup sweep waited on deletion: %s", req.URL.Path)
			}
			if req.URL.Query().Get("labelSelector") != liveNamespaceLabel {
				t.Fatal("list has no attribution selector")
			}
			return http.StatusOK, &corev1.NamespaceList{Items: []corev1.Namespace{
				makeNS("old", 31*time.Minute, true), makeNS("gone", time.Hour, true),
				makeNS("young", 29*time.Minute, true), makeNS("boundary", 30*time.Minute, true),
				makeNS("foreign", time.Hour, false), {ObjectMeta: metav1.ObjectMeta{Name: "unknown-age", Labels: map[string]string{"app.kubernetes.io/managed-by": "brine-runtime-tests"}}},
			}}
		}
		name := strings.TrimPrefix(req.URL.Path, "/api/v1/namespaces/")
		deleted = append(deleted, name)
		var opts metav1.DeleteOptions
		if err := json.NewDecoder(req.Body).Decode(&opts); err != nil {
			t.Fatal(err)
		}
		if opts.Preconditions == nil || opts.Preconditions.UID == nil || string(*opts.Preconditions.UID) != name+"-uid" {
			t.Fatalf("unsafe stale delete: %+v", opts)
		}
		if name == "gone" {
			return namespaceSweepNotFound()
		}
		return http.StatusOK, &metav1.Status{Status: metav1.StatusSuccess}
	})
	var out bytes.Buffer
	if err := sweepStaleLiveNamespaces(context.Background(), client, now, &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(deleted, []string{"old", "gone"}) || !strings.Contains(out.String(), "old (old-uid)") || !strings.Contains(out.String(), "gone (gone-uid)") {
		t.Fatalf("wrong stale sweep: %v %s", deleted, &out)
	}
}

func resetNamespaceSweep(t *testing.T) {
	t.Helper()
	liveNamespaces = liveNamespaceRegistry{}
	TakeDisposalFailures()
	t.Cleanup(func() { liveNamespaces = liveNamespaceRegistry{}; TakeDisposalFailures() })
}

// Exercise the production config/client path without a cluster or fake clientset.
func namespaceSweepServer(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	config := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(config, []byte(fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: sweep
  cluster:
    server: %s
contexts:
- name: sweep
  context:
    cluster: sweep
current-context: sweep
`, server.URL)), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", config)
	t.Setenv("BRINE_KUBE_CONTEXT", "sweep")
}

func TestNamespaceSweepWithoutLiveNamespaceMakesNoAPIRequests(t *testing.T) {
	resetNamespaceSweep(t)
	var requests atomic.Int32
	namespaceSweepServer(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "API unavailable", http.StatusForbidden)
	})
	resources, err := brine.NewResourceRegistry([]brine.ResourceDefinition{LiveNamespaceResourceDefinition()})
	if err != nil {
		t.Fatal(err)
	}
	given := brine.DefineMap[brine.Empty, brine.Empty]("a local step", func(brine.Empty, brine.Params, *brine.Recorder) (brine.Empty, error) { return brine.Empty{}, nil })
	pipeline := brine.NewPipeline(brine.NewStepRegistry([]brine.StepDefinition{given}), brine.NewEmitter(io.Discard)).WithResources(brine.NewResourceState(resources))
	feature := brine.ParseFeatureText("local.feature", "Feature: local\n  Scenario: no live namespace\n    Given a local step\n")
	result, _, err := pipeline.Run([]*brine.ParsedFeature{feature}, brine.TagFilter{})
	if err != nil || result.Passed != 1 || result.Failed != 0 {
		t.Fatalf("local run failed: %+v %v", result, err)
	}
	if err := SweepLiveNamespaces(); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 0 {
		t.Fatalf("local run made %d API requests", requests.Load())
	}
	// Even an unusable configuration cannot fail suite acquisition.
	t.Setenv("BRINE_KUBE_CONTEXT", "missing-context")
	definition := LiveNamespaceResourceDefinition()
	value, err := definition.Factory(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := definition.Disposer(value); err != nil {
		t.Fatal(err)
	}
}

func TestNamespaceStaleSweepRunsOnceOnFirstCreationAndIsBestEffort(t *testing.T) {
	for _, failure := range []string{"none", "list", "delete"} {
		t.Run(failure, func(t *testing.T) {
			resetNamespaceSweep(t)
			var creates, lists, deletes atomic.Int32
			namespaceSweepServer(t, func(w http.ResponseWriter, req *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				var body any
				switch {
				case req.Method == http.MethodPost && req.URL.Path == "/api/v1/namespaces":
					n := creates.Add(1)
					body = &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("owned-%d", n), UID: types.UID(fmt.Sprintf("uid-%d", n))}}
				case req.Method == http.MethodGet && req.URL.Path == "/api/v1/namespaces":
					lists.Add(1)
					if creates.Load() != 1 || req.URL.Query().Get("labelSelector") != liveNamespaceLabel {
						t.Errorf("stale sweep not on first creation with attribution: creates=%d URL=%s", creates.Load(), req.URL)
					}
					if failure == "list" {
						w.WriteHeader(http.StatusForbidden)
						body = &metav1.Status{Status: metav1.StatusFailure, Reason: metav1.StatusReasonForbidden, Code: http.StatusForbidden, Message: "list denied"}
					} else {
						body = &corev1.NamespaceList{Items: []corev1.Namespace{
							{ObjectMeta: metav1.ObjectMeta{Name: "stale", UID: "stale-uid", Labels: map[string]string{"app.kubernetes.io/managed-by": "brine-runtime-tests"}, CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Hour))}},
						}}
					}
				case req.Method == http.MethodDelete && req.URL.Path == "/api/v1/namespaces/stale":
					deletes.Add(1)
					if failure == "delete" {
						w.WriteHeader(http.StatusForbidden)
						body = &metav1.Status{Status: metav1.StatusFailure, Reason: metav1.StatusReasonForbidden, Code: http.StatusForbidden, Message: "delete denied"}
					} else {
						body = &metav1.Status{Status: metav1.StatusSuccess}
					}
				case req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/resourcequotas"):
					body = &corev1.ResourceQuota{}
				case req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/limitranges"):
					body = &corev1.LimitRange{}
				case req.Method == http.MethodDelete:
					body = &metav1.Status{Status: metav1.StatusSuccess}
				default:
					t.Errorf("unexpected request: %s %s", req.Method, req.URL)
					w.WriteHeader(http.StatusNotFound)
				}
				if status, ok := body.(*metav1.Status); ok {
					status.TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "Status"}
				}
				if err := json.NewEncoder(w).Encode(body); err != nil {
					t.Error(err)
				}
			})
			output, err := os.CreateTemp(t.TempDir(), "diagnostics")
			if err != nil {
				t.Fatal(err)
			}
			stderr := os.Stderr
			os.Stderr = output
			t.Cleanup(func() { os.Stderr = stderr; output.Close() })
			rec := new(brine.Recorder)
			defer func() {
				for _, dispose := range rec.DrainDisposers() {
					dispose()
				}
			}()
			for i := 0; i < 2; i++ {
				if _, err := newLiveKubernetes(context.Background(), rec); err != nil {
					t.Fatalf("live namespace creation failed: %v", err)
				}
			}
			if creates.Load() != 2 || lists.Load() != 1 || (failure != "list" && deletes.Load() != 1) {
				t.Fatalf("creates=%d lists=%d stale deletes=%d", creates.Load(), lists.Load(), deletes.Load())
			}
			diagnostics, err := os.ReadFile(output.Name())
			if err != nil {
				t.Fatal(err)
			}
			if failure == "none" {
				if len(diagnostics) != 0 {
					t.Fatalf("unexpected diagnostic: %s", diagnostics)
				}
			} else if !strings.HasPrefix(string(diagnostics), "stale live namespace sweep skipped: ") || strings.Count(string(diagnostics), "\n") != 1 || !strings.Contains(string(diagnostics), failure+" denied") {
				t.Fatalf("expected one diagnostic line: %q", diagnostics)
			}
			if failures := TakeDisposalFailures(); len(failures) != 0 {
				t.Fatalf("stale sweep changed run verdict: %v", failures)
			}
		})
	}
}

func TestNamespaceSuiteDisposalFailsRunEndProtocol(t *testing.T) {
	resetNamespaceSweep(t)
	t.Setenv("BRINE_KUBE_CONTEXT", "")
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "survivor", UID: "uid"}}
	client := namespaceSweepClient(t, func(*http.Request) (int, any) { return http.StatusOK, ns })
	liveNamespaces.namespaces = []*ownedLiveNamespace{{name: ns.Name, uid: ns.UID, client: client, deleteLanded: true}}
	liveNamespaces.boundWait(50 * time.Millisecond)
	definitions := ResourceDefinitions()
	if definitions[0].Name != "live-namespace-sweep" || definitions[0].Scope != brine.ScopeSuite {
		t.Fatal("namespace sweep must be first suite resource (last disposed)")
	}
	resources, err := brine.NewResourceRegistry(definitions[:1])
	if err != nil {
		t.Fatal(err)
	}
	given := brine.DefineMap[brine.Empty, brine.Empty]("a passing step", func(brine.Empty, brine.Params, *brine.Recorder) (brine.Empty, error) { return brine.Empty{}, nil })
	var events bytes.Buffer
	pipeline := brine.NewPipeline(brine.NewStepRegistry([]brine.StepDefinition{given}), brine.NewEmitter(WithDisposalVerdict(&events))).WithResources(brine.NewResourceState(resources))
	feature := brine.ParseFeatureText("features/live/cleanup.feature", "Feature: cleanup\n  Scenario: passes before disposal\n    Given a passing step\n")
	result, _, err := pipeline.Run([]*brine.ParsedFeature{feature}, brine.TagFilter{})
	if err != nil || result.Passed != 1 {
		t.Fatalf("scenario failed before cleanup: %+v %v", result, err)
	}
	var end brine.RunEnd
	for _, line := range bytes.Split(bytes.TrimSpace(events.Bytes()), []byte("\n")) {
		var event struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatal(err)
		}
		if event.Type == "run_end" {
			if err := json.Unmarshal(line, &end); err != nil {
				t.Fatal(err)
			}
		}
	}
	if end.Type != "run_end" || end.Failed != 1 || end.Passed != 1 || !strings.Contains(strings.Join(TakeDisposalFailures(), "\n"), "survivor (uid)") {
		t.Fatalf("survivor missing at protocol boundary: %s", &events)
	}
}

func TestNamespaceCancellationShortensAnActiveSweepAndBackstop(t *testing.T) {
	resetNamespaceSweep(t)
	if namespaceCancellationBudget != 3*time.Second {
		t.Fatal("signal sweep no longer fits engine grace")
	}
	requested := make(chan struct{})
	client := namespaceSweepClient(t, func(req *http.Request) (int, any) {
		select {
		case <-requested:
		default:
			close(requested)
		}
		<-req.Context().Done()
		return http.StatusGatewayTimeout, &metav1.Status{Reason: metav1.StatusReasonTimeout, Code: http.StatusGatewayTimeout}
	})
	liveNamespaces.namespaces = []*ownedLiveNamespace{{name: "terminating", uid: "uid", client: client, deleteLanded: true}}
	done := make(chan error, 1)
	go func() { done <- SweepLiveNamespaces() }()
	<-requested
	liveNamespaces.boundWait(50 * time.Millisecond)
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "terminating") {
			t.Fatalf("missing survivor: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("signal did not shorten active sweep")
	}
	start := time.Now()
	if err := SweepLiveNamespaces(); err == nil {
		t.Fatal("backstop lost survivor")
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("backstop bought a fresh cancellation budget")
	}
}
