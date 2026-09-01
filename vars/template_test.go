package vars_test

import (
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	. "github.com/concourse/concourse/vars"
)

var _ = Describe("Template", func() {
	It("returns error if finding variable fails", func() {
		template := NewTemplate([]byte("((key))"))
		vars := &FakeVariables{GetErr: errors.New("fake-err")}

		_, err := template.Evaluate(vars, EvaluateOpts{})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("fake-err"))
	})
})
