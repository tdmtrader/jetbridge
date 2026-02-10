package claude_test

import (
	"context"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/plan/adapter"
	"github.com/concourse/ci-agent/plan/adapter/claude"
	"github.com/concourse/ci-agent/schema"
)

func TestClaude(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Claude Adapter Suite")
}

var _ = Describe("Adapter", func() {
	It("implements the Adapter interface", func() {
		var _ adapter.Adapter = claude.New("claude")
	})

	Describe("GenerateSpec", func() {
		It("handles non-existent CLI binary", func() {
			a := claude.New("/nonexistent/binary")
			ctx := context.Background()
			input := &schema.PlanningInput{
				Title:       "Test",
				Description: "Test desc",
			}
			_, err := a.GenerateSpec(ctx, input, adapter.SpecOpts{})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("generate spec"))
		})

		It("handles context timeout", func() {
			a := claude.New("sleep")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
			defer cancel()
			input := &schema.PlanningInput{
				Title:       "Test",
				Description: "Test desc",
			}
			_, err := a.GenerateSpec(ctx, input, adapter.SpecOpts{})
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("GeneratePlan", func() {
		It("handles non-existent CLI binary", func() {
			a := claude.New("/nonexistent/binary")
			ctx := context.Background()
			input := &schema.PlanningInput{
				Title:       "Test",
				Description: "Test desc",
			}
			_, err := a.GeneratePlan(ctx, input, "# Spec", adapter.PlanOpts{})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("generate plan"))
		})
	})
})
