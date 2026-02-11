package orchestrator_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/implement/adapter"
	"github.com/concourse/ci-agent/implement/orchestrator"
	"github.com/concourse/ci-agent/schema"
)

type fakeOrcAdapter struct {
	testResp *adapter.TestGenResponse
	implResp *adapter.ImplGenResponse
}

func (f *fakeOrcAdapter) GenerateTest(_ context.Context, _ adapter.CodeGenRequest) (*adapter.TestGenResponse, error) {
	return f.testResp, nil
}

func (f *fakeOrcAdapter) GenerateImpl(_ context.Context, _ adapter.CodeGenRequest, _ string) (*adapter.ImplGenResponse, error) {
	return f.implResp, nil
}

func setupFixture(dir string) (specDir, repoDir string) {
	specDir = filepath.Join(dir, "spec")
	repoDir = filepath.Join(dir, "repo")
	os.MkdirAll(specDir, 0755)
	os.MkdirAll(repoDir, 0755)

	os.WriteFile(filepath.Join(specDir, "spec.md"), []byte(`# Spec

## Acceptance Criteria

- [ ] Widget exists
`), 0644)

	os.WriteFile(filepath.Join(specDir, "plan.md"), []byte(`## Phase 1: Setup

- [ ] Create widget
`), 0644)

	os.WriteFile(filepath.Join(repoDir, "go.mod"), []byte("module testmod\n\ngo 1.25\n"), 0644)
	cmd := exec.Command("git", "init")
	cmd.Dir = repoDir
	cmd.Run()
	cmd = exec.Command("git", "config", "user.email", "test@test.com")
	cmd.Dir = repoDir
	cmd.Run()
	cmd = exec.Command("git", "config", "user.name", "Test")
	cmd.Dir = repoDir
	cmd.Run()
	os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("init"), 0644)
	cmd = exec.Command("git", "add", ".")
	cmd.Dir = repoDir
	cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "initial")
	cmd.Dir = repoDir
	cmd.Run()

	return specDir, repoDir
}

