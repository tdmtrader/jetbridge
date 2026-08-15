package metric_test

import (
	"net/http"
	"net/http/httptest"

	"code.cloudfoundry.org/lager/v3"
	. "github.com/concourse/concourse/atc/metric"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func noopHandler(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/success":
		return
	case "/failure":
		w.WriteHeader(500)
		return
	}

	w.WriteHeader(404)
}

var _ = Describe("MetricsHandler", func() {
	var (
		ts      *httptest.Server
		output  *metricLogOutput
		monitor *Monitor
		logger  lager.Logger
	)

	BeforeEach(func() {
		monitor, output, logger = monitorWithLager()

		ts = httptest.NewServer(
			WrapHandler(
				logger,
				monitor,
				"ApiEndpoint",
				http.HandlerFunc(noopHandler),
			),
		)
	})

	AfterEach(func() {
		ts.Close()
	})

	Context("when serving requests", func() {
		var (
			endpoint = "/"
			event    lager.Data
		)

		JustBeforeEach(func() {
			res, err := http.Get(ts.URL + endpoint)
			Expect(err).ToNot(HaveOccurred())
			res.Body.Close()

			Eventually(output.EventCount).Should(BeNumerically("==", 1))
			event = output.MetricEvents()[0]
		})

		It("captures request and response properties", func() {
			Expect(event).To(HaveKeyWithValue("status", "404"))
			Expect(event).To(HaveKeyWithValue("method", "GET"))
			Expect(event).To(HaveKeyWithValue("route", "ApiEndpoint"))
			Expect(event).To(HaveKeyWithValue("path", "/"))
		})

		Context("to endpoint that returns success statuses", func() {
			BeforeEach(func() {
				endpoint = "/success"
			})

			It("captures error code", func() {
				Expect(event).To(HaveKeyWithValue("status", "200"))
			})

			It("captures route", func() {
				Expect(event).To(HaveKeyWithValue("path", "/success"))
			})
		})

		Context("to faulty endpoint", func() {
			BeforeEach(func() {
				endpoint = "/failure"
			})

			It("captures error code", func() {
				Expect(event).To(HaveKeyWithValue("status", "500"))
			})

			It("captures route", func() {
				Expect(event).To(HaveKeyWithValue("path", "/failure"))
			})
		})
	})
})
