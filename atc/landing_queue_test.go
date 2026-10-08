package atc_test

import (
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

	It("A landing queue's config with a gate is refused naming gates until gates land", func() {
		_, err := atc.ParseLandingQueueConfig([]byte(valid + "gates:\n- template: unit-tests\n"))
		Expect(err).To(MatchError(ContainSubstring("gates")))
	})

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
