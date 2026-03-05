// Minimal Ginkgo suite that exercises the OTel test helper.
// Used to verify that ReportAfterEach spans arrive in Tempo.
//
// Run:
//   OTLP_HTTP_ENDPOINT=http://tempo-otlp.home \
//   go test ./testhelpers/otel/minisuite/ -v -count=1
package minisuite_test

import (
	"testing"
	"time"

	testotel "github.com/concourse/concourse/testhelpers/otel"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestMiniSuite(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "OTel Mini Suite")
}

var _ = BeforeSuite(func() {
	testotel.InitTestTracing("otel-minisuite")
})

var _ = AfterSuite(func() {
	testotel.Shutdown()
})

var _ = ReportAfterEach(testotel.ReportTestSpan)

var _ = Describe("OTel Verification", func() {
	It("completes a fast test", func() {
		Expect(1 + 1).To(Equal(2))
	})

	It("completes a slightly slower test", func() {
		time.Sleep(100 * time.Millisecond)
		Expect(true).To(BeTrue())
	})
})
