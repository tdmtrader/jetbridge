package implement_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/implement"
)

var _ = Describe("ReadSpec", func() {
	It("reads spec from file and extracts acceptance criteria", func() {
		dir := GinkgoT().TempDir()
		specPath := dir + "/spec.md"
		err := writeFile(specPath, `# Spec: Widget Service

## Requirements

1. Widgets have names.

## Acceptance Criteria

- [ ] POST /widgets returns 201
- [ ] GET /widgets returns list
- [x] DELETE /widgets/:id returns 204
`)
		Expect(err).NotTo(HaveOccurred())

		ctx, err := implement.ReadSpec(specPath)
		Expect(err).NotTo(HaveOccurred())
		Expect(ctx.Raw).To(ContainSubstring("Widget Service"))
		Expect(ctx.AcceptanceCriteria).To(HaveLen(3))
		Expect(ctx.AcceptanceCriteria[0]).To(Equal("POST /widgets returns 201"))
		Expect(ctx.AcceptanceCriteria[1]).To(Equal("GET /widgets returns list"))
		Expect(ctx.AcceptanceCriteria[2]).To(Equal("DELETE /widgets/:id returns 204"))
	})

	It("returns empty acceptance criteria when section is missing", func() {
		dir := GinkgoT().TempDir()
		specPath := dir + "/spec.md"
		err := writeFile(specPath, `# Spec: Simple

No acceptance criteria section here.
`)
		Expect(err).NotTo(HaveOccurred())

		ctx, err := implement.ReadSpec(specPath)
		Expect(err).NotTo(HaveOccurred())
		Expect(ctx.Raw).To(ContainSubstring("Simple"))
		Expect(ctx.AcceptanceCriteria).To(BeEmpty())
	})

	It("returns error on missing file", func() {
		_, err := implement.ReadSpec("/nonexistent/spec.md")
		Expect(err).To(HaveOccurred())
	})

	It("returns error on empty file", func() {
		dir := GinkgoT().TempDir()
		specPath := dir + "/spec.md"
		err := writeFile(specPath, "")
		Expect(err).NotTo(HaveOccurred())

		_, err = implement.ReadSpec(specPath)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("empty"))
	})
})
