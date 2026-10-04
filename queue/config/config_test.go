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

	It("An alias cannot bypass the strict key check", func() {
		_, err := config.Parse([]byte(minimal + "runner: &b {maxx: 9}\nbatch: *b\n"))
		Expect(err).To(MatchError(`aliases are not supported (at "batch")`))
	})

	It("reads a notify section for its adapter", func() {
		c, err := config.Parse([]byte(minimal + "notify: {kind: log, path: \"-\"}\n"))
		Expect(err).NotTo(HaveOccurred())
		Expect(c.Notify.IsZero()).To(BeFalse())
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
		Expect(c.Admission.Prefix).To(Equal("refs/queue/admit/"))
		_, err = config.Parse([]byte(minimal + "admission: {prefix: refs/queue/admit}\n"))
		Expect(err).To(MatchError("admission.prefix must be a ref prefix under refs/ ending in /"))
		Expect(c.Batch).To(Equal(config.Batch{Max: 4, RetryNone: 1, Strategy: "serial"}))
		Expect(c.Runner.Content).To(HaveLen(4))
	})

	It("The strategy defaults to serial and an unknown one is refused", func() {
		c, err := config.Parse([]byte(minimal))
		Expect(err).NotTo(HaveOccurred())
		Expect(c.Batch.Strategy).To(Equal("serial"))
		c, err = config.Parse([]byte(minimal + "batch: {strategy: serial}\n"))
		Expect(err).NotTo(HaveOccurred())
		Expect(c.Batch.Strategy).To(Equal("serial"))
		_, err = config.Parse([]byte(minimal + "batch: {strategy: speculative}\n"))
		Expect(err).To(MatchError(`batch.strategy: unsupported value; use one of: serial`))
		_, err = config.Parse([]byte(minimal + "batch: {max: https://user:SECRET@host}\n"))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).NotTo(ContainSubstring("SECRET"))
		_, err = config.Parse([]byte("apiVersion: https://user:SECRET@host\nrepository: {uri: u}\n"))
		Expect(err).To(MatchError(ContainSubstring("apiVersion: a URL must not hold credentials")))
		_, err = config.Parse([]byte(minimal + "repository: {main: \"https://user:SECRET@host\", candidate: \"https://user:SECRET@host\"}\n"))
		Expect(err.Error()).NotTo(ContainSubstring("SECRET"))
		_, err = config.Parse([]byte(minimal + "batch: {strategey: serial}\n"))
		Expect(err).To(MatchError(`unknown key "batch.strategey"; did you mean "batch.strategy"?`))
	})

	It("allows three land failures in a row by default and refuses fewer than one", func() {
		c, err := config.Parse([]byte(minimal))
		Expect(err).NotTo(HaveOccurred())
		Expect(c.Lander.MaxFailures).To(Equal(3))
		c, err = config.Parse([]byte(minimal + "lander: {max_failures: 5}\n"))
		Expect(err).NotTo(HaveOccurred())
		Expect(c.Lander.MaxFailures).To(Equal(5))
		_, err = config.Parse([]byte(minimal + "lander: {max_failures: 0}\n"))
		Expect(err).To(MatchError("lander.max_failures must be at least 1"))
		_, err = config.Parse([]byte(minimal + "lander: {max_failure: 2}\n"))
		Expect(err).To(MatchError(`unknown key "lander.max_failure"; did you mean "lander.max_failures"?`))
	})

	It("reads the git lander's lease ref, defaulting the lease ref", func() {
		c, err := config.Parse([]byte(minimal + "lander: {scratch: /var/tmp/q}\n"))
		Expect(err).NotTo(HaveOccurred())
		Expect(c.Lander).To(Equal(config.Lander{MaxFailures: 3,
			LeaseRef: "refs/queue/lease", Scratch: "/var/tmp/q"}))
		_, err = config.Parse([]byte(minimal + "lander: {remote: ../origin.git}\n"))
		Expect(err).To(MatchError(ContainSubstring(`unknown key "lander.remote"`)))
		_, err = config.Parse([]byte(minimal + "lander: {lease_ref: lease}\n"))
		Expect(err).To(MatchError("lander.lease_ref must be a full ref name under refs/"))
		_, err = config.Parse([]byte(minimal + "lander: {lease_rf: refs/x}\n"))
		Expect(err).To(MatchError(`unknown key "lander.lease_rf"; did you mean "lander.lease_ref"?`))
	})

	It("batch.order is refused as an unknown key", func() {
		_, err := config.Parse([]byte(minimal + "batch: {order: strict}\n"))
		Expect(err).To(MatchError(ContainSubstring(`unknown key "batch.order"`)))
	})

	It("refuses an unknown value, listing the allowed ones", func() {
		_, err := config.Parse([]byte(minimal + "admission: {source: email}\n"))
		Expect(err).To(MatchError(`admission.source: unsupported value; use one of: refs`))
		_, err = config.Parse([]byte("apiVersion: jetbridge.dev/queue/v1\nrepository: {uri: u}\n"))
		Expect(err).To(MatchError(`apiVersion: unsupported value; use one of: jetbridge.dev/queue/v2`))
	})

	It("refuses an admission prefix that overlaps a ref the queue owns, and accepts the default", func() {
		for _, p := range []string{"refs/queue/", "refs/", "refs/queue/state/", "refs/queue/lease/", "refs/heads/", "refs/heads/core/", "refs/heads/queue-next/"} {
			_, err := config.Parse([]byte(minimal + "admission: {prefix: " + p + "}\n"))
			Expect(err).To(MatchError(ContainSubstring("overlaps")), p)
		}
		_, err := config.Parse([]byte(minimal + "admission: {prefix: refs/queue/state}\n"))
		Expect(err).To(HaveOccurred())
		_, err = config.Parse([]byte(minimal + "admission: {prefix: refs/queue/admit/}\nstore: {ref: refs/queue/admit/x}\n"))
		Expect(err).To(MatchError(ContainSubstring("overlaps")))
		_, err = config.Parse([]byte(minimal))
		Expect(err).NotTo(HaveOccurred())
	})

	It("A control prefix that overlaps a ref the queue owns is refused", func() {
		for _, p := range []string{"refs/queue/admit/", "refs/queue/admit/x/", "refs/queue/", "refs/queue/state/", "refs/queue/lease/", "refs/heads/core/", "refs/"} {
			_, err := config.Parse([]byte(minimal + "admission: {control_prefix: " + p + "}\n"))
			Expect(err).To(MatchError(ContainSubstring("overlaps")), p)
		}
		_, err := config.Parse([]byte(minimal + "admission: {control_prefix: refs/queue/control}\n"))
		Expect(err).To(MatchError("admission.control_prefix must be a ref prefix under refs/ ending in /"))
		c, err := config.Parse([]byte(minimal))
		Expect(err).NotTo(HaveOccurred())
		Expect(c.Admission.ControlPrefix).To(Equal("refs/queue/control/"))
	})

	It("A key nothing reads is refused as unknown", func() {
		for _, bad := range []string{"admission: {require: [x]}", "compose: {hook: x}", "compose: {mode: squash}",
			"lander: {credential: x}", "lander: {mode: ff-only}", "batch: {bisect: halves}"} {
			_, err := config.Parse([]byte(minimal + bad + "\n"))
			Expect(err).To(MatchError(ContainSubstring("unknown key")), bad)
		}
		_, err := config.Parse([]byte(minimal + "admission: {source: github-pr}\n"))
		Expect(err).To(MatchError(`admission.source: unsupported value; use one of: refs`))
	})

	It("adaptive batch size is off by default and read when set", func() {
		c, err := config.Parse([]byte(minimal))
		Expect(err).NotTo(HaveOccurred())
		Expect(c.Batch.Adaptive).To(BeNil())
		c, err = config.Parse([]byte(minimal + "batch: {max: 8, adaptive: {start: 4, min: 2, grow_after: 3}}\n"))
		Expect(err).NotTo(HaveOccurred())
		Expect(c.Batch.Adaptive).To(Equal(&config.Adaptive{Start: 4, Min: 2, GrowAfter: 3}))
	})

	It("refuses adaptive sizes out of order, or no growth streak, or an unknown key", func() {
		for _, bad := range []string{"{start: 9, min: 1, grow_after: 1}", "{start: 4, min: 5, grow_after: 1}",
			"{start: 4, min: 0, grow_after: 1}", "{start: 4, min: 1, grow_after: 0}", "{start: 4, min: 1}"} {
			_, err := config.Parse([]byte(minimal + "batch: {max: 8, adaptive: " + bad + "}\n"))
			Expect(err).To(MatchError("batch.adaptive needs 1 <= min <= start <= batch.max and grow_after >= 1"), bad)
		}
		_, err := config.Parse([]byte(minimal + "batch: {adaptive: {strat: 2}}\n"))
		Expect(err).To(MatchError(`unknown key "batch.adaptive.strat"; did you mean "batch.adaptive.start"?`))
	})

	It("A URL holding a credential is refused naming the setting", func() {
		for _, uri := range []string{"https://user:F4ke/Pa55@host/%zz", "https://user:F4kePa55@host/repo.git",
			"https://F4keTok3n@host/repo.git", "ssh://git:F4kePa55@host/repo.git"} {
			_, err := config.Parse([]byte("apiVersion: jetbridge.dev/queue/v2\nrepository: {uri: '" + uri + "'}\n"))
			Expect(err).To(MatchError(ContainSubstring("repository.uri: a URL must not hold credentials")), "uri %q", uri)
			Expect(err.Error()).NotTo(ContainSubstring("F4ke"), "uri %q", uri)
		}
		for _, uri := range []string{"ssh://git@host/repo.git", "git@host:org/repo.git", "/srv/repo.git", "file:///srv/repo.git", "https://host/F4ke@x"} {
			_, err := config.Parse([]byte("apiVersion: jetbridge.dev/queue/v2\nrepository: {uri: '" + uri + "'}\n"))
			Expect(err).NotTo(HaveOccurred(), "uri %q", uri) // an ssh login name is no secret
		}
		_, err := config.Parse([]byte("apiVersion: jetbridge.dev/queue/v2\nrepository: {uri: 'https://host/%zz'}\n"))
		Expect(err).To(MatchError(ContainSubstring("repository.uri: a URL must not hold credentials and must parse")))
		Expect(err.Error()).NotTo(ContainSubstring("%zz"))
	})

	It("checks every string in the file for a URL credential, naming only its path", func() {
		for extra, key := range map[string]string{
			"notify: {kind: log, path: 'https://user:F4ke,Pa55@host/events.jsonl'}\n": "notify.path",
			"runner: {kind: jetbridge, anything: ['https://F4keTok3n@host/x']}\n":     "runner.anything",
			"lander: {scratch: 'https://user:F4ke@host/x'}\n":                         "lander.scratch",
			"'https://user:F4ke@host/x': 1\n":                                         "config",
		} {
			_, err := config.Parse([]byte(minimal + extra))
			Expect(err).To(MatchError(ContainSubstring(key+": a URL must not hold credentials")), extra)
			Expect(err.Error()).NotTo(MatchRegexp("F4ke|Pa55"), extra)
		}
	})

	It("looks for credentials only in a URL's authority", func() {
		for _, uri := range []string{"file:///tmp/queue@home.git", "https://host?contact=dev@example.com", "https://host/x#dev@example.com"} {
			_, err := config.Parse([]byte("apiVersion: jetbridge.dev/queue/v2\nrepository: {uri: '" + uri + "'}\n"))
			Expect(err).NotTo(HaveOccurred(), "uri %q", uri)
		}
		// the authority "user:F4ke" has no @, but it does not parse (an invalid port), so it is refused
		_, err := config.Parse([]byte("apiVersion: jetbridge.dev/queue/v2\nrepository: {uri: 'https://user:F4ke/Pa55@host/%zz'}\n"))
		Expect(err).To(MatchError(ContainSubstring("repository.uri: a URL must not hold credentials")))
		Expect(err.Error()).NotTo(MatchRegexp("F4ke|Pa55"))
	})
	It("checks each value as it decodes, so a tag cannot hide a URL credential", func() {
		const hidden = "!!binary aHR0cHM6Ly91c2VyOkY0a2UvUGE1NUBob3N0LyV6eg==" // https://user:F4ke/Pa55@host/%zz
		for extra, key := range map[string]string{
			"runner: {kind: jetbridge, url: " + hidden + "}\n":      "runner.url",
			"lander: {scratch: !!str 'https://user:F4ke@host/x'}\n": "lander.scratch",
			"? " + hidden + "\n: 1\n":                               "config",
		} {
			_, err := config.Parse([]byte(minimal + extra))
			Expect(err).To(MatchError(ContainSubstring(key+": a URL must not hold credentials")), extra)
			Expect(err.Error()).NotTo(MatchRegexp("F4ke|Pa55"), extra)
		}
	})

	It("allows a URL inside free text unless its authority holds credentials", func() {
		for _, name := range []string{"Queue (https://ci.example.test)", "Queue, see https://host/%zz", "a :// b"} {
			c, err := config.Parse([]byte(minimal + "compose: {committer: {name: '" + name + "'}}\n"))
			Expect(err).NotTo(HaveOccurred(), name)
			Expect(c.Compose.Committer.Name).To(Equal(name))
		}
		for _, name := range []string{"Queue (https://u:F4ke@ci.example.test)", "see https://F4ke@host/x", "Queue <ssh://git:F4ke@host>", "https://ci.example.test/ or https://u:F4ke@ci.example.test",
			"Queue (https://u:a/F4ke@x)", "see https://u:F4ke/x@host now", "Queue [https://u:F4ke?x@host]", "Queue \"https://u#F4ke@host\""} {
			_, err := config.Parse([]byte(minimal + "compose: {committer: {name: '" + name + "'}}\n"))
			Expect(err).To(MatchError(ContainSubstring("compose.committer.name: a URL must not hold credentials")), name)
			Expect(err.Error()).NotTo(ContainSubstring("F4ke"), name)
		}
	})
})
