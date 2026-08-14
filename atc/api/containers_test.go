package api_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/runtime/runtimetest"
	. "github.com/concourse/concourse/atc/testhelpers"
	"github.com/gorilla/websocket"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type hijackResourceCleanup struct {
	lock    sync.Mutex
	release func()
	closers []io.Closer
}

func (cleanup *hijackResourceCleanup) setRelease(release func()) {
	cleanup.lock.Lock()
	defer cleanup.lock.Unlock()
	cleanup.release = release
}

func (cleanup *hijackResourceCleanup) addCloser(closer io.Closer) {
	if closer == nil {
		return
	}
	cleanup.lock.Lock()
	defer cleanup.lock.Unlock()
	cleanup.closers = append(cleanup.closers, closer)
}

func (cleanup *hijackResourceCleanup) close() {
	cleanup.lock.Lock()
	release := cleanup.release
	closers := append([]io.Closer(nil), cleanup.closers...)
	cleanup.release = nil
	cleanup.closers = nil
	cleanup.lock.Unlock()

	if release != nil {
		release()
	}
	for _, closer := range closers {
		_ = closer.Close()
	}
}

func (cleanup *hijackResourceCleanup) duringTeardown(teardown func()) {
	defer cleanup.close()
	teardown()
}

type hijackCloserFunc func() error

func (close hijackCloserFunc) Close() error {
	return close()
}

func TestHijackResourceCleanupRunsOnDiagnosticFailure(t *testing.T) {
	cleanup := new(hijackResourceCleanup)
	releaseCount := 0
	connectionCloseCount := 0
	responseCloseCount := 0
	cleanup.setRelease(func() { releaseCount++ })
	cleanup.addCloser(hijackCloserFunc(func() error {
		connectionCloseCount++
		return errors.New("connection close diagnostic")
	}))
	cleanup.addCloser(hijackCloserFunc(func() error {
		responseCloseCount++
		return errors.New("response close diagnostic")
	}))

	diagnosticPanic := func() (recovered any) {
		defer func() { recovered = recover() }()
		cleanup.duringTeardown(func() {
			panic("pre-release diagnostic failed")
		})
		return nil
	}()
	if diagnosticPanic != "pre-release diagnostic failed" {
		t.Fatalf("diagnostic panic = %v, want pre-release diagnostic failure", diagnosticPanic)
	}
	if releaseCount != 1 {
		t.Errorf("release count = %d, want 1", releaseCount)
	}
	if connectionCloseCount != 1 {
		t.Errorf("connection close count = %d, want 1", connectionCloseCount)
	}
	if responseCloseCount != 1 {
		t.Errorf("response close count = %d, want 1", responseCloseCount)
	}

	cleanup.close()
	if releaseCount != 1 || connectionCloseCount != 1 || responseCloseCount != 1 {
		t.Errorf(
			"cleanup was not idempotent: release=%d connection=%d response=%d, want all 1",
			releaseCount,
			connectionCloseCount,
			responseCloseCount,
		)
	}
}

type containersAPIFixture struct {
	container db.Container
	created   db.CreatedContainer
	worker    db.Worker
	build     db.Build
}

func createContainersAPIBuildStepContainer(
	deps apiDBDeps,
	team db.Team,
	workerName string,
	planID atc.PlanID,
	metadata func(buildID int) db.ContainerMetadata,
	markCreated bool,
) containersAPIFixture {
	GinkgoHelper()

	worker, err := deps.workerFactory.SaveWorker(atc.Worker{Name: workerName}, 0)
	Expect(err).NotTo(HaveOccurred())
	build, err := team.CreateOneOffBuild()
	Expect(err).NotTo(HaveOccurred())
	creating, err := worker.CreateContainer(
		db.NewBuildStepContainerOwner(build.ID(), planID, team.ID()),
		metadata(build.ID()),
	)
	Expect(err).NotTo(HaveOccurred())

	fixture := containersAPIFixture{container: creating, worker: worker, build: build}
	if markCreated {
		fixture.created, err = creating.Created()
		Expect(err).NotTo(HaveOccurred())
		fixture.container = fixture.created
	}
	return fixture
}

