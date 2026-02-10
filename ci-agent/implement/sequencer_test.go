package implement_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/implement"
	"github.com/concourse/ci-agent/implement/adapter"
)

type fakeAdapter struct {
	testResp *adapter.TestGenResponse
	testErr  error
	implResp *adapter.ImplGenResponse
	implErr  error
	calls    int
}

func (f *fakeAdapter) GenerateTest(_ context.Context, _ adapter.CodeGenRequest) (*adapter.TestGenResponse, error) {
	f.calls++
	return f.testResp, f.testErr
}

func (f *fakeAdapter) GenerateImpl(_ context.Context, _ adapter.CodeGenRequest, _ string) (*adapter.ImplGenResponse, error) {
	f.calls++
	return f.implResp, f.implErr
}

func setupTestGitRepo(dir string) string {
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module testmod\n\ngo 1.25\n"), 0644)
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	cmd.Run()
	cmd = exec.Command("git", "config", "user.email", "test@test.com")
	cmd.Dir = dir
	cmd.Run()
	cmd = exec.Command("git", "config", "user.name", "Test")
	cmd.Dir = dir
	cmd.Run()
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("init"), 0644)
	cmd = exec.Command("git", "add", ".")
	cmd.Dir = dir
	cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "initial")
	cmd.Dir = dir
	cmd.Run()
	return dir
}

var _ = Describe("RunAll", func() {
	It("sequences through multiple tasks", func() {
		repoDir := setupTestGitRepo(GinkgoT().TempDir())

		fake := &fakeAdapter{
			testResp: &adapter.TestGenResponse{
				TestFilePath: "t_test.go",
				TestContent: `package testmod

import "testing"

func TestAlreadyPasses(t *testing.T) {}
`,
				PackageName: "testmod",
			},
		}

		phases := []implement.Phase{
			{
				Name: "Setup",
				Tasks: []implement.PlanTask{
					{ID: "1.1", Description: "Task A", Phase: "Setup"},
					{ID: "1.2", Description: "Task B", Phase: "Setup"},
				},
			},
		}
		tracker := implement.NewTaskTracker(phases)

		opts := implement.SequencerOpts{
			RepoDir:                repoDir,
			Phases:                 phases,
			Adapter:                fake,
			Tracker:                tracker,
			TestCmd:                "go test ./...",
			MaxRetries:             1,
			MaxConsecutiveFailures: 3,
		}

		result, err := implement.RunAll(context.Background(), opts)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Total).To(Equal(2))
		// Both should be skipped (test passes immediately).
		Expect(result.Skipped).To(Equal(2))
		Expect(tracker.IsComplete()).To(BeTrue())
	})

	It("stops when consecutive failures exceed threshold", func() {
		repoDir := setupTestGitRepo(GinkgoT().TempDir())

		fake := &fakeAdapter{
			testErr: context.DeadlineExceeded,
		}

		phases := []implement.Phase{
			{
				Name: "Fail",
				Tasks: []implement.PlanTask{
					{ID: "1.1", Description: "Fail A", Phase: "Fail"},
					{ID: "1.2", Description: "Fail B", Phase: "Fail"},
					{ID: "1.3", Description: "Fail C", Phase: "Fail"},
				},
			},
		}
		tracker := implement.NewTaskTracker(phases)

		opts := implement.SequencerOpts{
			RepoDir:                repoDir,
			Phases:                 phases,
			Adapter:                fake,
			Tracker:                tracker,
			TestCmd:                "go test ./...",
			MaxRetries:             0,
			MaxConsecutiveFailures: 2,
		}

		result, err := implement.RunAll(context.Background(), opts)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Failed).To(Equal(2))
		// Third task should remain pending (stopped early).
		Expect(result.Pending).To(Equal(1))
	})

	It("persists progress after each task", func() {
		repoDir := setupTestGitRepo(GinkgoT().TempDir())
		outputDir := GinkgoT().TempDir()

		fake := &fakeAdapter{
			testResp: &adapter.TestGenResponse{
				TestFilePath: "t_test.go",
				TestContent:  "package testmod\nimport \"testing\"\nfunc TestP(t *testing.T) {}",
				PackageName:  "testmod",
			},
		}

		phases := []implement.Phase{
			{Name: "S", Tasks: []implement.PlanTask{
				{ID: "1.1", Description: "T", Phase: "S"},
			}},
		}
		tracker := implement.NewTaskTracker(phases)

		opts := implement.SequencerOpts{
			RepoDir:                repoDir,
			Phases:                 phases,
			Adapter:                fake,
			Tracker:                tracker,
			TestCmd:                "go test ./...",
			MaxRetries:             0,
			MaxConsecutiveFailures: 3,
			OutputDir:              outputDir,
		}

		_, err := implement.RunAll(context.Background(), opts)
		Expect(err).NotTo(HaveOccurred())

		_, statErr := os.Stat(filepath.Join(outputDir, "progress.json"))
		Expect(statErr).NotTo(HaveOccurred())
	})
})
