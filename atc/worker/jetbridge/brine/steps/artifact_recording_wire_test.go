package steps

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/concourse/concourse/artifactwire"
	"github.com/concourse/concourse/atc/worker/jetbridge"
)

// These are inputs to the wire oracle, not replacements for a daemon. The
// oracle must accept both encodings of optional zero values without accepting
// a different registration, request sequence, or artifact location.
func TestArtifactRecordingWireContract(t *testing.T) {
	for _, cache := range []bool{false, true} {
		kind := "output"
		if cache {
			kind = "resource-cache"
		}
		t.Run(kind, func(t *testing.T) {
			for _, explicit := range []bool{false, true} {
				t.Run("optional-fields-explicit-"+strconv.FormatBool(explicit), func(t *testing.T) {
					in, register, _ := recordingContractInput(t, cache)
					if explicit {
						changeRecordingBody(t, &in.recording.requests[register], func(body map[string]any) {
							body["read_only"], body["durable_key"] = false, ""
						})
					}
					if err := in.requireRecording(); err != nil {
						t.Fatalf("valid recording rejected: %v", err)
					}
				})
			}

			badBodies := map[string]func(map[string]any){
				"wrong-key":          func(b map[string]any) { b["key"] = "another-artifact" },
				"missing-key":        func(b map[string]any) { delete(b, "key") },
				"key-type":           func(b map[string]any) { b["key"] = 42 },
				"wrong-local-path":   func(b map[string]any) { b["local_path"] = "/another/path" },
				"missing-local-path": func(b map[string]any) { delete(b, "local_path") },
				"read-only":          func(b map[string]any) { b["read_only"] = true },
				"read-only-type":     func(b map[string]any) { b["read_only"] = "false" },
				"read-only-null":     func(b map[string]any) { b["read_only"] = nil },
				"durable-key":        func(b map[string]any) { b["durable_key"] = "content/key" },
				"durable-key-type":   func(b map[string]any) { b["durable_key"] = false },
				"durable-key-null":   func(b map[string]any) { b["durable_key"] = nil },
				"unexpected-field":   func(b map[string]any) { b["unexpected"] = "value" },
			}
			for name, change := range badBodies {
				t.Run(name, func(t *testing.T) {
					in, register, _ := recordingContractInput(t, cache)
					changeRecordingBody(t, &in.recording.requests[register], change)
					if err := in.requireRecording(); err == nil {
						t.Fatal("invalid registration accepted")
					}
				})
			}

			badRecordings := map[string]func(*ArtifactCluster, int, int){
				"missing-request": func(in *ArtifactCluster, _, _ int) {
					in.recording.requests = in.recording.requests[:1]
				},
				"duplicate-registration": func(in *ArtifactCluster, register, mirror int) {
					in.recording.requests[mirror] = in.recording.requests[register]
				},
				"duplicate-mirror": func(in *ArtifactCluster, register, mirror int) {
					in.recording.requests[register] = in.recording.requests[mirror]
				},
				"extra-request": func(in *ArtifactCluster, register, _ int) {
					in.recording.requests = append(in.recording.requests, in.recording.requests[register])
				},
				"reversed-order": func(in *ArtifactCluster, _, _ int) {
					in.recording.requests[0], in.recording.requests[1] = in.recording.requests[1], in.recording.requests[0]
				},
				"wrong-route": func(in *ArtifactCluster, register, _ int) {
					in.recording.requests[register].Path = "/register/other"
				},
				"wrong-method": func(in *ArtifactCluster, register, _ int) {
					in.recording.requests[register].Method = http.MethodPut
				},
				"wrong-mirror-key": func(in *ArtifactCluster, _, mirror int) {
					in.recording.requests[mirror].Body = []byte(`{"key":"another/path"}`)
				},
				"malformed-json": func(in *ArtifactCluster, register, _ int) {
					in.recording.requests[register].Body = []byte(`{"key":`)
				},
				"missing-location": func(in *ArtifactCluster, _, _ int) {
					in.Locator.Remove("artifact-alias")
				},
				"wrong-node": func(in *ArtifactCluster, _, _ int) {
					loc, _ := in.Locator.Locate("artifact-alias")
					in.Locator.Record("artifact-alias", "another-node", loc.HostDir)
				},
				"wrong-host-directory": func(in *ArtifactCluster, _, _ int) {
					in.Locator.Record("artifact-alias", in.NodeName, "another/path")
				},
			}
			for name, change := range badRecordings {
				t.Run(name, func(t *testing.T) {
					in, register, mirror := recordingContractInput(t, cache)
					change(&in, register, mirror)
					if err := in.requireRecording(); err == nil {
						t.Fatal("invalid recording accepted")
					}
				})
			}
		})
	}
}

