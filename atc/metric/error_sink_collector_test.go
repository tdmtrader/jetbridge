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
		emitter            *recordingEmitter
		monitor            *metric.Monitor
	)

	BeforeEach(func() {
		monitor, emitter = monitorWithRecorder()
		errorSinkCollector = metric.NewErrorSinkCollector(testLogger, monitor)
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
				Eventually(emitter.EventCount).Should(BeNumerically("==", 1))
				Expect(emitter.Events()[0].Attributes).To(HaveKeyWithValue("message", "err-msg"))
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
					Consistently(emitter.EventCount).Should(BeNumerically("==", 0))
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
				Consistently(emitter.EventCount).Should(BeNumerically("==", 0))
			})
		})
	})
})
