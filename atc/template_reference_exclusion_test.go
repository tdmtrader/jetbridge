package atc_test

import (
	. "github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/vars"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Template parameter reference exclusion", func() {
	excludes := func(exclude vars.ReferenceExclusion, name string) bool {
		reference, err := vars.ParseReference(name)
		Expect(err).NotTo(HaveOccurred())
		return exclude(reference)
	}

	It("excludes the declared parameters and the run identity of a template payload", func() {
		// This fails if an evaluator that runs before the config can be
		// unmarshaled cannot tell which placeholders the template owns.
		exclude := ParamReferenceExclusionFromPayload([]byte("template: true\nparams:\n- name: environment\n  type: string\n"))
		Expect(exclude).NotTo(BeNil())
		Expect(excludes(exclude, "environment")).To(BeTrue())
		Expect(excludes(exclude, "run")).To(BeTrue())
		Expect(excludes(exclude, "run_id")).To(BeTrue())
		Expect(excludes(exclude, "other")).To(BeFalse())
		Expect(excludes(exclude, "source:environment")).To(BeFalse())
	})

	It("returns no exclusion for an ordinary pipeline payload", func() {
		Expect(ParamReferenceExclusionFromPayload([]byte("jobs:\n- name: build\n"))).To(BeNil())
	})

	It("returns no exclusion for a task config whose params are environment variables", func() {
		// This fails if `fly execute`'s task file, where `params` is a map, is
		// mistaken for a pipeline template declaration.
		Expect(ParamReferenceExclusionFromPayload([]byte("platform: linux\nparams:\n  FOO: bar\n"))).To(BeNil())
	})

	It("returns no exclusion for a pipeline that declares params without the template bit", func() {
		Expect(ParamReferenceExclusionFromPayload([]byte("params:\n- name: environment\n  type: string\n"))).To(BeNil())
	})

	It("excludes the declared parameters of a parsed template config", func() {
		exclude := Config{Template: true, Params: []ParamSchema{{Name: "environment", Type: ParamTypeString}}}.ParamReferenceExclusion()
		Expect(exclude).NotTo(BeNil())
		Expect(excludes(exclude, "environment")).To(BeTrue())
		Expect(excludes(exclude, "environment.key")).To(BeFalse())
	})

	It("returns no exclusion for a parsed ordinary pipeline config", func() {
		Expect(Config{Params: []ParamSchema{{Name: "environment"}}}.ParamReferenceExclusion()).To(BeNil())
	})
})