package tdd_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/implement/adapter"
	"github.com/concourse/ci-agent/implement/tdd"
)

type fakeAdapterTDD struct {
	testResp *adapter.TestGenResponse
	testErr  error
	implResp *adapter.ImplGenResponse
	implErr  error
}

func (f *fakeAdapterTDD) GenerateTest(_ context.Context, _ adapter.CodeGenRequest) (*adapter.TestGenResponse, error) {
	return f.testResp, f.testErr
}

func (f *fakeAdapterTDD) GenerateImpl(_ context.Context, _ adapter.CodeGenRequest, _ string) (*adapter.ImplGenResponse, error) {
	return f.implResp, f.implErr
}

func setupGitRepo(dir string) string {
	repoDir := setupGoModule(dir)
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
	return repoDir
}

var _ = Describe("ExecuteTask", func() {
	It("happy path: red → green → commit", func() {
		repoDir := setupGitRepo(GinkgoT().TempDir())

		fake := &fakeAdapterTDD{
			testResp: &adapter.TestGenResponse{
				TestFilePath: "widget_test.go",
				TestContent: `package testmod

import (
	"os"
	"testing"
)

func TestWidget(t *testing.T) {
	if _, err := os.Stat("widget.go"); os.IsNotExist(err) {
		t.Fatal("widget.go does not exist")
	}
}
`,
				PackageName: "testmod",
			},
			implResp: &adapter.ImplGenResponse{
				Patches: []adapter.FilePatch{
					{Path: "widget.go", Content: "package testmod\n"},
				},
			},
		}

		task := tdd.TaskInfo{
			ID:          "1.1",
			Description: "Create widget",
			Phase:       "Setup",
		}

		opts := tdd.TaskLoopOpts{
			RepoDir:    repoDir,
			Task:       task,
			Adapter:    fake,
			TestCmd:    "go test ./...",
			MaxRetries: 2,
		}

		result, err := tdd.ExecuteTask(context.Background(), opts)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Status).To(Equal(tdd.TaskCommitted))
		Expect(result.CommitSHA).NotTo(BeEmpty())
	})

	It("skips task when test already passes (pre-satisfied)", func() {
		repoDir := setupGitRepo(GinkgoT().TempDir())

		fake := &fakeAdapterTDD{
			testResp: &adapter.TestGenResponse{
				TestFilePath: "pass_test.go",
				TestContent: `package testmod

import "testing"

func TestAlreadyPasses(t *testing.T) {
	// already passes
}
`,
				PackageName: "testmod",
			},
		}

		task := tdd.TaskInfo{
			ID:          "1.1",
			Description: "Already done",
			Phase:       "Setup",
		}

		opts := tdd.TaskLoopOpts{
			RepoDir:    repoDir,
			Task:       task,
			Adapter:    fake,
			TestCmd:    "go test ./...",
			MaxRetries: 2,
		}

		result, err := tdd.ExecuteTask(context.Background(), opts)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Status).To(Equal(tdd.TaskSkipped))
		Expect(result.Reason).To(ContainSubstring("already"))
	})

	It("fails task when adapter returns error", func() {
		repoDir := setupGitRepo(GinkgoT().TempDir())

		fake := &fakeAdapterTDD{
			testErr: context.DeadlineExceeded,
		}

		task := tdd.TaskInfo{
			ID:          "1.1",
			Description: "Timeout",
			Phase:       "Setup",
		}

		opts := tdd.TaskLoopOpts{
			RepoDir:    repoDir,
			Task:       task,
			Adapter:    fake,
			TestCmd:    "go test ./...",
			MaxRetries: 0,
		}

		result, err := tdd.ExecuteTask(context.Background(), opts)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Status).To(Equal(tdd.TaskFailed))
	})

	It("fails task when green phase fails after retries", func() {
		repoDir := setupGitRepo(GinkgoT().TempDir())

		fake := &fakeAdapterTDD{
			testResp: &adapter.TestGenResponse{
				TestFilePath: "need_test.go",
				TestContent: `package testmod

import "testing"

func TestNeed(t *testing.T) {
	t.Fatal("need implementation")
}
`,
				PackageName: "testmod",
			},
			implResp: &adapter.ImplGenResponse{
				Patches: []adapter.FilePatch{},
			},
		}

		task := tdd.TaskInfo{
			ID:          "1.1",
			Description: "Cannot implement",
			Phase:       "Setup",
		}

		opts := tdd.TaskLoopOpts{
			RepoDir:    repoDir,
			Task:       task,
			Adapter:    fake,
			TestCmd:    "go test ./...",
			MaxRetries: 1,
		}

		result, err := tdd.ExecuteTask(context.Background(), opts)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Status).To(Equal(tdd.TaskFailed))
	})
})