func createContainersAPICheckContainer(
	realdb *realDB,
	deps apiDBDeps,
	team db.Team,
	workerName string,
	pipelineRef atc.PipelineRef,
	resourceName string,
	source atc.Source,
) containersAPIFixture {
	GinkgoHelper()

	builder := dbtest.NewBuilder(realdb.Conn, realdb.LockFactory)
	dbtest.Setup(builder.WithBaseResourceType(realdb.Conn, dbtest.BaseResourceType))

	worker, err := deps.workerFactory.SaveWorker(dbtest.BaseWorker(workerName), 0)
	Expect(err).NotTo(HaveOccurred())
	pipeline, _, err := team.SavePipeline(
		pipelineRef,
		atc.Config{Resources: atc.ResourceConfigs{{
			Name:   resourceName,
			Type:   dbtest.BaseResourceType,
			Source: source,
		}}},
		db.ConfigVersion(0),
		false,
	)
	Expect(err).NotTo(HaveOccurred())

	scenario := &dbtest.Scenario{Team: team, Pipeline: pipeline, Workers: []db.Worker{worker}}
	scenario.Run(builder.WithResourceVersions(resourceName, atc.Version{"version": "1"}))
	resource := scenario.Resource(resourceName)
	found, err := resource.Reload()
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	resourceConfig, found, err := builder.ResourceConfigFactory.FindResourceConfigByID(resource.ResourceConfigID())
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())

	creating, err := worker.CreateContainer(
		db.NewResourceConfigCheckSessionContainerOwner(
			resourceConfig.ID(),
			resourceConfig.OriginBaseResourceType().ID,
			db.ContainerOwnerExpiries{Min: 5 * time.Minute, Max: time.Hour},
		),
		db.ContainerMetadata{Type: db.ContainerTypeCheck},
	)
	Expect(err).NotTo(HaveOccurred())
	created, err := creating.Created()
	Expect(err).NotTo(HaveOccurred())
	return containersAPIFixture{container: created, created: created, worker: worker}
}

type containersAPIRuntimeContainer struct {
	*runtimetest.Container
	dbContainer db.CreatedContainer
}

func (container *containersAPIRuntimeContainer) DBContainer() db.CreatedContainer {
	return container.dbContainer
}

