package orchestrator_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/plan/adapter"
	"github.com/concourse/ci-agent/plan/confidence"
	"github.com/concourse/ci-agent/plan/orchestrator"
	"github.com/concourse/ci-agent/schema"
)

// fakeAdapter returns canned responses.
type fakeAdapter struct {
	specOutput *adapter.SpecOutput
	specErr    error
	planOutput *adapter.PlanOutput
	planErr    error
}

func (f *fakeAdapter) GenerateSpec(_ context.Context, _ *schema.PlanningInput, _ adapter.SpecOpts) (*adapter.SpecOutput, error) {
	return f.specOutput, f.specErr
}

func (f *fakeAdapter) GeneratePlan(_ context.Context, _ *schema.PlanningInput, _ string, _ adapter.PlanOpts) (*adapter.PlanOutput, error) {
	return f.planOutput, f.planErr
}

func goodAdapter() *fakeAdapter {
	return &fakeAdapter{
		specOutput: &adapter.SpecOutput{
			SpecMarkdown: "Users can authenticate with password credentials. Tokens expire after 24 hours.",
			Assumptions:  []string{"Database exists"},
		},
		planOutput: &adapter.PlanOutput{
			PlanMarkdown: "Detailed plan here",
			Phases: []adapter.Phase{
				{
					Name: "Phase 1: Auth Setup",
					Tasks: []adapter.Task{
						{Description: "Create auth handler", Files: []string{"auth/handler.go"}},
						{Description: "Add JWT middleware", Files: []string{"middleware/jwt.go"}},
					},
				},
			},
			KeyFiles: []adapter.KeyFile{
				{Path: "auth/handler.go", Change: "NEW"},
				{Path: "middleware/jwt.go", Change: "NEW"},
			},
			Risks: []string{"Breaking change to auth flow"},
		},
	}
}

func writeInput(dir string, input *schema.PlanningInput) string {
	data, _ := json.Marshal(input)
	path := filepath.Join(dir, "input.json")
	os.WriteFile(path, data, 0644)
	return path
}

