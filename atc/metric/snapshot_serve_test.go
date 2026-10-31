package metric_test

import (
	"github.com/concourse/concourse/atc/metric"

	. "github.com/onsi/gomega"
)

// servePrometheus opens the process-wide /metrics listener with attributes as
// its constant labels and returns the address it listens on. The collectors
// register with the default registry, so it can run once per process.
func servePrometheus(attributes map[string]string) string {
	address, err := metric.PrometheusConfig{BindIP: "127.0.0.1", BindPort: "0"}.Serve(attributes)
	Expect(err).NotTo(HaveOccurred())

	return address
}
