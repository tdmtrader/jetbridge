package feedback_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/feedback"
	"github.com/concourse/ci-agent/schema"
)

var _ = Describe("ClassifyVerdict", func() {
	It("classifies 'good catch' as accurate", func() {
		v, conf, err := feedback.ClassifyVerdict("good catch, this is a real bug")
		Expect(err).NotTo(HaveOccurred())
		Expect(v).To(Equal(schema.VerdictAccurate))
		Expect(conf).To(BeNumerically(">", 0.7))
	})

	It("classifies 'real issue' as accurate", func() {
		v, _, err := feedback.ClassifyVerdict("this is a real issue")
		Expect(err).NotTo(HaveOccurred())
		Expect(v).To(Equal(schema.VerdictAccurate))
	})

	It("classifies 'false positive' as false_positive", func() {
		v, conf, err := feedback.ClassifyVerdict("this is a false positive")
		Expect(err).NotTo(HaveOccurred())
		Expect(v).To(Equal(schema.VerdictFalsePositive))
		Expect(conf).To(BeNumerically(">", 0.7))
	})

	It("classifies 'not a bug' as false_positive", func() {
		v, _, err := feedback.ClassifyVerdict("not a bug, this is expected behavior")
		Expect(err).NotTo(HaveOccurred())
		Expect(v).To(Equal(schema.VerdictFalsePositive))
	})

	It("classifies 'noisy' as noisy", func() {
		v, _, err := feedback.ClassifyVerdict("too noisy, not important enough to flag")
		Expect(err).NotTo(HaveOccurred())
		Expect(v).To(Equal(schema.VerdictNoisy))
	})

	It("classifies 'not important' as noisy", func() {
		v, _, err := feedback.ClassifyVerdict("this is not important")
		Expect(err).NotTo(HaveOccurred())
		Expect(v).To(Equal(schema.VerdictNoisy))
	})

	It("classifies 'style issue' as overly_strict", func() {
		v, _, err := feedback.ClassifyVerdict("this is just a style issue, preference really")
		Expect(err).NotTo(HaveOccurred())
		Expect(v).To(Equal(schema.VerdictOverlyStrict))
	})

	It("classifies 'preference' as overly_strict", func() {
		v, _, err := feedback.ClassifyVerdict("it's a matter of preference")
		Expect(err).NotTo(HaveOccurred())
		Expect(v).To(Equal(schema.VerdictOverlyStrict))
	})

	It("classifies 'partially right' as partially_correct", func() {
		v, _, err := feedback.ClassifyVerdict("partially right but wrong diagnosis")
		Expect(err).NotTo(HaveOccurred())
		Expect(v).To(Equal(schema.VerdictPartiallyCorrect))
	})

	It("classifies 'missing context' as missed_context", func() {
		v, _, err := feedback.ClassifyVerdict("agent is missing context here")
		Expect(err).NotTo(HaveOccurred())
		Expect(v).To(Equal(schema.VerdictMissedContext))
	})

	It("classifies 'not a false positive' as accurate (negation)", func() {
		v, _, err := feedback.ClassifyVerdict("not a false positive, this is real")
		Expect(err).NotTo(HaveOccurred())
		Expect(v).To(Equal(schema.VerdictAccurate))
	})

	It("returns low confidence for ambiguous input", func() {
		v, conf, err := feedback.ClassifyVerdict("hmm I'm not sure about this one")
		Expect(err).NotTo(HaveOccurred())
		Expect(v).NotTo(BeEmpty())
		Expect(conf).To(BeNumerically("<", 0.5))
	})

	It("errors on empty input", func() {
		_, _, err := feedback.ClassifyVerdict("")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("empty"))
	})
})
