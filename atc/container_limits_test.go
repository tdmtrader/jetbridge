package atc_test

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/atc"
)

var _ = Describe("ContainerLimits", func() {
	Describe("ParseMemoryLimit", func() {
		It("parses plain bytes", func() {
			limit, err := atc.ParseMemoryLimit("1024")
			Expect(err).NotTo(HaveOccurred())
			Expect(limit).To(Equal(atc.MemoryLimit(1024)))
		})

		It("parses KB as binary units", func() {
			limit, err := atc.ParseMemoryLimit("1KB")
			Expect(err).NotTo(HaveOccurred())
			Expect(limit).To(Equal(atc.MemoryLimit(1024)))
		})

		It("parses MB as binary units", func() {
			limit, err := atc.ParseMemoryLimit("1MB")
			Expect(err).NotTo(HaveOccurred())
			Expect(limit).To(Equal(atc.MemoryLimit(1048576)))
		})

		It("parses GB as binary units", func() {
			limit, err := atc.ParseMemoryLimit("1GB")
			Expect(err).NotTo(HaveOccurred())
			Expect(limit).To(Equal(atc.MemoryLimit(1073741824)))
		})

		It("parses KiB as binary units", func() {
			limit, err := atc.ParseMemoryLimit("1KiB")
			Expect(err).NotTo(HaveOccurred())
			Expect(limit).To(Equal(atc.MemoryLimit(1024)))
		})

		It("parses MiB as binary units", func() {
			limit, err := atc.ParseMemoryLimit("1MiB")
			Expect(err).NotTo(HaveOccurred())
			Expect(limit).To(Equal(atc.MemoryLimit(1048576)))
		})

		It("parses GiB as binary units", func() {
			limit, err := atc.ParseMemoryLimit("1GiB")
			Expect(err).NotTo(HaveOccurred())
			Expect(limit).To(Equal(atc.MemoryLimit(1073741824)))
		})

		It("is case insensitive for units", func() {
			lowercaseLimit, err := atc.ParseMemoryLimit("1kb")
			Expect(err).NotTo(HaveOccurred())

			uppercaseLimit, err := atc.ParseMemoryLimit("1KB")
			Expect(err).NotTo(HaveOccurred())

			Expect(lowercaseLimit).To(Equal(uppercaseLimit))
		})

		It("is case insensitive for binary prefix", func() {
			lowercaseLimit, err := atc.ParseMemoryLimit("1kib")
			Expect(err).NotTo(HaveOccurred())

			uppercaseLimit, err := atc.ParseMemoryLimit("1KiB")
			Expect(err).NotTo(HaveOccurred())

			Expect(lowercaseLimit).To(Equal(uppercaseLimit))
		})

		It("returns error for invalid format", func() {
			_, err := atc.ParseMemoryLimit("invalid")
			Expect(err).To(HaveOccurred())
		})

		It("handles single prefix units (K, M, G) as binary", func() {
			kLimit, err := atc.ParseMemoryLimit("1K")
			Expect(err).NotTo(HaveOccurred())
			Expect(kLimit).To(Equal(atc.MemoryLimit(1024)))

			mLimit, err := atc.ParseMemoryLimit("1m")
			Expect(err).NotTo(HaveOccurred())
			Expect(mLimit).To(Equal(atc.MemoryLimit(1048576)))

			gLimit, err := atc.ParseMemoryLimit("1G")
			Expect(err).NotTo(HaveOccurred())
			Expect(gLimit).To(Equal(atc.MemoryLimit(1073741824)))
		})
	})

	Describe("EphemeralStorageLimit", func() {
		It("unmarshals a numeric value as bytes", func() {
			var limits atc.ContainerLimits
			err := json.Unmarshal([]byte(`{"ephemeral_storage": 1073741824}`), &limits)
			Expect(err).NotTo(HaveOccurred())
			Expect(limits.EphemeralStorage).ToNot(BeNil())
			Expect(*limits.EphemeralStorage).To(Equal(atc.EphemeralStorageLimit(1073741824)))
		})

		It("unmarshals a string value with units", func() {
			var limits atc.ContainerLimits
			err := json.Unmarshal([]byte(`{"ephemeral_storage": "5G"}`), &limits)
			Expect(err).NotTo(HaveOccurred())
			Expect(limits.EphemeralStorage).ToNot(BeNil())
			Expect(*limits.EphemeralStorage).To(Equal(atc.EphemeralStorageLimit(5 * 1024 * 1024 * 1024)))
		})

		It("unmarshals GiB string", func() {
			var limits atc.ContainerLimits
			err := json.Unmarshal([]byte(`{"ephemeral_storage": "2GiB"}`), &limits)
			Expect(err).NotTo(HaveOccurred())
			Expect(limits.EphemeralStorage).ToNot(BeNil())
			Expect(*limits.EphemeralStorage).To(Equal(atc.EphemeralStorageLimit(2 * 1024 * 1024 * 1024)))
		})

		It("is omitted when nil", func() {
			limits := atc.ContainerLimits{}
			data, err := json.Marshal(limits)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(data)).To(Equal("{}"))
		})
	})
})
