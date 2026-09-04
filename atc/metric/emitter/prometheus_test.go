package emitter_test

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"code.cloudfoundry.org/lager/v3/lagertest"

	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/metric/emitter"
)

// scrape and scrapedValue read the emitter's own /metrics page, because the
// question these specs ask is what a Prometheus server would see.
func scrape() string {
	req, _ := http.NewRequest("GET", fmt.Sprintf("http://%s:%s/metrics", scrapeConfig.BindIP, scrapeConfig.BindPort), nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return string(body)
}

// scrapedValue returns the value of the first non-comment sample line whose
// metric name matches, or "" when the page carries no such sample.
func scrapedValue(page, name string) string {
	for _, line := range strings.Split(page, "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		metricName, value, found := strings.Cut(line, " ")
		if !found {
			continue
		}
		if metricName == name || strings.HasPrefix(metricName, name+"{") {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

var scrapeConfig *emitter.PrometheusConfig

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
		scrapeConfig = prometheusConfig
	})

	It("exposes the pipeline run reclaimer's backlog and duration", func() {
		// Both are new surfaces on the /metrics page rather than internal
		// counters, so this asserts what a scrape actually sees: a backlog
		// that reads back the last level set, and a duration histogram.
		prometheusEmitter.Emit(logger, metric.Event{Name: "pipeline run reclaim backlog", Value: 7})
		prometheusEmitter.Emit(logger, metric.Event{Name: "gc: pipeline run reclaim duration (ms)", Value: 42})

		Eventually(scrape).Should(SatisfyAll(
			ContainSubstring("concourse_gc_pipeline_run_reclaim_backlog"),
			ContainSubstring("concourse_gc_pipeline_run_reclaim_duration_count"),
		))
		Expect(scrapedValue(scrape(), "concourse_gc_pipeline_run_reclaim_backlog")).To(Equal("7"))

		// A gauge, not a counter: a shrinking backlog must be able to say so.
		prometheusEmitter.Emit(logger, metric.Event{Name: "pipeline run reclaim backlog", Value: 2})
		Eventually(func() string { return scrapedValue(scrape(), "concourse_gc_pipeline_run_reclaim_backlog") }).Should(Equal("2"))
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
