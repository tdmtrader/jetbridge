package main_test

import (
	"net/http"
	"os/exec"
	"strconv"

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
			"--postgres-port", strconv.Itoa(5433+GinkgoParallelProcess()),
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
		ginkgomon.Interrupt(concourseProcess)
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
