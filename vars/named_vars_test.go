package vars_test

import (
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	. "github.com/concourse/concourse/vars"
)

var _ = Describe("NamedVariables", func() {
	Describe("Get", func() {
		It("return found value as soon as one source succeeds", func() {
			vars1 := &FakeVariables{}
			vars2 := StaticVariables{"key2": "val"}
			vars3 := &FakeVariables{GetErr: errors.New("fake-err")}
			vars := NamedVariables{"s1": vars1, "s2": vars2, "s3": vars3}

			val, found, err := vars.Get(Reference{Source: "s2", Path: "key2"})
			Expect(val).To(Equal("val"))
			Expect(found).To(BeTrue())
			Expect(err).ToNot(HaveOccurred())

			// Didn't get past other variables
			Expect(vars1.GetCallCount).To(Equal(0))
			Expect(vars3.GetCallCount).To(Equal(0))
		})

		It("return error as soon as one source fails", func() {
			vars1 := StaticVariables{"key1": "val"}
			vars2 := &FakeVariables{GetErr: errors.New("fake-err")}
			vars := NamedVariables{"s1": vars1, "s2": vars2}

			val, found, err := vars.Get(Reference{Source: "s2", Path: "key3"})
			Expect(val).To(BeNil())
			Expect(found).To(BeFalse())
			Expect(err).To(Equal(errors.New("fake-err")))
		})
	})
})
