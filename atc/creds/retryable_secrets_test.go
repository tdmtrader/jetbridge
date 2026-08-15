package creds_test

import (
	"errors"
	"sync"
	"time"

	"github.com/concourse/concourse/atc/creds"
	"github.com/concourse/concourse/atc/creds/dummy"
	"github.com/concourse/concourse/vars"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type secretBackend struct {
	secrets              creds.Secrets
	available            chan struct{}
	unavailabilitySeen   chan struct{}
	availabilityResolved chan struct{}
	unavailabilityOnce   sync.Once
}

func newUnavailableSecretBackend() *secretBackend {
	return &secretBackend{
		secrets:              &dummy.Secrets{StaticVariables: vars.StaticVariables{"team/pipeline/somevar": "received value"}},
		available:            make(chan struct{}),
		unavailabilitySeen:   make(chan struct{}),
		availabilityResolved: make(chan struct{}),
	}
}

func (backend *secretBackend) makeAvailable() {
	close(backend.available)
	close(backend.availabilityResolved)
}

func (backend *secretBackend) remainUnavailable() {
	close(backend.availabilityResolved)
}

func (backend *secretBackend) Get(secretPath string) (any, *time.Time, bool, error) {
	select {
	case <-backend.available:
		return backend.secrets.Get(secretPath)
	default:
		backend.unavailabilityOnce.Do(func() {
			close(backend.unavailabilitySeen)
			<-backend.availabilityResolved
		})
		return nil, nil, false, errors.New("secret backend unavailable: remote handshake failure")
	}
}

func (backend *secretBackend) NewSecretLookupPaths(teamName string, pipelineName string, allowRootPath bool) []creds.SecretLookupPath {
	return backend.secrets.NewSecretLookupPaths(teamName, pipelineName, allowRootPath)
}

type secretLookupResult struct {
	value any
	found bool
	err   error
}

func beginSecretLookup(secrets creds.Secrets) <-chan secretLookupResult {
	lookupResult := make(chan secretLookupResult, 1)
	go func() {
		value, found, err := creds.NewVariables(secrets, creds.SecretLookupParams{Team: "team", Pipeline: "pipeline"}, false).Get(vars.Reference{Path: "somevar"})
		lookupResult <- secretLookupResult{value: value, found: found, err: err}
	}()
	return lookupResult
}

func awaitSecretBackendUnavailable(backend *secretBackend) {
	select {
	case <-backend.unavailabilitySeen:
	case <-time.After(time.Second):
		Fail("secret backend did not expose its unavailable state")
	}
}

func awaitSecretLookup(lookupResult <-chan secretLookupResult) secretLookupResult {
	select {
	case result := <-lookupResult:
		return result
	case <-time.After(time.Second):
		Fail("secret lookup did not finish after backend availability was resolved")
		return secretLookupResult{}
	}
}

var _ = Describe("Re-retrieval of secrets on retryable errors", func() {
	It("should implement the SecretsWithParams interface", func() {
		var _ creds.SecretsWithParams = creds.RetryableSecrets{}
	})

	It("retrieves the secret after the backend becomes available", func() {
		backend := newUnavailableSecretBackend()
		retryableSecrets := creds.NewRetryableSecrets(backend, creds.SecretRetryConfig{Attempts: 5, Interval: time.Millisecond})
		lookupResult := beginSecretLookup(retryableSecrets)

		awaitSecretBackendUnavailable(backend)
		backend.makeAvailable()

		result := awaitSecretLookup(lookupResult)
		Expect(result.value).To(BeEquivalentTo("received value"))
		Expect(result.found).To(BeTrue())
		Expect(result.err).NotTo(HaveOccurred())
	})

	It("returns the backend unavailable error when availability is not restored", func() {
		backend := newUnavailableSecretBackend()
		retryableSecrets := creds.NewRetryableSecrets(backend, creds.SecretRetryConfig{Attempts: 3, Interval: time.Millisecond})
		lookupResult := beginSecretLookup(retryableSecrets)

		awaitSecretBackendUnavailable(backend)
		backend.remainUnavailable()
		result := awaitSecretLookup(lookupResult)

		Expect(result.value).To(BeNil())
		Expect(result.found).To(BeFalse())
		Expect(result.err).To(MatchError(ContainSubstring("secret backend unavailable")))
	})
})
