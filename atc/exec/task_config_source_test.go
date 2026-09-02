package exec_test

import (
	"context"
	"errors"
	"io"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/compression"
	. "github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/exec/build"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/runtime/runtimetest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/yaml"
)

// fetchErrorConfigSource preserves a real config source while replacing only
// FetchConfig for the failure path.
type fetchErrorConfigSource struct {
	TaskConfigSource
	err error
}

func (source fetchErrorConfigSource) FetchConfig(context.Context, lager.Logger, *build.Repository) (atc.TaskConfig, error) {
	return atc.TaskConfig{}, source.err
}

// streamOutErrorArtifact preserves a real artifact while replacing only
// StreamOut for the failure path.
type streamOutErrorArtifact struct {
	runtime.Artifact
	err error
}

func (artifact streamOutErrorArtifact) StreamOut(context.Context, string, compression.Compression) (io.ReadCloser, error) {
	return nil, artifact.err
}

var _ = Describe("TaskConfigSource", func() {
	var (
		taskConfig atc.TaskConfig
		repo       *build.Repository
		logger     *lagertest.TestLogger
	)

	BeforeEach(func() {
		logger = lagertest.NewTestLogger("task-config-source-test")
		repo = build.NewRepository()
		taskConfig = atc.TaskConfig{
			Platform:  "some-platform",
			RootfsURI: "some-image",
			ImageResource: &atc.ImageResource{
				Type: "docker",
				Source: atc.Source{
					"a":               "b",
					"evaluated-value": "((task-variable-name))",
				},
				Params: atc.Params{
					"some":            "params",
					"evaluated-value": "((task-variable-name))",
				},
				Version: atc.Version{"some": "version"},
			},
			Params: atc.TaskEnv{
				"key1": "key1-((task-variable-name))",
				"key2": "key2-((task-variable-name))",
			},
			Run: atc.TaskRunConfig{
				Path: "ls",
				Args: []string{"-al", "((task-variable-name))"},
				Dir:  "some/dir",
				User: "some-user",
			},
			Inputs: []atc.TaskInputConfig{
				{Name: "some-input", Path: "some-path"},
			},
		}
	})

	Describe("StaticConfigSource", func() {

		It("fetches task config successfully", func() {
			configSource := StaticConfigSource{Config: &taskConfig}
			fetchedConfig, fetchErr := configSource.FetchConfig(context.TODO(), logger, repo)
			Expect(fetchErr).ToNot(HaveOccurred())
			Expect(fetchedConfig).To(Equal(taskConfig))
		})

	})

	Describe("FileConfigSource", func() {
		var (
			configSource  FileConfigSource
			streamer      *recordingStreamer
			fetchedConfig atc.TaskConfig
			fetchErr      error
			artifactName  string
		)

		BeforeEach(func() {

			artifactName = "some-artifact-name"
			streamer = newRecordingStreamer()
			configSource = FileConfigSource{
				ConfigPath: artifactName + "/build.yml",
				Streamer:   streamer,
			}
		})

		JustBeforeEach(func() {
			fetchedConfig, fetchErr = configSource.FetchConfig(context.TODO(), logger, repo)
		})

		Context("when the path does not indicate an artifact source", func() {
			BeforeEach(func() {
				configSource.ConfigPath = "foo-bar.yml"
			})

			It("returns an error", func() {
				Expect(fetchErr).To(Equal(UnspecifiedArtifactSourceError{"foo-bar.yml"}))
			})
		})

		Context("when the file's artifact can be found in the repository", func() {
			registerConfigFile := func(content []byte) {
				volume := runtimetest.NewVolume("some-volume").WithContent(runtimetest.VolumeContent{
					"build.yml": {Data: content},
				})
				repo.RegisterArtifact(build.ArtifactName(artifactName), volume, false)
			}

			Context("when the artifact provides a proper file", func() {
				BeforeEach(func() {
					marshalled, err := yaml.Marshal(taskConfig)
					Expect(err).NotTo(HaveOccurred())

					registerConfigFile(marshalled)
				})

				It("fetches the file via the correct artifact & path", func() {
					Expect(fetchedConfig).To(Equal(taskConfig))
				})

				It("succeeds", func() {
					Expect(fetchErr).NotTo(HaveOccurred())
				})

				It("closes the stream", func() {
					Expect(streamer.streams).To(HaveLen(1))
					Expect(streamer.streams[0].closed).To(BeTrue())
				})
			})

			Context("when the artifact source provides an invalid configuration", func() {
				BeforeEach(func() {
					invalidConfig := taskConfig
					invalidConfig.Platform = ""
					invalidConfig.Run = atc.TaskRunConfig{}

					marshalled, err := yaml.Marshal(invalidConfig)
					Expect(err).NotTo(HaveOccurred())

					registerConfigFile(marshalled)
				})

				It("returns an error", func() {
					Expect(fetchErr).To(HaveOccurred())
				})
			})

			Context("when the artifact source provides a malformed file", func() {
				BeforeEach(func() {
					registerConfigFile([]byte("bogus"))
				})

				It("fails", func() {
					Expect(fetchErr).To(HaveOccurred())
				})

				It("closes the stream", func() {
					Expect(streamer.streams).To(HaveLen(1))
					Expect(streamer.streams[0].closed).To(BeTrue())
				})
			})

			Context("when the artifact source provides a valid file with invalid keys", func() {
				BeforeEach(func() {
					registerConfigFile([]byte(`
platform: beos

intputs: []

run: {path: a/file}
`))
				})

				It("fails", func() {
					Expect(fetchErr).To(HaveOccurred())
				})

				It("closes the stream", func() {
					Expect(streamer.streams).To(HaveLen(1))
					Expect(streamer.streams[0].closed).To(BeTrue())
				})
			})

			Context("when streaming the file out fails", func() {
				disaster := errors.New("nope")

				BeforeEach(func() {
					repo.RegisterArtifact(build.ArtifactName(artifactName), streamOutErrorArtifact{
						Artifact: runtimetest.NewVolume("some-volume"),
						err:      disaster,
					}, false)
				})

				It("returns the error", func() {
					Expect(fetchErr).To(MatchError(disaster))
				})
			})

			Context("when the config file is not in the artifact", func() {
				BeforeEach(func() {
					repo.RegisterArtifact(build.ArtifactName(artifactName), runtimetest.NewVolume("some-volume"), false)
				})

				It("returns the error", func() {
					Expect(fetchErr).To(MatchError("task config '" + artifactName + "/build.yml' not found"))
				})
			})
		})

		Context("when the file's artifact source cannot be found in the repository", func() {
			It("returns an UnknownArtifactSourceError", func() {
				Expect(fetchErr).To(Equal(UnknownArtifactSourceError{SourceName: build.ArtifactName(artifactName), ConfigPath: artifactName + "/build.yml"}))
			})
		})
	})

	Describe("OverrideParamsConfigSource", func() {
		var (
			config       atc.TaskConfig
			configSource TaskConfigSource

			overrideParams atc.TaskEnv

			fetchErr error
		)

		BeforeEach(func() {
			config = atc.TaskConfig{
				Platform:  "some-platform",
				RootfsURI: "some-image",
				Params:    atc.TaskEnv{"PARAM": "A", "ORIG_PARAM": "D"},
				Run: atc.TaskRunConfig{
					Path: "echo",
					Args: []string{"bananapants"},
				},
			}

			overrideParams = atc.TaskEnv{"PARAM": "B", "EXTRA_PARAM": "C"}
		})

		Context("when there are no params to override", func() {
			BeforeEach(func() {
				configSource = &OverrideParamsConfigSource{
					ConfigSource: StaticConfigSource{Config: &config},
				}
			})

			JustBeforeEach(func() {
				_, fetchErr = configSource.FetchConfig(context.TODO(), logger, repo)
			})

			It("succeeds", func() {
				Expect(fetchErr).NotTo(HaveOccurred())
			})

			It("returns no warnings", func() {
				Expect(configSource.Warnings()).To(HaveLen(0))
			})
		})

		Context("when override params are specified", func() {
			BeforeEach(func() {
				configSource = &OverrideParamsConfigSource{
					ConfigSource: StaticConfigSource{Config: &config},
					Params:       overrideParams,
				}
			})

			JustBeforeEach(func() {
				_, fetchErr = configSource.FetchConfig(context.TODO(), logger, repo)
			})

			It("succeeds", func() {
				Expect(fetchErr).NotTo(HaveOccurred())
			})

		})
	})

	Describe("OverrideContainerLimitsSource", func() {
		var (
			config         atc.TaskConfig
			noLimitsConfig atc.TaskConfig
			configSource   TaskConfigSource

			overrideLimits atc.ContainerLimits

			fetchedConfig atc.TaskConfig
			fetchErr      error
		)

		BeforeEach(func() {
			config = atc.TaskConfig{
				Platform:  "some-platform",
				RootfsURI: "some-image",
				Limits:    &atc.ContainerLimits{CPU: newCPULimit(1024), Memory: newMemoryLimit(209715200)},
				Run: atc.TaskRunConfig{
					Path: "echo",
					Args: []string{"bananapants"},
				},
			}

			noLimitsConfig = atc.TaskConfig{
				Platform:  "some-platform",
				RootfsURI: "some-image",
				Run: atc.TaskRunConfig{
					Path: "echo",
					Args: []string{"bananapants"},
				},
			}

			overrideLimits = atc.ContainerLimits{CPU: newCPULimit(2048), Memory: newMemoryLimit(209715200)}
		})

		Context("when there are no container limits to override", func() {
			BeforeEach(func() {
				configSource = &OverrideContainerLimitsSource{
					ConfigSource: StaticConfigSource{Config: &config},
				}
			})

			JustBeforeEach(func() {
				fetchedConfig, fetchErr = configSource.FetchConfig(context.TODO(), logger, repo)
			})

			It("succeeds", func() {
				Expect(fetchErr).NotTo(HaveOccurred())
			})

			It("returns the same config", func() {
				Expect(fetchedConfig).To(Equal(config))
			})
		})

		Context("when override container limits are specified but no configuration container limits", func() {
			BeforeEach(func() {
				configSource = &OverrideContainerLimitsSource{
					ConfigSource: StaticConfigSource{Config: &noLimitsConfig},
					Limits:       &overrideLimits,
				}
			})

			JustBeforeEach(func() {
				fetchedConfig, fetchErr = configSource.FetchConfig(context.TODO(), logger, repo)
			})

		})

		Context("when override container limits are specified", func() {
			BeforeEach(func() {
				configSource = &OverrideContainerLimitsSource{
					ConfigSource: StaticConfigSource{Config: &config},
					Limits:       &overrideLimits,
				}
			})

			JustBeforeEach(func() {
				fetchedConfig, fetchErr = configSource.FetchConfig(context.TODO(), logger, repo)
			})

			It("succeeds", func() {
				Expect(fetchErr).NotTo(HaveOccurred())
			})

		})
		Context("when override container requests are specified", func() {
			var overrideRequests atc.ContainerLimits

			BeforeEach(func() {
				overrideRequests = atc.ContainerLimits{CPU: newCPULimit(512), Memory: newMemoryLimit(1073741824)}
				configSource = &OverrideContainerLimitsSource{
					ConfigSource: StaticConfigSource{Config: &config},
					Requests:     &overrideRequests,
				}
			})

			JustBeforeEach(func() {
				fetchedConfig, fetchErr = configSource.FetchConfig(context.TODO(), logger, repo)
			})

			It("succeeds", func() {
				Expect(fetchErr).NotTo(HaveOccurred())
			})

		})

		Context("when both override limits and requests are specified", func() {
			var overrideRequests atc.ContainerLimits

			BeforeEach(func() {
				overrideRequests = atc.ContainerLimits{CPU: newCPULimit(256)}
				configSource = &OverrideContainerLimitsSource{
					ConfigSource: StaticConfigSource{Config: &config},
					Limits:       &overrideLimits,
					Requests:     &overrideRequests,
				}
			})

			JustBeforeEach(func() {
				fetchedConfig, fetchErr = configSource.FetchConfig(context.TODO(), logger, repo)
			})

			It("succeeds", func() {
				Expect(fetchErr).NotTo(HaveOccurred())
			})

		})

		Context("when override requests are specified but task has no existing requests", func() {
			var overrideRequests atc.ContainerLimits

			BeforeEach(func() {
				overrideRequests = atc.ContainerLimits{Memory: newMemoryLimit(536870912)}
				configSource = &OverrideContainerLimitsSource{
					ConfigSource: StaticConfigSource{Config: &noLimitsConfig},
					Requests:     &overrideRequests,
				}
			})

			JustBeforeEach(func() {
				fetchedConfig, fetchErr = configSource.FetchConfig(context.TODO(), logger, repo)
			})

			It("succeeds", func() {
				Expect(fetchErr).NotTo(HaveOccurred())
			})

		})
	})

	Describe("ValidatingConfigSource", func() {
		var (
			innerConfig atc.TaskConfig

			configSource TaskConfigSource

			fetchErr error
		)

		BeforeEach(func() {
			innerConfig = atc.TaskConfig{}

			configSource = ValidatingConfigSource{StaticConfigSource{Config: &innerConfig}}
		})

		JustBeforeEach(func() {
			_, fetchErr = configSource.FetchConfig(context.TODO(), logger, repo)
		})

		Context("when fetching the config fails", func() {
			disaster := errors.New("nope")

			BeforeEach(func() {
				configSource = ValidatingConfigSource{fetchErrorConfigSource{
					TaskConfigSource: StaticConfigSource{Config: &innerConfig},
					err:              disaster,
				}}
			})

			It("returns the error", func() {
				Expect(fetchErr).To(Equal(disaster))
			})
		})
	})

	Context("BaseResourceTypeDefaultsApplySource", func() {
		var (
			configSource  TaskConfigSource
			resourceTypes atc.ResourceTypes
			fetchedConfig atc.TaskConfig
			fetchErr      error
		)

		JustBeforeEach(func() {
			configSource = StaticConfigSource{Config: &taskConfig}
			configSource = BaseResourceTypeDefaultsApplySource{
				ConfigSource:  configSource,
				ResourceTypes: resourceTypes,
			}
			fetchedConfig, fetchErr = configSource.FetchConfig(context.TODO(), logger, repo)
		})

		Context("resourceTypes is empty, and no base resource type defaults configured", func() {
			It("fetchedConfig should be identical to the original", func() {
				Expect(fetchErr).ToNot(HaveOccurred())
				Expect(fetchedConfig).To(Equal(taskConfig))
			})
		})

		Context("resourceTypes is empty, and base resource type defaults configured", func() {
			BeforeEach(func() {
				atc.LoadBaseResourceTypeDefaults(map[string]atc.Source{"docker": atc.Source{"some-key": "some-value"}})
			})
			AfterEach(func() {
				atc.LoadBaseResourceTypeDefaults(map[string]atc.Source{})
			})

		})

		Context("resourceTypes contains image source type", func() {
			BeforeEach(func() {
				resourceTypes = atc.ResourceTypes{
					{
						Name:     "docker",
						Defaults: atc.Source{"some-key": "some-value"},
					},
				}
			})

		})
	})
})

func newCPULimit(cpuLimit uint64) *atc.CPULimit {
	limit := atc.CPULimit(cpuLimit)
	return &limit
}

func newMemoryLimit(memoryLimit uint64) *atc.MemoryLimit {
	limit := atc.MemoryLimit(memoryLimit)
	return &limit
}