func recordingContractInput(t *testing.T, cache bool) (ArtifactCluster, int, int) {
	t.Helper()
	in := ArtifactCluster{
		Handle: "build-42/.", StoreRoot: "/artifact-store", NodeName: "producer",
		Outputs:         map[string]string{"result": "/work/result"},
		ExpectedVolumes: map[string]string{"/work/result": "artifact-alias"},
		Locator:         jetbridge.NewArtifactLocator(), recording: &artifactRecordingObservation{},
	}
	key, hostDir := "build-42/result", "build-42/result"
	register, mirror := 0, 1
	if cache {
		in.recording.cacheKey = "artifact-alias"
		key, hostDir = "build-42/dir", "artifact-alias"
		register, mirror = 1, 0
	}
	registrationBody, err := json.Marshal(map[string]any{
		"key": "artifact-alias", "local_path": filepath.Join(in.StoreRoot, "steps", key),
	})
	if err != nil {
		t.Fatal(err)
	}
	mirrorBody, err := json.Marshal(map[string]any{"key": key})
	if err != nil {
		t.Fatal(err)
	}
	in.recording.requests = make([]daemonWireRequest, 2)
	in.recording.requests[register] = daemonWireRequest{Method: http.MethodPost, Path: "/register", Body: registrationBody}
	in.recording.requests[mirror] = daemonWireRequest{Method: http.MethodPost, Path: "/mirror", Body: mirrorBody}
	in.Locator.Record("artifact-alias", in.NodeName, hostDir)
	return in, register, mirror
}

func changeRecordingBody(t *testing.T, request *daemonWireRequest, change func(map[string]any)) {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(request.Body, &body); err != nil {
		t.Fatal(err)
	}
	change(body)
	var err error
	request.Body, err = json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
}

func TestArtifactRecordingCapturesActionOnAnExistingConnection(t *testing.T) {
	d, err := startRealDaemon()
	if err != nil {
		t.Fatal(err)
	}
	defer d.stop()
	endpoint, err := url.Parse(d.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(endpoint.Port())
	if err != nil {
		t.Fatal(err)
	}
	var client *artifactwire.Client
	var clientErr error
	trace, err := observeDaemonConstruction(map[string]bool{endpoint.Host: true}, func() {
		client, clientErr = artifactwire.NewClient(port, artifactwire.TLS{})
	})
	if err != nil || clientErr != nil {
		t.Fatalf("constructing observed client: observation=%v client=%v", err, clientErr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Establish the connection before recording begins. Replacing only the
	// default transport around the action cannot see this client's traffic.
	if _, err := client.HeadArtifact(ctx, endpoint.Hostname(), "setup"); err != nil {
		t.Fatal(err)
	}
	localPath := filepath.Join(d.Root, "steps", "build-42", "result")
	if err := os.MkdirAll(localPath, 0755); err != nil {
		t.Fatal(err)
	}
	var actionErr error
	requests, err := trace.captureAction(func() {
		actionErr = client.Register(ctx, endpoint.Hostname(), artifactwire.RegisterRequest{
			Key: "artifact-alias", LocalPath: localPath,
		})
		if actionErr == nil {
			actionErr = client.Mirror(ctx, endpoint.Hostname(), "build-42/result")
		}
	})
	if err != nil || actionErr != nil {
		t.Fatalf("recording action: observation=%v action=%v", err, actionErr)
	}
	if len(requests) != 2 || requests[0].Path != "/register" || requests[1].Path != "/mirror" {
		t.Fatalf("want only register then mirror, got %+v", requests)
	}
	later, err := trace.captureAction(func() {
		_, actionErr = client.HeadArtifact(ctx, endpoint.Hostname(), "artifact-alias")
	})
	if err != nil || actionErr != nil {
		t.Fatalf("later action: observation=%v action=%v", err, actionErr)
	}
	if len(later) != 1 || later[0].Method != http.MethodHead {
		t.Fatalf("later capture includes earlier requests: %+v", later)
	}
	if len(requests) != 2 || requests[0].Path != "/register" || requests[1].Path != "/mirror" {
		t.Fatalf("later traffic changed the completed recording: %+v", requests)
	}
	trace.mu.Lock()
	connections := len(trace.connections)
	trace.mu.Unlock()
	if connections != 1 {
		t.Fatalf("test did not exercise a reused connection: got %d connections", connections)
	}
}
