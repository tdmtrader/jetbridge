package api_test

import (
	"context"
	"encoding/json"
	"fmt"
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
			realdb *realDB
			deps   apiDBDeps
			team   db.Team
			server *httptest.Server

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

			_, err = deps.workerFactory.SaveWorker(atc.Worker{Name: "artifact-worker"}, 0)
			Expect(err).NotTo(HaveOccurred())
			workerRuntime.connectArtifactVolumeRepository(deps.volumeRepository)

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
			var volumesBefore, artifactsBefore int

			BeforeEach(func() {
				grantProfile(team, memberProfile, accessor.MemberRole)
				useProfile(memberProfile)

				tarContents = runtimetest.VolumeContent{
					"some/file": {Data: []byte("some contents")},
				}
				Expect(realdb.Conn.QueryRow(
					`SELECT count(*) FROM volumes WHERE team_id = $1`, team.ID(),
				).Scan(&volumesBefore)).To(Succeed())
				Expect(realdb.Conn.QueryRow(
					`SELECT count(*) FROM worker_artifacts`,
				).Scan(&artifactsBefore)).To(Succeed())
			})

			It("creates a fresh team-owned artifact volume, streams the payload, and returns it", func() {
				Expect(response.StatusCode).To(Equal(http.StatusCreated))
				Expect(response).To(IncludeHeaderEntries(map[string]string{
					"Content-Type": "application/json",
				}))

				var presented atc.WorkerArtifact
				Expect(json.NewDecoder(response.Body).Decode(&presented)).To(Succeed())
				Expect(presented.ID).To(BeNumerically(">", 0))
				Expect(presented.Name).To(BeEmpty())
				Expect(presented.BuildID).To(BeZero())
				Expect(presented.CreatedAt).To(BeNumerically(">", 0))

				var volumesAfter, artifactsAfter int
				Expect(realdb.Conn.QueryRow(
					`SELECT count(*) FROM volumes WHERE team_id = $1`, team.ID(),
				).Scan(&volumesAfter)).To(Succeed())
				Expect(realdb.Conn.QueryRow(
					`SELECT count(*) FROM worker_artifacts`,
				).Scan(&artifactsAfter)).To(Succeed())
				Expect(volumesAfter).To(Equal(volumesBefore + 1))
				Expect(artifactsAfter).To(Equal(artifactsBefore + 1))

				created, found, err := team.FindVolumeForWorkerArtifact(presented.ID)
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(created.TeamID()).To(Equal(team.ID()))

				runtimeVolume, found := workerRuntime.volumeByHandle(created.Handle())
				Expect(found).To(BeTrue())
				volume, ok := runtimeVolume.(*runtimetest.Volume)
				Expect(ok).To(BeTrue())
				Expect(volume.Content).To(Equal(tarContents))
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
