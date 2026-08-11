package pipelineserver_test

import (
	"net/http"
	"net/http/httptest"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/pipelineserver"
	"github.com/concourse/concourse/atc/db"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gstruct"
)

var _ = Describe("Unpause Handler", func() {
	var (
		fakeLogger *lagertest.TestLogger
		server     *pipelineserver.Server
		recorder   *httptest.ResponseRecorder
		request    *http.Request
	)

	BeforeEach(func() {
		fakeLogger = lagertest.NewTestLogger("test")
		// A real teamFactory, not nil: the success path calls
		// NotifyResourceScanner() (unpause.go:23), which the old fake answered
		// with a silent nil -- so the previous specs never proved the scanner is
		// notified at all. The pipelineFactory really is unused here.
		server = pipelineserver.NewServer(fakeLogger, teamFactory, nil, "")
		recorder = httptest.NewRecorder()
		request = httptest.NewRequest("PUT", "http://example.com", nil)
	})

	It("unpauses the pipeline", func() {
		pipeline := createPipeline(createTeam("some-team"), "some-pipeline")
		Expect(pipeline.Pause("some-user")).To(Succeed())

		server.UnpausePipeline(pipeline).ServeHTTP(recorder, request)

		Expect(recorder.Code).To(Equal(http.StatusOK))

		reloaded, err := pipeline.Reload()
		Expect(err).NotTo(HaveOccurred())
		Expect(reloaded).To(BeTrue())
		Expect(pipeline.Paused()).To(BeFalse(), "the pipeline should actually be unpaused")
	})

	It("notifies the lidar scanner", func() {
		pipeline := createPipeline(createTeam("some-team"), "some-pipeline")
		Expect(pipeline.Pause("some-user")).To(Succeed())

		signal, err := dbConn.Bus().ListenSignal(atc.ComponentLidarScanner)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			Expect(dbConn.Bus().UnlistenSignal(atc.ComponentLidarScanner, signal)).To(Succeed())
		})

		server.UnpausePipeline(pipeline).ServeHTTP(recorder, request)

		Expect(recorder.Code).To(Equal(http.StatusOK))
		Eventually(signal.C()).Should(Receive())
	})

	Context("when there is a database error", func() {
		// A closed connection is a failure the database produces on demand. The
		// pipeline is built over a second connection because the suite asserts
		// its own conn closes cleanly in AfterEach.
		var doomedPipeline db.Pipeline

		BeforeEach(func() {
			doomed := postgresRunner.OpenConn()
			team, err := db.NewTeamFactory(doomed, lockFactory).CreateTeam(atc.Team{Name: "doomed-team"})
			Expect(err).NotTo(HaveOccurred())
			doomedPipeline, _, err = team.SavePipeline(
				atc.PipelineRef{Name: "doomed-pipeline"},
				atc.Config{Jobs: atc.JobConfigs{{Name: "some-job"}}},
				db.ConfigVersion(0), false,
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(doomed.Close()).To(Succeed())
		})

		It("logs the error against the right action", func() {
			server.UnpausePipeline(doomedPipeline).ServeHTTP(recorder, request)

			// The real logger nests the handler's session, which the old
			// FakeLogger flattened away with SessionReturns(fakeLogger). The
			// session name is part of what makes the log findable in production,
			// so assert on the whole path.
			Expect(fakeLogger.Logs()).To(ContainElement(gstruct.MatchFields(gstruct.IgnoreExtras, gstruct.Fields{
				"Message":  Equal("test.unpause-pipeline.failed-to-unpause-pipeline"),
				"LogLevel": Equal(lager.ERROR),
			})))
		})

		It("returns a 500 status code", func() {
			server.UnpausePipeline(doomedPipeline).ServeHTTP(recorder, request)

			Expect(recorder.Code).To(Equal(http.StatusInternalServerError))
		})
	})
})
