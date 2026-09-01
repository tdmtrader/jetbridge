package atc_test

import (
	"encoding/json"

	. "github.com/concourse/concourse/atc"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SidecarConfig", func() {
	Describe("SidecarSource", func() {
		Describe("JSON unmarshaling", func() {
			Context("when the entry is a string", func() {
				It("parses as a file reference", func() {
					data := []byte(`"my-repo/ci/sidecars/postgres.yml"`)
					var ss SidecarSource
					err := json.Unmarshal(data, &ss)
					Expect(err).ToNot(HaveOccurred())
					Expect(ss.File).To(Equal("my-repo/ci/sidecars/postgres.yml"))
					Expect(ss.Config).To(BeNil())
				})
			})

			Context("when the entry is an object", func() {
				It("parses as an inline SidecarConfig", func() {
					data := []byte(`{"name":"postgres","image":"postgres:15","env":[{"name":"POSTGRES_PASSWORD","value":"test"}]}`)
					var ss SidecarSource
					err := json.Unmarshal(data, &ss)
					Expect(err).ToNot(HaveOccurred())
					Expect(ss.File).To(BeEmpty())
					Expect(ss.Config).ToNot(BeNil())
					Expect(ss.Config.Name).To(Equal("postgres"))
					Expect(ss.Config.Image).To(Equal("postgres:15"))
					Expect(ss.Config.Env).To(Equal([]SidecarEnvVar{
						{Name: "POSTGRES_PASSWORD", Value: "test"},
					}))
				})
			})
		})

		Describe("JSON marshaling", func() {
			Context("when it is a file reference", func() {
				It("marshals as a string", func() {
					ss := SidecarSource{File: "my-repo/ci/sidecars/postgres.yml"}
					data, err := json.Marshal(ss)
					Expect(err).ToNot(HaveOccurred())
					Expect(string(data)).To(Equal(`"my-repo/ci/sidecars/postgres.yml"`))
				})
			})
		})

		Describe("mixed list round-trip", func() {
			It("parses and re-marshals a list of strings and objects", func() {
				data := []byte(`["my-repo/ci/sidecars/custom.yml",{"name":"postgres","image":"postgres:15"},{"name":"redis","image":"redis:7"}]`)
				var sources []SidecarSource
				err := json.Unmarshal(data, &sources)
				Expect(err).ToNot(HaveOccurred())
				Expect(sources).To(HaveLen(3))

				Expect(sources[0].File).To(Equal("my-repo/ci/sidecars/custom.yml"))
				Expect(sources[0].Config).To(BeNil())

				Expect(sources[1].File).To(BeEmpty())
				Expect(sources[1].Config).ToNot(BeNil())
				Expect(sources[1].Config.Name).To(Equal("postgres"))

				Expect(sources[2].Config).ToNot(BeNil())
				Expect(sources[2].Config.Name).To(Equal("redis"))

				out, err := json.Marshal(sources)
				Expect(err).ToNot(HaveOccurred())

				var restored []SidecarSource
				err = json.Unmarshal(out, &restored)
				Expect(err).ToNot(HaveOccurred())
				Expect(restored).To(HaveLen(3))
				Expect(restored[0].File).To(Equal("my-repo/ci/sidecars/custom.yml"))
				Expect(restored[1].Config.Name).To(Equal("postgres"))
				Expect(restored[2].Config.Name).To(Equal("redis"))
			})
		})
	})
})