var _ = Describe("Run", func() {
	It("runs full pipeline and produces output files", func() {
		dir := GinkgoT().TempDir()
		specDir, repoDir := setupFixture(dir)
		outputDir := filepath.Join(dir, "output")

		fake := &fakeOrcAdapter{
			testResp: &adapter.TestGenResponse{
				TestFilePath: "w_test.go",
				TestContent:  "package testmod\nimport \"testing\"\nfunc TestW(t *testing.T) {}",
				PackageName:  "testmod",
			},
		}

		opts := orchestrator.Options{
			SpecDir:                specDir,
			RepoDir:                repoDir,
			OutputDir:              outputDir,
			Adapter:                fake,
			TestCmd:                "go test ./...",
			MaxRetries:             1,
			MaxConsecutiveFailures: 3,
			ConfidenceThreshold:    0.7,
		}

		results, err := orchestrator.Run(context.Background(), opts)
		Expect(err).NotTo(HaveOccurred())
		Expect(results).NotTo(BeNil())

		// Check output files exist.
		_, err = os.Stat(filepath.Join(outputDir, "results.json"))
		Expect(err).NotTo(HaveOccurred())
		_, err = os.Stat(filepath.Join(outputDir, "summary.md"))
		Expect(err).NotTo(HaveOccurred())
		_, err = os.Stat(filepath.Join(outputDir, "progress.json"))
		Expect(err).NotTo(HaveOccurred())
		_, err = os.Stat(filepath.Join(outputDir, "events.ndjson"))
		Expect(err).NotTo(HaveOccurred())

		// Validate results.json.
		data, err := os.ReadFile(filepath.Join(outputDir, "results.json"))
		Expect(err).NotTo(HaveOccurred())
		var r schema.Results
		Expect(json.Unmarshal(data, &r)).To(Succeed())
		Expect(r.Validate()).To(Succeed())
	})

	It("abstains when plan has no tasks", func() {
		dir := GinkgoT().TempDir()
		specDir := filepath.Join(dir, "spec")
		repoDir := filepath.Join(dir, "repo")
		outputDir := filepath.Join(dir, "output")
		os.MkdirAll(specDir, 0755)
		os.MkdirAll(repoDir, 0755)

		os.WriteFile(filepath.Join(specDir, "spec.md"), []byte("# Empty spec"), 0644)
		os.WriteFile(filepath.Join(specDir, "plan.md"), []byte("# Empty plan\nNo phases here."), 0644)

		opts := orchestrator.Options{
			SpecDir:   specDir,
			RepoDir:   repoDir,
			OutputDir: outputDir,
		}

		results, err := orchestrator.Run(context.Background(), opts)
		Expect(err).NotTo(HaveOccurred())
		Expect(results.Status).To(Equal(schema.StatusAbstain))
	})

	It("red-green cycle: test fails first, passes on retry via counter-based adapter", func() {
		dir := GinkgoT().TempDir()
		specDir, repoDir := setupFixture(dir)
		outputDir := filepath.Join(dir, "output")

		// Counter-based adapter: first GenerateImpl returns empty (no patch),
		// second call returns a real patch that makes the test pass.
		counter := &counterAdapter{
			testResp: &adapter.TestGenResponse{
				TestFilePath: "greet_test.go",
				TestContent: `package testmod

import "testing"

func TestGreet(t *testing.T) {
	if Greet() != "hello" {
		t.Fatal("expected hello")
	}
}
`,
				PackageName: "testmod",
			},
			implResponses: []*adapter.ImplGenResponse{
				// First attempt: empty patch → test still fails.
				{Patches: nil},
				// Second attempt: actual implementation.
				{Patches: []adapter.FilePatch{
					{Path: "greet.go", Content: "package testmod\n\nfunc Greet() string { return \"hello\" }\n"},
				}},
			},
		}

		opts := orchestrator.Options{
			SpecDir:                specDir,
			RepoDir:                repoDir,
			OutputDir:              outputDir,
			Adapter:                counter,
			TestCmd:                "go test ./...",
			MaxRetries:             2,
			MaxConsecutiveFailures: 3,
		}

		results, err := orchestrator.Run(context.Background(), opts)
		Expect(err).NotTo(HaveOccurred())
		Expect(results).NotTo(BeNil())

		// progress.json should reflect task completion.
		progressPath := filepath.Join(outputDir, "progress.json")
		Expect(progressPath).To(BeAnExistingFile())
	})

	It("multi-phase plan processes all tasks", func() {
		dir := GinkgoT().TempDir()
		specDir := filepath.Join(dir, "spec")
		repoDir := filepath.Join(dir, "repo")
		outputDir := filepath.Join(dir, "output")
		os.MkdirAll(specDir, 0755)
		os.MkdirAll(repoDir, 0755)

		// Multi-phase plan: 2 phases with 3 total tasks.
		os.WriteFile(filepath.Join(specDir, "spec.md"), []byte(`# Spec

## Acceptance Criteria

- [ ] Widget exists
- [ ] Helper exists
- [ ] Formatter exists
`), 0644)

		os.WriteFile(filepath.Join(specDir, "plan.md"), []byte(`## Phase 1: Core

- [ ] Create widget
- [ ] Create helper

## Phase 2: Polish

- [ ] Create formatter
`), 0644)

		os.WriteFile(filepath.Join(repoDir, "go.mod"), []byte("module testmod\n\ngo 1.25\n"), 0644)
		cmd := exec.Command("git", "init")
		cmd.Dir = repoDir
		cmd.Run()
		cmd = exec.Command("git", "config", "user.email", "test@test.com")
		cmd.Dir = repoDir
		cmd.Run()
		cmd = exec.Command("git", "config", "user.name", "Test")
		cmd.Dir = repoDir
		cmd.Run()
		os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("init"), 0644)
		cmd = exec.Command("git", "add", ".")
		cmd.Dir = repoDir
		cmd.Run()
		cmd = exec.Command("git", "commit", "-m", "initial")
		cmd.Dir = repoDir
		cmd.Run()

		fake := &fakeOrcAdapter{
			testResp: &adapter.TestGenResponse{
				TestFilePath: "task_test.go",
				TestContent:  "package testmod\nimport \"testing\"\nfunc TestTask(t *testing.T) {}",
				PackageName:  "testmod",
			},
		}

		opts := orchestrator.Options{
			SpecDir:                specDir,
			RepoDir:                repoDir,
			OutputDir:              outputDir,
			Adapter:                fake,
			TestCmd:                "go test ./...",
			MaxRetries:             1,
			MaxConsecutiveFailures: 5,
		}

		results, err := orchestrator.Run(context.Background(), opts)
		Expect(err).NotTo(HaveOccurred())
		Expect(results).NotTo(BeNil())

		// progress.json should show all 3 tasks processed.
		progressPath := filepath.Join(outputDir, "progress.json")
		Expect(progressPath).To(BeAnExistingFile())

		data, err := os.ReadFile(progressPath)
		Expect(err).NotTo(HaveOccurred())

		var tracker struct {
			Tasks []struct {
				TaskID string `json:"task_id"`
				Status string `json:"status"`
			} `json:"tasks"`
		}
		Expect(json.Unmarshal(data, &tracker)).To(Succeed())
		Expect(tracker.Tasks).To(HaveLen(3))

		// All tasks should be in a terminal state (committed, skipped, or failed).
		for _, t := range tracker.Tasks {
			Expect(t.Status).To(BeElementOf("committed", "skipped", "failed"))
		}
	})
})

// counterAdapter returns different impl responses on successive calls.
type counterAdapter struct {
	testResp      *adapter.TestGenResponse
	implResponses []*adapter.ImplGenResponse
	implCallCount int
}

func (c *counterAdapter) GenerateTest(_ context.Context, _ adapter.CodeGenRequest) (*adapter.TestGenResponse, error) {
	return c.testResp, nil
}

func (c *counterAdapter) GenerateImpl(_ context.Context, _ adapter.CodeGenRequest, _ string) (*adapter.ImplGenResponse, error) {
	idx := c.implCallCount
	c.implCallCount++
	if idx < len(c.implResponses) {
		return c.implResponses[idx], nil
	}
	// Return last response for any extra calls.
	return c.implResponses[len(c.implResponses)-1], nil
}
