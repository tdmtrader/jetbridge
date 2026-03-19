package envconfig_test

import (
	"os"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/envconfig"
)

func TestEnvconfig(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Envconfig Suite")
}

var _ = Describe("Envconfig", func() {
	const testKey = "CI_AGENT_TEST_VAR"

	AfterEach(func() {
		os.Unsetenv(testKey)
	})

	Describe("StringOrDefault", func() {
		It("returns default when not set", func() {
			Expect(envconfig.StringOrDefault(testKey, "fallback")).To(Equal("fallback"))
		})

		It("returns env value when set", func() {
			os.Setenv(testKey, "hello")
			Expect(envconfig.StringOrDefault(testKey, "fallback")).To(Equal("hello"))
		})
	})

	Describe("Float64OrDefault", func() {
		It("returns default when not set", func() {
			Expect(envconfig.Float64OrDefault(testKey, 3.14)).To(Equal(3.14))
		})

		It("parses valid float", func() {
			os.Setenv(testKey, "0.75")
			Expect(envconfig.Float64OrDefault(testKey, 0.0)).To(Equal(0.75))
		})

		It("returns default on invalid float", func() {
			os.Setenv(testKey, "notanumber")
			Expect(envconfig.Float64OrDefault(testKey, 1.0)).To(Equal(1.0))
		})
	})

	Describe("IntOrDefault", func() {
		It("returns default when not set", func() {
			Expect(envconfig.IntOrDefault(testKey, 42)).To(Equal(42))
		})

		It("parses valid int", func() {
			os.Setenv(testKey, "7")
			Expect(envconfig.IntOrDefault(testKey, 0)).To(Equal(7))
		})

		It("returns default on invalid int", func() {
			os.Setenv(testKey, "abc")
			Expect(envconfig.IntOrDefault(testKey, 5)).To(Equal(5))
		})
	})

	Describe("DurationOrDefault", func() {
		It("returns default when not set", func() {
			Expect(envconfig.DurationOrDefault(testKey, 5*time.Minute)).To(Equal(5 * time.Minute))
		})

		It("parses valid duration", func() {
			os.Setenv(testKey, "30s")
			Expect(envconfig.DurationOrDefault(testKey, 0)).To(Equal(30 * time.Second))
		})

		It("returns default on invalid duration", func() {
			os.Setenv(testKey, "badval")
			Expect(envconfig.DurationOrDefault(testKey, time.Minute)).To(Equal(time.Minute))
		})
	})

	Describe("BoolOrDefault", func() {
		It("returns default when not set", func() {
			Expect(envconfig.BoolOrDefault(testKey, true)).To(BeTrue())
		})

		It("parses true", func() {
			os.Setenv(testKey, "true")
			Expect(envconfig.BoolOrDefault(testKey, false)).To(BeTrue())
		})

		It("parses false", func() {
			os.Setenv(testKey, "false")
			Expect(envconfig.BoolOrDefault(testKey, true)).To(BeFalse())
		})
	})

	Describe("Required", func() {
		It("returns error when not set", func() {
			_, err := envconfig.Required(testKey)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(testKey))
		})

		It("returns value when set", func() {
			os.Setenv(testKey, "present")
			val, err := envconfig.Required(testKey)
			Expect(err).NotTo(HaveOccurred())
			Expect(val).To(Equal("present"))
		})
	})
})
