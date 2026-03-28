package phaseconfig_test

import (
	"os"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/phaseconfig"
)

func TestPhaseconfig(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Phaseconfig Suite")
}

var _ = Describe("Config", func() {
	Describe("Parse", func() {
		It("parses a valid config", func() {
			yaml := []byte(`
name: plan
env:
  input_dir:
    var: INPUT_DIR
    default: story
  output_dir:
    var: OUTPUT_DIR
    default: plan-output
steps:
  - name: generate-spec
    template: prompts/plan/spec.md
    output_schema: schemas/spec_output.json
    artifacts:
      - name: spec
        path: spec.md
        media_type: text/markdown
  - name: generate-plan
    template: prompts/plan/plan.md
    input_from: [generate-spec]
    output_schema: schemas/plan_output.json
    artifacts:
      - name: plan
        path: plan.md
        media_type: text/markdown
scoring:
  threshold: 0.6
  weights:
    completeness: 0.3
    coverage: 0.4
    actionability: 0.3
`)
			cfg, err := phaseconfig.Parse(yaml)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Name).To(Equal("plan"))
			Expect(cfg.Steps).To(HaveLen(2))
			Expect(cfg.Steps[0].Name).To(Equal("generate-spec"))
			Expect(cfg.Steps[0].Template).To(Equal("prompts/plan/spec.md"))
			Expect(cfg.Steps[0].Artifacts).To(HaveLen(1))
			Expect(cfg.Steps[1].InputFrom).To(Equal([]string{"generate-spec"}))
			Expect(cfg.Scoring.Threshold).To(Equal(0.6))
			Expect(cfg.Scoring.Weights).To(HaveKeyWithValue("completeness", 0.3))
		})

		It("parses a config with MCP tools", func() {
			yaml := []byte(`
name: implement
mcp:
  - git
  - test
  - filesystem
steps:
  - name: implement-tasks
    template: prompts/implement/tasks.md
    verify_cmd: "go test ./..."
`)
			cfg, err := phaseconfig.Parse(yaml)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.MCP).To(Equal([]string{"git", "test", "filesystem"}))
			Expect(cfg.Steps[0].VerifyCmd).To(Equal("go test ./..."))
		})

		It("rejects config without name", func() {
			yaml := []byte(`
steps:
  - name: foo
    template: bar.md
`)
			_, err := phaseconfig.Parse(yaml)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("name is required"))
		})

		It("rejects config without steps", func() {
			yaml := []byte(`name: test`)
			_, err := phaseconfig.Parse(yaml)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("at least one step"))
		})

		It("rejects step without template", func() {
			yaml := []byte(`
name: test
steps:
  - name: missing-template
`)
			_, err := phaseconfig.Parse(yaml)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("template is required"))
		})
	})

	Describe("Validate input_from", func() {
		It("rejects input_from referencing a nonexistent step", func() {
			yaml := []byte(`
name: test
steps:
  - name: step1
    template: t.md
  - name: step2
    template: t.md
    input_from: [nonexistent]
`)
			_, err := phaseconfig.Parse(yaml)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("nonexistent"))
		})

		It("accepts valid input_from referencing an earlier step", func() {
			yaml := []byte(`
name: test
steps:
  - name: step1
    template: t.md
  - name: step2
    template: t.md
    input_from: [step1]
`)
			cfg, err := phaseconfig.Parse(yaml)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Steps[1].InputFrom).To(Equal([]string{"step1"}))
		})

		It("rejects self-referential input_from", func() {
			yaml := []byte(`
name: test
steps:
  - name: step1
    template: t.md
    input_from: [step1]
`)
			_, err := phaseconfig.Parse(yaml)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("step1"))
		})

		It("rejects input_from referencing a later step", func() {
			yaml := []byte(`
name: test
steps:
  - name: step1
    template: t.md
    input_from: [step2]
  - name: step2
    template: t.md
`)
			_, err := phaseconfig.Parse(yaml)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("step2"))
		})

		It("accepts empty input_from", func() {
			yaml := []byte(`
name: test
steps:
  - name: step1
    template: t.md
    input_from: []
`)
			_, err := phaseconfig.Parse(yaml)
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Describe("ValidateSuite", func() {
		It("returns no warnings when required var is provided by another phase", func() {
			// Both phases declare "repo_dir" — plan provides it with a default,
			// implement requires it. The suite validator sees the matching key.
			plan := &phaseconfig.Config{
				Name: "plan",
				Env: map[string]phaseconfig.EnvVar{
					"repo_dir": {Var: "REPO_DIR", Default: "repo"},
				},
				Steps: []phaseconfig.Step{{Name: "s", Template: "t.md"}},
			}
			impl := &phaseconfig.Config{
				Name: "implement",
				Env: map[string]phaseconfig.EnvVar{
					"repo_dir": {Var: "REPO_DIR", Required: true},
				},
				Steps: []phaseconfig.Step{{Name: "s", Template: "t.md"}},
			}
			warnings := phaseconfig.ValidateSuite([]*phaseconfig.Config{plan, impl})
			Expect(warnings).To(BeEmpty())
		})

		It("warns when required env var has no upstream provider", func() {
			impl := &phaseconfig.Config{
				Name: "implement",
				Env: map[string]phaseconfig.EnvVar{
					"spec_dir": {Var: "SPEC_DIR", Required: true},
				},
				Steps: []phaseconfig.Step{{Name: "s", Template: "t.md"}},
			}
			warnings := phaseconfig.ValidateSuite([]*phaseconfig.Config{impl})
			Expect(warnings).To(HaveLen(1))
			Expect(warnings[0].Message).To(ContainSubstring("spec_dir"))
		})

		It("returns no warnings for single config without required vars", func() {
			cfg := &phaseconfig.Config{
				Name: "review",
				Env: map[string]phaseconfig.EnvVar{
					"repo_dir": {Var: "REPO_DIR", Default: "repo"},
				},
				Steps: []phaseconfig.Step{{Name: "s", Template: "t.md"}},
			}
			warnings := phaseconfig.ValidateSuite([]*phaseconfig.Config{cfg})
			Expect(warnings).To(BeEmpty())
		})

		It("returns no warnings for empty input", func() {
			warnings := phaseconfig.ValidateSuite(nil)
			Expect(warnings).To(BeEmpty())
		})
	})

	Describe("ResolveEnv", func() {
		const testKey = "CI_AGENT_PHASE_TEST"

		AfterEach(func() {
			os.Unsetenv(testKey)
		})

		It("uses env var when set", func() {
			os.Setenv(testKey, "/my/dir")
			cfg := &phaseconfig.Config{
				Name: "test",
				Env: map[string]phaseconfig.EnvVar{
					"input_dir": {Var: testKey, Default: "fallback"},
				},
				Steps: []phaseconfig.Step{{Name: "s", Template: "t.md"}},
			}
			resolved, err := cfg.ResolveEnv()
			Expect(err).NotTo(HaveOccurred())
			Expect(resolved["input_dir"]).To(Equal("/my/dir"))
		})

		It("uses default when env var not set", func() {
			cfg := &phaseconfig.Config{
				Name: "test",
				Env: map[string]phaseconfig.EnvVar{
					"input_dir": {Var: testKey, Default: "fallback"},
				},
				Steps: []phaseconfig.Step{{Name: "s", Template: "t.md"}},
			}
			resolved, err := cfg.ResolveEnv()
			Expect(err).NotTo(HaveOccurred())
			Expect(resolved["input_dir"]).To(Equal("fallback"))
		})

		It("errors on missing required var", func() {
			cfg := &phaseconfig.Config{
				Name: "test",
				Env: map[string]phaseconfig.EnvVar{
					"repo_dir": {Var: testKey, Required: true},
				},
				Steps: []phaseconfig.Step{{Name: "s", Template: "t.md"}},
			}
			_, err := cfg.ResolveEnv()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(testKey))
		})
	})

	Describe("Hash", func() {
		It("produces consistent hash", func() {
			data := []byte("name: test\nsteps:\n  - name: s\n    template: t.md\n")
			h1 := phaseconfig.Hash(data)
			h2 := phaseconfig.Hash(data)
			Expect(h1).To(Equal(h2))
			Expect(h1).To(HaveLen(64)) // SHA256 hex
		})

		It("produces different hash for different content", func() {
			h1 := phaseconfig.Hash([]byte("a"))
			h2 := phaseconfig.Hash([]byte("b"))
			Expect(h1).NotTo(Equal(h2))
		})
	})
})
