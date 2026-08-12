package wrappa_test

import (
	"net/http"
	"net/http/httptest"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/wrappa"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
)

var _ = Describe("Concurrent Request Limits Wrappa", func() {
	var (
		pool                  wrappa.Pool
		slotFreeDuringRequest bool
		testLogger            *lagertest.TestLogger
		handler               http.Handler
		request               *http.Request
	)

	BeforeEach(func() {
		slotFreeDuringRequest = false
		testLogger = lagertest.NewTestLogger("test")
		request, _ = http.NewRequest("GET", "localhost:8080", nil)
	})

	AfterEach(func() {
		metric.Metrics.ConcurrentRequests = map[string]*metric.Gauge{}
	})

	givenConcurrentRequestLimit := func(limit int) {
		policy := wrappa.NewConcurrentRequestPolicy(map[wrappa.LimitedRoute]int{
			wrappa.LimitedRoute(atc.ListAllJobs): limit,
		})

		var found bool
		pool, found = policy.HandlerPool(atc.ListAllJobs)
		Expect(found).To(BeTrue())

		handler = wrappa.NewConcurrentRequestLimitsWrappa(testLogger, policy).
			Wrap(map[string]http.Handler{
				atc.ListAllJobs: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					slotFreeDuringRequest = pool.TryAcquire()
					if slotFreeDuringRequest {
						pool.Release()
					}
					w.Write([]byte("wrapped"))
				}),
			})[atc.ListAllJobs]
	}

	Context("when the limit is reached", func() {
		BeforeEach(func() {
			givenConcurrentRequestLimit(1)
			Expect(pool.TryAcquire()).To(BeTrue())
		})

		It("responds with a 503", func() {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)

			Expect(recorder.Code).To(Equal(http.StatusServiceUnavailable))
			Expect(recorder.Body.String()).To(BeEmpty())
		})

		It("logs an INFO message", func() {
			handler.ServeHTTP(httptest.NewRecorder(), request)

			Expect(testLogger.Logs()).To(ConsistOf(
				MatchFields(IgnoreExtras, Fields{
					"Message":  Equal("test.concurrent-request-limit-reached"),
					"LogLevel": Equal(lager.INFO),
				}),
			))
		})

		It("increments the 'limitHit' counter", func() {
			handler.ServeHTTP(httptest.NewRecorder(), request)
			handler.ServeHTTP(httptest.NewRecorder(), request)

			Expect(metric.Metrics.ConcurrentRequestsLimitHit[atc.ListAllJobs].Delta()).To(Equal(float64(2)))
		})
	})

	Context("when the limit is not reached", func() {
		BeforeEach(func() {
			givenConcurrentRequestLimit(1)
		})

		It("invokes the wrapped handler", func() {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)

			Expect(recorder.Body.String()).To(Equal("wrapped"))
		})

		It("holds a slot for the duration of the request", func() {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)

			Expect(recorder.Body.String()).To(Equal("wrapped"))
			Expect(slotFreeDuringRequest).To(BeFalse())
		})

		It("releases the slot once the request is done", func() {
			handler.ServeHTTP(httptest.NewRecorder(), request)

			Expect(pool.TryAcquire()).To(BeTrue())
		})

		It("records the number of requests in-flight", func() {
			handler.ServeHTTP(httptest.NewRecorder(), request)
			handler.ServeHTTP(httptest.NewRecorder(), request)

			Expect(metric.Metrics.ConcurrentRequests[atc.ListAllJobs].Max()).To(Equal(float64(1)))
		})
	})

	Context("when the endpoint is disabled", func() {
		BeforeEach(func() {
			givenConcurrentRequestLimit(0)
		})

		It("responds with a 501", func() {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)

			Expect(recorder.Code).To(Equal(http.StatusNotImplemented))
			Expect(recorder.Body.String()).To(BeEmpty())
		})

		It("logs a DEBUG message", func() {
			handler.ServeHTTP(httptest.NewRecorder(), request)

			Expect(testLogger.Logs()).To(ConsistOf(
				MatchFields(IgnoreExtras, Fields{
					"Message":  Equal("test.endpoint-disabled"),
					"LogLevel": Equal(lager.DEBUG),
				}),
			))
		})

		It("increments the 'limitHit' counter", func() {
			handler.ServeHTTP(httptest.NewRecorder(), request)
			handler.ServeHTTP(httptest.NewRecorder(), request)

			Expect(metric.Metrics.ConcurrentRequestsLimitHit[atc.ListAllJobs].Delta()).To(Equal(float64(2)))
		})
	})

	Context("when the route has no configured limit", func() {
		It("passes the handler through untouched", func() {
			unlimited := &stupidHandler{}

			wrapped := wrappa.NewConcurrentRequestLimitsWrappa(
				testLogger,
				wrappa.NewConcurrentRequestPolicy(map[wrappa.LimitedRoute]int{}),
			).Wrap(map[string]http.Handler{
				atc.ListAllJobs: unlimited,
			})

			Expect(wrapped[atc.ListAllJobs]).To(BeIdenticalTo(unlimited))
		})
	})
})
