package emitter_test

import (
	"fmt"
	"io"
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"code.cloudfoundry.org/lager/v3/lagertest"

	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/metric/emitter"
)

var _ = Describe("PrometheusEmitter", Ordered, func() {
	var (
		prometheusConfig  *emitter.PrometheusConfig
		prometheusEmitter metric.Emitter
		logger            *lagertest.TestLogger
	)

	BeforeAll(func() {
		logger = lagertest.NewTestLogger("test")
		prometheusConfig = &emitter.PrometheusConfig{
			BindIP:   "localhost",
			BindPort: "19091",
		}
		var err error
		prometheusEmitter, err = prometheusConfig.NewEmitter(map[string]string{
			// Ensure invalid labels are sanitized.
			"invalid-label":     "foo",
			"__prefix__test__":  "bar",
			"_prefix_testtwo__": "baz",
		})
		Expect(err).To(BeNil())
	})

	It("attaches trace ID exemplar to build duration histogram", func() {
		prometheusEmitter.Emit(logger, metric.Event{
			Name:  "build finished",
			Value: 5000, // 5000ms = 5s
			Attributes: map[string]string{
				"team_name":    "my-team",
				"pipeline":     "my-pipeline",
				"job":          "my-job",
				"build_status": "succeeded",
			},
			TraceID: "4bf92f3577b34da6a3ce929d0e0e4736",
		})

		getOpenMetrics := func() string {
			req, _ := http.NewRequest("GET", fmt.Sprintf("http://%s:%s/metrics", prometheusConfig.BindIP, prometheusConfig.BindPort), nil)
			req.Header.Set("Accept", "application/openmetrics-text;version=1.0.0,application/openmetrics-text;version=0.0.1;q=0.75,text/plain;version=0.0.4;q=0.5")
			res, _ := http.DefaultClient.Do(req)
			body, _ := io.ReadAll(res.Body)
			defer res.Body.Close()
			return string(body)
		}
		Eventually(getOpenMetrics).Should(ContainSubstring("trace_id=\"4bf92f3577b34da6a3ce929d0e0e4736\""))
	})

	It("attaches trace ID exemplar to HTTP response time histogram", func() {
		prometheusEmitter.Emit(logger, metric.Event{
			Name:  "http response time",
			Value: 250, // 250ms
			Attributes: map[string]string{
				"route":  "ListBuilds",
				"method": "GET",
				"status": "200",
			},
			TraceID: "abcdef1234567890abcdef1234567890",
		})

		getOpenMetrics := func() string {
			req, _ := http.NewRequest("GET", fmt.Sprintf("http://%s:%s/metrics", prometheusConfig.BindIP, prometheusConfig.BindPort), nil)
			req.Header.Set("Accept", "application/openmetrics-text;version=1.0.0,application/openmetrics-text;version=0.0.1;q=0.75,text/plain;version=0.0.4;q=0.5")
			res, _ := http.DefaultClient.Do(req)
			body, _ := io.ReadAll(res.Body)
			defer res.Body.Close()
			return string(body)
		}
		Eventually(getOpenMetrics).Should(ContainSubstring("trace_id=\"abcdef1234567890abcdef1234567890\""))
	})

	It("emits metric", func() {
		prometheusEmitter.Emit(logger, metric.Event{
			Name:  "steps waiting",
			Value: 4,
			Attributes: map[string]string{
				"platform":   "darwin",
				"teamId":     "42",
				"teamName":   "teamdev",
				"type":       "get",
				"workerTags": "tester",
			},
		})

		prometheusEmitter.Emit(logger, metric.Event{
			Name:  "latest completed build status",
			Value: 0,
			Attributes: map[string]string{
				"jobName":      "job1",
				"pipelineName": "pipeline1",
				"teamName":     "team1",
			},
		})

		getPrometheusMetrics := func() string {
			res, _ := http.Get(fmt.Sprintf("http://%s:%s/metrics", prometheusConfig.BindIP, prometheusConfig.BindPort))
			body, _ := io.ReadAll(res.Body)
			defer res.Body.Close()

			Expect(res.StatusCode).To(Equal(http.StatusOK))
			return string(body)
		}
		Eventually(getPrometheusMetrics()).Should(ContainSubstring("concourse_steps_waiting{invalid_label=\"foo\",platform=\"darwin\",prefix_test=\"bar\",prefix_testtwo=\"baz\",teamId=\"42\",teamName=\"teamdev\",type=\"get\",workerTags=\"tester\"} 4"))
		Eventually(getPrometheusMetrics()).Should(ContainSubstring("concourse_builds_latest_completed_build_status{invalid_label=\"foo\",jobName=\"job1\",pipelineName=\"pipeline1\",prefix_test=\"bar\",prefix_testtwo=\"baz\",teamName=\"team1\"} 0"))

		prometheusEmitter.Emit(logger, metric.Event{
			Name:  "latest completed build status",
			Value: 1,
			Attributes: map[string]string{
				"teamName": "team1",
			},
		})
		Eventually(getPrometheusMetrics()).Should(ContainSubstring("concourse_builds_latest_completed_build_status{invalid_label=\"foo\",jobName=\"\",pipelineName=\"\",prefix_test=\"bar\",prefix_testtwo=\"baz\",teamName=\"team1\"} 1"))
	})
})
