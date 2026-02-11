package creds_test

import (
	"fmt"
	"time"

	"github.com/concourse/concourse/atc/creds"
	"github.com/concourse/concourse/atc/creds/credsfakes"
	"github.com/concourse/concourse/tracing"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func makeGetStub(name string, value any, expiration *time.Time, found bool, err error, cntReads *int, cntMisses *int) func(string) (any, *time.Time, bool, error) {
	return func(secretPath string) (any, *time.Time, bool, error) {
		if secretPath == name {
			*cntReads++
			return value, expiration, found, err
		}
		*cntMisses++
		return nil, nil, false, nil
	}
}

var _ = Describe("Caching of secrets", func() {

	var secretManager *credsfakes.FakeSecrets
	var cacheConfig creds.SecretCacheConfig
	var cachedSecretManager *creds.CachedSecrets
	var underlyingReads int
	var underlyingMisses int

	BeforeEach(func() {
		secretManager = new(credsfakes.FakeSecrets)
		cacheConfig = creds.SecretCacheConfig{
			Duration:         400 * time.Millisecond,
			DurationNotFound: 200 * time.Millisecond,
			PurgeInterval:    100 * time.Millisecond,
		}
		cachedSecretManager = creds.NewCachedSecrets(secretManager, cacheConfig)
		underlyingReads = 0
		underlyingMisses = 0
	})

	It("should implement the SecretsWithParams interface", func() {
		var _ creds.SecretsWithParams = cachedSecretManager
	})

	It("should handle missing secrets correctly and cache misses", func() {
		secretManager.GetStub = makeGetStub("foo", "value", nil, true, nil, &underlyingReads, &underlyingMisses)

		// miss
		value, expiration, found, err := cachedSecretManager.Get("bar")
		Expect(value).To(BeNil())
		Expect(expiration).To(BeNil())
		Expect(found).To(BeFalse())
		Expect(err).To(BeNil())
		Expect(underlyingReads).To(BeIdenticalTo(0))
		Expect(underlyingMisses).To(BeIdenticalTo(1))

		// cached miss
		value, expiration, found, err = cachedSecretManager.Get("bar")
		Expect(value).To(BeNil())
		Expect(expiration).To(BeNil())
		Expect(found).To(BeFalse())
		Expect(err).To(BeNil())
		Expect(underlyingReads).To(BeIdenticalTo(0))
		Expect(underlyingMisses).To(BeIdenticalTo(1))
	})

	It("should handle existing secrets correctly and cache them, returning previous value if the underlying value has changed", func() {
		secretManager.GetStub = makeGetStub("foo", "value", nil, true, nil, &underlyingReads, &underlyingMisses)

		// hit
		value, expiration, found, err := cachedSecretManager.Get("foo")
		Expect(value).To(BeIdenticalTo("value"))
		Expect(expiration).To(BeNil())
		Expect(found).To(BeTrue())
		Expect(err).To(BeNil())
		Expect(underlyingReads).To(BeIdenticalTo(1))
		Expect(underlyingMisses).To(BeIdenticalTo(0))

		// cached hit
		secretManager.GetStub = makeGetStub("foo", "different-value", nil, true, nil, &underlyingReads, &underlyingMisses)
		value, expiration, found, err = cachedSecretManager.Get("foo")
		Expect(value).To(BeIdenticalTo("value"))
		Expect(expiration).To(BeNil())
		Expect(found).To(BeTrue())
		Expect(err).To(BeNil())
		Expect(underlyingReads).To(BeIdenticalTo(1))
		Expect(underlyingMisses).To(BeIdenticalTo(0))
	})

	It("should handle errors correctly and avoid caching errors", func() {
		secretManager.GetStub = makeGetStub("baz", nil, nil, false, fmt.Errorf("unexpected error"), &underlyingReads, &underlyingMisses)

		// error
		value, expiration, found, err := cachedSecretManager.Get("baz")
		Expect(value).To(BeNil())
		Expect(expiration).To(BeNil())
		Expect(found).To(BeFalse())
		Expect(err).NotTo(BeNil())
		Expect(underlyingReads).To(BeIdenticalTo(1))
		Expect(underlyingMisses).To(BeIdenticalTo(0))

		// no caching of error
		value, expiration, found, err = cachedSecretManager.Get("baz")
		Expect(value).To(BeNil())
		Expect(expiration).To(BeNil())
		Expect(found).To(BeFalse())
		Expect(err).NotTo(BeNil())
		Expect(underlyingReads).To(BeIdenticalTo(2))
		Expect(underlyingMisses).To(BeIdenticalTo(0))
	})

	It("should re-retrieve expired entries", func() {
		secretManager.GetStub = makeGetStub("foo", "value", nil, true, nil, &underlyingReads, &underlyingMisses)

		// get few entries first
		_, _, _, _ = cachedSecretManager.Get("foo")
		_, _, _, _ = cachedSecretManager.Get("bar")
		_, _, _, _ = cachedSecretManager.Get("baz")
		Expect(underlyingReads).To(BeIdenticalTo(1))
		Expect(underlyingMisses).To(BeIdenticalTo(2))

		// get these entries again and make sure they are cached
		_, _, _, _ = cachedSecretManager.Get("foo")
		_, _, _, _ = cachedSecretManager.Get("bar")
		_, _, _, _ = cachedSecretManager.Get("baz")
		Expect(underlyingReads).To(BeIdenticalTo(1))
		Expect(underlyingMisses).To(BeIdenticalTo(2))

		// sleep
		time.Sleep(cacheConfig.Duration + time.Millisecond)

		// check counters again and make sure the entries are re-retrieved
		_, _, _, _ = cachedSecretManager.Get("foo")
		_, _, _, _ = cachedSecretManager.Get("bar")
		_, _, _, _ = cachedSecretManager.Get("baz")
		Expect(underlyingReads).To(BeIdenticalTo(2))
		Expect(underlyingMisses).To(BeIdenticalTo(4))
	})

	It("should cache negative responses for a separately specified duration", func() {
		secretManager.GetStub = makeGetStub("foo", "value", nil, true, nil, &underlyingReads, &underlyingMisses)

		// get few entries first
		_, _, _, _ = cachedSecretManager.Get("foo")
		_, _, _, _ = cachedSecretManager.Get("bar")
		_, _, _, _ = cachedSecretManager.Get("baz")
		Expect(underlyingReads).To(BeIdenticalTo(1))
		Expect(underlyingMisses).To(BeIdenticalTo(2))

		// sleep
		time.Sleep(cacheConfig.DurationNotFound + time.Millisecond)

		// existing secret should still be cached
		_, _, _, _ = cachedSecretManager.Get("foo")
		Expect(underlyingReads).To(BeIdenticalTo(1))
		Expect(underlyingMisses).To(BeIdenticalTo(2))

		// non-existing secrets should be attempted to be retrieved again
		_, _, _, _ = cachedSecretManager.Get("bar")
		_, _, _, _ = cachedSecretManager.Get("baz")
		Expect(underlyingReads).To(BeIdenticalTo(1))
		Expect(underlyingMisses).To(BeIdenticalTo(4))
	})

	It("should not cache longer than default duration if durarion is 0 or less", func() {
		secretManager.GetStub = makeGetStub("foo", "value", &time.Time{}, true, nil, &underlyingReads, &underlyingMisses)

		// get few entries first
		_, _, _, _ = cachedSecretManager.Get("foo")
		_, _, _, _ = cachedSecretManager.Get("bar")
		_, _, _, _ = cachedSecretManager.Get("baz")
		Expect(underlyingReads).To(BeIdenticalTo(1))
		Expect(underlyingMisses).To(BeIdenticalTo(2))

		// sleep
		time.Sleep(cacheConfig.Duration + time.Millisecond)

		// existing secret should be gone
		_, _, _, _ = cachedSecretManager.Get("foo")
		Expect(underlyingReads).To(BeIdenticalTo(2))
		Expect(underlyingMisses).To(BeIdenticalTo(2))

		// non-existing secrets should be attempted to be retrieved again
		_, _, _, _ = cachedSecretManager.Get("bar")
		_, _, _, _ = cachedSecretManager.Get("baz")
		Expect(underlyingReads).To(BeIdenticalTo(2))
		Expect(underlyingMisses).To(BeIdenticalTo(4))
	})

	Context("when tracing is enabled", func() {
		var spanRecorder *tracetest.SpanRecorder

		BeforeEach(func() {
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
			secretManager.GetStub = makeGetStub("foo", "value", nil, true, nil, &underlyingReads, &underlyingMisses)

			// First call: cache miss (populates cache)
			_, _, _, _ = cachedSecretManager.Get("foo")

			// Second call: cache hit
			_, _, _, _ = cachedSecretManager.Get("foo")

			ended := spanRecorder.Ended()
			Expect(len(ended)).To(BeNumerically(">=", 2))

			// Find the second span (cache hit)
			var cacheHitSpan sdktrace.ReadOnlySpan
			hitCount := 0
			for _, s := range ended {
				if s.Name() == "creds.lookup" {
					hitCount++
					if hitCount == 2 {
						cacheHitSpan = s
					}
				}
			}
			Expect(cacheHitSpan).ToNot(BeNil(), "expected second creds.lookup span")

			attrMap := make(map[string]string)
			for _, a := range cacheHitSpan.Attributes() {
				attrMap[string(a.Key)] = a.Value.AsString()
			}
			Expect(attrMap["secret.path"]).To(Equal("foo"))
			Expect(attrMap["cache.hit"]).To(Equal("true"))
		})

		It("emits a creds.lookup span on cache miss", func() {
			secretManager.GetStub = makeGetStub("bar", "value", nil, true, nil, &underlyingReads, &underlyingMisses)

			_, _, _, _ = cachedSecretManager.Get("bar")

			ended := spanRecorder.Ended()
			var lookupSpan sdktrace.ReadOnlySpan
			for _, s := range ended {
				if s.Name() == "creds.lookup" {
					lookupSpan = s
					break
				}
			}
			Expect(lookupSpan).ToNot(BeNil(), "expected creds.lookup span")

			attrMap := make(map[string]string)
			for _, a := range lookupSpan.Attributes() {
				attrMap[string(a.Key)] = a.Value.AsString()
			}
			Expect(attrMap["secret.path"]).To(Equal("bar"))
			Expect(attrMap["cache.hit"]).To(Equal("false"))
			Expect(attrMap["secret.found"]).To(Equal("true"))
		})
	})

})
