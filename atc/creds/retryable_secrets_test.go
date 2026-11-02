package creds_test

import (
	"fmt"
	"time"

	"github.com/concourse/concourse/atc/creds"
	"github.com/concourse/concourse/atc/creds/dummy"
	"github.com/concourse/concourse/vars"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type flakySecrets struct {
	creds.Secrets

	fails    int
	attempts int
}

func (secrets *flakySecrets) Get(secretPath string) (any, *time.Time, bool, error) {
	secrets.attempts++
	if secrets.attempts <= secrets.fails {
		return nil, nil, false, fmt.Errorf("remote error: handshake failure")
	}
	return secrets.Secrets.Get(secretPath)
}

func makeFlakySecretManager(numberOfFails int) creds.Secrets {
	return &flakySecrets{
		Secrets: &dummy.Secrets{StaticVariables: vars.StaticVariables{"team/pipeline/somevar": "received value"}},
		fails:   numberOfFails,
	}
}

var _ = Describe("Re-retrieval of secrets on retryable errors", func() {

	It("should implement the SecretsWithParams interface", func() {
		var _ creds.SecretsWithParams = creds.RetryableSecrets{}
	})

	It("should retry receiving a parameter in case of retryable error", func() {
		flakySecretManager := makeFlakySecretManager(3)
		retryableSecretManager := creds.NewRetryableSecrets(flakySecretManager, creds.SecretRetryConfig{Attempts: 5, Interval: time.Millisecond})
		varRef := vars.Reference{Path: "somevar"}
		value, found, err := creds.NewVariables(retryableSecretManager, creds.SecretLookupParams{Team: "team", Pipeline: "pipeline"}, false).Get(varRef)
		Expect(value).To(BeEquivalentTo("received value"))
		Expect(found).To(BeTrue())
		Expect(err).To(BeNil())
	})

	It("should not receive a parameter if the number of retryable errors exceeded the number of allowed attempts", func() {
		flakySecretManager := makeFlakySecretManager(10)
		retryableSecretManager := creds.NewRetryableSecrets(flakySecretManager, creds.SecretRetryConfig{Attempts: 5, Interval: time.Millisecond})
		varRef := vars.Reference{Path: "somevar"}
		value, found, err := creds.NewVariables(retryableSecretManager, creds.SecretLookupParams{Team: "team", Pipeline: "pipeline"}, false).Get(varRef)
		Expect(value).To(BeNil())
		Expect(found).To(BeFalse())
		Expect(err).NotTo(BeNil())
	})

})
