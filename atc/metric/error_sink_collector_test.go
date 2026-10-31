package metric_test

import (
	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/metric"
	"github.com/prometheus/client_golang/prometheus"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ErrorSink", func() {
	var reg *prometheus.Registry

	BeforeEach(func() {
		reg = freshRegistry()
	})

	count := func(message string) float64 {
		return gathered(reg, "concourse_error_logs", map[string]string{"message": message})
	}

	It("counts an ERROR line by its message", func() {
		before := count("err-msg")

		metric.ErrorSink{}.Log(lager.LogFormat{Message: "err-msg", LogLevel: lager.ERROR})

		Expect(count("err-msg")).To(Equal(before + 1))
	})

	It("ignores lines below ERROR", func() {
		before := count("info-msg")

		metric.ErrorSink{}.Log(lager.LogFormat{Message: "info-msg", LogLevel: lager.INFO})

		Expect(count("info-msg")).To(Equal(before))
	})
})
