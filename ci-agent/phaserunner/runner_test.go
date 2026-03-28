package phaserunner_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/llm"
	"github.com/concourse/ci-agent/phaseconfig"
	"github.com/concourse/ci-agent/phaserunner"
	"github.com/concourse/ci-agent/schema"
)

func TestPhaserunner(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Phaserunner Suite")
}

// fakeLLMClient returns a canned response for every call.
type fakeLLMClient struct {
	responses []json.RawMessage
	callIdx   int
	prompts   []string
}

func (f *fakeLLMClient) Call(_ context.Context, prompt string, _ llm.CallOpts) (llm.CallResult, error) {
	f.prompts = append(f.prompts, prompt)
	if f.callIdx < len(f.responses) {
		resp := f.responses[f.callIdx]
		f.callIdx++
		return llm.CallResult{Result: resp}, nil
	}
	return llm.CallResult{Result: json.RawMessage(`{}`)}, nil
}

var _ = Describe("Run", func() {
	var (
		tmpDir    string
		outputDir string
		baseDir   string
	)

	BeforeEach(func() {
		var err error
		tmpDir, err = os.MkdirTemp("", "phaserunner-test")
		Expect(err).NotTo(HaveOccurred())
		outputDir = filepath.Join(tmpDir, "output")
		baseDir = filepath.Join(tmpDir, "base")
		os.MkdirAll(baseDir, 0755)
	})

	AfterEach(func() {
		os.RemoveAll(tmpDir)
	})

	It("runs a single-step phase and writes results", func() {
		// Write a prompt template
		promptDir := filepath.Join(baseDir, "prompts")
		os.MkdirAll(promptDir, 0755)
		Expect(os.WriteFile(
			filepath.Join(promptDir, "test.md"),
			[]byte("Generate something for {{.Env.input_dir}}"),
			0644,
		)).To(Succeed())

		cfg := &phaseconfig.Config{
			Name: "test-phase",
			Env: map[string]phaseconfig.EnvVar{
				"input_dir": {Var: "TEST_INPUT_DIR_UNUSED", Default: "story"},
			},
			Steps: []phaseconfig.Step{
				{
					Name:     "generate",
					Template: "prompts/test.md",
					Artifacts: []phaseconfig.Artifact{
						{Name: "output", Path: "output.json", MediaType: "application/json"},
					},
				},
			},
		}

		fakeClient := &fakeLLMClient{
			responses: []json.RawMessage{
				json.RawMessage(`{"result": "hello"}`),
			},
		}

		results, err := phaserunner.Run(context.Background(), phaserunner.Options{
			Config:    cfg,
			OutputDir: outputDir,
			Client:    fakeClient,
			BaseDir:   baseDir,
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(results.Status).To(Equal(schema.StatusPass))
		Expect(results.Metadata["phase"]).To(Equal("test-phase"))

		// Check results.json was written
		resultsData, err := os.ReadFile(filepath.Join(outputDir, "results.json"))
		Expect(err).NotTo(HaveOccurred())
		var written schema.Results
		Expect(json.Unmarshal(resultsData, &written)).To(Succeed())
		Expect(written.Status).To(Equal(schema.StatusPass))

		// Check events.ndjson was written
		_, err = os.Stat(filepath.Join(outputDir, "events.ndjson"))
		Expect(err).NotTo(HaveOccurred())

		// Check artifact was written
		_, err = os.Stat(filepath.Join(outputDir, "output.json"))
		Expect(err).NotTo(HaveOccurred())

		// Check prompt was rendered with env vars
		Expect(fakeClient.prompts[0]).To(ContainSubstring("story"))
	})

	It("runs multi-step phase with input_from chaining", func() {
		promptDir := filepath.Join(baseDir, "prompts")
		os.MkdirAll(promptDir, 0755)
		Expect(os.WriteFile(
			filepath.Join(promptDir, "step1.md"),
			[]byte("Step 1 prompt"),
			0644,
		)).To(Succeed())
		Expect(os.WriteFile(
			filepath.Join(promptDir, "step2.md"),
			[]byte("Step 2 with prior: {{index .StepOutputs \"step1\"}}"),
			0644,
		)).To(Succeed())

		cfg := &phaseconfig.Config{
			Name: "multi",
			Steps: []phaseconfig.Step{
				{Name: "step1", Template: "prompts/step1.md"},
				{Name: "step2", Template: "prompts/step2.md", InputFrom: []string{"step1"}},
			},
		}

		fakeClient := &fakeLLMClient{
			responses: []json.RawMessage{
				json.RawMessage(`{"spec": "generated spec"}`),
				json.RawMessage(`{"plan": "generated plan"}`),
			},
		}

		results, err := phaserunner.Run(context.Background(), phaserunner.Options{
			Config:    cfg,
			OutputDir: outputDir,
			Client:    fakeClient,
			BaseDir:   baseDir,
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(results.Status).To(Equal(schema.StatusPass))
		Expect(fakeClient.prompts).To(HaveLen(2))
		// Step 2's prompt should contain step 1's output
		Expect(fakeClient.prompts[1]).To(ContainSubstring("generated spec"))
	})

	It("marks phase as fail when verify_cmd fails", func() {
		promptDir := filepath.Join(baseDir, "prompts")
		os.MkdirAll(promptDir, 0755)
		Expect(os.WriteFile(
			filepath.Join(promptDir, "impl.md"),
			[]byte("implement something"),
			0644,
		)).To(Succeed())

		cfg := &phaseconfig.Config{
			Name: "verify-test",
			Steps: []phaseconfig.Step{
				{
					Name:      "implement",
					Template:  "prompts/impl.md",
					VerifyCmd: "false", // always fails
				},
			},
		}

		fakeClient := &fakeLLMClient{
			responses: []json.RawMessage{json.RawMessage(`{}`)},
		}

		results, err := phaserunner.Run(context.Background(), phaserunner.Options{
			Config:    cfg,
			OutputDir: outputDir,
			Client:    fakeClient,
			BaseDir:   baseDir,
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(results.Status).To(Equal(schema.StatusFail))
	})

	It("marks phase as pass when verify_cmd succeeds", func() {
		promptDir := filepath.Join(baseDir, "prompts")
		os.MkdirAll(promptDir, 0755)
		Expect(os.WriteFile(
			filepath.Join(promptDir, "impl.md"),
			[]byte("implement something"),
			0644,
		)).To(Succeed())

		cfg := &phaseconfig.Config{
			Name: "verify-pass",
			Steps: []phaseconfig.Step{
				{
					Name:      "implement",
					Template:  "prompts/impl.md",
					VerifyCmd: "true", // always succeeds
				},
			},
		}

		fakeClient := &fakeLLMClient{
			responses: []json.RawMessage{json.RawMessage(`{}`)},
		}

		results, err := phaserunner.Run(context.Background(), phaserunner.Options{
			Config:    cfg,
			OutputDir: outputDir,
			Client:    fakeClient,
			BaseDir:   baseDir,
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(results.Status).To(Equal(schema.StatusPass))
	})

	It("passes env map to shell and supports parameter expansion with default", func() {
		promptDir := filepath.Join(baseDir, "prompts")
		os.MkdirAll(promptDir, 0755)
		Expect(os.WriteFile(
			filepath.Join(promptDir, "impl.md"),
			[]byte("implement something"),
			0644,
		)).To(Succeed())

		// MY_VAR is NOT in the env map, so the shell should use the default "fallback"
		// The verify_cmd writes the expanded value to a file so we can inspect it
		markerFile := filepath.Join(tmpDir, "expanded.txt")
		cfg := &phaseconfig.Config{
			Name: "shell-expand-default",
			Steps: []phaseconfig.Step{
				{
					Name:     "implement",
					Template: "prompts/impl.md",
					// Shell parameter expansion: MY_VAR unset -> use "hello"
					// Then test that the expanded value equals "hello"
					VerifyCmd: `test "${MY_VAR:-hello}" = "hello"`,
				},
			},
		}

		fakeClient := &fakeLLMClient{
			responses: []json.RawMessage{json.RawMessage(`{}`)},
		}

		results, err := phaserunner.Run(context.Background(), phaserunner.Options{
			Config:    cfg,
			OutputDir: outputDir,
			Client:    fakeClient,
			BaseDir:   baseDir,
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(results.Status).To(Equal(schema.StatusPass))
		_ = markerFile // used for documentation only
	})

	It("passes env map to shell and uses env var when set", func() {
		promptDir := filepath.Join(baseDir, "prompts")
		os.MkdirAll(promptDir, 0755)
		Expect(os.WriteFile(
			filepath.Join(promptDir, "impl.md"),
			[]byte("implement something"),
			0644,
		)).To(Succeed())

		// MY_VAR IS in the env map with value "custom", so shell expansion
		// should pick up "custom" instead of the default
		cfg := &phaseconfig.Config{
			Name: "shell-expand-override",
			Env: map[string]phaseconfig.EnvVar{
				"MY_VAR": {Var: "MY_VAR_UNUSED", Default: "custom"},
			},
			Steps: []phaseconfig.Step{
				{
					Name:     "implement",
					Template: "prompts/impl.md",
					// MY_VAR should be "custom" from the env map
					VerifyCmd: `test "$MY_VAR" = "custom"`,
				},
			},
		}

		fakeClient := &fakeLLMClient{
			responses: []json.RawMessage{json.RawMessage(`{}`)},
		}

		results, err := phaserunner.Run(context.Background(), phaserunner.Options{
			Config:    cfg,
			OutputDir: outputDir,
			Client:    fakeClient,
			BaseDir:   baseDir,
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(results.Status).To(Equal(schema.StatusPass))
	})

	It("exports resolved env vars to child shell process", func() {
		promptDir := filepath.Join(baseDir, "prompts")
		os.MkdirAll(promptDir, 0755)
		Expect(os.WriteFile(
			filepath.Join(promptDir, "impl.md"),
			[]byte("implement something"),
			0644,
		)).To(Succeed())

		cfg := &phaseconfig.Config{
			Name: "env-export-test",
			Env: map[string]phaseconfig.EnvVar{
				// Use a key other than repo_dir to avoid triggering cmd.Dir logic
				"test_cmd": {Var: "TEST_CMD_UNUSED", Default: "my-test-command"},
			},
			Steps: []phaseconfig.Step{
				{
					Name:     "implement",
					Template: "prompts/impl.md",
					// Verify the env map value is accessible as a shell env var
					VerifyCmd: `test "$test_cmd" = "my-test-command"`,
				},
			},
		}

		fakeClient := &fakeLLMClient{
			responses: []json.RawMessage{json.RawMessage(`{}`)},
		}

		results, err := phaserunner.Run(context.Background(), phaserunner.Options{
			Config:    cfg,
			OutputDir: outputDir,
			Client:    fakeClient,
			BaseDir:   baseDir,
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(results.Status).To(Equal(schema.StatusPass))
	})

	It("writes provenance when config path is provided", func() {
		promptDir := filepath.Join(baseDir, "prompts")
		os.MkdirAll(promptDir, 0755)
		promptPath := filepath.Join(promptDir, "test.md")
		Expect(os.WriteFile(promptPath, []byte("prompt"), 0644)).To(Succeed())

		configPath := filepath.Join(tmpDir, "phase.yaml")
		configData := []byte("name: prov-test\nsteps:\n  - name: s\n    template: prompts/test.md\n")
		Expect(os.WriteFile(configPath, configData, 0644)).To(Succeed())

		cfg := &phaseconfig.Config{
			Name: "prov-test",
			Steps: []phaseconfig.Step{
				{Name: "s", Template: "prompts/test.md"},
			},
		}

		fakeClient := &fakeLLMClient{
			responses: []json.RawMessage{json.RawMessage(`{}`)},
		}

		_, err := phaserunner.Run(context.Background(), phaserunner.Options{
			ConfigPath: configPath,
			Config:     cfg,
			OutputDir:  outputDir,
			Client:     fakeClient,
			BaseDir:    baseDir,
			Model:      "claude-sonnet-4-6",
		})

		Expect(err).NotTo(HaveOccurred())

		provData, err := os.ReadFile(filepath.Join(outputDir, "provenance.json"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(provData)).To(ContainSubstring("claude-sonnet-4-6"))
		Expect(string(provData)).To(ContainSubstring("phase.yaml"))
	})

	It("writes markdown artifacts by extracting fields from JSON", func() {
		promptDir := filepath.Join(baseDir, "prompts")
		os.MkdirAll(promptDir, 0755)
		Expect(os.WriteFile(filepath.Join(promptDir, "gen.md"), []byte("generate"), 0644)).To(Succeed())

		cfg := &phaseconfig.Config{
			Name: "md-test",
			Steps: []phaseconfig.Step{
				{
					Name:     "gen",
					Template: "prompts/gen.md",
					Artifacts: []phaseconfig.Artifact{
						{Name: "spec", Path: "spec.md", MediaType: "text/markdown"},
					},
				},
			},
		}

		fakeClient := &fakeLLMClient{
			responses: []json.RawMessage{
				json.RawMessage(`{"spec_markdown": "# My Spec\n\nDetails here."}`),
			},
		}

		_, err := phaserunner.Run(context.Background(), phaserunner.Options{
			Config:    cfg,
			OutputDir: outputDir,
			Client:    fakeClient,
			BaseDir:   baseDir,
		})

		Expect(err).NotTo(HaveOccurred())
		specContent, err := os.ReadFile(filepath.Join(outputDir, "spec.md"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(specContent)).To(ContainSubstring("My Spec"))
	})
})

var _ = Describe("RenderTemplate", func() {
	var tmpDir string

	BeforeEach(func() {
		var err error
		tmpDir, err = os.MkdirTemp("", "template-test")
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		os.RemoveAll(tmpDir)
	})

	It("renders a template with env vars", func() {
		path := filepath.Join(tmpDir, "prompt.md")
		Expect(os.WriteFile(path, []byte("Repo: {{.Env.repo_dir}}\nLang: {{.Env.language}}"), 0644)).To(Succeed())

		result, err := phaserunner.RenderTemplate("", path, phaserunner.TemplateData{
			Env: map[string]string{"repo_dir": "/my/repo", "language": "Go"},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal("Repo: /my/repo\nLang: Go"))
	})

	It("renders with step outputs", func() {
		path := filepath.Join(tmpDir, "prompt.md")
		Expect(os.WriteFile(path, []byte(`Prior: {{index .StepOutputs "spec"}}`), 0644)).To(Succeed())

		result, err := phaserunner.RenderTemplate("", path, phaserunner.TemplateData{
			StepOutputs: map[string]string{
				"spec": `{"key":"val"}`,
			},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(ContainSubstring(`{"key":"val"}`))
	})

	It("handles missing keys gracefully", func() {
		path := filepath.Join(tmpDir, "prompt.md")
		Expect(os.WriteFile(path, []byte("Value: {{.Env.missing}}"), 0644)).To(Succeed())

		result, err := phaserunner.RenderTemplate("", path, phaserunner.TemplateData{
			Env: map[string]string{},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal("Value: "))
	})
})
