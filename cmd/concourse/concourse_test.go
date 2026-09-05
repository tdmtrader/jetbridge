package main_test

import (
	"net/http"
	"os/exec"
	"strconv"
	"time"

	"github.com/concourse/concourse/atc/postgresrunner"
	"github.com/onsi/gomega/gbytes"
	"github.com/tedsuo/ifrit"
	ginkgomon "github.com/tedsuo/ifrit/ginkgomon_v2"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Web Command", func() {

	var (
		concourseCommand *exec.Cmd
		concourseProcess ifrit.Process
		concourseRunner  *ginkgomon.Runner
		postgresRunner   postgresrunner.Runner
		dbProcess        ifrit.Process
	)

	BeforeEach(func() {
		postgresrunner.InitializeRunnerForGinkgo(&postgresRunner, &dbProcess)

		postgresRunner.CreateEmptyTestDB()

		concourseCommand = exec.Command(
			concoursePath,
			"web",
			"--postgres-user", "postgres",
			"--postgres-database", "testdb",
			"--postgres-port", strconv.Itoa(postgresRunner.Port),
			"--main-team-local-user", "test",
			"--add-local-user", "test:test",
			"--debug-bind-port", strconv.Itoa(8000+GinkgoParallelProcess()),
			"--bind-port", strconv.Itoa(8080+GinkgoParallelProcess()),
			"--client-id", "client-id",
			"--client-secret", "client-secret",
		)
	})

	JustBeforeEach(func() {
		concourseRunner = ginkgomon.New(ginkgomon.Config{
			Command:       concourseCommand,
			Name:          "web",
			AnsiColorCode: "32m",
		})

		concourseProcess = ifrit.Background(concourseRunner)

		// workaround to avoid panic due to registering http handlers multiple times
		http.DefaultServeMux = new(http.ServeMux)
	})

	AfterEach(func() {
		// ginkgomon's default gives the web one second to exit after
		// SIGINT. A web mid-startup is still wiring its DB pool and
		// components, and on a loaded CI node that shutdown took longer
		// than a second twice in a row (unit-tests #970, #971), failing
		// the suite in AfterEach with nothing wrong in the test itself.
		ginkgomon.Interrupt(concourseProcess, 30*time.Second)
		<-concourseProcess.Wait()
		postgresRunner.DropTestDB()

		postgresrunner.FinalizeRunnerForGinkgo(&postgresRunner, &dbProcess)
	})

	It("starts atc", func() {
		Eventually(concourseRunner.Buffer(), "30s", "2s").Should(gbytes.Say("atc.listening"))
	})

	Context("when CONCOURSE_CONCURRENT_REQUEST_LIMIT is invalid", func() {
		BeforeEach(func() {
			concourseCommand.Env = append(concourseCommand.Env, "CONCOURSE_CONCURRENT_REQUEST_LIMIT=InvalidAction:0")
		})

		It("prints an error and exits", func() {
			Eventually(concourseRunner.Err()).Should(gbytes.Say("'InvalidAction' is not a valid action"))
		})
	})
})
