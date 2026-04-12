package creds_test

import (
	"github.com/concourse/concourse/atc/creds"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("K8sSecretRef", func() {
	It("holds namespace, name, and key", func() {
		ref := creds.K8sSecretRef{
			Namespace: "concourse-main",
			Name:      "my-pipeline.db-password",
			Key:       "value",
		}
		Expect(ref.Namespace).To(Equal("concourse-main"))
		Expect(ref.Name).To(Equal("my-pipeline.db-password"))
		Expect(ref.Key).To(Equal("value"))
	})
})

var _ = Describe("SecretRefProvider", func() {
	It("can be implemented by a credential manager", func() {
		var provider creds.SecretRefProvider = &fakeSecretRefProvider{
			refs: map[string]*creds.K8sSecretRef{
				"concourse-main/my-secret": {
					Namespace: "concourse-main",
					Name:      "my-secret",
					Key:       "value",
				},
			},
		}

		ref, found := provider.GetSecretRef("concourse-main/my-secret")
		Expect(found).To(BeTrue())
		Expect(ref.Namespace).To(Equal("concourse-main"))
		Expect(ref.Name).To(Equal("my-secret"))
		Expect(ref.Key).To(Equal("value"))
	})

	It("returns false for unknown paths", func() {
		var provider creds.SecretRefProvider = &fakeSecretRefProvider{
			refs: map[string]*creds.K8sSecretRef{},
		}

		ref, found := provider.GetSecretRef("concourse-main/nonexistent")
		Expect(found).To(BeFalse())
		Expect(ref).To(BeNil())
	})
})

type fakeSecretRefProvider struct {
	refs map[string]*creds.K8sSecretRef
}

func (f *fakeSecretRefProvider) GetSecretRef(path string) (*creds.K8sSecretRef, bool) {
	ref, ok := f.refs[path]
	return ref, ok
}
