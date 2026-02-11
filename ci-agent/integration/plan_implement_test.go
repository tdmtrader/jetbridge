package integration_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/implement/adapter"
	implorch "github.com/concourse/ci-agent/implement/orchestrator"
	planadapter "github.com/concourse/ci-agent/plan/adapter"
	"github.com/concourse/ci-agent/plan/confidence"
	planorch "github.com/concourse/ci-agent/plan/orchestrator"
	"github.com/concourse/ci-agent/schema"
)

// fakePlanAdapter returns canned spec and plan.
type fakePlanAdapter struct {
	specOutput *planadapter.SpecOutput
	planOutput *planadapter.PlanOutput
}

func (f *fakePlanAdapter) GenerateSpec(_ context.Context, _ *schema.PlanningInput, _ planadapter.SpecOpts) (*planadapter.SpecOutput, error) {
	return f.specOutput, nil
}

func (f *fakePlanAdapter) GeneratePlan(_ context.Context, _ *schema.PlanningInput, _ string, _ planadapter.PlanOpts) (*planadapter.PlanOutput, error) {
	return f.planOutput, nil
}

// fakeImplAdapter returns canned test and impl responses.
type fakeImplAdapter struct {
	testResp *adapter.TestGenResponse
	implResp *adapter.ImplGenResponse
}

func (f *fakeImplAdapter) GenerateTest(_ context.Context, _ adapter.CodeGenRequest) (*adapter.TestGenResponse, error) {
	return f.testResp, nil
}

func (f *fakeImplAdapter) GenerateImpl(_ context.Context, _ adapter.CodeGenRequest, _ string) (*adapter.ImplGenResponse, error) {
	return f.implResp, nil
}

