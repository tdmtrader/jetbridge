package atc_test

import (
	"encoding/json"
	"time"

	. "github.com/concourse/concourse/atc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/yaml"
)

var _ = Describe("Config", func() {
	Describe("Template configuration", func() {
		keepLast := 20
		ttlDays := 30
		expected := Config{
			Template: true,
			Params: []ParamSchema{
				{
					Name:        "environment",
					Type:        ParamTypeEnum,
					Required:    true,
					Values:      []any{"staging", "production"},
					Description: "deployment environment",
				},
				{
					Name:    "dry_run",
					Type:    ParamTypeBool,
					Default: false,
				},
			},
			RunRetention: &RunRetentionConfig{
				KeepLast: &keepLast,
				TTLDays:  &ttlDays,
			},
			Jobs: JobConfigs{{Name: "entry", PlanSequence: []Step{}}},
		}

		DescribeTable("strictly decodes and round trips template fields",
			func(marshal func(any) ([]byte, error)) {
				payload, err := marshal(expected)
				Expect(err).NotTo(HaveOccurred())

				var actual Config
				Expect(UnmarshalConfig(payload, &actual)).To(Succeed())
				Expect(actual).To(Equal(expected))
			},
			Entry("JSON", json.Marshal),
			Entry("YAML", yaml.Marshal),
		)

		It("strictly rejects unknown parameter fields", func() {
			var config Config
			Expect(UnmarshalConfig([]byte(`
template: true
params:
- name: environment
  type: string
  typo: true
jobs:
- name: entry
  plan: []
`), &config)).To(MatchError(ContainSubstring(`unknown field "typo"`)))
		})

		It("refuses a prototypes section rather than dropping it with the other unknown top-level keys", func() {
			var config Config
			Expect(UnmarshalConfig([]byte(`
prototypes:
- name: some-prototype
  type: registry-image
jobs:
- name: entry
  plan: []
`), &config)).To(MatchError(ContainSubstring("prototypes: is not a pipeline section")))
		})
	})

	Describe("ResourceTypes.ImageForType", func() {
		var types ResourceTypes

		Context("when the resource type has a direct image reference", func() {
			BeforeEach(func() {
				types = ResourceTypes{
					{
						Name:       "custom-git",
						Image:      "my-registry/custom-git:latest",
						Privileged: true,
					},
				}
			})

			It("returns a TypeImage with ImageRef and no GetPlan", func() {
				typeImage := types.ImageForType("some-plan", "custom-git", nil)
				Expect(typeImage.ImageRef).To(Equal("my-registry/custom-git:latest"))
				Expect(typeImage.BaseType).To(BeEmpty())
				Expect(typeImage.Privileged).To(BeTrue())
				Expect(typeImage.GetPlan).To(BeNil())
			})
		})

		Context("when the resource type has type: registry-image and a source", func() {
			BeforeEach(func() {
				types = ResourceTypes{
					{
						Name:       "custom-git",
						Type:       "registry-image",
						Source:     Source{"repository": "my-registry/custom-git", "tag": "v2"},
						Params:     Params{"format": "oci"},
						Tags:       Tags{"type-tag"},
						Privileged: true,
					},
				}
			})

			It("returns the registry-image reference as the GetPlan and plans no check", func() {
				typeImage := types.ImageForType("some-plan", "custom-git", Tags{"step-tag"})
				Expect(typeImage.ImageRef).To(BeEmpty())
				Expect(typeImage.BaseType).To(Equal("registry-image"))
				Expect(typeImage.Privileged).To(BeTrue())
				Expect(typeImage.GetPlan).To(Equal(&Plan{
					ID: "some-plan/image-get",
					Get: &GetPlan{
						Name:      "custom-git",
						Type:      "registry-image",
						Source:    Source{"repository": "my-registry/custom-git", "tag": "v2"},
						Params:    Params{"format": "oci"},
						Tags:      Tags{"type-tag"},
						TypeImage: TypeImage{BaseType: "registry-image"},
					},
				}))
			})

			It("takes the step's tags when the type declares none", func() {
				types[0].Tags = nil
				typeImage := types.ImageForType("some-plan", "custom-git", Tags{"step-tag"})
				Expect(typeImage.GetPlan.Get.Tags).To(Equal(Tags{"step-tag"}))
			})
		})

		Context("when the resource type has a resolved digest (from native registry resolution)", func() {
			BeforeEach(func() {
				types = ResourceTypes{
					{
						Name:   "custom-git",
						Type:   "registry-image",
						Source: Source{"repository": "my-registry/custom-git"},
						// Image is set by Deserialize() from the resolved digest.
						Image: "my-registry/custom-git@sha256:abc123",
					},
				}
			})

			It("returns a TypeImage with ImageRef and no GetPlan", func() {
				typeImage := types.ImageForType("some-plan", "custom-git", nil)
				Expect(typeImage.ImageRef).To(Equal("my-registry/custom-git@sha256:abc123"))
				Expect(typeImage.BaseType).To(BeEmpty())
				Expect(typeImage.GetPlan).To(BeNil())
			})
		})

		Context("when the resource type is not found (base type)", func() {
			BeforeEach(func() {
				types = ResourceTypes{}
			})

			It("returns a TypeImage with just BaseType", func() {
				typeImage := types.ImageForType("some-plan", "git", nil)
				Expect(typeImage.ImageRef).To(BeEmpty())
				Expect(typeImage.BaseType).To(Equal("git"))
				Expect(typeImage.GetPlan).To(BeNil())
			})
		})
	})

	Describe("FetchImagePlan", func() {
		It("pins the version the image resource carries", func() {
			plan := FetchImagePlan("task-plan", ImageResource{
				Name:    "image",
				Type:    "registry-image",
				Source:  Source{"repository": "my-registry/task"},
				Version: Version{"digest": "sha256:abc"},
			}, nil)
			Expect(plan.ID).To(Equal(PlanID("task-plan/image-get")))
			Expect(plan.Get.Version).To(Equal(&Version{"digest": "sha256:abc"}))
			Expect(plan.Get.VersionFrom).To(BeNil())
		})

		It("leaves the version open when none is carried", func() {
			plan := FetchImagePlan("task-plan", ImageResource{
				Name:   "image",
				Type:   "registry-image",
				Source: Source{"repository": "my-registry/task"},
			}, nil)
			Expect(plan.Get.Version).To(BeNil())
			Expect(plan.Get.VersionFrom).To(BeNil())
		})
	})

	Describe("ValidateImageType", func() {
		It("accepts registry-image", func() {
			Expect(ValidateImageType("registry-image")).To(Succeed())
		})

		It("leaves an empty type to the caller", func() {
			Expect(ValidateImageType("")).To(Succeed())
		})

		It("refuses any other type by name", func() {
			err := ValidateImageType("docker-image")
			Expect(err).To(Equal(UnsupportedImageTypeError{Type: "docker-image"}))
			Expect(err.Error()).To(Equal("an image declared with type 'docker-image' is not registry-image: an image is a registry-image reference, declared as 'type: registry-image' with a source, or for a resource type a direct 'image:'"))
		})
	})

	Describe("VersionConfig", func() {
		Context("when unmarshaling a pinned version from JSON", func() {
			Context("when the version is all string", func() {
				It("produces the correct version config without error", func() {
					var versionConfig VersionConfig
					bs := []byte(`{ "some": "version", "other": "8" }`)
					err := json.Unmarshal(bs, &versionConfig)
					Expect(err).NotTo(HaveOccurred())

					expected := VersionConfig{
						Pinned: Version{
							"some":  "version",
							"other": "8",
						},
					}

					Expect(versionConfig).To(Equal(expected))
				})
			})

			Context("when the version contains not all string", func() {
				It("produces an error", func() {
					var versionConfig VersionConfig
					bs := []byte(`{ "some": 8 }`)
					err := json.Unmarshal(bs, &versionConfig)
					Expect(err).To(HaveOccurred())
					Expect(err.Error()).To(Equal("the value 8 of some is not a string"))
				})
			})
		})
	})

	Describe("VarSourceConfigs.OrderByDependency", func() {
		var (
			varSources VarSourceConfigs
			ordered    VarSourceConfigs
			orderError error
		)

		vs1 := VarSourceConfig{
			Name: "vs1",
			Type: "dummy",
			Config: map[string]any{
				"vars": map[string]any{"pk": "pv"},
			},
		}
		vs1_5 := VarSourceConfig{
			Name: "vs1",
			Type: "dummy",
			Config: map[string]any{
				"vars": map[string]any{"pk": "((vs5:pk))"},
			},
		}
		vs2 := VarSourceConfig{
			Name: "vs2",
			Type: "dummy",
			Config: map[string]any{
				"vars": map[string]any{"pk": "pv"},
			},
		}
		vs3 := VarSourceConfig{
			Name: "vs3",
			Type: "dummy",
			Config: map[string]any{
				"vars": map[string]any{"pk": "((vs1:pk))"},
			},
		}
		vs4 := VarSourceConfig{
			Name: "vs4",
			Type: "dummy",
			Config: map[string]any{
				"vars": map[string]any{"pk": "((vs2:pk))"},
			},
		}
		vs5 := VarSourceConfig{
			Name: "vs5",
			Type: "dummy",
			Config: map[string]any{
				"vars": map[string]any{"pk": "((vs3:pk))", "pk2": "((vs4:pk))"},
			},
		}

		JustBeforeEach(func() {
			ordered, orderError = varSources.OrderByDependency()
		})

		Context("var_sources with ideal order", func() {
			BeforeEach(func() {
				varSources = VarSourceConfigs{vs1, vs2, vs3, vs4, vs5}
			})
			It("should keep the original order", func() {
				Expect(orderError).ToNot(HaveOccurred())
				Expect(ordered[0].Name).To(Equal("vs1"))
				Expect(ordered[1].Name).To(Equal("vs2"))
				Expect(ordered[2].Name).To(Equal("vs3"))
				Expect(ordered[3].Name).To(Equal("vs4"))
				Expect(ordered[4].Name).To(Equal("vs5"))
			})
		})

		Context("var_sources with random order", func() {
			BeforeEach(func() {
				varSources = VarSourceConfigs{vs4, vs2, vs5, vs1, vs3}
			})
			It("should order properly", func() {
				Expect(orderError).ToNot(HaveOccurred())
				Expect(ordered[0].Name).To(Equal("vs2"))
				Expect(ordered[1].Name).To(Equal("vs4"))
				Expect(ordered[2].Name).To(Equal("vs1"))
				Expect(ordered[3].Name).To(Equal("vs3"))
				Expect(ordered[4].Name).To(Equal("vs5"))
			})
		})

		Context("var_sources with unresolved dependency", func() {
			BeforeEach(func() {
				varSources = VarSourceConfigs{vs4, vs2, vs5, vs3}
			})
			It("should raise error", func() {
				Expect(orderError).To(HaveOccurred())
				Expect(orderError.Error()).To(Equal("could not resolve inter-dependent var sources: vs5, vs3"))
			})
		})

		Context("var_sources with cyclic dependencies", func() {
			BeforeEach(func() {
				varSources = VarSourceConfigs{vs1_5, vs4, vs2, vs5, vs3}
			})
			It("should raise error", func() {
				Expect(orderError).To(HaveOccurred())
				Expect(orderError.Error()).To(Equal("could not resolve inter-dependent var sources: vs1, vs5, vs3"))
			})
		})
	})

	Describe("CheckEvery", func() {
		Context("when unmarshaling", func() {
			Context("check_every is never", func() {
				It("parses as a bool without error", func() {
					var resourceConfig ResourceConfig
					bs := []byte(`{ "check_every": "never" }`)
					err := json.Unmarshal(bs, &resourceConfig)
					Expect(err).NotTo(HaveOccurred())

					expected := ResourceConfig{
						CheckEvery: &CheckEvery{Never: true},
					}

					Expect(resourceConfig).To(Equal(expected))
				})
			})

			Context("check_every is a duration", func() {
				It("parses as a duration without error", func() {
					var resourceConfig ResourceConfig
					bs := []byte(`{ "check_every": "10s" }`)
					err := json.Unmarshal(bs, &resourceConfig)
					Expect(err).NotTo(HaveOccurred())

					expected := ResourceConfig{
						CheckEvery: &CheckEvery{Interval: 10 * time.Second},
					}

					Expect(resourceConfig).To(Equal(expected))
				})
			})

			Context("check_every is a non-duration string", func() {
				It("errors", func() {
					var resourceConfig ResourceConfig
					bs := []byte(`{ "check_every": "some-string" }`)
					err := json.Unmarshal(bs, &resourceConfig)
					Expect(err).To(MatchError(`time: invalid duration "some-string"`))
				})
			})

			Context("check_every is not a string", func() {
				It("errors", func() {
					var resourceConfig ResourceConfig
					bs := []byte(`{ "check_every": [1,2,3] }`)
					err := json.Unmarshal(bs, &resourceConfig)
					Expect(err).To(MatchError("non-string value provided"))
				})
			})
		})

		Context("marshaling", func() {
			Context("never is true", func() {
				It("returns never", func() {
					checkEvery := CheckEvery{Never: true}
					result, err := checkEvery.MarshalJSON()
					Expect(err).ToNot(HaveOccurred())
					Expect(result).To(Equal([]byte(`"never"`)))
				})
			})
			Context("interval is set", func() {
				It("returns the duration as a string", func() {
					checkEvery := CheckEvery{Interval: 10 * time.Second}
					result, err := checkEvery.MarshalJSON()
					Expect(err).ToNot(HaveOccurred())
					Expect(result).To(Equal([]byte(`"10s"`)))
				})
			})
			Context("both never and interval are not set", func() {
				It("returns an empty byte", func() {
					checkEvery := CheckEvery{}
					result, err := checkEvery.MarshalJSON()
					Expect(err).ToNot(HaveOccurred())
					Expect(result).To(Equal([]byte(`""`)))
				})
			})
		})
	})
})
