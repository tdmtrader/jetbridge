package atc_test

import (
	"github.com/concourse/concourse/atc"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ValidateRunParams", func() {
	schema := []atc.ParamSchema{
		{Name: "commit", Type: "string", Required: true},
		{Name: "suite", Type: "enum", Values: []string{"unit", "integration"}, Default: "unit"},
		{Name: "procs", Type: "number", Default: 2},
		{Name: "verbose", Type: "bool"},
	}

	It("fills defaults and coerces values", func() {
		out, err := atc.ValidateRunParams(schema, map[string]any{
			"commit":  "abc123",
			"procs":   "4",
			"verbose": "true",
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(out).To(Equal(map[string]any{
			"commit":  "abc123",
			"suite":   "unit",
			"procs":   float64(4),
			"verbose": true,
		}))
	})

	It("rejects unknown params", func() {
		_, err := atc.ValidateRunParams(schema, map[string]any{"commit": "x", "bogus": "y"})
		Expect(err).To(MatchError(ContainSubstring(`unknown param "bogus"`)))
	})

	It("rejects missing required params", func() {
		_, err := atc.ValidateRunParams(schema, map[string]any{})
		Expect(err).To(MatchError(ContainSubstring(`missing required param "commit"`)))
	})

	It("rejects enum values outside the declared set", func() {
		_, err := atc.ValidateRunParams(schema, map[string]any{"commit": "x", "suite": "smoke"})
		Expect(err).To(MatchError(ContainSubstring(`not one of`)))
	})

	It("rejects type mismatches", func() {
		_, err := atc.ValidateRunParams(schema, map[string]any{"commit": 42})
		Expect(err).To(MatchError(ContainSubstring(`expected string`)))
	})

	It("omits optional params without defaults", func() {
		out, err := atc.ValidateRunParams(schema, map[string]any{"commit": "x"})
		Expect(err).ToNot(HaveOccurred())
		Expect(out).ToNot(HaveKey("verbose"))
	})
})
