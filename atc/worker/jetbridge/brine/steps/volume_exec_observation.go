package steps

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync"

	"github.com/brine-dev/brine-go/pkg/brine"
	"k8s.io/client-go/rest"
)

// Observe only: every original request, response and transport error passes
// through unchanged. The observer neither executes commands nor supplies data.
type execObservation struct {
	mu       sync.Mutex
	requests []volumeExecRequest
}

type volumeExecObservation struct {
	execObservation
	bindings map[string]volumeExecBinding
	capture  SpanCapture
	wire     *volumeWireRoute
}

type volumeExecBinding struct {
	namespace, pod, container, mount string
}

type volumeExecRequest struct {
	method, path string
	query        url.Values
	status       int
	err          error
}

type volumeExecTransport struct {
	next  http.RoundTripper
	trace *execObservation
}

func (t *volumeExecTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	entry := volumeExecRequest{method: req.Method, path: req.URL.Path, query: req.URL.Query()}
	response, err := t.next.RoundTrip(req)
	entry.err = err
	if response != nil {
		entry.status = response.StatusCode
	}
	t.trace.mu.Lock()
	t.trace.requests = append(t.trace.requests, entry)
	t.trace.mu.Unlock()
	return response, err
}

func (t *execObservation) config(config *rest.Config) *rest.Config {
	copy := rest.CopyConfig(config)
	copy.Wrap(func(next http.RoundTripper) http.RoundTripper {
		return &volumeExecTransport{next: next, trace: t}
	})
	return copy
}

// requireNodeRead proves named-node resolution reached the actual API once.
func (t *execObservation) requireNodeRead(nodeName string) error {
	if t == nil {
		return fmt.Errorf("missing real node-read observation")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	count := 0
	for _, r := range t.requests {
		if r.path == "/api/v1/nodes/"+nodeName {
			if r.method != http.MethodGet || r.status != http.StatusOK || r.err != nil {
				return fmt.Errorf("real node read failed: %s %s HTTP %d: %v", r.method, r.path, r.status, r.err)
			}
			count++
		}
	}
	if count != 1 {
		return fmt.Errorf("expected one real node read, got %d", count)
	}
	return nil
}

// requireSupervisedExec reads the actual request, without replacing execution.
// Callers provide the expected quoted command independently of production.
func (t *execObservation) requireSupervisedExec(namespace, pod, command string, tty bool) error {
	if t == nil {
		return fmt.Errorf("supervised has no real exec observation")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.requests) != 1 {
		return fmt.Errorf("expected exactly one supervised exec request, observed %d", len(t.requests))
	}
	got := t.requests[0]
	route := "/api/v1/namespaces/" + namespace + "/pods/" + pod + "/exec"
	if got.err != nil || got.method != http.MethodPost || got.path != route || got.status != http.StatusSwitchingProtocols {
		return fmt.Errorf("supervised route: %s %s HTTP %d error=%v, want POST %s HTTP 101", got.method, got.path, got.status, got.err, route)
	}
	wantTTY := ""
	if tty {
		wantTTY = "true"
	}
	if got.query.Get("tty") != wantTTY || got.query.Get("stdin") != "" || got.query.Get("container") != "main" {
		return fmt.Errorf("supervised exec options: got %v, want tty=%q, nil stdin and main container", got.query, wantTTY)
	}
	argv := got.query["command"]
	if len(argv) != 3 || argv[0] != "sh" || argv[1] != "-c" || !strings.Contains(argv[2], command) || !strings.Contains(argv[2], "trap '' HUP") {
		return fmt.Errorf("supervised supervisor command %q does not preserve %q and HUP handling", argv, command)
	}

	return nil
}

func VolumeRouteDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		Assert[VolumeRead]("volume transfers retain their declared execution metadata",
			func(in VolumeRead, _ Args) error {
				if in.Err != nil {
					return fmt.Errorf("volume read failed: %w", in.Err)
				}
				if in.source == nil {
					return fmt.Errorf("volume metadata has no source")
				}
				if err := in.source.execTrace.requireTransferMetadata(); err != nil {
					return err
				}
				return in.source.execTrace.wire.requireAllChecked()
			}),
		brine.DefineCheck[VolumeSet]("the upload reaches volume {string} at {string}",
			func(in VolumeSet, p brine.Params, _ *brine.Recorder) error {
				if in.execTrace == nil {
					return fmt.Errorf("upload routing requires a real exec observation")
				}
				name, named := p.GetString(0)
				destination, addressed := p.GetString(1)
				if !named || !addressed {
					return fmt.Errorf("expected volume name and absolute destination")
				}
				return in.execTrace.requireUpload(name, destination)
			}),
		Assert[VolumeRead]("reading only {string} from volume {string} at {string} yields {string}",
			func(in VolumeRead, a Args) error {
				if in.Err != nil {
					return fmt.Errorf("preceding volume read failed: %w", in.Err)
				}
				if in.source == nil || in.source.execTrace == nil {
					return fmt.Errorf("file selection requires a real volume and exec observation")
				}
				member, name, mount, content := a.String(0), a.String(1), a.String(2), a.String(3)
				volume, err := in.source.volume(name)
				if err != nil {
					return err
				}
				trace := in.source.execTrace
				binding, found := trace.bindings[name]
				if !found {
					return fmt.Errorf("no real pod binding for volume %q", name)
				}
				trace.mu.Lock()
				before := len(trace.requests)
				trace.mu.Unlock()
				stream, err := volume.StreamOut(in.source.Ctx, member, nil)
				if err != nil {
					return fmt.Errorf("open raw selected-file stream: %w", err)
				}
				if stream == nil {
					return fmt.Errorf("selected-file read returned no stream")
				}
				data, readErr := io.ReadAll(stream)
				closeErr := stream.Close()
				if readErr != nil {
					return fmt.Errorf("read selected file: %w", readErr)
				}
				if closeErr != nil {
					return fmt.Errorf("close selected-file reader: %w", closeErr)
				}
				if err := in.source.requireVolumeBytes(name, member, "stdout", data); err != nil {
					return err
				}
				files, err := filesInTar(bytes.NewReader(data))
				if err != nil {
					return err
				}
				want := map[string]string{member: content}
				if !reflect.DeepEqual(files, want) {
					return fmt.Errorf("selected-file archive contains %v, want only %v", files, want)
				}
				trace.mu.Lock()
				calls := len(trace.requests) - before
				trace.mu.Unlock()
				if calls != 1 {
					return fmt.Errorf("selected-file read made %d exec requests, want one", calls)
				}
				return trace.requireDownloadMember(binding.namespace, binding.pod, mount, member)
			}),
	}
}

