package main_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func buildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "platform-mcp")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v: %s", err, out)
	}
	return bin
}

func runCheckpointClient(t *testing.T, sidecarURL string, args ...string) *exec.ExitError {
	t.Helper()
	cmd := exec.Command(buildBinary(t), append([]string{"checkpoint"}, args...)...)
	cmd.Env = append(os.Environ(), "PLATFORM_MCP_URL="+sidecarURL+"/mcp")
	out, err := cmd.CombinedOutput()
	t.Logf("checkpoint client output: %s", out)
	if err == nil {
		return nil
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("unexpected error type: %v", err)
	}
	return exitErr
}

func TestCheckpointClientApproved(t *testing.T) {
	sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/checkpoint" || r.Method != http.MethodPost {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, `{"approved": true, "answer": "approve", "answered_by": "tdm"}`)
	}))
	defer sidecar.Close()
	if exitErr := runCheckpointClient(t, sidecar.URL, "--name", "plan-approval"); exitErr != nil {
		t.Fatalf("expected exit 0, got %d", exitErr.ExitCode())
	}
}

func TestCheckpointClientRejected(t *testing.T) {
	sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"approved": false, "answer": "reject", "answered_by": "tdm"}`)
	}))
	defer sidecar.Close()
	exitErr := runCheckpointClient(t, sidecar.URL, "--name", "plan-approval")
	if exitErr == nil || exitErr.ExitCode() != 1 {
		t.Fatalf("expected exit 1 on reject, got %v", exitErr)
	}
}

func TestCheckpointClientRequiresName(t *testing.T) {
	exitErr := runCheckpointClient(t, "http://127.0.0.1:1")
	if exitErr == nil || exitErr.ExitCode() != 2 {
		t.Fatalf("expected exit 2 without --name, got %v", exitErr)
	}
}

// TestCheckpointClientParkedExit3 (PARK-V2 §B4): 202 {"parked": true} from
// the sidecar means the park crossed the short-park threshold — the client
// exits with FROZEN code 3 (parked-past-threshold; 0/1/2 unchanged) so the
// TaskStep fails as the §B5 carrier while the open question row remains the
// authority the platform resumes on.
func TestCheckpointClientParkedExit3(t *testing.T) {
	sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, `{"parked": true}`)
	}))
	defer sidecar.Close()
	exitErr := runCheckpointClient(t, sidecar.URL, "--name", "plan-approval")
	if exitErr == nil || exitErr.ExitCode() != 3 {
		t.Fatalf("expected exit 3 on parked-past-threshold, got %v", exitErr)
	}
}

// TestCheckpointClientPrincipalRejected (D6/F31 leg 3): when the sidecar's
// AwaitAnswer hits the consecutive-401/403 limit it answers 502 with a
// "principal rejected:"-prefixed body; the client must exit 1 (frozen code)
// and echo that prefix on stderr — a loud failure, never a silent hang.
func TestCheckpointClientPrincipalRejected(t *testing.T) {
	sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "principal rejected: awaiting checkpoint: question 7: 12 consecutive 401/403 responses: agent principal rejected: consecutive auth failures exceeded limit", http.StatusBadGateway)
	}))
	defer sidecar.Close()

	cmd := exec.Command(buildBinary(t), "checkpoint", "--name", "plan-approval")
	cmd.Env = append(os.Environ(), "PLATFORM_MCP_URL="+sidecar.URL+"/mcp")
	out, err := cmd.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("expected exit 1 on fatal-auth, got %v (out=%s)", err, out)
	}
	if !strings.Contains(string(out), "principal rejected:") {
		t.Fatalf("expected 'principal rejected:' on stderr, got %s", out)
	}
}
