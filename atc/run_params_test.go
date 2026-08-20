package atc_test

import (
	"encoding/json"
	"errors"

	. "github.com/concourse/concourse/atc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Run parameter validation", func() {
	It("marks user-supplied validation failures with a typed error", func() {
		// This fails if API handlers must parse an unstable validation message to return 400.
		_, err := ValidateRunParams([]ParamSchema{{Name: "environment", Type: ParamTypeString, Required: true}}, RunParams{})
		var invalid InvalidRunParamsError
		Expect(errors.As(err, &invalid)).To(BeTrue())
	})

	It("normalizes supplied values after applying schema defaults", func() {
		// This fails if defaults are applied after interpolation/storage, or if
		// strings from an API request are not coerced to their declared scalars.
		result, err := ValidateRunParams([]ParamSchema{
			{Name: "environment", Type: ParamTypeString, Default: "staging"},
			{Name: "retries", Type: ParamTypeNumber, Default: 1},
			{Name: "dry_run", Type: ParamTypeBool, Default: false},
		}, RunParams{
			"retries": "2.5",
			"dry_run": "true",
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(RunParams{
			"environment": "staging",
			"retries":     float64(2.5),
			"dry_run":     true,
		}))
	})

	DescribeTable("preserves already typed scalar values while normalizing numbers",
		func(schema ParamSchema, supplied any, expected any) {
			// This fails if an already typed scalar is stringified or numbers retain
			// an implementation-dependent Go integer type.
			result, err := ValidateRunParams([]ParamSchema{schema}, RunParams{"value": supplied})
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(RunParams{"value": expected}))
		},
		Entry("string", ParamSchema{Name: "value", Type: ParamTypeString}, "production", "production"),
		Entry("number", ParamSchema{Name: "value", Type: ParamTypeNumber}, 2, float64(2)),
		Entry("JSON number", ParamSchema{Name: "value", Type: ParamTypeNumber}, json.Number("2.5"), float64(2.5)),
		Entry("bool", ParamSchema{Name: "value", Type: ParamTypeBool}, true, true),
	)

	DescribeTable("rejects invalid supplied parameters",
		func(schemas []ParamSchema, params RunParams, expected string) {
			// This fails if an invalid external value is admitted to a run.
			_, err := ValidateRunParams(schemas, params)
			Expect(err).To(MatchError(ContainSubstring(expected)))
		},
		Entry("unknown", []ParamSchema{{Name: "environment", Type: ParamTypeString}}, RunParams{"unknown": "x"}, "unknown parameter unknown"),
		Entry("missing required", []ParamSchema{{Name: "environment", Type: ParamTypeString, Required: true}}, RunParams{}, "parameter environment is required"),
		Entry("wrong type", []ParamSchema{{Name: "dry_run", Type: ParamTypeBool}}, RunParams{"dry_run": "not-a-bool"}, "parameter dry_run must be a bool"),
		Entry("enum member with a different scalar type", []ParamSchema{{Name: "retries", Type: ParamTypeEnum, Values: []any{1, 2}}}, RunParams{"retries": "2"}, "parameter retries must be one of"),
	)
})

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
