package flaghelpers_test

import (
	"github.com/concourse/concourse/vars"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/fly/commands/internal/flaghelpers"
)

var _ = Describe("JSON variable pair", func() {
	Describe("UnmarshalFlag", func() {
		It("accepts one JSON scalar and keeps its JSON type", func() {
			pair := &flaghelpers.JSONVariablePairFlag{}

			Expect(pair.UnmarshalFlag("number=12.5")).To(Succeed())
			Expect(pair.Ref).To(Equal(vars.Reference{Path: "number", Fields: []string{}}))
			Expect(pair.Value).To(Equal(float64(12.5)))

			Expect(pair.UnmarshalFlag(`truth=true`)).To(Succeed())
			Expect(pair.Value).To(Equal(true))

			Expect(pair.UnmarshalFlag(`message="hello"`)).To(Succeed())
			Expect(pair.Value).To(Equal("hello"))
		})

		DescribeTable("rejects values that are not exactly one non-null scalar",
			func(value string) {
				pair := &flaghelpers.JSONVariablePairFlag{}
				Expect(pair.UnmarshalFlag(value)).To(HaveOccurred())
			},
			Entry("missing equals", "name"),
			Entry("null", "name=null"),
			Entry("object", "name={\"key\":1}"),
			Entry("array", "name=[1]"),
			Entry("trailing JSON", "name=true false"),
		)
	})
})
