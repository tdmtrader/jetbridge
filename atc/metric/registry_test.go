package metric_test

import (
	"github.com/concourse/concourse/atc/metric"
	"github.com/prometheus/client_golang/prometheus"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// freshRegistry registers every collector with a registry of its own, so a
// spec reads what the page would carry without opening a listener.
func freshRegistry() *prometheus.Registry {
	GinkgoHelper()

	reg := prometheus.NewRegistry()
	Expect(metric.Register(reg, nil)).To(Succeed())
	return reg
}

// gathered reads the series of family name whose labels include want: a
// gauge's or counter's value, or a histogram's sample count. The collectors
// are process-wide, so specs compare before and after rather than assume a
// starting point.
func gathered(reg *prometheus.Registry, name string, want map[string]string) float64 {
	GinkgoHelper()

	families, err := reg.Gather()
	Expect(err).NotTo(HaveOccurred())

	for _, family := range families {
		if family.GetName() != name {
			continue
		}
	series:
		for _, m := range family.GetMetric() {
			labels := map[string]string{}
			for _, label := range m.GetLabel() {
				labels[label.GetName()] = label.GetValue()
			}
			for k, v := range want {
				if labels[k] != v {
					continue series
				}
			}
			switch {
			case m.GetGauge() != nil:
				return m.GetGauge().GetValue()
			case m.GetCounter() != nil:
				return m.GetCounter().GetValue()
			case m.GetHistogram() != nil:
				return float64(m.GetHistogram().GetSampleCount())
			}
		}
	}
	return 0
}

// exemplarTraceIDs lists the trace_id of every exemplar family name carries.
func exemplarTraceIDs(reg *prometheus.Registry, name string) []string {
	GinkgoHelper()

	families, err := reg.Gather()
	Expect(err).NotTo(HaveOccurred())

	var ids []string
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, m := range family.GetMetric() {
			for _, bucket := range m.GetHistogram().GetBucket() {
				for _, label := range bucket.GetExemplar().GetLabel() {
					if label.GetName() == "trace_id" {
						ids = append(ids, label.GetValue())
					}
				}
			}
		}
	}
	return ids
}