var _ = Describe("Plan → Implement Cross-Subsystem Integration", func() {
	var tmpDir string

	BeforeEach(func() {
		var err error
		tmpDir, err = os.MkdirTemp("", "plan-impl-integ-*")
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		os.RemoveAll(tmpDir)
	})

	It("plan outputs feed into implement pipeline", func() {
		ctx := context.Background()
		inputDir := filepath.Join(tmpDir, "input")
		planOutputDir := filepath.Join(tmpDir, "plan-output")
		implOutputDir := filepath.Join(tmpDir, "impl-output")
		repoDir := filepath.Join(tmpDir, "repo")
		os.MkdirAll(inputDir, 0755)
		os.MkdirAll(repoDir, 0755)

		// Write input.json.
		input := &schema.PlanningInput{
			Title:       "Add Widget Feature",
			Description: "Implement a widget that returns hello with a longer description for completeness scoring",
			Type:        schema.StoryFeature,
			Priority:    schema.PriorityHigh,
			AcceptanceCriteria: []string{
				"Widget function exists",
				"Widget returns hello",
			},
		}
		inputData, _ := json.Marshal(input)
		inputPath := filepath.Join(inputDir, "input.json")
		Expect(os.WriteFile(inputPath, inputData, 0644)).To(Succeed())

		// Step 1: Run plan pipeline.
		planAdapter := &fakePlanAdapter{
			specOutput: &planadapter.SpecOutput{
				SpecMarkdown: "# Spec\n\nWidget function exists and returns hello.\n\n## Acceptance Criteria\n\n- [ ] Widget function exists\n- [ ] Widget returns hello\n",
			},
			planOutput: &planadapter.PlanOutput{
				PlanMarkdown: "## Phase 1: Setup\n\n- [ ] Create widget\n",
				Phases: []planadapter.Phase{
					{
						// Phase name must include "Phase N:" prefix to match
						// the implement parser regex.
						Name: "Phase 1: Setup",
						Tasks: []planadapter.Task{
							{Description: "Create widget", Files: []string{"widget.go"}},
						},
					},
				},
				KeyFiles: []planadapter.KeyFile{{Path: "widget.go", Change: "NEW"}},
			},
		}

		planResults, err := planorch.Run(ctx, planorch.Options{
			InputPath:           inputPath,
			OutputDir:           planOutputDir,
			Adapter:             planAdapter,
			ConfidenceThreshold: 0.3,
			ConfidenceWeights:   confidence.DefaultWeights(),
			Timeout:             5 * time.Minute,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(planResults.Status).NotTo(Equal(schema.StatusError))

		// Verify spec.md and plan.md were produced.
		specPath := filepath.Join(planOutputDir, "spec.md")
		planPath := filepath.Join(planOutputDir, "plan.md")
		resultsPath := filepath.Join(planOutputDir, "results.json")
		Expect(specPath).To(BeAnExistingFile())
		Expect(planPath).To(BeAnExistingFile())
		Expect(resultsPath).To(BeAnExistingFile())

		// Step 2: Set up repo and run implement pipeline consuming plan output.
		Expect(os.WriteFile(filepath.Join(repoDir, "go.mod"),
			[]byte("module testmod\n\ngo 1.25\n"), 0644)).To(Succeed())

		cmd := exec.Command("git", "init")
		cmd.Dir = repoDir
		cmd.Run()
		cmd = exec.Command("git", "config", "user.email", "test@test.com")
		cmd.Dir = repoDir
		cmd.Run()
		cmd = exec.Command("git", "config", "user.name", "Test")
		cmd.Dir = repoDir
		cmd.Run()
		Expect(os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("init"), 0644)).To(Succeed())
		cmd = exec.Command("git", "add", ".")
		cmd.Dir = repoDir
		cmd.Run()
		cmd = exec.Command("git", "commit", "-m", "initial")
		cmd.Dir = repoDir
		cmd.Run()

		implAdapter := &fakeImplAdapter{
			testResp: &adapter.TestGenResponse{
				TestFilePath: "widget_test.go",
				TestContent:  "package testmod\nimport \"testing\"\nfunc TestWidget(t *testing.T) {}",
				PackageName:  "testmod",
			},
		}

		implResults, err := implorch.Run(ctx, implorch.Options{
			SpecDir:                planOutputDir,
			RepoDir:                repoDir,
			OutputDir:              implOutputDir,
			Adapter:                implAdapter,
			TestCmd:                "go test ./...",
			MaxRetries:             1,
			MaxConsecutiveFailures: 3,
			ConfidenceThreshold:    0.5,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(implResults).NotTo(BeNil())

		// Verify results.json was written.
		implResultsPath := filepath.Join(implOutputDir, "results.json")
		Expect(implResultsPath).To(BeAnExistingFile())

		data, err := os.ReadFile(implResultsPath)
		Expect(err).NotTo(HaveOccurred())
		var decoded schema.Results
		Expect(json.Unmarshal(data, &decoded)).To(Succeed())
		Expect(decoded.Validate()).To(Succeed())
	})

	It("plan with low confidence still produces parseable files for implement", func() {
		ctx := context.Background()
		inputDir := filepath.Join(tmpDir, "input")
		planOutputDir := filepath.Join(tmpDir, "plan-output")
		implOutputDir := filepath.Join(tmpDir, "impl-output")
		repoDir := filepath.Join(tmpDir, "repo")
		os.MkdirAll(inputDir, 0755)
		os.MkdirAll(repoDir, 0755)

		// Minimal input → low confidence.
		input := &schema.PlanningInput{
			Title:       "Simple task",
			Description: "Do something",
		}
		inputData, _ := json.Marshal(input)
		inputPath := filepath.Join(inputDir, "input.json")
		Expect(os.WriteFile(inputPath, inputData, 0644)).To(Succeed())

		planAdapter := &fakePlanAdapter{
			specOutput: &planadapter.SpecOutput{
				SpecMarkdown:        "# Spec\n\nDo something\n\n## Acceptance Criteria\n\n- [ ] Task completed\n",
				UnresolvedQuestions: []string{"What exactly should be done?", "Which files?", "Any constraints?"},
			},
			planOutput: &planadapter.PlanOutput{
				PlanMarkdown: "## Phase 1: Implementation\n\n- [ ] Implement the task\n",
				Phases: []planadapter.Phase{
					// Phase name must include "Phase N:" prefix to match the
					// implement parser regex: ^##\s+Phase\s+\d+:\s+(.+)$
					{Name: "Phase 1: Implementation", Tasks: []planadapter.Task{{Description: "Implement the task"}}},
				},
			},
		}

		planResults, err := planorch.Run(ctx, planorch.Options{
			InputPath:           inputPath,
			OutputDir:           planOutputDir,
			Adapter:             planAdapter,
			ConfidenceThreshold: 0.99, // Very high threshold → should fail.
			ConfidenceWeights:   confidence.DefaultWeights(),
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(planResults.Status).To(Equal(schema.StatusFail))

		// Even though plan failed, spec.md and plan.md should be parseable.
		specPath := filepath.Join(planOutputDir, "spec.md")
		planPath := filepath.Join(planOutputDir, "plan.md")
		Expect(specPath).To(BeAnExistingFile())
		Expect(planPath).To(BeAnExistingFile())

		// Implement should still be able to read these files.
		Expect(os.WriteFile(filepath.Join(repoDir, "go.mod"),
			[]byte("module testmod\n\ngo 1.25\n"), 0644)).To(Succeed())
		cmd := exec.Command("git", "init")
		cmd.Dir = repoDir
		cmd.Run()
		cmd = exec.Command("git", "config", "user.email", "test@test.com")
		cmd.Dir = repoDir
		cmd.Run()
		cmd = exec.Command("git", "config", "user.name", "Test")
		cmd.Dir = repoDir
		cmd.Run()
		Expect(os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("init"), 0644)).To(Succeed())
		cmd = exec.Command("git", "add", ".")
		cmd.Dir = repoDir
		cmd.Run()
		cmd = exec.Command("git", "commit", "-m", "initial")
		cmd.Dir = repoDir
		cmd.Run()

		implAdapter := &fakeImplAdapter{
			testResp: &adapter.TestGenResponse{
				TestFilePath: "task_test.go",
				TestContent:  "package testmod\nimport \"testing\"\nfunc TestTask(t *testing.T) {}",
				PackageName:  "testmod",
			},
		}

		implResults, err := implorch.Run(ctx, implorch.Options{
			SpecDir:                planOutputDir,
			RepoDir:                repoDir,
			OutputDir:              implOutputDir,
			Adapter:                implAdapter,
			TestCmd:                "go test ./...",
			MaxRetries:             1,
			MaxConsecutiveFailures: 3,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(implResults).NotTo(BeNil())
		// Implement should not abstain since spec.md and plan.md exist.
		Expect(implResults.Status).NotTo(Equal(schema.StatusAbstain))
	})
})
