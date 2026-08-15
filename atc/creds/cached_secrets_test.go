package creds_test

import (
	"errors"
	"sync"
	"time"

	"github.com/concourse/concourse/atc/creds"
	"github.com/concourse/concourse/tracing"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type secretEntry struct {
	value      any
	expiration *time.Time
}

type stateSecrets struct {
	mu       sync.RWMutex
	entries  map[string]secretEntry
	failures map[string]error
}

func newStateSecrets(entries map[string]secretEntry) *stateSecrets {
	return &stateSecrets{
		entries:  entries,
		failures: map[string]error{},
	}
}

func (secrets *stateSecrets) Get(secretPath string) (any, *time.Time, bool, error) {
	secrets.mu.RLock()
	defer secrets.mu.RUnlock()

	if err, failed := secrets.failures[secretPath]; failed {
		return nil, nil, false, err
	}

	entry, found := secrets.entries[secretPath]
	if !found {
		return nil, nil, false, nil
	}
	return entry.value, entry.expiration, true, nil
}

func (*stateSecrets) NewSecretLookupPaths(string, string, bool) []creds.SecretLookupPath {
	return []creds.SecretLookupPath{creds.NewSecretLookupWithPrefix("")}
}

func (secrets *stateSecrets) Set(secretPath string, value any, expiration *time.Time) {
	secrets.mu.Lock()
	defer secrets.mu.Unlock()

	secrets.entries[secretPath] = secretEntry{value: value, expiration: expiration}
}

func (secrets *stateSecrets) Delete(secretPath string) {
	secrets.mu.Lock()
	defer secrets.mu.Unlock()

	delete(secrets.entries, secretPath)
}

func (secrets *stateSecrets) Fail(secretPath string, err error) {
	secrets.mu.Lock()
	defer secrets.mu.Unlock()

	secrets.failures[secretPath] = err
}

func (secrets *stateSecrets) Recover(secretPath string, value any, expiration *time.Time) {
	secrets.mu.Lock()
	defer secrets.mu.Unlock()

	delete(secrets.failures, secretPath)
	secrets.entries[secretPath] = secretEntry{value: value, expiration: expiration}
}

var _ = Describe("Caching of secrets", func() {
	const expiryTimeout = 3 * time.Second

	var underlyingSecrets *stateSecrets
	var cacheConfig creds.SecretCacheConfig
	var cachedSecretManager *creds.CachedSecrets

	BeforeEach(func() {
		underlyingSecrets = newStateSecrets(map[string]secretEntry{
			"foo": {value: "value"},
		})
		cacheConfig = creds.SecretCacheConfig{
			Duration:         time.Minute,
			DurationNotFound: time.Minute,
			PurgeInterval:    10 * time.Minute,
		}
	})

	JustBeforeEach(func() {
		cachedSecretManager = creds.NewCachedSecrets(underlyingSecrets, cacheConfig)
	})

	It("should implement the SecretsWithParams interface", func() {
		var _ creds.SecretsWithParams = cachedSecretManager
	})

	It("caches missing secrets", func() {
		value, expiration, found, err := cachedSecretManager.Get("bar")
		Expect(err).NotTo(HaveOccurred())
		Expect(value).To(BeNil())
		Expect(expiration).To(BeNil())
		Expect(found).To(BeFalse())

		underlyingSecrets.Set("bar", "new-value", nil)
		value, expiration, found, err = cachedSecretManager.Get("bar")
		Expect(err).NotTo(HaveOccurred())
		Expect(value).To(BeNil())
		Expect(expiration).To(BeNil())
		Expect(found).To(BeFalse())
	})

	It("returns a cached value after the backing value changes", func() {
		value, expiration, found, err := cachedSecretManager.Get("foo")
		Expect(err).NotTo(HaveOccurred())
		Expect(value).To(Equal("value"))
		Expect(expiration).To(BeNil())
		Expect(found).To(BeTrue())

		underlyingSecrets.Set("foo", "different-value", nil)
		value, expiration, found, err = cachedSecretManager.Get("foo")
		Expect(err).NotTo(HaveOccurred())
		Expect(value).To(Equal("value"))
		Expect(expiration).To(BeNil())
		Expect(found).To(BeTrue())
	})

	It("does not cache backend errors", func() {
		backendErr := errors.New("transient backend error")
		underlyingSecrets.Fail("baz", backendErr)

		value, expiration, found, err := cachedSecretManager.Get("baz")
		Expect(err).To(MatchError(backendErr))
		Expect(value).To(BeNil())
		Expect(expiration).To(BeNil())
		Expect(found).To(BeFalse())

		underlyingSecrets.Recover("baz", "recovered-value", nil)
		value, expiration, found, err = cachedSecretManager.Get("baz")
		Expect(err).NotTo(HaveOccurred())
		Expect(value).To(Equal("recovered-value"))
		Expect(expiration).To(BeNil())
		Expect(found).To(BeTrue())
	})

	Context("when a positive cache entry expires", func() {
		BeforeEach(func() {
			cacheConfig.Duration = 225 * time.Millisecond
		})

		It("returns the changed backing value", func() {
			value, _, found, err := cachedSecretManager.Get("foo")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(value).To(Equal("value"))

			underlyingSecrets.Set("foo", "different-value", nil)
			value, _, found, err = cachedSecretManager.Get("foo")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(value).To(Equal("value"))

			Eventually(func(g Gomega) any {
				value, _, found, err := cachedSecretManager.Get("foo")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(found).To(BeTrue())
				return value
			}).WithTimeout(expiryTimeout).WithPolling(15 * time.Millisecond).Should(Equal("different-value"))
		})
	})

	Context("when missing entries have a shorter cache duration", func() {
		BeforeEach(func() {
			cacheConfig.DurationNotFound = 225 * time.Millisecond
		})

		It("refreshes the missing entry while keeping a positive entry cached", func() {
			value, _, found, err := cachedSecretManager.Get("foo")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(value).To(Equal("value"))

			value, _, found, err = cachedSecretManager.Get("bar")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse())
			Expect(value).To(BeNil())

			underlyingSecrets.Set("foo", "different-value", nil)
			underlyingSecrets.Set("bar", "new-value", nil)

			value, _, found, err = cachedSecretManager.Get("bar")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse())
			Expect(value).To(BeNil())

			Eventually(func(g Gomega) any {
				value, _, found, err := cachedSecretManager.Get("bar")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(found).To(BeTrue())
				return value
			}).WithTimeout(expiryTimeout).WithPolling(15 * time.Millisecond).Should(Equal("new-value"))

			value, _, found, err = cachedSecretManager.Get("foo")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(value).To(Equal("value"))
		})
	})

	Context("when the backing secret lease is already expired", func() {
		BeforeEach(func() {
			cacheConfig.Duration = 225 * time.Millisecond
			expired := time.Now().Add(-time.Minute)
			underlyingSecrets.Set("foo", "value", &expired)
		})

		It("falls back to the configured cache duration", func() {
			value, expiration, found, err := cachedSecretManager.Get("foo")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(value).To(Equal("value"))
			Expect(expiration).NotTo(BeNil())
			Expect(expiration.Before(time.Now())).To(BeTrue())

			underlyingSecrets.Set("foo", "different-value", nil)
			value, _, found, err = cachedSecretManager.Get("foo")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(value).To(Equal("value"))

			Eventually(func(g Gomega) any {
				value, _, found, err := cachedSecretManager.Get("foo")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(found).To(BeTrue())
				return value
			}).WithTimeout(expiryTimeout).WithPolling(15 * time.Millisecond).Should(Equal("different-value"))
		})
	})

	Context("when the backing secret lease expires before the configured cache duration", func() {
		It("uses the shorter lease duration", func() {
			expiresSoon := time.Now().Add(250 * time.Millisecond)
			underlyingSecrets.Set("foo", "value", &expiresSoon)

			value, expiration, found, err := cachedSecretManager.Get("foo")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(value).To(Equal("value"))
			Expect(expiration).NotTo(BeNil())

			underlyingSecrets.Set("foo", "different-value", nil)
			value, _, found, err = cachedSecretManager.Get("foo")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(value).To(Equal("value"))

			Eventually(func(g Gomega) any {
				value, _, found, err := cachedSecretManager.Get("foo")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(found).To(BeTrue())
				return value
			}).WithTimeout(expiryTimeout).WithPolling(15 * time.Millisecond).Should(Equal("different-value"))
		})
	})

	Context("when tracing is enabled", func() {
		var spanRecorder *tracetest.SpanRecorder

		BeforeEach(func() {
			underlyingSecrets.Set("bar", "value", nil)

			spanRecorder = new(tracetest.SpanRecorder)
			tp := sdktrace.NewTracerProvider(
				sdktrace.WithSpanProcessor(spanRecorder),
				sdktrace.WithSyncer(tracetest.NewInMemoryExporter()),
			)
			tracing.ConfigureTraceProvider(tp)
		})

		AfterEach(func() {
			tracing.Configured = false
		})

		It("emits a creds.lookup span on cache hit", func() {
			_, _, _, _ = cachedSecretManager.Get("foo")
			_, _, _, _ = cachedSecretManager.Get("foo")

			ended := spanRecorder.Ended()
			Expect(len(ended)).To(BeNumerically(">=", 2))

			var cacheHitSpan sdktrace.ReadOnlySpan
			hitCount := 0
			for _, span := range ended {
				if span.Name() == "creds.lookup" {
					hitCount++
					if hitCount == 2 {
						cacheHitSpan = span
					}
				}
			}
			Expect(cacheHitSpan).NotTo(BeNil(), "expected second creds.lookup span")

			attributes := make(map[string]string)
			for _, attribute := range cacheHitSpan.Attributes() {
				attributes[string(attribute.Key)] = attribute.Value.AsString()
			}
			Expect(attributes["secret.path"]).To(Equal("foo"))
			Expect(attributes["cache.hit"]).To(Equal("true"))
		})

		It("emits a creds.lookup span on cache miss", func() {
			_, _, _, _ = cachedSecretManager.Get("bar")

			ended := spanRecorder.Ended()
			var lookupSpan sdktrace.ReadOnlySpan
			for _, span := range ended {
				if span.Name() == "creds.lookup" {
					lookupSpan = span
					break
				}
			}
			Expect(lookupSpan).NotTo(BeNil(), "expected creds.lookup span")

			attributes := make(map[string]string)
			for _, attribute := range lookupSpan.Attributes() {
				attributes[string(attribute.Key)] = attribute.Value.AsString()
			}
			Expect(attributes["secret.path"]).To(Equal("bar"))
			Expect(attributes["cache.hit"]).To(Equal("false"))
			Expect(attributes["secret.found"]).To(Equal("true"))
		})
	})
})
