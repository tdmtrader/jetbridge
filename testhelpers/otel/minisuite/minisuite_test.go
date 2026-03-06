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
	It("completes instantly", func() {
		Expect(1 + 1).To(Equal(2))
	})

	It("takes 50ms", func() {
		time.Sleep(50 * time.Millisecond)
		Expect(true).To(BeTrue())
	})

	It("takes 100ms", func() {
		time.Sleep(100 * time.Millisecond)
		Expect(42).To(BeNumerically(">", 0))
	})

	It("takes 200ms", func() {
		time.Sleep(200 * time.Millisecond)
		Expect("hello").To(HaveLen(5))
	})

	It("takes 500ms", func() {
		time.Sleep(500 * time.Millisecond)
		Expect([]int{1, 2, 3}).To(HaveLen(3))
	})

	It("takes 750ms", func() {
		time.Sleep(750 * time.Millisecond)
		Expect(nil).To(BeNil())
	})

	It("takes 1 second", func() {
		time.Sleep(1 * time.Second)
		Expect("concourse").To(ContainSubstring("course"))
	})

	It("takes 1.5 seconds", func() {
		time.Sleep(1500 * time.Millisecond)
		Expect(map[string]int{"a": 1}).To(HaveKey("a"))
	})

	It("takes 2 seconds", func() {
		time.Sleep(2 * time.Second)
		Expect(3.14).To(BeNumerically("~", 3.14, 0.01))
	})

	It("takes 3 seconds", func() {
		time.Sleep(3 * time.Second)
		Expect(true).ToNot(BeFalse())
	})
})
