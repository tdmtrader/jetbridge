package exec_test

import (
	"context"
	"encoding/json"
	"strings"

	"code.cloudfoundry.org/clock"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"code.cloudfoundry.org/lager/v3/lagertest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/engine"
	"github.com/concourse/concourse/atc/event"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/exec/build"
	"github.com/concourse/concourse/atc/policy"
	"github.com/concourse/concourse/atc/runtime/runtimetest"
	"github.com/concourse/concourse/vars"
)

const plainString = "  pv  \n\n"

const yamlString = `
k1: yv1
k2: yv2
k3: 123
`

const jsonString = `
{
  "k1": "jv1", "k2": "jv2", "k3": 123
}
`

var _ = Describe("LoadVarStep", func() {

	var (
		ctx        context.Context
		cancel     func()
		testLogger *lagertest.TestLogger

		delegateFactory exec.BuildStepDelegateFactory

		fixture *execDBFixture
		dbBuild db.Build

		streamer *recordingStreamer

		loadVarPlan        *atc.LoadVarPlan
		fileContent        string
		artifactRepository *build.Repository
		state              exec.RunState

		spStep  exec.Step
		stepOk  bool
		stepErr error

		stepMetadata = exec.StepMetadata{
			TeamID:       123,
			TeamName:     "some-team",
			BuildID:      42,
			BuildName:    "some-build",
			PipelineID:   4567,
			PipelineName: "some-pipeline",
		}

		planID = "56"
	)

	BeforeEach(func() {
		testLogger = lagertest.NewTestLogger("var-step-test")
		ctx, cancel = context.WithCancel(context.Background())
		ctx = lagerctx.NewContext(ctx, testLogger)

		state = exec.NewRunState(noopStepper, vars.StaticVariables{})
		artifactRepository = state.ArtifactRepository()
		fileContent = ""

		fixture = useExecDB()
		_, _, _, dbBuild = createExecJobBuild(
			fixture,
			"load-var-team",
			atc.PipelineRef{Name: "some-pipeline"},
			atc.Config{Jobs: atc.JobConfigs{{Name: "some-job"}}},
			"some-user",
		)

		delegateFactory = buildStepDelegateFactory(func(state exec.RunState) exec.BuildStepDelegate {
			return engine.NewBuildStepDelegate(dbBuild, atc.PlanID(planID), state, clock.NewClock(), policy.NoopChecker{}, false)
		})

		streamer = newRecordingStreamer()
	})

	expectLocalVarAdded := func(expectKey string, expectValue any, expectRedact bool) {
		value, found, err := state.Get(vars.Reference{Source: ".", Path: expectKey})
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(value).To(Equal(expectValue))

		redacted := vars.TrackedVarsMap{}
		state.IterateInterpolatedCreds(redacted)
		if expectRedact {
			Expect(redacted).ToNot(BeEmpty())
		} else {
			Expect(redacted).To(BeEmpty())
		}
	}

	AfterEach(func() {
		cancel()
	})

	JustBeforeEach(func() {
		_, filePath, _ := strings.Cut(loadVarPlan.File, "/")
		artifactRepository.RegisterArtifact("some-resource", runtimetest.NewVolume("some-handle").WithContent(runtimetest.VolumeContent{
			filePath: {Data: []byte(fileContent)},
		}), false)

		plan := atc.Plan{
			ID:      atc.PlanID(planID),
			LoadVar: loadVarPlan,
		}

		spStep = exec.NewLoadVarStep(
			plan.ID,
			*plan.LoadVar,
			stepMetadata,
			delegateFactory,
			streamer,
		)

		stepOk, stepErr = spStep.Run(ctx, state)
	})

	Context("when format is specified", func() {
		Context("when format is invalid", func() {
			BeforeEach(func() {
				loadVarPlan = &atc.LoadVarPlan{
					Name:   "some-var",
					File:   "some-resource/a.diff",
					Format: "diff",
				}
			})

			It("step should fail", func() {
				Expect(stepErr).To(HaveOccurred())
				Expect(stepErr.Error()).To(Equal("invalid format diff"))
			})
		})

		Context("when format is trim", func() {
			BeforeEach(func() {
				loadVarPlan = &atc.LoadVarPlan{
					Name:   "some-var",
					File:   "some-resource/a.diff",
					Format: "trim",
				}

				fileContent = plainString
			})

			It("succeeds", func() {
				Expect(stepErr).ToNot(HaveOccurred())
				Expect(stepOk).To(BeTrue())
			})

			It("var should be parsed correctly", func() {
				expectLocalVarAdded("some-var", strings.TrimSpace(plainString), true)
			})

			It("persists the step lifecycle events and its stdout", func() {
				Expect(execBuildEventTypes(fixture, dbBuild)).To(ContainElements(
					event.EventTypeInitialize,
					event.EventTypeStart,
					event.EventTypeFinish,
				))
				Expect(execBuildLog(fixture, dbBuild, event.OriginSourceStdout)).To(Equal(
					"var some-var fetched.\nadded var some-var to build.\n",
				))
			})
		})

		Context("when format is raw", func() {
			BeforeEach(func() {
				loadVarPlan = &atc.LoadVarPlan{
					Name:   "some-var",
					File:   "some-resource/a.diff",
					Format: "raw",
				}

				fileContent = plainString
			})

			It("succeeds", func() {
				Expect(stepErr).ToNot(HaveOccurred())
				Expect(stepOk).To(BeTrue())
			})

			It("var should be parsed correctly", func() {
				expectLocalVarAdded("some-var", plainString, true)
			})
		})

		Context("when format is json", func() {
			BeforeEach(func() {
				loadVarPlan = &atc.LoadVarPlan{
					Name:   "some-var",
					File:   "some-resource/a.diff",
					Format: "json",
				}

				fileContent = jsonString
			})

			It("succeeds", func() {
				Expect(stepErr).ToNot(HaveOccurred())
				Expect(stepOk).To(BeTrue())
			})

			It("var should be parsed correctly", func() {
				expectLocalVarAdded("some-var", map[string]any{"k1": "jv1", "k2": "jv2", "k3": json.Number("123")}, true)
			})
		})

		Context("when format is yml", func() {
			BeforeEach(func() {
				loadVarPlan = &atc.LoadVarPlan{
					Name:   "some-var",
					File:   "some-resource/a.diff",
					Format: "yml",
				}

				fileContent = yamlString
			})

			It("succeeds", func() {
				Expect(stepErr).ToNot(HaveOccurred())
				Expect(stepOk).To(BeTrue())
			})

			It("var should be parsed correctly", func() {
				expectLocalVarAdded("some-var", map[string]any{"k1": "yv1", "k2": "yv2", "k3": json.Number("123")}, true)
			})
		})

		Context("when format is yaml", func() {
			BeforeEach(func() {
				loadVarPlan = &atc.LoadVarPlan{
					Name:   "some-var",
					File:   "some-resource/a.diff",
					Format: "yaml",
				}

				fileContent = yamlString
			})

			It("succeeds", func() {
				Expect(stepErr).ToNot(HaveOccurred())
				Expect(stepOk).To(BeTrue())
			})

			It("var should be parsed correctly", func() {
				expectLocalVarAdded("some-var", map[string]any{"k1": "yv1", "k2": "yv2", "k3": json.Number("123")}, true)
			})
		})
	})

	Context("when format is not specified", func() {
		Context("when file extension is other than json, yml and yaml", func() {
			BeforeEach(func() {
				loadVarPlan = &atc.LoadVarPlan{
					Name: "some-var",
					File: "some-resource/a.diff",
				}

				fileContent = plainString
			})

			It("succeeds", func() {
				Expect(stepErr).ToNot(HaveOccurred())
				Expect(stepOk).To(BeTrue())
			})

			It("var should be parsed correctly as trim", func() {
				expectLocalVarAdded("some-var", strings.TrimSpace(plainString), true)
			})
		})

		Context("when format is json", func() {
			BeforeEach(func() {
				loadVarPlan = &atc.LoadVarPlan{
					Name: "some-var",
					File: "some-resource/a.json",
				}

				fileContent = jsonString
			})

			It("succeeds", func() {
				Expect(stepErr).ToNot(HaveOccurred())
				Expect(stepOk).To(BeTrue())
			})

			It("var should be parsed correctly", func() {
				expectLocalVarAdded("some-var", map[string]any{"k1": "jv1", "k2": "jv2", "k3": json.Number("123")}, true)
			})
		})

		Context("when format is yml", func() {
			BeforeEach(func() {
				loadVarPlan = &atc.LoadVarPlan{
					Name: "some-var",
					File: "some-resource/a.yml",
				}

				fileContent = yamlString
			})

			It("succeeds", func() {
				Expect(stepErr).ToNot(HaveOccurred())
				Expect(stepOk).To(BeTrue())
			})

			It("var should be parsed correctly", func() {
				expectLocalVarAdded("some-var", map[string]any{"k1": "yv1", "k2": "yv2", "k3": json.Number("123")}, true)
			})
		})

		Context("when format is yaml", func() {
			BeforeEach(func() {
				loadVarPlan = &atc.LoadVarPlan{
					Name: "some-var",
					File: "some-resource/a.yaml",
				}

				fileContent = yamlString
			})

			It("succeeds", func() {
				Expect(stepErr).ToNot(HaveOccurred())
				Expect(stepOk).To(BeTrue())
			})

			It("var should be parsed correctly", func() {
				expectLocalVarAdded("some-var", map[string]any{"k1": "yv1", "k2": "yv2", "k3": json.Number("123")}, true)
			})
		})
	})

	Context("when file is bad", func() {
		Context("when json file is invalid JSON", func() {
			BeforeEach(func() {
				loadVarPlan = &atc.LoadVarPlan{
					Name: "some-var",
					File: "some-resource/a.json",
				}

				fileContent = jsonString + "{}"
			})

			It("step should fail", func() {
				Expect(stepErr).To(HaveOccurred())
				Expect(stepErr).To(MatchError(ContainSubstring("failed to parse some-resource/a.json in format json")))
			})
		})

		Context("when yaml file is bad", func() {
			BeforeEach(func() {
				loadVarPlan = &atc.LoadVarPlan{
					Name: "some-var",
					File: "some-resource/a.yaml",
				}

				fileContent = "a:\nb"
			})

			It("step should fail", func() {
				Expect(stepErr).To(HaveOccurred())
				Expect(stepErr).To(MatchError(ContainSubstring("failed to parse some-resource/a.yaml in format yaml")))
			})
		})

		Context("when file path artifact is not registered", func() {
			BeforeEach(func() {
				loadVarPlan = &atc.LoadVarPlan{
					Name: "some-var",
					File: "some-resource-not-in-the-registry/a.json",
				}

				fileContent = plainString
			})

			It("step should fail", func() {
				Expect(stepErr).To(MatchError("unknown artifact source: 'some-resource-not-in-the-registry' in file path 'a.json'"))
			})
		})
	})

	Context("reveal", func() {
		Context("when reveal is not specified", func() {
			BeforeEach(func() {
				loadVarPlan = &atc.LoadVarPlan{
					Name: "some-var",
					File: "some-resource/a.diff",
				}
				fileContent = plainString
			})

			It("local var should be redacted", func() {
				expectLocalVarAdded("some-var", strings.TrimSpace(plainString), true)
			})
		})

		Context("when reveal is false", func() {
			BeforeEach(func() {
				loadVarPlan = &atc.LoadVarPlan{
					Name:   "some-var",
					File:   "some-resource/a.diff",
					Reveal: false,
				}
				fileContent = plainString
			})

			It("local var should be redacted", func() {
				expectLocalVarAdded("some-var", strings.TrimSpace(plainString), true)
			})
		})

		Context("when reveal is true", func() {
			BeforeEach(func() {
				loadVarPlan = &atc.LoadVarPlan{
					Name:   "some-var",
					File:   "some-resource/a.diff",
					Reveal: true,
				}
				fileContent = plainString
			})

			It("local var should not be redacted", func() {
				expectLocalVarAdded("some-var", strings.TrimSpace(plainString), false)
			})
		})
	})
})
