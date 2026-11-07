package atc_test

import (
	"time"

	"github.com/concourse/concourse/atc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("A landing queue's config", func() {
	valid := "repository: https://example.test/repo.git\ntrunk: core\ncompose: landing-compose\nland: landing-land\ngates: []\n"

	It("A landing queue's config decodes with no gates", func() {
		config, err := atc.ParseLandingQueueConfig([]byte(valid))
		Expect(err).NotTo(HaveOccurred())
		Expect(config.Trunk).To(Equal("core"))
		Expect(config.Gates).To(BeEmpty())
	})

	It("A landing queue's config with an unknown key is refused naming it", func() {
		_, err := atc.ParseLandingQueueConfig([]byte(valid + "branch: core\n"))
		Expect(err).To(MatchError(ContainSubstring("branch")))
	})

	It("A landing queue's config without a trunk is refused naming trunk", func() {
		_, err := atc.ParseLandingQueueConfig([]byte("repository: https://r\ncompose: c\nland: l\n"))
		Expect(err).To(MatchError(ContainSubstring("trunk is required")))
	})

	DescribeTable("A landing queue's config refuses a trunk or repository git could read as an option",
		func(body string, key string) {
			_, err := atc.ParseLandingQueueConfig([]byte(body))
			Expect(err).To(MatchError(ContainSubstring(key)))
		},
		Entry("trunk as a ref", "repository: https://r\ntrunk: refs/heads/core\ncompose: c\nland: l\n", "trunk"),
		Entry("trunk as an option", "repository: https://r\ntrunk: --upload-pack=x\ncompose: c\nland: l\n", "trunk"),
		Entry("repository as an option", "repository: --upload-pack=x\ntrunk: core\ncompose: c\nland: l\n", "repository"),
		Entry("repository with a space", "repository: 'https://r x'\ntrunk: core\ncompose: c\nland: l\n", "repository"),
	)

	It("A landing queue's config declares gates, each a template with an optional wait that defaults to 45m", func() {
		body := "repository: https://example.test/repo.git\ntrunk: core\ncompose: landing-compose\nland: landing-land\n" +
			"gates:\n- template: landing-gate-tests\n- template: landing-gate-review\n  wait: 40m\n"
		config, err := atc.ParseLandingQueueConfig([]byte(body))
		Expect(err).NotTo(HaveOccurred())
		Expect(config.Gates).To(Equal([]atc.LandingQueueGate{{Template: "landing-gate-tests"}, {Template: "landing-gate-review", Wait: "40m"}}))
		Expect(config.Gates[0].WaitDuration()).To(Equal(45 * time.Minute))
		Expect(config.Gates[1].WaitDuration()).To(Equal(40 * time.Minute))
	})

	DescribeTable("A landing queue's config refuses a gate it could not run, naming it",
		func(gates string, want string) {
			body := "repository: https://example.test/repo.git\ntrunk: core\ncompose: landing-compose\nland: landing-land\ngates:\n" + gates
			_, err := atc.ParseLandingQueueConfig([]byte(body))
			Expect(err).To(MatchError(ContainSubstring(want)))
		},
		Entry("an unknown key", "- template: t\n  params: {}\n", "params"),
		Entry("no template", "- wait: 5m\n", "gates[0].template is required"),
		Entry("the same template twice", "- template: t\n- template: t\n", "gates[1].template \"t\" is declared twice"),
		Entry("the compose template", "- template: landing-compose\n", "compose or land template"),
		Entry("a wait that is not a duration", "- template: t\n  wait: soon\n", "gates[0].wait \"soon\""),
		Entry("a wait of nothing", "- template: t\n  wait: 0s\n", "gates[0].wait"),
		Entry("a wait past a day", "- template: t\n  wait: 25h\n", "gates[0].wait"),
	)

	DescribeTable("An entry id is one safe ref component",
		func(id string, ok bool) { Expect(atc.ValidLandingEntryID(id)).To(Equal(ok)) },
		Entry("short sha", "a1b2c3d", true),
		Entry("with dots and dashes", "fix-1.2_x", true),
		Entry("leading dash", "-x", false),
		Entry("double dot", "a..b", false),
		Entry("slash", "a/b", false),
		Entry("lock suffix", "a.lock", false),
		Entry("empty", "", false),
	)
})
