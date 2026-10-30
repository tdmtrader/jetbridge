package db_test

import (
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("PinnedImage", func() {
	DescribeTable("is a registry-image get of the exact reference",
		func(imageRef string, source atc.Source, version atc.Version) {
			resourceType, gotSource, gotVersion := db.PinnedImage(imageRef)
			Expect(resourceType).To(Equal("registry-image"))
			Expect(gotSource).To(Equal(source))
			Expect(gotVersion).To(Equal(version))
		},
		Entry("pinned by digest",
			"registry.example/team/tool@sha256:aaa",
			atc.Source{"repository": "registry.example/team/tool"}, atc.Version{"digest": "sha256:aaa"}),
		Entry("pinned by digest behind a docker:/// prefix",
			"docker:///concourse/git-resource@sha256:bbb",
			atc.Source{"repository": "concourse/git-resource"}, atc.Version{"digest": "sha256:bbb"}),
		Entry("pinned by tag",
			"registry.example:5000/team/tool:1.2",
			atc.Source{"repository": "registry.example:5000/team/tool", "tag": "1.2"}, atc.Version{"tag": "1.2"}),
		Entry("pinned by the implicit latest tag",
			"busybox",
			atc.Source{"repository": "busybox", "tag": "latest"}, atc.Version{"tag": "latest"}),
	)
})
