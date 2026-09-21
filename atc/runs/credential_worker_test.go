package runs_test

import (
	"strings"

	"github.com/concourse/concourse/atc/runs"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The owner's credentials go on stdin to whatever image the Run's snapshotted
// result producer names, and a team member who can set the template chooses
// that. So delivery is admitted only into an image the operator pinned by
// digest, and nothing is admitted when no image is pinned.
var _ = Describe("Credential worker image pin", func() {
	digest := "sha256:" + strings.Repeat("ab", 32)
	pinned := "registry.example/review-worker@" + digest

	DescribeTable("admits delivery only into an exact pinned digest",
		func(pins []string, producer string, admitted bool) {
			err := runs.CredentialHandoffConfig{WorkerImages: pins}.AdmitWorkerImage(producer)
			if admitted {
				Expect(err).NotTo(HaveOccurred())
			} else {
				Expect(err).To(MatchError(runs.ErrCredentialDelivery))
			}
		},
		Entry("the pinned digest", []string{pinned}, "docker:///"+pinned, true),
		Entry("one of several pins", []string{"other.example/w@" + digest, pinned}, "docker:///"+pinned, true),
		Entry("no pin configured", nil, "docker:///"+pinned, false),
		Entry("another image", []string{pinned}, "docker:///attacker.example/w@"+digest, false),
		Entry("the same repository by tag", []string{pinned}, "docker:///registry.example/review-worker:latest", false),
		Entry("the same repository with another digest", []string{pinned}, "docker:///registry.example/review-worker@sha256:"+strings.Repeat("cd", 32), false),
		Entry("a producer whose Pod can run another image", []string{pinned}, "", false),
		Entry("another transport", []string{pinned}, "raw:///"+pinned, false),
		Entry("a tag-only pin never matches its own tag", []string{"registry.example/review-worker:latest"}, "docker:///registry.example/review-worker:latest", false),
	)

	DescribeTable("validates pins as digest-qualified references",
		func(pin string, valid bool) {
			err := runs.ValidateCredentialWorkerImage(pin)
			if valid {
				Expect(err).NotTo(HaveOccurred())
			} else {
				Expect(err).To(HaveOccurred())
			}
		},
		Entry("repository at digest", pinned, true),
		Entry("registry with port", "registry.example:5000/team/worker@"+digest, true),
		Entry("tag and digest", "registry.example/review-worker:v1@"+digest, true),
		Entry("tag only", "registry.example/review-worker:v1", false),
		Entry("short digest", "registry.example/review-worker@sha256:abc", false),
		Entry("uppercase digest", "registry.example/review-worker@sha256:"+strings.Repeat("AB", 32), false),
		Entry("transport prefix", "docker:///"+pinned, false),
		Entry("whitespace", pinned+" ", false),
		Entry("empty", "", false),
	)
})