var _ = Describe("Containers API", func() {
	var (
		stepType         = db.ContainerTypeTask
		stepName         = "some-step"
		pipelineID       = 1111
		jobID            = 2222
		workingDirectory = "/tmp/build/my-favorite-guid"
		attempt          = "1.5"
		user             = "snoopy"
	)

	fullMetadata := func(buildID int) db.ContainerMetadata {
		return db.ContainerMetadata{
			Type:             stepType,
			StepName:         stepName,
			Attempt:          attempt,
			PipelineID:       pipelineID,
			JobID:            jobID,
			BuildID:          buildID,
			WorkingDirectory: workingDirectory,
			User:             user,
		}
	}

	Describe("GET /api/v1/teams/a-team/containers", func() {
		var (
			realdb   *realDB
			deps     apiDBDeps
			team     db.Team
			server   *httptest.Server
			query    url.Values
			response *http.Response
			fixture  containersAPIFixture
		)

		BeforeEach(func() {
			realdb = useRealDB()
			deps = realdb.Deps
			var err error
			team, err = deps.teamFactory.CreateTeam(atc.Team{Name: "a-team"})
			Expect(err).NotTo(HaveOccurred())
			query = url.Values{}
		})

		JustBeforeEach(func() {
			realdb.Deps = deps
			server = realdb.Serve()
			request, err := http.NewRequest("GET", server.URL+"/api/v1/teams/a-team/containers", nil)
			Expect(err).NotTo(HaveOccurred())
			request.URL.RawQuery = query.Encode()
			request.Header.Set("Content-Type", "application/json")
			response, err = client.Do(request)
			if response != nil {
				DeferCleanup(response.Body.Close)
			}
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				useProfile(anonymousProfile)
			})

			It("returns 401 Unauthorized", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
			})
		})

		Context("when authenticated", func() {
			BeforeEach(func() {
				grantProfile(team, memberProfile, accessor.ViewerRole)
				useProfile(memberProfile)
			})

			Context("with no params", func() {
				Context("when no errors are returned", func() {
					var (
						firstFixture  containersAPIFixture
						secondFixture containersAPIFixture
					)

					BeforeEach(func() {
						firstFixture = createContainersAPIBuildStepContainer(
							deps, team, "some-worker-name", "some-plan", fullMetadata, false,
						)
						secondFixture = createContainersAPIBuildStepContainer(
							deps, team, "some-other-worker-name", "some-other-plan",
							func(buildID int) db.ContainerMetadata {
								return db.ContainerMetadata{
									Type:             stepType,
									StepName:         stepName + "-other",
									Attempt:          attempt + ".1",
									PipelineID:       pipelineID + 1,
									JobID:            jobID + 1,
									BuildID:          buildID,
									WorkingDirectory: workingDirectory + "/other",
									User:             user + "-other",
								}
							},
							true,
						)
					})

					It("returns 200", func() {
						Expect(response.StatusCode).To(Equal(http.StatusOK))
					})

					It("returns Content-Type application/json", func() {
						expectedHeaderEntries := map[string]string{
							"Content-Type": "application/json",
						}
						Expect(response).Should(IncludeHeaderEntries(expectedHeaderEntries))
					})

					It("returns all containers", func() {
						body, err := io.ReadAll(response.Body)
						Expect(err).NotTo(HaveOccurred())
						var presented []json.RawMessage
						Expect(json.Unmarshal(body, &presented)).To(Succeed())

						firstExpected, err := json.Marshal(atc.Container{
							ID: firstFixture.container.Handle(), WorkerName: "some-worker-name",
							State: atc.ContainerStateCreating, Type: string(stepType),
							StepName: stepName, Attempt: attempt,
							PipelineID: pipelineID, JobID: jobID, BuildID: firstFixture.build.ID(),
							WorkingDirectory: workingDirectory, User: user,
						})
						Expect(err).NotTo(HaveOccurred())
						secondExpected, err := json.Marshal(atc.Container{
							ID: secondFixture.container.Handle(), WorkerName: "some-other-worker-name",
							State: atc.ContainerStateCreated, Type: string(stepType),
							StepName: stepName + "-other", Attempt: attempt + ".1",
							PipelineID: pipelineID + 1, JobID: jobID + 1, BuildID: secondFixture.build.ID(),
							WorkingDirectory: workingDirectory + "/other", User: user + "-other",
						})
						Expect(err).NotTo(HaveOccurred())

						Expect(presented).To(ConsistOf(
							MatchJSON(firstExpected),
							MatchJSON(secondExpected),
						))
					})
				})

				Context("when no containers are found", func() {
					It("returns 200", func() {
						Expect(response.StatusCode).To(Equal(http.StatusOK))
					})

					It("returns an empty array", func() {
						body, err := io.ReadAll(response.Body)
						Expect(err).NotTo(HaveOccurred())

						Expect(body).To(MatchJSON(`
						  []
						`))
					})
				})

			})

			Describe("querying with pipeline id", func() {
				BeforeEach(func() {
					fixture = createContainersAPIBuildStepContainer(deps, team, "pipeline-worker", "pipeline-plan", fullMetadata, true)
					createContainersAPIBuildStepContainer(
						deps, team, "pipeline-decoy-worker", "pipeline-decoy-plan",
						func(buildID int) db.ContainerMetadata {
							metadata := fullMetadata(buildID)
							metadata.PipelineID = pipelineID + 1
							return metadata
						},
						true,
					)
					query = url.Values{
						"pipeline_id": []string{strconv.Itoa(pipelineID)},
					}
				})

				It("returns the container with the requested pipeline ID", func() {
					var presented []atc.Container
					Expect(json.NewDecoder(response.Body).Decode(&presented)).To(Succeed())
					Expect(presented).To(ConsistOf(HaveField("ID", fixture.container.Handle())))
				})
			})

			Describe("querying with pipeline instance vars", func() {
				BeforeEach(func() {
					fixture = createContainersAPIBuildStepContainer(deps, team, "vars-worker", "vars-plan", func(buildID int) db.ContainerMetadata {
						metadata := fullMetadata(buildID)
						metadata.PipelineInstanceVars = `{"branch":"master"}`
						return metadata
					}, true)
					createContainersAPIBuildStepContainer(deps, team, "vars-decoy-worker", "vars-decoy-plan", func(buildID int) db.ContainerMetadata {
						metadata := fullMetadata(buildID)
						metadata.PipelineInstanceVars = `{"branch":"other"}`
						return metadata
					}, true)
					query = url.Values{
						"vars": []string{`{"branch":"master"}`},
					}
				})

				It("returns the container with the requested pipeline instance vars", func() {
					var presented []atc.Container
					Expect(json.NewDecoder(response.Body).Decode(&presented)).To(Succeed())
					Expect(presented).To(ConsistOf(HaveField("ID", fixture.container.Handle())))
				})
			})

			Describe("querying with job id", func() {
				BeforeEach(func() {
					fixture = createContainersAPIBuildStepContainer(deps, team, "job-worker", "job-plan", fullMetadata, true)
					createContainersAPIBuildStepContainer(
						deps, team, "job-decoy-worker", "job-decoy-plan",
						func(buildID int) db.ContainerMetadata {
							metadata := fullMetadata(buildID)
							metadata.JobID = jobID + 1
							return metadata
						},
						true,
					)
					query = url.Values{
						"job_id": []string{strconv.Itoa(jobID)},
					}
				})

				It("returns the container with the requested job ID", func() {
					var presented []atc.Container
					Expect(json.NewDecoder(response.Body).Decode(&presented)).To(Succeed())
					Expect(presented).To(ConsistOf(HaveField("ID", fixture.container.Handle())))
				})
			})

			Describe("querying with type", func() {
				BeforeEach(func() {
					fixture = createContainersAPIBuildStepContainer(deps, team, "type-worker", "type-plan", fullMetadata, true)
					createContainersAPIBuildStepContainer(
						deps, team, "type-decoy-worker", "type-decoy-plan",
						func(buildID int) db.ContainerMetadata {
							metadata := fullMetadata(buildID)
							metadata.Type = db.ContainerTypeGet
							return metadata
						},
						true,
					)
					query = url.Values{
						"type": []string{string(stepType)},
					}
				})

				It("returns the container with the requested type", func() {
					var presented []atc.Container
					Expect(json.NewDecoder(response.Body).Decode(&presented)).To(Succeed())
					Expect(presented).To(ConsistOf(HaveField("ID", fixture.container.Handle())))
				})
			})

			Describe("querying with step name", func() {
				BeforeEach(func() {
					fixture = createContainersAPIBuildStepContainer(deps, team, "step-worker", "step-plan", fullMetadata, true)
					createContainersAPIBuildStepContainer(
						deps, team, "step-decoy-worker", "step-decoy-plan",
						func(buildID int) db.ContainerMetadata {
							metadata := fullMetadata(buildID)
							metadata.StepName = stepName + "-other"
							return metadata
						},
						true,
					)
					query = url.Values{
						"step_name": []string{stepName},
					}
				})

				It("returns the container with the requested step name", func() {
					var presented []atc.Container
					Expect(json.NewDecoder(response.Body).Decode(&presented)).To(Succeed())
					Expect(presented).To(ConsistOf(HaveField("ID", fixture.container.Handle())))
				})
			})

			Describe("querying with build id", func() {
				Context("when the buildID can be parsed as an int", func() {
					BeforeEach(func() {
						fixture = createContainersAPIBuildStepContainer(deps, team, "build-worker", "build-plan", fullMetadata, true)
						createContainersAPIBuildStepContainer(deps, team, "build-decoy-worker", "build-decoy-plan", fullMetadata, true)
						query = url.Values{"build_id": []string{strconv.Itoa(fixture.build.ID())}}
					})

					It("returns the container with the requested build ID", func() {
						var presented []atc.Container
						Expect(json.NewDecoder(response.Body).Decode(&presented)).To(Succeed())
						Expect(presented).To(ConsistOf(HaveField("ID", fixture.container.Handle())))
					})

					Context("when the buildID fails to be parsed as an int", func() {
						BeforeEach(func() {
							query = url.Values{
								"build_id": []string{"not-an-int"},
							}
						})

						It("returns 400 Bad Request", func() {
							Expect(response.StatusCode).To(Equal(http.StatusBadRequest))
						})

					})
				})
			})

			Describe("querying with attempts", func() {
				Context("when the attempts can be parsed as a slice of int", func() {
					BeforeEach(func() {
						fixture = createContainersAPIBuildStepContainer(deps, team, "attempt-worker", "attempt-plan", fullMetadata, true)
						createContainersAPIBuildStepContainer(
							deps, team, "attempt-decoy-worker", "attempt-decoy-plan",
							func(buildID int) db.ContainerMetadata {
								metadata := fullMetadata(buildID)
								metadata.Attempt = attempt + ".1"
								return metadata
							},
							true,
						)
						query = url.Values{
							"attempt": []string{attempt},
						}
					})

					It("returns the container with the requested attempt", func() {
						var presented []atc.Container
						Expect(json.NewDecoder(response.Body).Decode(&presented)).To(Succeed())
						Expect(presented).To(ConsistOf(HaveField("ID", fixture.container.Handle())))
					})
				})
			})

			Describe("querying with type 'check'", func() {
				BeforeEach(func() {
					fixture = createContainersAPICheckContainer(
						realdb,
						deps,
						team,
						"check-list-worker",
						atc.PipelineRef{Name: "some-pipeline", InstanceVars: atc.InstanceVars{"branch": "master"}},
						"some-resource",
						atc.Source{"some": "source"},
					)
					createContainersAPICheckContainer(
						realdb,
						deps,
						team,
						"check-list-decoy-worker",
						atc.PipelineRef{Name: "other-pipeline", InstanceVars: atc.InstanceVars{"branch": "other"}},
						"other-resource",
						atc.Source{"other": "source"},
					)
					rawInstanceVars, _ := json.Marshal(atc.InstanceVars{"branch": "master"})
					query = url.Values{
						"type":          []string{"check"},
						"resource_name": []string{"some-resource"},
						"pipeline_name": []string{"some-pipeline"},
						"vars":          []string{string(rawInstanceVars)},
					}
				})

				It("returns the check container for the requested pipeline and resource", func() {
					var presented []atc.Container
					Expect(json.NewDecoder(response.Body).Decode(&presented)).To(Succeed())
					Expect(presented).To(ConsistOf(HaveField("ID", fixture.container.Handle())))
				})
			})
		})
	})

	Describe("GET /api/v1/containers/:id", func() {
		var (
			realdb   *realDB
			deps     apiDBDeps
			team     db.Team
			fixture  containersAPIFixture
			handle   string
			server   *httptest.Server
			response *http.Response
		)

		BeforeEach(func() {
			realdb = useRealDB()
			deps = realdb.Deps
			var err error
			team, err = deps.teamFactory.CreateTeam(atc.Team{Name: "a-team"})
			Expect(err).NotTo(HaveOccurred())
			fixture = createContainersAPIBuildStepContainer(
				deps, team, "get-worker", "get-plan", fullMetadata, true,
			)
			createContainersAPIBuildStepContainer(
				deps, team, "get-decoy-worker", "get-decoy-plan", fullMetadata, true,
			)
			handle = fixture.container.Handle()
		})

		JustBeforeEach(func() {
			realdb.Deps = deps
			server = realdb.Serve()
			request, err := http.NewRequest("GET", server.URL+"/api/v1/teams/a-team/containers/"+handle, nil)
			Expect(err).NotTo(HaveOccurred())
			request.Header.Set("Content-Type", "application/json")
			response, err = client.Do(request)
			if response != nil {
				DeferCleanup(response.Body.Close)
			}
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				useProfile(anonymousProfile)
			})

			It("returns 401 Unauthorized", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
			})
		})

		Context("when authenticated", func() {
			BeforeEach(func() {
				grantProfile(team, memberProfile, accessor.ViewerRole)
				useProfile(memberProfile)
			})

			Context("when the container is not found", func() {
				BeforeEach(func() {
					destroying, err := fixture.created.Destroying()
					Expect(err).NotTo(HaveOccurred())
					destroyed, err := destroying.Destroy()
					Expect(err).NotTo(HaveOccurred())
					Expect(destroyed).To(BeTrue())
				})

				It("returns 404 Not Found", func() {
					Expect(response.StatusCode).To(Equal(http.StatusNotFound))
				})
			})

			Context("when the container is found", func() {
				Context("when the container is within the team", func() {
					It("returns 200 OK", func() {
						Expect(response.StatusCode).To(Equal(http.StatusOK))
					})

					It("returns Content-Type application/json", func() {
						expectedHeaderEntries := map[string]string{
							"Content-Type": "application/json",
						}
						Expect(response).Should(IncludeHeaderEntries(expectedHeaderEntries))
					})

					It("returns the container", func() {
						body, err := io.ReadAll(response.Body)
						Expect(err).NotTo(HaveOccurred())
						expected, err := json.Marshal(atc.Container{
							ID: fixture.container.Handle(), WorkerName: "get-worker",
							State: atc.ContainerStateCreated, Type: string(stepType),
							StepName: stepName, Attempt: attempt,
							PipelineID: pipelineID, JobID: jobID, BuildID: fixture.build.ID(),
							WorkingDirectory: workingDirectory, User: user,
						})
						Expect(err).NotTo(HaveOccurred())
						Expect(body).To(MatchJSON(expected))
					})
				})

				Context("when the container is not within the team", func() {
					BeforeEach(func() {
						outsideTeam, err := deps.teamFactory.CreateTeam(atc.Team{Name: "outside-team"})
						Expect(err).NotTo(HaveOccurred())
						outsideFixture := createContainersAPIBuildStepContainer(
							deps, outsideTeam, "outside-get-worker", "outside-get-plan", fullMetadata, true,
						)
						handle = outsideFixture.container.Handle()
					})

					It("returns 404 Not Found", func() {
						Expect(response.StatusCode).To(Equal(http.StatusNotFound))
					})
				})
			})

		})
	})

	Describe("GET /api/v1/containers/:id/hijack", func() {
		var (
			realdb             *realDB
			deps               apiDBDeps
			team               db.Team
			outsideTeam        db.Team
			fixture            containersAPIFixture
			handle             string
			server             *httptest.Server
			requestPayload     string
			conn               *websocket.Conn
			response           *http.Response
			expectBadHandshake bool
			container          *runtimetest.Container
			runtimeContainer   *containersAPIRuntimeContainer
			releaseProcess     func()
			resourceCleanup    *hijackResourceCleanup
		)

		useFixture := func(selected containersAPIFixture) {
			GinkgoHelper()
			fixture = selected
			handle = selected.container.Handle()
			runtimeContainer.dbContainer = selected.created
			workerRuntime.addContainer(handle, runtimeContainer)
		}

		installBlockingProcess := func() chan int {
			GinkgoHelper()
			processExit := make(chan int)
			exit := processExit
			var releaseOnce sync.Once
			releaseProcess = func() {
				releaseOnce.Do(func() { close(processExit) })
			}
			resourceCleanup.setRelease(releaseProcess)
			container.ProcessDefs[0].Stub.Call = func(_ context.Context, _ *runtimetest.Process) (runtime.ProcessResult, error) {
				return runtime.ProcessResult{ExitStatus: <-exit}, nil
			}
			return processExit
		}

		waitForHijack := func() *runtimetest.Process {
			GinkgoHelper()
			Eventually(container.RunningProcesses).Should(HaveLen(1))
			return container.RunningProcesses()[0]
		}

		freshLastHijack := func() time.Time {
			GinkgoHelper()
			created, found, err := team.FindCreatedContainerByHandle(handle)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			return created.LastHijack()
		}

		BeforeEach(func() {
			conn = nil
			response = nil
			releaseProcess = nil
			resourceCleanup = new(hijackResourceCleanup)

			realdb = useRealDB()
			deps = realdb.Deps
			var err error
			team, err = deps.teamFactory.CreateTeam(atc.Team{Name: "a-team"})
			Expect(err).NotTo(HaveOccurred())
			outsideTeam, err = deps.teamFactory.CreateTeam(atc.Team{Name: "outside-team"})
			Expect(err).NotTo(HaveOccurred())
			container = runtimetest.NewContainer().WithProcess(
				runtime.ProcessSpec{Path: "ls", User: "snoopy"},
				runtimetest.ProcessStub{},
			)
			runtimeContainer = &containersAPIRuntimeContainer{Container: container}
			fixture = createContainersAPIBuildStepContainer(
				deps, team, "hijack-worker", "hijack-plan", fullMetadata, true,
			)
			useFixture(fixture)

			expectBadHandshake = false
			requestPayload = `{"path":"ls", "user": "snoopy"}`
		})

		JustBeforeEach(func() {
			realdb.Deps = deps
			server = realdb.Serve()
			DeferCleanup(resourceCleanup.close)
			wsURL, err := url.Parse(server.URL)
			Expect(err).NotTo(HaveOccurred())

			wsURL.Scheme = "ws"
			wsURL.Path = "/api/v1/teams/a-team/containers/" + handle + "/hijack"

			conn, response, err = dialWebsocket(wsURL.String())
			if conn != nil {
				resourceCleanup.addCloser(conn)
			}
			if response != nil && response.Body != nil {
				resourceCleanup.addCloser(response.Body)
			}
			if expectBadHandshake {
				Expect(err).To(HaveOccurred())
				Expect(response).NotTo(BeNil())
				return
			}
			Expect(err).NotTo(HaveOccurred())

			writer, err := conn.NextWriter(websocket.TextMessage)
			Expect(err).NotTo(HaveOccurred())
			_, err = writer.Write([]byte(requestPayload))
			Expect(err).NotTo(HaveOccurred())
			Expect(writer.Close()).To(Succeed())
		})

		AfterEach(func() {
			resourceCleanup.duringTeardown(func() {
				var runContext context.Context
				if releaseProcess != nil {
					Eventually(container.RunningProcesses).Should(HaveLen(1))
					runContext = container.ContextOfRun()
					Expect(runContext).NotTo(BeNil())
					releaseProcess()
				} else if container != nil && len(container.RunningProcesses()) > 0 {
					// RunningProcesses takes the same mutex Run releases after storing
					// its context, so observing a process synchronizes this read.
					runContext = container.ContextOfRun()
					Expect(runContext).NotTo(BeNil())
				}
				resourceCleanup.close()
				if runContext != nil {
					Eventually(runContext.Done()).Should(BeClosed())
				}
			})
		})

		Context("when authenticated", func() {
			BeforeEach(func() {
				grantProfile(team, memberProfile, accessor.MemberRole)
				useProfile(memberProfile)
			})

			Context("and the worker pool returns a container", func() {
				Context("when the container is a check container", func() {
					Context("when the user is not admin", func() {
						BeforeEach(func() {
							useFixture(createContainersAPICheckContainer(
								realdb,
								deps,
								team,
								"check-non-admin-worker",
								atc.PipelineRef{Name: "some-pipeline", InstanceVars: atc.InstanceVars{"branch": "master"}},
								"some-resource",
								atc.Source{"route": "source"},
							))
							expectBadHandshake = true
						})

						It("returns Forbidden", func() {
							Expect(response.StatusCode).To(Equal(http.StatusForbidden))
						})
					})

					Context("when the user is an admin", func() {
						BeforeEach(func() {
							useProfile(adminProfile)
						})

						Context("when the container is not within the team", func() {
							BeforeEach(func() {
								useFixture(createContainersAPICheckContainer(
									realdb,
									deps,
									outsideTeam,
									"outside-check-worker",
									atc.PipelineRef{Name: "outside-pipeline", InstanceVars: atc.InstanceVars{"branch": "outside"}},
									"outside-resource",
									atc.Source{"outside": "distinct-source"},
								))
								expectBadHandshake = true
							})

							It("returns 404 not found", func() {
								Expect(response.StatusCode).To(Equal(http.StatusNotFound))
							})
						})

						Context("when the container is within the team", func() {
							BeforeEach(func() {
								useFixture(createContainersAPICheckContainer(
									realdb,
									deps,
									team,
									"check-admin-worker",
									atc.PipelineRef{Name: "some-pipeline", InstanceVars: atc.InstanceVars{"branch": "master"}},
									"some-resource",
									atc.Source{"route": "source"},
								))
								installBlockingProcess()
							})

							It("should try to hijack the container", func() {
								waitForHijack()
							})
						})
					})
				})

				Context("when the container is a build step container", func() {
					Context("when the container is not within the team", func() {
						BeforeEach(func() {
							useFixture(createContainersAPIBuildStepContainer(
								deps, outsideTeam, "outside-hijack-worker", "outside-hijack-plan", fullMetadata, true,
							))
							expectBadHandshake = true
						})

						It("returns 404 not found", func() {
							Expect(response.StatusCode).To(Equal(http.StatusNotFound))
						})
					})

					Context("when the container is within the team", func() {
						Context("when the container could not be found on the worker client", func() {
							BeforeEach(func() {
								expectBadHandshake = true

								// A container the database still records, on a worker
								// that no longer holds it.
								handle = createContainersAPIBuildStepContainer(
									deps, team, "forgetful-worker", "forgotten-plan", fullMetadata, true,
								).container.Handle()
							})

							It("returns 404 Not Found", func() {
								Expect(response.StatusCode).To(Equal(http.StatusNotFound))
							})
						})

						Context("when the request payload is invalid", func() {
							BeforeEach(func() {
								requestPayload = "ß"
							})

							It("closes the connection with an error", func() {
								_, _, err := conn.ReadMessage()

								Expect(websocket.IsCloseError(err, 1003)).To(BeTrue()) // unsupported data
								Expect(err).To(MatchError(ContainSubstring("malformed process spec")))
							})
						})

						Context("when running the process fails", func() {
							BeforeEach(func() {
								// unsetting the expected process means that running will fail
								container.ProcessDefs = nil
							})

							It("receives the error in the output", func() {
								var hijackOutput atc.HijackOutput
								err := conn.ReadJSON(&hijackOutput)
								Expect(err).ToNot(HaveOccurred())
								Expect(hijackOutput.Error).ToNot(BeEmpty())
							})
						})

						Context("when running the process succeeds", func() {
							var processExit chan int

							BeforeEach(func() {
								processExit = installBlockingProcess()
							})

							It("hijacks the build", func() {
								waitForHijack()
							})

							It("updates the last hijack value", func() {
								waitForHijack()
								Eventually(freshLastHijack).ShouldNot(BeZero())
							})

							Context("when the hijack timer elapses", func() {
								var initialLastHijack time.Time

								JustBeforeEach(func() {
									waitForHijack()
									Eventually(freshLastHijack).ShouldNot(BeZero())
									initialLastHijack = freshLastHijack()
									fakeClock.WaitForWatcherAndIncrement(time.Second)
								})

								It("updates the last hijack value again", func() {
									Eventually(freshLastHijack).Should(BeTemporally(">", initialLastHijack))
								})
							})

							Context("when stdin is sent over the API", func() {
								JustBeforeEach(func() {
									err := conn.WriteJSON(atc.HijackInput{
										Stdin: []byte("some stdin\n"),
									})
									Expect(err).NotTo(HaveOccurred())
								})

								It("forwards the payload to the process", func() {
									process := waitForHijack()

									receivedStdin, err := bufio.NewReader(process.Stdin()).ReadBytes('\n')
									Expect(err).NotTo(HaveOccurred())
									Expect(receivedStdin).To(Equal([]byte("some stdin\n")))

									Expect(interceptTimeout.resetCount()).To(Equal(1))
								})
							})

							Context("when stdin is closed via the API", func() {
								JustBeforeEach(func() {
									err := conn.WriteJSON(atc.HijackInput{
										Closed: true,
									})
									Expect(err).NotTo(HaveOccurred())
								})

								It("closes the process's stdin", func() {
									process := waitForHijack()

									_, err := process.Stdin().Read(make([]byte, 10))
									Expect(err).To(Equal(io.EOF))
								})
							})

							Context("when the process prints to stdout", func() {
								JustBeforeEach(func() {
									process := waitForHijack()
									_, err := fmt.Fprintf(process.Stdout(), "some stdout\n")
									Expect(err).NotTo(HaveOccurred())
								})

								It("forwards it to the response", func() {
									var hijackOutput atc.HijackOutput
									err := conn.ReadJSON(&hijackOutput)
									Expect(err).NotTo(HaveOccurred())

									Expect(hijackOutput).To(Equal(atc.HijackOutput{
										Stdout: []byte("some stdout\n"),
									}))
								})
							})

							Context("when the process prints to stderr", func() {
								JustBeforeEach(func() {
									process := waitForHijack()

									_, err := fmt.Fprintf(process.Stderr(), "some stderr\n")
									Expect(err).NotTo(HaveOccurred())
								})

								It("forwards it to the response", func() {
									var hijackOutput atc.HijackOutput
									err := conn.ReadJSON(&hijackOutput)
									Expect(err).NotTo(HaveOccurred())

									Expect(hijackOutput).To(Equal(atc.HijackOutput{
										Stderr: []byte("some stderr\n"),
									}))
								})
							})

							Context("when the process exits", func() {
								JustBeforeEach(func() {
									Eventually(processExit).Should(BeSent(123))
								})

								It("forwards its exit status to the response", func() {
									var hijackOutput atc.HijackOutput
									err := conn.ReadJSON(&hijackOutput)
									Expect(err).NotTo(HaveOccurred())

									exitStatus := 123
									Expect(hijackOutput).To(Equal(atc.HijackOutput{
										ExitStatus: &exitStatus,
									}))
								})

								It("closes the process' stdin pipe", func() {
									process := waitForHijack()

									c := make(chan bool, 1)

									go func() {
										var b []byte
										_, err := process.Stdin().Read(b)
										if err != nil {
											c <- true
										}
									}()

									Eventually(c, 2*time.Second).Should(Receive())
								})
							})

							Context("when new tty settings are sent over the API", func() {
								JustBeforeEach(func() {
									err := conn.WriteJSON(atc.HijackInput{
										TTYSpec: &atc.HijackTTYSpec{
											WindowSize: atc.HijackWindowSize{
												Columns: 123,
												Rows:    456,
											},
										},
									})
									Expect(err).NotTo(HaveOccurred())
								})

								It("forwards it to the process", func() {
									process := waitForHijack()

									Eventually(process.TTY).Should(Equal(&runtime.TTYSpec{
										WindowSize: runtime.WindowSize{
											Columns: 123,
											Rows:    456,
										},
									}))
								})
							})

							Context("when waiting on the process fails", func() {
								BeforeEach(func() {
									container.ProcessDefs[0].Stub.Call = nil
									container.ProcessDefs[0].Stub.Err = "oh no!"
								})

								It("forwards the error to the response", func() {
									waitForHijack()

									var hijackOutput atc.HijackOutput
									err := conn.ReadJSON(&hijackOutput)
									Expect(err).NotTo(HaveOccurred())

									Expect(hijackOutput).To(Equal(atc.HijackOutput{
										Error: "oh no!",
									}))
								})
							})

							Context("when intercept timeout channel sends a value", func() {
								It("exits with timeout error", func() {
									interceptTimeout.expire()

									var hijackOutput atc.HijackOutput
									err := conn.ReadJSON(&hijackOutput)
									Expect(err).NotTo(HaveOccurred())

									Expect(hijackOutput.Error).To(Equal("idle timeout (1h0m0s) reached"))
								})
							})
						})
					})
				})
			})
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				expectBadHandshake = true

				useProfile(anonymousProfile)
			})

			It("returns 401 Unauthorized", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
			})
		})
	})

})
