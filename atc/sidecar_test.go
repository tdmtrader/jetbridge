package atc_test

import (
	"encoding/json"

	. "github.com/concourse/concourse/atc"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SidecarConfig", func() {
	Describe("ParseSidecarConfigs", func() {
		Context("given a valid single sidecar", func() {
			It("parses all fields", func() {
				data := []byte(`
- name: postgres
  image: postgres:15
  command: ["docker-entrypoint.sh"]
  args: ["postgres"]
  workingDir: /var/lib/postgresql
  env:
  - name: POSTGRES_PASSWORD
    value: test
  - name: POSTGRES_DB
    value: myapp_test
  ports:
  - containerPort: 5432
    protocol: TCP
  resources:
    requests:
      cpu: 100m
      memory: 256Mi
    limits:
      cpu: 500m
      memory: 512Mi
`)
				configs, err := ParseSidecarConfigs(data)
				Expect(err).ToNot(HaveOccurred())
				Expect(configs).To(HaveLen(1))

				sc := configs[0]
				Expect(sc.Name).To(Equal("postgres"))
				Expect(sc.Image).To(Equal("postgres:15"))
				Expect(sc.Command).To(Equal([]string{"docker-entrypoint.sh"}))
				Expect(sc.Args).To(Equal([]string{"postgres"}))
				Expect(sc.WorkingDir).To(Equal("/var/lib/postgresql"))
				Expect(sc.Env).To(Equal([]SidecarEnvVar{
					{Name: "POSTGRES_PASSWORD", Value: "test"},
					{Name: "POSTGRES_DB", Value: "myapp_test"},
				}))
				Expect(sc.Ports).To(Equal([]SidecarPort{
					{ContainerPort: 5432, Protocol: "TCP"},
				}))
				Expect(sc.Resources).ToNot(BeNil())
				Expect(sc.Resources.Requests.CPU).To(Equal("100m"))
				Expect(sc.Resources.Requests.Memory).To(Equal("256Mi"))
				Expect(sc.Resources.Limits.CPU).To(Equal("500m"))
				Expect(sc.Resources.Limits.Memory).To(Equal("512Mi"))
			})
		})

		Context("given multiple sidecars in one file", func() {
			It("parses all sidecars", func() {
				data := []byte(`
- name: postgres
  image: postgres:15
  env:
  - name: POSTGRES_PASSWORD
    value: test
  ports:
  - containerPort: 5432
- name: redis
  image: redis:7
  ports:
  - containerPort: 6379
`)
				configs, err := ParseSidecarConfigs(data)
				Expect(err).ToNot(HaveOccurred())
				Expect(configs).To(HaveLen(2))
				Expect(configs[0].Name).To(Equal("postgres"))
				Expect(configs[0].Image).To(Equal("postgres:15"))
				Expect(configs[1].Name).To(Equal("redis"))
				Expect(configs[1].Image).To(Equal("redis:7"))
			})
		})

		Context("given a minimal sidecar (name + image only)", func() {
			It("parses successfully", func() {
				data := []byte(`
- name: redis
  image: redis:7
`)
				configs, err := ParseSidecarConfigs(data)
				Expect(err).ToNot(HaveOccurred())
				Expect(configs).To(HaveLen(1))
				Expect(configs[0].Name).To(Equal("redis"))
				Expect(configs[0].Image).To(Equal("redis:7"))
				Expect(configs[0].Command).To(BeNil())
				Expect(configs[0].Env).To(BeNil())
				Expect(configs[0].Ports).To(BeNil())
				Expect(configs[0].Resources).To(BeNil())
			})
		})

		Context("when name is missing", func() {
			It("returns a validation error", func() {
				data := []byte(`
- image: postgres:15
`)
				_, err := ParseSidecarConfigs(data)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("missing 'name'"))
			})
		})

		Context("when image is missing", func() {
			It("returns a validation error", func() {
				data := []byte(`
- name: postgres
`)
				_, err := ParseSidecarConfigs(data)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("missing 'image'"))
			})
		})

		Context("when name is empty string", func() {
			It("returns a validation error", func() {
				data := []byte(`
- name: ""
  image: postgres:15
`)
				_, err := ParseSidecarConfigs(data)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("missing 'name'"))
			})
		})

		Context("when duplicate names exist", func() {
			It("returns a validation error", func() {
				data := []byte(`
- name: db
  image: postgres:15
- name: db
  image: mysql:8
`)
				_, err := ParseSidecarConfigs(data)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("duplicate sidecar name"))
			})
		})

		Context("when a sidecar name conflicts with reserved names", func() {
			It("returns a validation error for 'main'", func() {
				data := []byte(`
- name: main
  image: postgres:15
`)
				_, err := ParseSidecarConfigs(data)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("reserved container name"))
			})

			It("returns a validation error for 'artifact-helper'", func() {
				data := []byte(`
- name: artifact-helper
  image: postgres:15
`)
				_, err := ParseSidecarConfigs(data)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("reserved container name"))
			})
		})

		Context("when YAML has unknown fields", func() {
			It("returns an error", func() {
				data := []byte(`
- name: postgres
  image: postgres:15
  bogusField: something
`)
				_, err := ParseSidecarConfigs(data)
				Expect(err).To(HaveOccurred())
			})
		})

		Context("when YAML is invalid", func() {
			It("returns an error", func() {
				data := []byte(`not: valid: yaml: [`)
				_, err := ParseSidecarConfigs(data)
				Expect(err).To(HaveOccurred())
			})
		})

		Context("when the list is empty", func() {
			It("returns an empty slice", func() {
				data := []byte(`[]`)
				configs, err := ParseSidecarConfigs(data)
				Expect(err).ToNot(HaveOccurred())
				Expect(configs).To(BeEmpty())
			})
		})
	})

	Describe("JSON round-trip", func() {
		It("marshals and unmarshals correctly", func() {
			original := SidecarConfig{
				Name:    "postgres",
				Image:   "postgres:15",
				Command: []string{"docker-entrypoint.sh"},
				Args:    []string{"postgres"},
				Env: []SidecarEnvVar{
					{Name: "POSTGRES_PASSWORD", Value: "test"},
				},
				Ports: []SidecarPort{
					{ContainerPort: 5432, Protocol: "TCP"},
				},
				Resources: &SidecarResources{
					Requests: SidecarResourceList{CPU: "100m", Memory: "256Mi"},
					Limits:   SidecarResourceList{CPU: "500m", Memory: "512Mi"},
				},
			}

			data, err := json.Marshal(original)
			Expect(err).ToNot(HaveOccurred())

			var restored SidecarConfig
			err = json.Unmarshal(data, &restored)
			Expect(err).ToNot(HaveOccurred())
			Expect(restored).To(Equal(original))
		})
	})

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

			Context("when the entry is neither a string nor an object", func() {
				It("returns an error", func() {
					data := []byte(`123`)
					var ss SidecarSource
					err := json.Unmarshal(data, &ss)
					Expect(err).To(HaveOccurred())
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

			Context("when it is an inline config", func() {
				It("marshals as an object", func() {
					ss := SidecarSource{Config: &SidecarConfig{Name: "redis", Image: "redis:7"}}
					data, err := json.Marshal(ss)
					Expect(err).ToNot(HaveOccurred())

					var restored SidecarSource
					err = json.Unmarshal(data, &restored)
					Expect(err).ToNot(HaveOccurred())
					Expect(restored.Config).ToNot(BeNil())
					Expect(restored.Config.Name).To(Equal("redis"))
					Expect(restored.Config.Image).To(Equal("redis:7"))
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

				// Round-trip
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

	Describe("Validate", func() {
		It("returns nil for a valid config", func() {
			sc := SidecarConfig{Name: "db", Image: "postgres:15"}
			Expect(sc.Validate()).ToNot(HaveOccurred())
		})

		It("returns error when name is empty", func() {
			sc := SidecarConfig{Image: "postgres:15"}
			Expect(sc.Validate()).To(MatchError(ContainSubstring("missing 'name'")))
		})

		It("returns error when image is empty", func() {
			sc := SidecarConfig{Name: "db"}
			Expect(sc.Validate()).To(MatchError(ContainSubstring("missing 'image'")))
		})
	})
})
