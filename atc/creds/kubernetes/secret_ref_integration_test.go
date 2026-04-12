package kubernetes_test

import (
	"context"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc/creds"
	"github.com/concourse/concourse/atc/creds/kubernetes"
	"github.com/concourse/concourse/vars"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

var _ = Describe("K8s Secret Ref Integration", func() {
	var (
		fakeClientset *fake.Clientset
		tracker       *vars.CredVarsTracker
	)

	BeforeEach(func() {
		fakeClientset = fake.NewSimpleClientset()

		// Create a K8s Secret that the credential manager will find
		fakeClientset.CoreV1().Secrets("prefix-some-team").Create(
			context.TODO(),
			&v1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "some-pipeline.db-password"},
				Data:       map[string][]byte{"value": []byte("s3cret-value")},
			},
			metav1.CreateOptions{},
		)

		factory := kubernetes.NewKubernetesFactory(
			lagertest.NewTestLogger("test"),
			fakeClientset,
			"prefix-",
		)

		variables := creds.NewVariables(
			factory.NewSecrets(),
			creds.SecretLookupParams{Team: "some-team", Pipeline: "some-pipeline"},
			false,
		)

		tracker = &vars.CredVarsTracker{
			CredVars: variables,
			Tracker:  vars.NewTracker(),
		}
	})

	It("tracks both the resolved value and the K8s Secret ref through a single Get()", func() {
		// Resolve the variable — this triggers tracking
		val, found, err := tracker.Get(vars.Reference{Path: "db-password"})
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(val).To(Equal("s3cret-value"))

		// Verify the value is tracked for redaction
		credValues := vars.TrackedVarsMap{}
		tracker.IterateInterpolatedCreds(credValues)
		Expect(credValues).To(HaveKeyWithValue("db-password", "s3cret-value"))

		// Verify the K8s Secret ref is tracked
		secretRefs := map[string]vars.SecretRef{}
		tracker.IterateSecretRefs(secretRefIterator(secretRefs))
		Expect(secretRefs).To(HaveKey("db-password"))

		ref := secretRefs["db-password"]
		Expect(ref.Namespace).To(Equal("prefix-some-team"))
		Expect(ref.Name).To(Equal("some-pipeline.db-password"))
		Expect(ref.Key).To(Equal("value"))
	})

	It("does not track a secret ref for variables that are not found", func() {
		_, found, err := tracker.Get(vars.Reference{Path: "nonexistent"})
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())

		secretRefs := map[string]vars.SecretRef{}
		tracker.IterateSecretRefs(secretRefIterator(secretRefs))
		Expect(secretRefs).To(BeEmpty())
	})
})

type secretRefIterator map[string]vars.SecretRef

func (it secretRefIterator) YieldSecretRef(varPath string, ref vars.SecretRef) {
	it[varPath] = ref
}