func (t *volumeExecObservation) requireUpload(name, destination string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	binding, found := t.bindings[name]
	if !found {
		return fmt.Errorf("no real pod binding for volume %q", name)
	}
	if len(t.requests) != 1 {
		return fmt.Errorf("expected one upload exec request, observed %d", len(t.requests))
	}
	got := t.requests[0]
	if got.err != nil {
		return fmt.Errorf("actual upload transport failed: %w", got.err)
	}
	path := "/api/v1/namespaces/" + binding.namespace + "/pods/" + binding.pod + "/exec"
	if got.method != http.MethodPost || got.path != path || got.status != http.StatusSwitchingProtocols {
		return fmt.Errorf("upload route: got %s %s HTTP %d, want POST %s HTTP 101", got.method, got.path, got.status, path)
	}
	want := url.Values{
		"container": {binding.container},
		"command":   {"sh", "-c", `mkdir -p -- "$1" && exec tar xf - -C "$1"`, "stream-in", destination},
		"stdin":     {"true"},
	}
	if !reflect.DeepEqual(got.query, want) {
		return fmt.Errorf("upload exec options: got %v, want %v", got.query, want)
	}
	fmt.Printf("observed one unchanged upload: POST %s HTTP %d container=%s destination=%s\n", path, got.status, binding.container, destination)
	return nil
}

// The last request must be the actual raw StreamOut call, not the task command.
func (t *execObservation) requireDownload(namespace, pod, mount string) error {
	return t.requireDownloadMember(namespace, pod, mount, ".")
}

