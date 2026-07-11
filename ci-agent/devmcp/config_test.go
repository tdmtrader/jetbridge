package devmcp_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/devmcp"
)

var _ = Describe("Config", func() {
	valid := []byte(`
schema_version: 1
repo:
  test: { cmd: ["make", "test-quick"], failed_exit_codes: [1, 2] }
components:
  - id: app
    description: single shell-script application
    paths: ["src/"]
    kind: cli
    build: { cmd: ["sh", "scripts/build.sh"] }
    test:  { cmd: ["sh", "scripts/test.sh"], focus_flag: "--focus" }
    lint:  { cmd: ["sh", "scripts/lint.sh"] }
`)

	It("parses a valid config", func() {
		cfg, err := devmcp.Parse(valid)
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.SchemaVersion).To(Equal(1))
		Expect(cfg.Repo.Test.Cmd).To(Equal([]string{"make", "test-quick"}))
		Expect(cfg.Repo.Test.FailedExitCodes).To(Equal([]int{1, 2}))
		Expect(cfg.Components).To(HaveLen(1))
		Expect(cfg.Components[0].ID).To(Equal("app"))
		Expect(cfg.Components[0].Test.FocusFlag).To(Equal("--focus"))

		comp, found := cfg.Component("app")
		Expect(found).To(BeTrue())
		Expect(comp.Kind).To(Equal("cli"))
		_, found = cfg.Component("nope")
		Expect(found).To(BeFalse())
	})

	DescribeTable("rejects invalid configs",
		func(yaml string, msg string) {
			_, err := devmcp.Parse([]byte(yaml))
			Expect(err).To(MatchError(ContainSubstring(msg)))
		},
		Entry("wrong schema_version",
			"schema_version: 2\ncomponents:\n  - {id: a, description: d, paths: [\"a/\"], kind: cli}\n",
			"unsupported schema_version"),
		Entry("no components",
			"schema_version: 1\ncomponents: []\n",
			"at least one component"),
		Entry("duplicate ids",
			"schema_version: 1\ncomponents:\n  - {id: a, description: d, paths: [\"a/\"], kind: cli}\n  - {id: a, description: d, paths: [\"b/\"], kind: cli}\n",
			"duplicate id"),
		Entry("invalid kind",
			"schema_version: 1\ncomponents:\n  - {id: a, description: d, paths: [\"a/\"], kind: banana}\n",
			"invalid kind"),
		Entry("missing paths",
			"schema_version: 1\ncomponents:\n  - {id: a, description: d, paths: [], kind: cli}\n",
			"paths is required"),
		Entry("empty cmd",
			"schema_version: 1\ncomponents:\n  - {id: a, description: d, paths: [\"a/\"], kind: cli, build: {cmd: []}}\n",
			"cmd must be non-empty"),
		Entry("unknown top-level key (strict decoding)",
			"schema_version: 1\nbogus: true\ncomponents:\n  - {id: a, description: d, paths: [\"a/\"], kind: cli}\n",
			"bogus"),
	)
})
