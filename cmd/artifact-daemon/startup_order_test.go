package main_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The node readiness label is what build pods are scheduled on, so the daemon
// must not raise it until every configuration it can refuse has been checked.
// It used to label the node and only then load --resolve-capability-key, and
// exit on a bad key without taking the label down: a crash-looping daemon kept
// attracting pods to a node that could serve none of them.
//
// The daemon binary runs for real against a real HTTP server standing in for
// the Kubernetes API, which records every node PATCH it is sent.
func TestDaemonRefusesABadCapabilityKeyBeforeLabellingTheNode(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the daemon binary")
	}
	dir := t.TempDir()

	bin := filepath.Join(dir, "artifact-daemon")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build daemon: %v\n%s", err, out)
	}

	var mu sync.Mutex
	var patches []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Method == http.MethodPatch && r.URL.Path == "/api/v1/nodes/node-a" {
			mu.Lock()
			patches = append(patches, string(body))
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"kind":"Node","apiVersion":"v1","metadata":{"name":"node-a"}}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer api.Close()

	kubeconfig := filepath.Join(dir, "kubeconfig")
	if err := os.WriteFile(kubeconfig, []byte(fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: fake
  cluster:
    server: %s
contexts:
- name: fake
  context:
    cluster: fake
    user: fake
current-context: fake
users:
- name: fake
  user:
    token: fake
`, api.URL)), 0600); err != nil {
		t.Fatal(err)
	}

	// Wrong length: LoadKeyFile refuses anything but 32 bytes.
	badKey := filepath.Join(dir, "resolve.key")
	if err := os.WriteFile(badKey, []byte("too-short"), 0600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	daemon := exec.CommandContext(ctx, bin,
		"--node-name=node-a",
		"--kubeconfig="+kubeconfig,
		"--storage-path="+filepath.Join(dir, "storage"),
		"--listen-address=127.0.0.1",
		"--port=0",
		"--mirror-replicas=0",
		"--resolve-capability-key="+badKey,
	)
	out, err := daemon.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("daemon did not exit on a bad capability key; output:\n%s", out)
	}
	if err == nil {
		t.Fatalf("daemon exited 0 with a bad capability key; output:\n%s", out)
	}
	if !strings.Contains(string(out), "failed-to-load-resolve-capability-key") {
		t.Fatalf("daemon failed for some other reason; output:\n%s", out)
	}

	mu.Lock()
	defer mu.Unlock()
	for _, p := range patches {
		if strings.Contains(p, `"ready"`) {
			t.Errorf("daemon advertised readiness before refusing its capability key: PATCH %s\nall patches: %v", p, patches)
		}
	}
}
