package fix_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/fix"
)

var _ = Describe("ParseFixPatches", func() {
	It("parses valid JSON into file patches", func() {
		raw := []byte(`[
			{"path": "handler.go", "content": "package handler\n\nfunc Handle() {}\n"}
		]`)
		patches, err := fix.ParseFixPatches(raw)
		Expect(err).NotTo(HaveOccurred())
		Expect(patches).To(HaveLen(1))
		Expect(patches[0].Path).To(Equal("handler.go"))
		Expect(patches[0].Content).To(ContainSubstring("func Handle"))
	})

	It("parses multi-file patches", func() {
		raw := []byte(`[
			{"path": "a.go", "content": "package a"},
			{"path": "b.go", "content": "package b"}
		]`)
		patches, err := fix.ParseFixPatches(raw)
		Expect(err).NotTo(HaveOccurred())
		Expect(patches).To(HaveLen(2))
	})

	It("returns error for malformed JSON", func() {
		_, err := fix.ParseFixPatches([]byte("{bad}"))
		Expect(err).To(HaveOccurred())
	})

	It("rejects patches with absolute paths", func() {
		raw := []byte(`[{"path": "/etc/passwd", "content": "bad"}]`)
		_, err := fix.ParseFixPatches(raw)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("absolute path"))
	})

	It("rejects patches with path traversal", func() {
		raw := []byte(`[{"path": "../../../etc/passwd", "content": "bad"}]`)
		_, err := fix.ParseFixPatches(raw)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("traversal"))
	})
})
