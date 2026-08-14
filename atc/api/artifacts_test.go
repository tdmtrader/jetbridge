package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime/runtimetest"
	. "github.com/concourse/concourse/atc/testhelpers"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type artifactAPITeam struct {
	db.Team

	mu                               sync.Mutex
	findVolumeForWorkerArtifactErr   error
	findVolumeForWorkerArtifactCalls []int
	findWorkerForVolumeCalls         []string
}

func (team *artifactAPITeam) FindVolumeForWorkerArtifact(artifactID int) (db.CreatedVolume, bool, error) {
	team.mu.Lock()
	team.findVolumeForWorkerArtifactCalls = append(team.findVolumeForWorkerArtifactCalls, artifactID)
	err := team.findVolumeForWorkerArtifactErr
	team.mu.Unlock()

	if err != nil {
		return nil, false, err
	}

	return team.Team.FindVolumeForWorkerArtifact(artifactID)
}

// FindWorkerForVolume is how the worker pool turns the volume handle the
// handler asked about into a worker. Recording it here rather than on the pool
// observes both arguments the handler passed: the handle, and -- because the
// team factory only hands this team out under its own ID -- the team ID.
func (team *artifactAPITeam) FindWorkerForVolume(handle string) (db.Worker, bool, error) {
	team.mu.Lock()
	team.findWorkerForVolumeCalls = append(team.findWorkerForVolumeCalls, handle)
	team.mu.Unlock()

	return team.Team.FindWorkerForVolume(handle)
}

func (team *artifactAPITeam) FindWorkerForVolumeCalls() []string {
	team.mu.Lock()
	defer team.mu.Unlock()
	return append([]string(nil), team.findWorkerForVolumeCalls...)
}

func (team *artifactAPITeam) SetFindVolumeForWorkerArtifactError(err error) {
	team.mu.Lock()
	defer team.mu.Unlock()
	team.findVolumeForWorkerArtifactErr = err
}

func (team *artifactAPITeam) FindVolumeForWorkerArtifactCalls() []int {
	team.mu.Lock()
	defer team.mu.Unlock()
	return append([]int(nil), team.findVolumeForWorkerArtifactCalls...)
}

type artifactAPITeamFactory struct {
	db.TeamFactory
	teamName string
	team     db.Team
}

func (factory artifactAPITeamFactory) GetByID(teamID int) db.Team {
	if teamID == factory.team.ID() {
		return factory.team
	}
	return factory.TeamFactory.GetByID(teamID)
}

func (factory artifactAPITeamFactory) FindTeam(name string) (db.Team, bool, error) {
	team, found, err := factory.TeamFactory.FindTeam(name)
	if err != nil || !found || name != factory.teamName {
		return team, found, err
	}
	return factory.team, true, nil
}