func (t *execObservation) requireDownloadMember(namespace, pod, mount, member string) error {
	if t == nil {
		return fmt.Errorf("download has no real exec observation")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.requests) == 0 {
		return fmt.Errorf("download has no exec request")
	}
	got := t.requests[len(t.requests)-1]
	route := "/api/v1/namespaces/" + namespace + "/pods/" + pod + "/exec"
	want := url.Values{"container": {"main"}, "command": {"tar", "cf", "-", "-C", mount, member}, "stdout": {"true"}}
	if got.err != nil || got.method != http.MethodPost || got.path != route || got.status != http.StatusSwitchingProtocols || !reflect.DeepEqual(got.query, want) {
		return fmt.Errorf("download request: %s %s HTTP %d options=%v error=%v, want POST %s HTTP 101 options=%v", got.method, got.path, got.status, got.query, got.err, route, want)
	}
	return nil
}

// Compare metadata exported by the production executor against the declared
// fixture mounts and actual HTTP exec requests. No executor is replaced.
func (t *volumeExecObservation) requireTransferMetadata() error {
	if t == nil {
		return fmt.Errorf("volume metadata has no real exec observation")
	}
	t.mu.Lock()
	requests := append([]volumeExecRequest(nil), t.requests...)
	t.mu.Unlock()
	if len(requests) == 0 {
		return fmt.Errorf("volume metadata observed no transfer requests")
	}
	key := func(namespace, pod, container, purpose, mount string) string {
		return strings.Join([]string{namespace, pod, container, purpose, mount}, "\x00")
	}
	want := map[string]int{}
	for _, request := range requests {
		if request.err != nil || request.status != http.StatusSwitchingProtocols {
			return fmt.Errorf("metadata request did not execute successfully: status=%d error=%v", request.status, request.err)
		}
		var binding volumeExecBinding
		found := false
		for _, candidate := range t.bindings {
			if request.path == "/api/v1/namespaces/"+candidate.namespace+"/pods/"+candidate.pod+"/exec" {
				binding, found = candidate, true
				break
			}
		}
		if !found {
			return fmt.Errorf("transfer request has no declared pod/mount binding: %s", request.path)
		}
		argv := request.query["command"]
		if len(argv) == 0 {
			return fmt.Errorf("transfer request has no command")
		}
		var purpose string
		switch argv[0] {
		case "sh":
			purpose = "stream-in"
		case "tar":
			purpose = "stream-out"
		default:
			return fmt.Errorf("unexpected volume transfer command %q", argv)
		}
		want[key(binding.namespace, binding.pod, binding.container, purpose, binding.mount)]++
	}
	spans, err := t.capture.spans()
	if err != nil {
		return err
	}
	got := map[string]int{}
	for _, span := range spans {
		if span.Name != "k8s.spdy.exec" {
			continue
		}
		attrs := map[string]string{}
		for _, attr := range span.Attributes {
			if _, duplicate := attrs[attr.Key]; duplicate {
				return fmt.Errorf("exec span repeats attribute %q", attr.Key)
			}
			attrs[attr.Key] = attr.Value.StringValue
		}
		// The independent hostname probe is not a volume transfer. If a
		// transfer falsely claims this purpose, the missing count still fails.
		if attrs["exec.purpose"] == "volume-premise" {
			continue
		}
		got[key(attrs["namespace"], attrs["pod-name"], attrs["container-name"],
			attrs["exec.purpose"], attrs["volume.mount_path"])]++
	}
	if !reflect.DeepEqual(got, want) {
		return fmt.Errorf("exported volume execution metadata: got %q, want %q", got, want)
	}
	return nil
}

// requireRuntimeAttempts counts actual requests, including failed exec dials.
// Fixture clients are deliberately separate from the observed runtime client.
func (t *execObservation) requireRuntimeAttempts(namespace, pod string, creates, execs int) error {
	if t == nil {
		return fmt.Errorf("missing runtime request observation")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	gotCreates, gotExecs := 0, 0
	observedPodRead := false
	path := "/api/v1/namespaces/" + namespace + "/pods"
	for _, r := range t.requests {
		if r.method == http.MethodGet && r.path == path+"/"+pod {
			observedPodRead = true
		}
		if r.method != http.MethodPost {
			continue
		}
		if r.path == path && r.status == http.StatusCreated && r.err == nil {
			gotCreates++
		}
		if r.path == path+"/"+pod+"/exec" {
			gotExecs++
		}
	}
	if !observedPodRead {
		return fmt.Errorf("runtime observation contains no read of pod %s", pod)
	}
	if gotCreates != creates || gotExecs != execs {
		return fmt.Errorf("runtime made %d pod creations and %d exec attempts, want %d and %d", gotCreates, gotExecs, creates, execs)
	}
	return nil
}
