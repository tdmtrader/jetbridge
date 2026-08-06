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

var _ = Describe("Archive Handler", func() {
	var (
		fakeLogger *lagertest.TestLogger
		server     *pipelineserver.Server
		pipeline   db.Pipeline
		recorder   *httptest.ResponseRecorder
		request    *http.Request
	)

	loggedAt := func(level lager.LogLevel) gstruct.Fields {
		return gstruct.Fields{
			"Message":  Equal("test.archive-pipeline"),
			"LogLevel": Equal(level),
		}
	}

	BeforeEach(func() {
		fakeLogger = lagertest.NewTestLogger("test")
		// A real teamFactory. The pipelineFactory is genuinely unused by this
		// handler, and nil says so -- a fake would answer any future call with a
		// zero value instead of failing.
		server = pipelineserver.NewServer(fakeLogger, teamFactory, nil, "")

		pipeline = createPipeline(createTeam("some-team"), "some-pipeline")
		recorder = httptest.NewRecorder()
		request = httptest.NewRequest("PUT", "http://example.com", nil)
	})

	It("archives the pipeline and logs no error", func() {
		server.ArchivePipeline(pipeline).ServeHTTP(recorder, request)

		Expect(recorder.Code).To(Equal(http.StatusOK))

		reloaded, err := pipeline.Reload()
		Expect(err).NotTo(HaveOccurred())
		Expect(reloaded).To(BeTrue())
		Expect(pipeline.Archived()).To(BeTrue(), "the pipeline should actually be archived")

		Expect(fakeLogger.Logs()).ToNot(ContainElement(
			gstruct.MatchFields(gstruct.IgnoreExtras, loggedAt(lager.ERROR)),
		))
	})

	It("writes a debug log on every request", func() {
		server.ArchivePipeline(pipeline).ServeHTTP(recorder, request)

		Expect(fakeLogger.Logs()).To(ContainElement(
			gstruct.MatchFields(gstruct.IgnoreExtras, loggedAt(lager.DEBUG)),
		))
	})

	It("logs database errors", func() {
		// A closed connection is a failure the database produces on demand, so
		// the error path is reached without stubbing Archive(). The pipeline is
		// built over a second connection because the suite asserts its own conn
		// closes cleanly in AfterEach.
		doomed := postgresRunner.OpenConn()
		doomedTeam, err := db.NewTeamFactory(doomed, lockFactory).CreateTeam(atc.Team{Name: "doomed-team"})
		Expect(err).NotTo(HaveOccurred())
		doomedPipeline, _, err := doomedTeam.SavePipeline(
			atc.PipelineRef{Name: "doomed-pipeline"},
			atc.Config{Jobs: atc.JobConfigs{{Name: "some-job"}}},
			db.ConfigVersion(0), false,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(doomed.Close()).To(Succeed())

		server.ArchivePipeline(doomedPipeline).ServeHTTP(recorder, request)

		Expect(recorder.Code).To(Equal(http.StatusInternalServerError))
		Expect(fakeLogger.Logs()).To(ContainElement(
			gstruct.MatchFields(gstruct.IgnoreExtras, loggedAt(lager.ERROR)),
		))
	})
})