var _ = Describe("ArtifactRepository API", func() {
	Describe("POST /api/v1/teams/:team_name/artifacts", func() {
		var (
			realdb    *realDB
			deps      apiDBDeps
			team      db.Team
			routeTeam *artifactAPITeam
			artifact  db.WorkerArtifact
			handle    string
			server    *httptest.Server

			request  *http.Request
			response *http.Response

			tarContents runtimetest.VolumeContent
		)

		BeforeEach(func() {
			realdb = useRealDB()
			deps = realdb.Deps

			var err error
			team, err = deps.teamFactory.CreateTeam(atc.Team{Name: "some-team"})
			Expect(err).NotTo(HaveOccurred())

			worker, err := deps.workerFactory.SaveWorker(atc.Worker{Name: "artifact-worker"}, 0)
			Expect(err).NotTo(HaveOccurred())

			build, err := team.CreateOneOffBuild()
			Expect(err).NotTo(HaveOccurred())

			volumeHandle := "some-artifact-handle"
			creating, err := deps.volumeRepository.CreateVolumeWithHandle(
				volumeHandle, team.ID(), worker.Name(), db.VolumeTypeArtifact,
			)
			Expect(err).NotTo(HaveOccurred())
			created, err := creating.Created()
			Expect(err).NotTo(HaveOccurred())
			handle = created.Handle()
			artifact, err = created.InitializeArtifact("some-artifact", build.ID())
			Expect(err).NotTo(HaveOccurred())

			routeTeam = &artifactAPITeam{Team: team}
			deps.teamFactory = artifactAPITeamFactory{
				TeamFactory: deps.teamFactory,
				teamName:    team.Name(),
				team:        routeTeam,
			}

			useProfile(memberProfile)
		})

		JustBeforeEach(func() {
			realdb.Deps = deps
			server = realdb.Serve()

			body, err := tarContents.StreamOut(context.Background(), ".", compression.GzipEncoding)
			Expect(err).NotTo(HaveOccurred())

			request, err = http.NewRequest("POST", server.URL+"/api/v1/teams/some-team/artifacts", body)
			Expect(err).NotTo(HaveOccurred())

			request.Header.Set("Content-Type", "application/json")

			q := url.Values{}
			q.Add("platform", "some-platform")
			request.URL.RawQuery = q.Encode()

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

		Context("when not authorized", func() {
			BeforeEach(func() {
				useProfile(memberProfile)
			})

			It("returns 403 Forbidden", func() {
				Expect(response.StatusCode).To(Equal(http.StatusForbidden))
			})
		})

		Context("when authorized", func() {
			var volume *runtimetest.Volume

			BeforeEach(func() {
				grantProfile(team, memberProfile, accessor.MemberRole)
				useProfile(memberProfile)

				tarContents = runtimetest.VolumeContent{
					"some/file": {Data: []byte("some contents")},
				}

				volume = runtimetest.NewVolume(handle)
				workerRuntime.createsArtifact(volume, artifact)
			})

			It("creates the volume for the team in the request", func() {
				Expect(workerRuntime.artifactTeamIDSnapshot()).To(Equal([]int{team.ID()}))
			})

			It("streams into the volume", func() {
				Expect(volume.Content).To(Equal(tarContents))
			})

			It("returns 201 Created", func() {
				Expect(response.StatusCode).To(Equal(http.StatusCreated))
			})

			It("returns Content-Type 'application/json'", func() {
				expectedHeaderEntries := map[string]string{
					"Content-Type": "application/json",
				}
				Expect(response).Should(IncludeHeaderEntries(expectedHeaderEntries))
			})

			It("returns the artifact record", func() {
				body, err := io.ReadAll(response.Body)
				Expect(err).NotTo(HaveOccurred())

				expected, err := json.Marshal(atc.WorkerArtifact{
					ID:        artifact.ID(),
					Name:      artifact.Name(),
					BuildID:   artifact.BuildID(),
					CreatedAt: artifact.CreatedAt().Unix(),
				})
				Expect(err).NotTo(HaveOccurred())
				Expect(body).To(MatchJSON(expected))
			})
		})
	})

	Describe("GET /api/v1/teams/:team_name/artifacts/:artifact_id", func() {
		var (
			realdb     *realDB
			deps       apiDBDeps
			team       db.Team
			routeTeam  *artifactAPITeam
			artifact   db.WorkerArtifact
			artifactID int
			handle     string
			server     *httptest.Server

			request  *http.Request
			response *http.Response
		)

		BeforeEach(func() {
			realdb = useRealDB()
			deps = realdb.Deps

			var err error
			team, err = deps.teamFactory.CreateTeam(atc.Team{Name: "some-team"})
			Expect(err).NotTo(HaveOccurred())

			worker, err := deps.workerFactory.SaveWorker(atc.Worker{Name: "artifact-worker"}, 0)
			Expect(err).NotTo(HaveOccurred())

			build, err := team.CreateOneOffBuild()
			Expect(err).NotTo(HaveOccurred())

			volumeHandle := "some-artifact-handle"
			creating, err := deps.volumeRepository.CreateVolumeWithHandle(
				volumeHandle, team.ID(), worker.Name(), db.VolumeTypeArtifact,
			)
			Expect(err).NotTo(HaveOccurred())
			created, err := creating.Created()
			Expect(err).NotTo(HaveOccurred())
			handle = created.Handle()
			artifact, err = created.InitializeArtifact("some-artifact", build.ID())
			Expect(err).NotTo(HaveOccurred())
			artifactID = artifact.ID()

			routeTeam = &artifactAPITeam{Team: team}
			deps.teamFactory = artifactAPITeamFactory{
				TeamFactory: deps.teamFactory,
				teamName:    team.Name(),
				team:        routeTeam,
			}

			useProfile(memberProfile)
		})

		JustBeforeEach(func() {
			realdb.Deps = deps
			server = realdb.Serve()

			var err error
			request, err = http.NewRequest(
				http.MethodGet,
				server.URL+fmt.Sprintf("/api/v1/teams/some-team/artifacts/%d", artifactID),
				nil,
			)
			Expect(err).NotTo(HaveOccurred())

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

		Context("when not authorized", func() {
			BeforeEach(func() {
				useProfile(memberProfile)
			})

			It("returns 403 Forbidden", func() {
				Expect(response.StatusCode).To(Equal(http.StatusForbidden))
			})
		})

		Context("when authorized", func() {
			BeforeEach(func() {
				grantProfile(team, memberProfile, accessor.MemberRole)
				useProfile(memberProfile)
			})

			It("uses the artifactID to fetch the db volume record", func() {
				Expect(routeTeam.FindVolumeForWorkerArtifactCalls()).To(Equal([]int{artifact.ID()}))
			})

			Context("when retrieving db artifact volume fails", func() {
				BeforeEach(func() {
					routeTeam.SetFindVolumeForWorkerArtifactError(errors.New("nope"))
				})

				It("errors", func() {
					Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
				})
			})

			Context("when the db artifact volume is not found", func() {
				BeforeEach(func() {
					artifactID = artifact.ID() + 1
				})

				It("returns 404", func() {
					Expect(response.StatusCode).To(Equal(http.StatusNotFound))
				})
			})

			Context("when the db artifact volume is found", func() {
				It("uses the volume handle to lookup the worker volume", func() {
					Expect(routeTeam.FindWorkerForVolumeCalls()).To(Equal([]string{handle}))
				})

				Context("when the worker client errors", func() {
					BeforeEach(func() {
						workerRuntime.failVolumeLookup(errors.New("nope"))
					})

					It("returns 500", func() {
						Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
					})
				})

				Context("when the worker client can't find the volume", func() {
					BeforeEach(func() {
						workerRuntime.addVolume(runtimetest.NewVolume("some-other-handle"))
					})

					It("returns 404", func() {
						Expect(response.StatusCode).To(Equal(http.StatusNotFound))
					})
				})

				Context("when the worker client finds the volume", func() {
					var volume *runtimetest.Volume

					BeforeEach(func() {
						volume = runtimetest.NewVolume(handle).
							WithContent(runtimetest.VolumeContent{
								"some/file": {Data: []byte("some content")},
							})

						workerRuntime.addVolume(volume)
					})

					It("returns 200", func() {
						Expect(response.StatusCode).To(Equal(http.StatusOK))
					})

					It("returns Content-Type 'application/octet-stream'", func() {
						expectedHeaderEntries := map[string]string{
							"Content-Type": "application/octet-stream",
						}
						Expect(response).Should(IncludeHeaderEntries(expectedHeaderEntries))
					})

					It("streams out the contents of the volume from the root path", func() {
						tarStream := runtimetest.VolumeContent{}

						err := tarStream.StreamIn(context.Background(), ".", compression.GzipEncoding, 0, response.Body)
						Expect(err).ToNot(HaveOccurred())

						Expect(tarStream).To(Equal(volume.Content))
					})
				})
			})
		})
	})
})
