package kubernetes_test

import (
	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc/creds"
	"github.com/concourse/concourse/atc/creds/kubernetes"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/client-go/kubernetes/fake"
)

var _ = Describe("SecretRefProvider", func() {
	var (
		fakeClientset *fake.Clientset
		secrets       creds.Secrets
	)

	BeforeEach(func() {
		fakeClientset = fake.NewSimpleClientset()
		factory := kubernetes.NewKubernetesFactory(
			lagertest.NewTestLogger("test"),
			fakeClientset,
			"prefix-",
		)
		secrets = factory.NewSecrets()
	})

	It("implements the SecretRefProvider interface", func() {
		_, ok := secrets.(creds.SecretRefProvider)
		Expect(ok).To(BeTrue())
	})

	Describe("GetSecretRef", func() {
		var provider creds.SecretRefProvider

		BeforeEach(func() {
			provider = secrets.(creds.SecretRefProvider)
		})

		It("returns the correct namespace, name, and key for a valid path", func() {
			ref, found := provider.GetSecretRef("prefix-main/my-secret")
			Expect(found).To(BeTrue())
			Expect(ref).To(Equal(&creds.K8sSecretRef{
				Namespace: "prefix-main",
				Name:      "my-secret",
				Key:       "value",
			}))
		})

		It("returns the correct ref for pipeline-scoped secrets", func() {
			ref, found := provider.GetSecretRef("prefix-team/my-pipeline.db-password")
			Expect(found).To(BeTrue())
			Expect(ref).To(Equal(&creds.K8sSecretRef{
				Namespace: "prefix-team",
				Name:      "my-pipeline.db-password",
				Key:       "value",
			}))
		})

		It("returns false for paths that cannot be split into namespace/name", func() {
			ref, found := provider.GetSecretRef("no-slash-here")
			Expect(found).To(BeFalse())
			Expect(ref).To(BeNil())
		})

		It("returns false for empty paths", func() {
			ref, found := provider.GetSecretRef("")
			Expect(found).To(BeFalse())
			Expect(ref).To(BeNil())
		})
	})
})
