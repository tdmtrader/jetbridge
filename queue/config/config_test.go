package config_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/queue/config"
)

const minimal = "apiVersion: jetbridge.dev/queue/v2\nrepository: {uri: https://example.test/repo.git}\n"

var _ = Describe("Parse", func() {
	It("An unknown key is refused naming the nearest", func() {
		_, err := config.Parse([]byte(minimal + "batch: {maxx: 4}\n"))
		Expect(err).To(MatchError(`unknown key "batch.maxx"; did you mean "batch.max"?`))
	})

	It("A missing repository is refused", func() {
		_, err := config.Parse([]byte("apiVersion: jetbridge.dev/queue/v2\nrepository: {main: core}\n"))
		Expect(err).To(MatchError("repository.uri is required; it is never guessed"))
	})

	It("fills the defaults and keeps runner raw", func() {
		c, err := config.Parse([]byte(minimal + "runner: {job: x.yml, anything: [1, 2]}\n"))
		Expect(err).NotTo(HaveOccurred())
		Expect(c.Repository.Main).To(Equal("core"))
		Expect(c.Admission.Source).To(Equal("refs"))
		Expect(c.Admission.Require).To(BeEmpty())
		Expect(c.Lander.Mode).To(Equal("ff-only"))
		Expect(c.Batch).To(Equal(config.Batch{Max: 4, RetryNone: 1, Bisect: "halves"}))
		Expect(c.Runner.Content).To(HaveLen(4))
	})

	It("refuses an unknown value, listing the allowed ones", func() {
		_, err := config.Parse([]byte(minimal + "lander: {mode: merge}\n"))
		Expect(err).To(MatchError(`lander.mode "merge" is not allowed; use one of: ff-only`))
		_, err = config.Parse([]byte(minimal + "admission: {source: email}\n"))
		Expect(err).To(MatchError(`admission.source "email" is not allowed; use one of: refs, github-pr`))
		_, err = config.Parse([]byte("apiVersion: jetbridge.dev/queue/v1\nrepository: {uri: u}\n"))
		Expect(err).To(MatchError(`apiVersion "jetbridge.dev/queue/v1" is not allowed; use one of: jetbridge.dev/queue/v2`))
	})
})