var _ = Describe("Orchestrator", func() {
	var tmpDir string

	BeforeEach(func() {
		var err error
		tmpDir, err = os.MkdirTemp("", "orchestrator-test")
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		os.RemoveAll(tmpDir)
	})

	It("runs full pipeline and writes all output files", func() {
		inputDir := filepath.Join(tmpDir, "input")
		outputDir := filepath.Join(tmpDir, "output")
		os.MkdirAll(inputDir, 0755)

		input := &schema.PlanningInput{
			Title:       "Add Authentication",
			Description: "Implement JWT-based authentication for the API",
			Type:        schema.StoryFeature,
			Priority:    schema.PriorityHigh,
			AcceptanceCriteria: []string{
				"Users can authenticate with password",
				"Tokens expire after 24 hours",
			},
		}
		inputPath := writeInput(inputDir, input)

		results, err := orchestrator.Run(context.Background(), orchestrator.Options{
			InputPath:           inputPath,
			OutputDir:           outputDir,
			Adapter:             goodAdapter(),
			ConfidenceThreshold: 0.6,
			ConfidenceWeights:   confidence.DefaultWeights(),
			Timeout:             5 * time.Minute,
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(results.Status).To(Equal(schema.StatusPass))
		Expect(results.Confidence).To(BeNumerically(">", 0.0))
		Expect(results.SchemaVersion).To(Equal("1.0"))

		// Check output files exist
		Expect(filepath.Join(outputDir, "spec.md")).To(BeAnExistingFile())
		Expect(filepath.Join(outputDir, "plan.md")).To(BeAnExistingFile())
		Expect(filepath.Join(outputDir, "results.json")).To(BeAnExistingFile())
		Expect(filepath.Join(outputDir, "events.ndjson")).To(BeAnExistingFile())

		// Verify results.json validates
		resultsData, _ := os.ReadFile(filepath.Join(outputDir, "results.json"))
		var decodedResults schema.Results
		Expect(json.Unmarshal(resultsData, &decodedResults)).To(Succeed())
		Expect(decodedResults.Validate()).To(Succeed())
		Expect(decodedResults.Artifacts).To(HaveLen(3))
	})

	It("returns status=fail when confidence < threshold", func() {
		inputDir := filepath.Join(tmpDir, "input")
		outputDir := filepath.Join(tmpDir, "output")
		os.MkdirAll(inputDir, 0755)

		input := &schema.PlanningInput{
			Title:       "Simple task",
			Description: "Do a thing",
		}
		inputPath := writeInput(inputDir, input)

		results, err := orchestrator.Run(context.Background(), orchestrator.Options{
			InputPath:           inputPath,
			OutputDir:           outputDir,
			Adapter:             goodAdapter(),
			ConfidenceThreshold: 0.99, // very high threshold
			ConfidenceWeights:   confidence.DefaultWeights(),
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(results.Status).To(Equal(schema.StatusFail))
	})

	It("handles adapter errors gracefully", func() {
		inputDir := filepath.Join(tmpDir, "input")
		outputDir := filepath.Join(tmpDir, "output")
		os.MkdirAll(inputDir, 0755)

		input := &schema.PlanningInput{
			Title:       "Task",
			Description: "Description",
		}
		inputPath := writeInput(inputDir, input)

		fa := &fakeAdapter{
			specErr: os.ErrNotExist,
		}

		results, err := orchestrator.Run(context.Background(), orchestrator.Options{
			InputPath:           inputPath,
			OutputDir:           outputDir,
			Adapter:             fa,
			ConfidenceThreshold: 0.6,
			ConfidenceWeights:   confidence.DefaultWeights(),
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(results.Status).To(Equal(schema.StatusError))
		Expect(results.Confidence).To(BeNumerically("~", 0.0, 0.01))
	})

	It("abstains when completeness < 0.2", func() {
		// With just title+desc, base is 0.3, which exceeds the 0.2 threshold.
		// The abstain path requires future input types with lower base scores.
		Skip("base completeness of 0.3 always exceeds 0.2 threshold; abstain requires future input types")
	})

	It("writes events in chronological order", func() {
		inputDir := filepath.Join(tmpDir, "input")
		outputDir := filepath.Join(tmpDir, "output")
		os.MkdirAll(inputDir, 0755)

		input := &schema.PlanningInput{
			Title:       "Task",
			Description: "Description here for a thing",
			AcceptanceCriteria: []string{"AC 1"},
		}
		inputPath := writeInput(inputDir, input)

		_, err := orchestrator.Run(context.Background(), orchestrator.Options{
			InputPath:           inputPath,
			OutputDir:           outputDir,
			Adapter:             goodAdapter(),
			ConfidenceThreshold: 0.6,
			ConfidenceWeights:   confidence.DefaultWeights(),
		})
		Expect(err).NotTo(HaveOccurred())

		eventsData, _ := os.ReadFile(filepath.Join(outputDir, "events.ndjson"))
		Expect(eventsData).NotTo(BeEmpty())

		// Parse events
		lines := splitNonEmpty(string(eventsData))
		Expect(len(lines)).To(BeNumerically(">=", 5))

		// First event should be agent.start
		var firstEvent schema.Event
		Expect(json.Unmarshal([]byte(lines[0]), &firstEvent)).To(Succeed())
		Expect(firstEvent.EventType).To(Equal(schema.EventAgentStart))

		// Last event should be agent.end
		var lastEvent schema.Event
		Expect(json.Unmarshal([]byte(lines[len(lines)-1]), &lastEvent)).To(Succeed())
		Expect(lastEvent.EventType).To(Equal(schema.EventAgentEnd))
	})
})

var _ = Describe("Writer", func() {
	var tmpDir string

	BeforeEach(func() {
		var err error
		tmpDir, err = os.MkdirTemp("", "writer-test")
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		os.RemoveAll(tmpDir)
	})

	It("writes spec.md", func() {
		artifact, err := orchestrator.WriteSpec(tmpDir, "# Spec Content")
		Expect(err).NotTo(HaveOccurred())
		Expect(artifact.Name).To(Equal("spec"))
		Expect(artifact.Path).To(Equal("spec.md"))
		Expect(artifact.MediaType).To(Equal("text/markdown"))

		content, _ := os.ReadFile(filepath.Join(tmpDir, "spec.md"))
		Expect(string(content)).To(Equal("# Spec Content"))
	})

	It("writes plan.md", func() {
		artifact, err := orchestrator.WritePlan(tmpDir, "# Plan Content")
		Expect(err).NotTo(HaveOccurred())
		Expect(artifact.Name).To(Equal("plan"))
		Expect(artifact.Path).To(Equal("plan.md"))

		content, _ := os.ReadFile(filepath.Join(tmpDir, "plan.md"))
		Expect(string(content)).To(Equal("# Plan Content"))
	})

	It("writes results.json that validates", func() {
		results := &schema.Results{
			SchemaVersion: "1.0",
			Status:        schema.StatusPass,
			Confidence:    0.85,
			Summary:       "Success",
			Artifacts:     []schema.Artifact{{Name: "spec", Path: "spec.md", MediaType: "text/markdown"}},
		}
		Expect(orchestrator.WriteResults(tmpDir, results)).To(Succeed())

		data, _ := os.ReadFile(filepath.Join(tmpDir, "results.json"))
		var decoded schema.Results
		Expect(json.Unmarshal(data, &decoded)).To(Succeed())
		Expect(decoded.Validate()).To(Succeed())
	})

	It("uses file permissions 0644", func() {
		orchestrator.WriteSpec(tmpDir, "content")
		info, _ := os.Stat(filepath.Join(tmpDir, "spec.md"))
		Expect(info.Mode().Perm()).To(Equal(os.FileMode(0644)))
	})
})

func splitNonEmpty(s string) []string {
	var result []string
	for _, line := range splitLines(s) {
		if line != "" {
			result = append(result, line)
		}
	}
	return result
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}
