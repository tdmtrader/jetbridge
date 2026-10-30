package compression_test

import (
	"github.com/concourse/concourse/atc/compression"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Compression", func() {
	var (
		comp compression.Compression
	)

	Describe("Gzip", func() {
		BeforeEach(func() {
			comp = compression.NewGzipCompression()
		})

		It("returns gzip", func() {
			Expect(comp.Encoding()).To(Equal(compression.GzipEncoding))
		})
	})
})
