package creds_test

import (
	"github.com/concourse/concourse/atc/creds"
	"github.com/concourse/concourse/vars"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Secret Ref Fallback and Redaction", func() {
	Describe("non-K8s credential manager", func() {
		It("does not produce secret refs", func() {
			// Use a Secrets backend that does NOT implement SecretRefProvider
			secrets := &fakeSecretsNoRef{
				data: map[string]any{"prefix-main/db-password": "s3cret"},
			}
			variables := creds.NewVariables(
				secrets,
				creds.SecretLookupParams{Team: "main"},
				false,
			)

			tracker := &vars.CredVarsTracker{
				CredVars: variables,
				Tracker:  vars.NewTracker(),
			}

			val, found, err := tracker.Get(vars.Reference{Path: "db-password"})
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(val).To(Equal("s3cret"))

			// Value is tracked for redaction
			credValues := vars.TrackedVarsMap{}
			tracker.IterateInterpolatedCreds(credValues)
			Expect(credValues).To(HaveKeyWithValue("db-password", "s3cret"))

			// No secret refs should be tracked
			secretRefs := map[string]vars.SecretRef{}
			tracker.IterateSecretRefs(refCollector(secretRefs))
			Expect(secretRefs).To(BeEmpty())
		})
	})

	Describe("log redaction with secret refs", func() {
		It("still tracks values for redaction when secret refs are present", func() {
			// Use a Secrets backend that DOES implement SecretRefProvider
			secrets := &fakeSecretsWithRef{
				data: map[string]any{
					"concourse-main/db-password": "s3cret",
					"concourse-main/api-key":     "k3y",
				},
			}
			variables := creds.NewVariables(
				secrets,
				creds.SecretLookupParams{Team: "main"},
				false,
			)

			tracker := &vars.CredVarsTracker{
				CredVars: variables,
				Tracker:  vars.NewTracker(),
			}

			// Resolve both vars
			_, _, _ = tracker.Get(vars.Reference{Path: "db-password"})
			_, _, _ = tracker.Get(vars.Reference{Path: "api-key"})

			// Both values are tracked for redaction
			credValues := vars.TrackedVarsMap{}
			tracker.IterateInterpolatedCreds(credValues)
			Expect(credValues).To(HaveKeyWithValue("db-password", "s3cret"))
			Expect(credValues).To(HaveKeyWithValue("api-key", "k3y"))

			// Both secret refs are also tracked
			secretRefs := map[string]vars.SecretRef{}
			tracker.IterateSecretRefs(refCollector(secretRefs))
			Expect(secretRefs).To(HaveLen(2))
			Expect(secretRefs).To(HaveKey("db-password"))
			Expect(secretRefs).To(HaveKey("api-key"))
		})
	})

	Describe("mixed credential managers", func() {
		It("only produces secret refs for K8s-backed vars", func() {
			// Simulate: K8s backend has db-password, non-K8s has api-key
			k8sSecrets := &fakeSecretsWithRef{
				data: map[string]any{"concourse-main/db-password": "s3cret"},
			}
			k8sVars := creds.NewVariables(k8sSecrets, creds.SecretLookupParams{Team: "main"}, false)

			plainSecrets := &fakeSecretsNoRef{
				data: map[string]any{"prefix-main/api-key": "k3y"},
			}
			plainVars := creds.NewVariables(plainSecrets, creds.SecretLookupParams{Team: "main"}, false)

			// MultiVars tries sources in order
			multiVars := vars.NewMultiVars([]vars.Variables{k8sVars, plainVars})

			tracker := &vars.CredVarsTracker{
				CredVars: multiVars,
				Tracker:  vars.NewTracker(),
			}

			// db-password comes from K8s
			val, found, _ := tracker.Get(vars.Reference{Path: "db-password"})
			Expect(found).To(BeTrue())
			Expect(val).To(Equal("s3cret"))

			// api-key comes from non-K8s
			val, found, _ = tracker.Get(vars.Reference{Path: "api-key"})
			Expect(found).To(BeTrue())
			Expect(val).To(Equal("k3y"))

			// Only db-password should have a secret ref
			secretRefs := map[string]vars.SecretRef{}
			tracker.IterateSecretRefs(refCollector(secretRefs))
			Expect(secretRefs).To(HaveLen(1))
			Expect(secretRefs).To(HaveKey("db-password"))
		})
	})
})

type refCollector map[string]vars.SecretRef

func (c refCollector) YieldSecretRef(varPath string, ref vars.SecretRef) {
	c[varPath] = ref
}
