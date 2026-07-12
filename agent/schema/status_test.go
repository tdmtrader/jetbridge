package schema_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	schema "github.com/concourse/concourse/agent/schema"
)

var _ = Describe("ThreeWayStatus", func() {
	DescribeTable("maps results.json wire statuses to the DB/API taxonomy",
		func(in schema.Status, want string, wantAbstained bool) {
			got, abstained := schema.ThreeWayStatus(in)
			Expect(got).To(Equal(want))
			Expect(abstained).To(Equal(wantAbstained))
		},
		Entry("pass", schema.StatusPass, schema.RunStatusOK, false),
		Entry("fail", schema.StatusFail, schema.RunStatusFailed, false),
		Entry("error", schema.StatusError, schema.RunStatusError, false),
		Entry("abstain", schema.StatusAbstain, schema.RunStatusFailed, true),
		Entry("unknown", schema.Status("bogus"), schema.RunStatusError, false),
	)
})
