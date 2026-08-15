package metric_test

import (
	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/metric"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ErrorSinkCollector", func() {
	var (
		errorSinkCollector metric.ErrorSinkCollector
		output             *metricLogOutput
		monitor            *metric.Monitor
	)

	BeforeEach(func() {
		var logger lager.Logger
		monitor, output, logger = monitorWithLager()
		errorSinkCollector = metric.NewErrorSinkCollector(logger, monitor)
	})

	Context("Log", func() {
		var log lager.LogFormat

		JustBeforeEach(func() {
			errorSinkCollector.Log(log)
		})

		Context("with message of level ERROR", func() {
			BeforeEach(func() {
				log = lager.LogFormat{
					Message:  "err-msg",
					LogLevel: lager.ERROR,
				}
			})

			It("emits with the message in the tags", func() {
				Eventually(output.EventCount).Should(BeNumerically("==", 1))
				Expect(output.MetricEvents()[0]).To(HaveKeyWithValue("message", "err-msg"))
			})

			Context("with error being from failed emission", func() {
				BeforeEach(func() {
					log = lager.LogFormat{
						Message:  "message",
						LogLevel: lager.ERROR,
						Error:    metric.ErrFailedToEmit,
					}
				})

				It("doesn't emit", func() {
					Consistently(output.EventCount).Should(BeNumerically("==", 0))
				})
			})
		})

		Context("with message of non-ERROR level", func() {
			BeforeEach(func() {
				log = lager.LogFormat{
					Message:  "message",
					LogLevel: lager.INFO,
				}
			})

			It("doesn't emit", func() {
				Consistently(output.EventCount).Should(BeNumerically("==", 0))
			})
		})
	})
})
