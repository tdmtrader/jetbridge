package metric_test

import (
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/metric/emitter"

	. "github.com/onsi/gomega"
)

// servePrometheus opens the process-wide /metrics listener with attributes as
// its constant labels and returns the address it listens on. The collectors
// register with the default registry, so it can run once per process.
func servePrometheus(attributes map[string]string) string {
	const address = "127.0.0.1:19093"

	metric.Metrics.RegisterEmitter(&emitter.PrometheusConfig{BindIP: "127.0.0.1", BindPort: "19093"})
	Expect(metric.Metrics.Initialize(testLogger, "snapshot", attributes, 10000)).To(Succeed())

	return address
}
