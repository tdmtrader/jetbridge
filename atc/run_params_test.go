package atc_test

import (
	"encoding/json"
	"errors"
	"math"

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
	It("keeps a required parameter required even when the schema also declares a default", func() {
		// This fails if the default is assigned before the required check, which
		// silently downgrades `required: true` to optional.
		_, err := ValidateRunParams([]ParamSchema{
			{Name: "environment", Type: ParamTypeString, Required: true, Default: "staging"},
		}, RunParams{})
		Expect(err).To(MatchError(ContainSubstring("parameter environment is required")))
	})

	It("treats an explicit null as absent rather than as a supplied value", func() {
		// This fails if `{"environment": null}` satisfies a required parameter.
		_, err := ValidateRunParams([]ParamSchema{
			{Name: "environment", Type: ParamTypeString, Required: true},
		}, RunParams{"environment": nil})
		Expect(err).To(MatchError(ContainSubstring("parameter environment is required")))
	})

	It("reports every unknown parameter in a stable order", func() {
		// This fails if the message depends on Go's randomized map iteration
		// order, so the same request produces different 400 bodies.
		for i := 0; i < 20; i++ {
			_, err := ValidateRunParams([]ParamSchema{{Name: "environment", Type: ParamTypeString}}, RunParams{
				"zebra":  "x",
				"alpha":  "y",
				"middle": "z",
			})
			Expect(err).To(MatchError(ContainSubstring("unknown parameter alpha, middle, zebra")))
		}
	})

	DescribeTable("rejects numbers outside the safe binary64 integer domain",
		func(supplied any) {
			// This fails if a value that cannot survive the JSON round trip
			// through the API, the database and interpolation is admitted.
			_, err := ValidateRunParams([]ParamSchema{{Name: "count", Type: ParamTypeNumber}}, RunParams{"count": supplied})
			Expect(err).To(MatchError(ContainSubstring("parameter count must be")))
		},
		Entry("NaN", math.NaN()),
		Entry("positive infinity", math.Inf(1)),
		Entry("above the safe integer range", float64(1<<53)+2),
		Entry("below the safe integer range", -(float64(1<<53) + 2)),
		Entry("NaN as a supplied string", "NaN"),
		Entry("over-magnitude as a supplied string", "9007199254740993"),
	)

	It("folds negative zero", func() {
		// This fails if -0 and 0 are stored as distinct parameter values, so
		// two identical runs produce different canonical configs.
		result, err := ValidateRunParams([]ParamSchema{{Name: "offset", Type: ParamTypeNumber}}, RunParams{
			"offset": math.Copysign(0, -1),
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(math.Signbit(result["offset"].(float64))).To(BeFalse())
	})
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
