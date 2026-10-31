package metric_test

import (
	"net/http"
	"net/http/httptest"

	"github.com/concourse/concourse/atc/metric"
	"github.com/prometheus/client_golang/prometheus"

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

var _ = Describe("WrapHandler", func() {
	var (
		ts  *httptest.Server
		reg *prometheus.Registry
	)

	BeforeEach(func() {
		reg = freshRegistry()
		ts = httptest.NewServer(metric.WrapHandler("ApiEndpoint", http.HandlerFunc(noopHandler)))
	})

	AfterEach(func() {
		ts.Close()
	})

	DescribeTable("records the response time by method, route and status",
		func(path, status string) {
			labels := map[string]string{"method": "GET", "route": "ApiEndpoint", "status": status}
			before := gathered(reg, "concourse_http_responses_duration_seconds", labels)

			res, err := http.Get(ts.URL + path)
			Expect(err).ToNot(HaveOccurred())
			res.Body.Close()

			Expect(gathered(reg, "concourse_http_responses_duration_seconds", labels)).To(Equal(before + 1))
		},
		Entry("not found", "/", "404"),
		Entry("success", "/success", "200"),
		Entry("failure", "/failure", "500"),
	)
})
