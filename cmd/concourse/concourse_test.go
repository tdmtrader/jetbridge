package main_test

import (
	"net/http"
	"os/exec"
	"strconv"

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
	)

	BeforeEach(func() {
		postgresRunner.CreateEmptyTestDB()
		info := postgresRunner.ConnectionInfo()

		concourseCommand = exec.Command(
			concoursePath,
			"web",
			"--postgres-host", info.Host,
			"--postgres-port", strconv.Itoa(int(info.Port)),
			"--postgres-user", info.User,
			"--postgres-password", info.Password,
			"--postgres-database", info.Database,
			"--postgres-sslmode", info.SSLMode,
			"--main-team-local-user", "test",
			"--add-local-user", "test:test",
			"--debug-bind-port", strconv.Itoa(8000+GinkgoParallelProcess()),
			"--bind-port", strconv.Itoa(8080+GinkgoParallelProcess()),
			"--client-id", "client-id",
			"--client-secret", "client-secret",
		)

		database := commandFlagValue(concourseCommand.Args, "--postgres-database")
		Expect(database).To(Equal(postgresRunner.DatabaseName()))
		Expect(database).To(HavePrefix("cc_db_"))
		Expect(commandFlagValue(concourseCommand.Args, "--postgres-host")).To(Equal(info.Host))
		Expect(commandFlagValue(concourseCommand.Args, "--postgres-port")).To(Equal(strconv.Itoa(int(info.Port))))
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
	})

	It("starts atc", func() {
		Eventually(concourseRunner.Buffer(), "30s", "2s").Should(gbytes.Say("atc.listening"))
	})

	Context("when CONCOURSE_CONCURRENT_REQUEST_LIMIT is invalid", func() {
		BeforeEach(func() {
			concourseCommand.Env = append(concourseCommand.Env, "CONCOURSE_CONCURRENT_REQUEST_LIMIT=InvalidAction:0")
		})

		It("prints an error and exits", func() {
			// Explicit timeout: Gomega's default is 1s, which is not enough for
			// a freshly-exec'd binary to reach its flag validation on a loaded
			// machine. It passes alone and fails under a full parallel
			// make test-unit, which reads as a real regression every time.
			// Matches the 30s the sibling spec above already uses.
			Eventually(concourseRunner.Err(), "30s", "1s").Should(gbytes.Say("'InvalidAction' is not a valid action"))
		})
	})
})

func commandFlagValue(args []string, name string) string {
	for i := range args {
		if args[i] == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}
