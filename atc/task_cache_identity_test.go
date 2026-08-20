package atc_test

import (
	. "github.com/concourse/concourse/atc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Task cache identity", func() {
	DescribeTable("accepts exactly one complete cache scope",
		func(identity TaskCacheIdentity) {
			// This fails if a malformed or mixed cache identity can address a row.
			Expect(identity.Validate()).To(Succeed())
		},
		Entry("ordinary job", TaskCacheIdentity{JobID: 42}),
		Entry("numbered run job", TaskCacheIdentity{TeamID: 7, TemplatePipelineID: 11, RunJobName: "deploy-staging"}),
	)

	DescribeTable("rejects incomplete or mixed cache scopes",
		func(identity TaskCacheIdentity) {
			// This fails if a cache can be addressed without one unambiguous owner.
			Expect(identity.Validate()).To(MatchError(ContainSubstring("exactly one")))
		},
		Entry("empty", TaskCacheIdentity{}),
		Entry("ordinary plus run", TaskCacheIdentity{JobID: 42, TeamID: 7, TemplatePipelineID: 11, RunJobName: "deploy-staging"}),
		Entry("run missing team", TaskCacheIdentity{TemplatePipelineID: 11, RunJobName: "deploy-staging"}),
		Entry("run missing template", TaskCacheIdentity{TeamID: 7, RunJobName: "deploy-staging"}),
		Entry("run missing name", TaskCacheIdentity{TeamID: 7, TemplatePipelineID: 11}),
	)
})
