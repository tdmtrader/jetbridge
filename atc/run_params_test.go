package atc_test

import (
	"encoding/json"

	. "github.com/concourse/concourse/atc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Template parameter schema", func() {
	DescribeTable("uses the declared wire value for each scalar type",
		func(paramType ParamType, wireValue string) {
			payload, err := json.Marshal(paramType)
			Expect(err).NotTo(HaveOccurred())
			Expect(payload).To(MatchJSON(wireValue))
		},
		Entry("string", ParamTypeString, `"string"`),
		Entry("number", ParamTypeNumber, `"number"`),
		Entry("bool", ParamTypeBool, `"bool"`),
		Entry("enum", ParamTypeEnum, `"enum"`),
	)
})
