//go:build live
// +build live

package jetbridge_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
)

// TestLiveAgentSidecarMCPWiring proves the wiring every MCP sidecar
// workstream assumes: a sidecar serving HTTP on a fixed localhost port
// (7781, the platform slot), a main container that waits for /healthz then
// POSTs a stub MCP tool call to /mcp — with the main container starting
// BEFORE the sidecar is ready (startup-order tolerance), in the pause-pod
// model, and torn down cleanly. It goes through the jetbridge worker path
// (FindOrCreateContainer + Container.Run), the exact pod construction the
// agent step exec uses, with db.ContainerTypeAgent so the process runs
// under the in-pod supervisor exactly as production agent steps do.
//
//	kubectl create ns jetbridge-agent-step-test
//	KUBECONFIG=~/.kube/config K8S_TEST_NAMESPACE=jetbridge-agent-step-test \
//	  go test -tags live -run '^TestLiveAgentSidecarMCPWiring$' -v -count=1 \
//	  -timeout 5m ./atc/worker/jetbridge/
//	kubectl delete ns jetbridge-agent-step-test
func TestLiveAgentSidecarMCPWiring(t *testing.T) {
	handle := "live-agent-mcp-" + time.Now().Format("150405")
	ctx := context.Background()
	clientset, cfg := kubeClient(t)
	cleanupPod(t, clientset, cfg.Namespace, handle)

	worker, delegate := setupLiveWorker(t, handle)

	// Sidecar: python stub MCP server. Sleeps 10s BEFORE binding so the main
	// container demonstrably starts first and must wait — the startup-order
	// case. Serves GET /healthz (200) and POST /mcp (fixed JSON tool result).
	sidecarScript := `
import time, json
time.sleep(10)
from http.server import BaseHTTPRequestHandler, HTTPServer
class H(BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200); self.end_headers(); self.wfile.write(b"ok")
    def do_POST(self):
        n = int(self.headers.get("Content-Length", 0)); self.rfile.read(n)
        body = json.dumps({"jsonrpc": "2.0", "id": 1, "result": {"content": [{"type": "text", "text": "stub-tool-result-42"}]}}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json"); self.end_headers()
        self.wfile.write(body)
HTTPServer(("127.0.0.1", 7781), H).serve_forever()
`

	containerSpec := runtime.ContainerSpec{
		TeamID:    1,
		ImageSpec: runtime.ImageSpec{ImageURL: "docker:///python:3.12-alpine"},
		Dir:       "/tmp/build/agent",
		Env:       []string{"PLATFORM_MCP_URL=http://127.0.0.1:7781/mcp"},
		// Output volume for the flight-write tripwire (finding F25): jetbridge
		// hostPath output volumes are kubelet-created root:root 0755, so this
		// mount is exactly what agent-runner must be able to write results.json
		// into. A permission regression fails HERE, not on the first live run.
		Outputs: runtime.OutputPaths{"flight": "/tmp/build/agent/flight"},
		Sidecars: []atc.SidecarConfig{{
			Name:    "platform",
			Image:   "python:3.12-alpine",
			Command: []string{"python", "-c", sidecarScript},
		}},
	}

	container, _, err := worker.FindOrCreateContainer(
		ctx, db.NewFixedHandleContainerOwner(handle),
		db.ContainerMetadata{Type: db.ContainerTypeAgent},
		containerSpec, delegate,
	)
	if err != nil {
		t.Fatalf("creating container: %v", err)
	}

	// Main: wait for healthz (up to 60s), then POST a tool call — the exact
	// client behavior agent-runner implements.
	clientScript := `
import time, urllib.request, sys
base = "http://127.0.0.1:7781"
deadline = time.time() + 60
while True:
    try:
        urllib.request.urlopen(base + "/healthz", timeout=2); break
    except Exception:
        if time.time() > deadline: print("HEALTHZ-TIMEOUT"); sys.exit(1)
        time.sleep(1)
req = urllib.request.Request(base + "/mcp", data=b'{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"stub"}}', headers={"Content-Type": "application/json"})
print(urllib.request.urlopen(req, timeout=10).read().decode())
# Flight-mount write tripwire (finding F25): prove the main container can
# write the root:root 0755 hostPath output volume it runs on.
import os
os.makedirs("/tmp/build/agent/flight", exist_ok=True)
with open("/tmp/build/agent/flight/tripwire", "w") as f:
    f.write("ok\n")
print("flight-write-ok")
`

	var stdout, stderr bytes.Buffer
	process, err := container.Run(ctx, runtime.ProcessSpec{
		ID:   "agent",
		Path: "python",
		Args: []string{"-c", clientScript},
		TTY: &runtime.TTYSpec{
			WindowSize: runtime.WindowSize{Columns: 500, Rows: 500},
		},
	}, runtime.ProcessIO{Stdout: &stdout, Stderr: &stderr})
	if err != nil {
		t.Fatalf("running client process: %v", err)
	}

	result, err := process.Wait(ctx)
	if err != nil {
		t.Fatalf("waiting: %v (stderr: %s)", err, stderr.String())
	}
	if result.ExitStatus != 0 {
		t.Fatalf("client exited %d: %s / %s", result.ExitStatus, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "stub-tool-result-42") {
		t.Fatalf("expected stub tool result in output, got: %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "flight-write-ok") {
		t.Fatalf("flight-mount write tripwire failed — hostPath output volume not writable by the main container (finding F25): %s / %s",
			stdout.String(), stderr.String())
	}

	t.Logf("sidecar MCP wiring proven: startup-order tolerated, tool call answered, flight mount writable")
}
