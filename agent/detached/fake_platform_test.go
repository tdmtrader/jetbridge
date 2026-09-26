package detached

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/hangar"
)

const (
	testTeam     = "main"
	testTemplate = "workload"
	testRunID    = 41
	testNumber   = 3
)

var testSourceID = "input-v1-" + strings.Repeat("ab", 32)

// fakePlatform serves the public Run routes the client speaks. Its state is
// what a real platform would retain across client restarts.
type fakePlatform struct {
	t      *testing.T
	server *httptest.Server
	input  string
	result string

	mu sync.Mutex
	// Counters and captured requests.
	uploads, creates, handoffs, credentialReads int
	uploadedArchive                             []byte
	createRequests                              []atc.CreatePipelineRunV2Request
	handoffBody                                 []byte
	// Configurable responses.
	createStatus     []int // consumed per create call; default 201
	credentialStatus []int // consumed per credential GET; default 200
	credentialStates []string
	handoffState     string
	run              atc.PipelineRun
	archive          []byte
}

func newFakePlatform(t *testing.T, input, result string) *fakePlatform {
	p := &fakePlatform{t: t, input: input, result: result, handoffState: "ready"}
	p.run = atc.PipelineRun{ID: testRunID, Number: testNumber, ContractVersion: atc.RunContractV2, ActivationEpoch: 1, Status: atc.RunStatusRunning}
	p.server = httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(p.server.Close)
	return p
}

func (p *fakePlatform) client(t *testing.T) *Client {
	c, err := New(p.server.URL, p.server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (p *fakePlatform) counts() (uploads, creates, handoffs int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.uploads, p.creates, p.handoffs
}

func pop(values *[]int, fallback int) int {
	if len(*values) == 0 {
		return fallback
	}
	v := (*values)[0]
	*values = (*values)[1:]
	return v
}

func (p *fakePlatform) serve(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	v1 := "/api/v1/teams/" + testTeam + "/pipelines/" + testTemplate
	v2 := "/api/v2/teams/" + testTeam + "/pipelines/" + testTemplate
	run := "/runs/" + strconv.Itoa(testNumber)
	switch {
	case r.Method == http.MethodPost && r.URL.Path == v1+"/run-inputs/"+p.input:
		body, _ := io.ReadAll(r.Body)
		p.uploads++
		p.uploadedArchive = body
		writeJSON(w, http.StatusCreated, atc.RunInputSource{SourceID: testSourceID, Bearer: "transient-bearer"})
	case r.Method == http.MethodPost && r.URL.Path == v2+"/runs":
		var request atc.CreatePipelineRunV2Request
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		p.creates++
		p.createRequests = append(p.createRequests, request)
		status := pop(&p.createStatus, http.StatusCreated)
		if status != http.StatusCreated && status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		writeJSON(w, status, p.run)
	case r.URL.Path == v2+run+"/credentials/"+p.result:
		if r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			p.handoffs++
			p.handoffBody = body
			writeJSON(w, http.StatusOK, atc.RunCredentialSession{RunID: p.run.ID, Result: p.result, Status: p.handoffState})
			return
		}
		p.credentialReads++
		if status := pop(&p.credentialStatus, http.StatusOK); status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		state := "waiting"
		if len(p.credentialStates) > 0 {
			state = p.credentialStates[0]
			if len(p.credentialStates) > 1 {
				p.credentialStates = p.credentialStates[1:]
			}
		}
		writeJSON(w, http.StatusOK, atc.RunCredentialSession{RunID: p.run.ID, Result: p.result, Status: state})
	case r.Method == http.MethodGet && r.URL.Path == v1+run:
		writeJSON(w, http.StatusOK, p.run)
	case r.Method == http.MethodGet && r.URL.Path == v1+run+"/results/"+p.result:
		w.Header().Set("Content-Type", "application/x-tar")
		w.Header().Set("Content-Length", strconv.Itoa(len(p.archive)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(p.archive)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// fileInput is a one-file sealed input: its digest is the file's content hash,
// and WriteArchive refuses to upload content that changed after Load.
type fileInput struct{ dir, digest string }

func (f fileInput) Digest() string { return f.digest }
func (f fileInput) WriteArchive(_ context.Context, w io.Writer) error {
	data, err := os.ReadFile(filepath.Join(f.dir, "input.txt"))
	if err != nil {
		return err
	}
	if contentDigest(data) != f.digest {
		return errors.New("input changed before upload")
	}
	return writeTar(w, map[string]string{"input.txt": string(data)})
}

func loadFileInput(dir string) (Input, error) {
	data, err := os.ReadFile(filepath.Join(dir, "input.txt"))
	if err != nil {
		return nil, err
	}
	return fileInput{dir: dir, digest: contentDigest(data)}, nil
}

func contentDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func testWorkload(name string) Workload {
	return Workload{Name: name, Input: "source", CredentialResult: "outcome", Load: loadFileInput}
}

func writeTar(w io.Writer, files map[string]string) error {
	tw := tar.NewWriter(w)
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		if _, err := io.WriteString(tw, content); err != nil {
			return err
		}
	}
	return tw.Close()
}

// canonicalResult returns archive bytes exactly as the platform serves a
// result, and the binding the Run retains for them.
func canonicalResult(t *testing.T, files map[string]string) ([]byte, atc.RunResultBinding) {
	t.Helper()
	var raw bytes.Buffer
	if err := writeTar(&raw, files); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	tree, err := (hangar.Canonicalizer{MaxContentBytes: 1 << 20, MaxEntries: 32}).Capture(ctx, &raw)
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Close()
	archive, err := os.ReadFile(tree.ArchivePath)
	if err != nil {
		t.Fatal(err)
	}
	return archive, atc.RunResultBinding{Ref: hangar.TreeRef{Digest: tree.Digest}}
}

type testWorkspace struct {
	input, receipt, auth string
}

func newTestWorkspace(t *testing.T, content string) testWorkspace {
	t.Helper()
	root := t.TempDir()
	ws := testWorkspace{input: filepath.Join(root, "input"), receipt: filepath.Join(root, "request.json"), auth: filepath.Join(root, "auth.json")}
	if err := os.Mkdir(ws.input, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.input, "input.txt"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ws.auth, []byte(`{"synthetic-auth":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return ws
}

func (ws testWorkspace) options() SubmitOptions {
	return SubmitOptions{Team: testTeam, Template: testTemplate, Input: ws.input, Receipt: ws.receipt, AuthFile: ws.auth}
}

func readReceipt(t *testing.T, path string) (map[string]any, []byte) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	return fields, data
}
