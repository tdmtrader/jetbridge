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
})
