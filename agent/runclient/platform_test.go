package runclient

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/atctest"
)

func TestMain(m *testing.M) { os.Exit(atctest.Run(m)) }

const testTemplate = "workload"

// platform is the real platform, seen by one test through its own team and,
// when the test needs the platform to fail on demand, a decorator.
type platform struct {
	*atctest.Platform
	team, url string
}

// newPlatform installs the workload template, whose one producer takes the
// uploaded source and publishes result to the pinned worker image.
func newPlatform(t *testing.T, result string) *platform {
	p := &platform{Platform: atctest.Get(t)}
	p.team, p.url = p.NewTeam(t), p.URL
	p.Install(t, p.team, testTemplate, atc.Config{Jobs: atc.JobConfigs{{Name: "work", PlanSequence: []atc.Step{{Config: &atc.TaskStep{
		Name: "produce", TaskID: "8c1d4f3e-5a2b-4e6f-9d7c-1b2a3c4d5e6f",
		RunInputs: []atc.RunInput{{Name: "source", Input: "source"}}, RunResult: &atc.RunResult{Name: result, Output: "out"},
		Config: &atc.TaskConfig{Platform: "linux", RootfsURI: "docker:///" + atctest.Pin, Inputs: []atc.TaskInputConfig{{Name: "source"}},
			Outputs: []atc.TaskOutputConfig{{Name: "out"}}, Run: atc.TaskRunConfig{Path: "true"}},
	}}}}}})
	return p
}

// decorate routes this test's clients through fault.
func (p *platform) decorate(t *testing.T, fault atctest.Fault) { p.url = p.Decorate(t, fault) }

func (p *platform) client(t *testing.T) *Client {
	c, err := New(p.url, p.Client(t))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// counts are the uploads registered and the Runs admitted for the template.
func (p *platform) counts(t *testing.T) (uploads, runs int) {
	return p.Uploads(t, p.team, testTemplate), p.Runs(t, p.team, testTemplate)
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

func (ws testWorkspace) options(p *platform) SubmitOptions {
	return SubmitOptions{Team: p.team, Template: testTemplate, Input: ws.input, Receipt: ws.receipt, AuthFile: ws.auth}
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
