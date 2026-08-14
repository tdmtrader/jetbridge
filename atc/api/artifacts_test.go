package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime/runtimetest"
	. "github.com/concourse/concourse/atc/testhelpers"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ArtifactRepository API", func() {
	Describe("POST /api/v1/teams/:team_name/artifacts", func() {
		var (
			realdb   *realDB
			deps     apiDBDeps
			team     db.Team
			artifact db.WorkerArtifact
			handle   string
			server   *httptest.Server

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
				workerRuntime.addArtifact(team.ID(), volume, artifact)
			})

			It("uses the artifact volume owned by the requested team", func() {
				Expect(response.StatusCode).To(Equal(http.StatusCreated))
				Expect(volume.Content).To(Equal(tarContents))

				created, found, err := team.FindVolumeForWorkerArtifact(artifact.ID())
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(created.TeamID()).To(Equal(team.ID()))
				Expect(created.Handle()).To(Equal(handle))
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

			Context("when the db artifact volume is not found", func() {
				BeforeEach(func() {
					artifactID = artifact.ID() + 1
				})

				It("returns 404", func() {
					Expect(response.StatusCode).To(Equal(http.StatusNotFound))
				})
			})

			Context("when the db artifact volume is found", func() {
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
