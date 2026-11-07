package integration_test

import (
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/concourse/concourse/atc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
	"github.com/onsi/gomega/ghttp"
	"github.com/tedsuo/rata"
)

var _ = Describe("landing-queue and resume-landing-queue", func() {
	path := func(route string) string {
		GinkgoHelper()
		p, err := atc.Routes.CreatePathForRoute(route, rata.Params{"team_name": "main", "queue_name": "core"})
		Expect(err).NotTo(HaveOccurred())
		return p
	}
	fly := func(args ...string) *gexec.Session {
		GinkgoHelper()
		sess, err := gexec.Start(exec.Command(flyPath, append([]string{"-t", targetName}, args...)...), GinkgoWriter, GinkgoWriter)
		Expect(err).NotTo(HaveOccurred())
		<-sess.Exited
		return sess
	}

	It("landing-queue shows a paused queue, how to resume it, and each in-flight entry's gate Runs and verdicts", func() {
		atcServer.AppendHandlers(ghttp.CombineHandlers(
			ghttp.VerifyRequest("GET", path(atc.GetLandingQueue)),
			ghttp.RespondWithJSONEncoded(http.StatusOK, atc.LandingQueueStatus{
				Name: "core",
				Config: atc.LandingQueueConfig{Repository: "https://github.com/tdmtrader/jetbridge.git", Trunk: "core", Compose: "landing-compose", Land: "landing-land",
					Gates: []atc.LandingQueueGate{{Template: "landing-gate-tests"}, {Template: "landing-gate-review"}}},
				Paused:      true,
				PauseReason: "gate landing-gate-tests Run #2 errored; no verdict after one retry, so the queue is paused",
				Entries: []atc.LandingEntry{
					{ID: "fix-0", Commit: strings.Repeat("c", 40), State: atc.LandingEntryEjected, SubmittedAt: time.Now(), ComposeRun: 1,
						SettleReason: "gate landing-gate-review Run #1 failed",
						Gates:        []atc.LandingGate{{Template: "landing-gate-review", Run: 1, Verdict: atc.LandingGateFailed, Attempts: 1}}},
					{ID: "fix-1", Commit: strings.Repeat("a", 40), State: atc.LandingEntryInFlight, SubmittedAt: time.Now(), ComposeRun: 2,
						Gates: []atc.LandingGate{
							{Template: "landing-gate-tests", Run: 2, Verdict: atc.LandingGateNoVerdict, Attempts: 2, LastError: "gate landing-gate-tests Run #2 errored"},
							{Template: "landing-gate-review", Run: 2, Verdict: atc.LandingGatePassed, Attempts: 1},
						}},
				},
			}),
		))
		sess := fly("landing-queue", "-q", "core")
		Expect(sess.ExitCode()).To(Equal(0))
		Expect(sess.Out).To(gbytes.Say(`landing queue core: core on https://github.com/tdmtrader/jetbridge.git`))
		Expect(sess.Out).To(gbytes.Say(`gates: landing-gate-tests, landing-gate-review`))
		Expect(sess.Out).To(gbytes.Say(`paused: gate landing-gate-tests Run #2 errored; no verdict after one retry, so the queue is paused`))
		Expect(sess.Out).To(gbytes.Say(`resume with: fly resume-landing-queue -q core`))
		Expect(sess.Out).To(gbytes.Say(`fix-0\s+cccccccccccc\s+ejected\s+#1\s+-\s+gate landing-gate-review Run #1 failed`))
		Expect(sess.Out).To(gbytes.Say(`fix-1\s+aaaaaaaaaaaa\s+in_flight\s+#2\s+-`))
		Expect(sess.Out).To(gbytes.Say(`entry\s+gate\s+run\s+verdict\s+attempts\s+last`))
		Expect(sess.Out).To(gbytes.Say(`fix-1\s+landing-gate-tests\s+#2\s+no_verdict\s+2\s+gate landing-gate-tests Run #2 errored`))
		Expect(sess.Out).To(gbytes.Say(`fix-1\s+landing-gate-review\s+#2\s+passed\s+1`))
		Expect(string(sess.Out.Contents())).NotTo(MatchRegexp(`fix-0\s+landing-gate-review`), "only entries in flight list their gates")
	})

	It("landing-queue --json carries the pause and the gates", func() {
		atcServer.AppendHandlers(ghttp.CombineHandlers(
			ghttp.VerifyRequest("GET", path(atc.GetLandingQueue)),
			ghttp.RespondWithJSONEncoded(http.StatusOK, atc.LandingQueueStatus{Name: "core", Paused: true, PauseReason: "held",
				Entries: []atc.LandingEntry{{ID: "fix-1", Commit: strings.Repeat("a", 40), State: atc.LandingEntryInFlight,
					Gates: []atc.LandingGate{{Template: "landing-gate-tests", Run: 4, Verdict: atc.LandingGatePending, Attempts: 1}}}}}),
		))
		sess := fly("landing-queue", "-q", "core", "--json")
		Expect(sess.ExitCode()).To(Equal(0))
		Expect(sess.Out).To(gbytes.Say(`"paused": true`))
		Expect(sess.Out).To(gbytes.Say(`"pause_reason": "held"`))
		Expect(sess.Out).To(gbytes.Say(`"template": "landing-gate-tests"`))
		Expect(sess.Out).To(gbytes.Say(`"run": 4`))
		Expect(sess.Out).To(gbytes.Say(`"verdict": "pending"`))
	})

	It("resume-landing-queue resumes a paused queue", func() {
		atcServer.AppendHandlers(ghttp.CombineHandlers(
			ghttp.VerifyRequest("PUT", path(atc.ResumeLandingQueue)),
			ghttp.RespondWith(http.StatusOK, nil),
		))
		sess := fly("resume-landing-queue", "-q", "core")
		Expect(sess.ExitCode()).To(Equal(0))
		Expect(sess.Out).To(gbytes.Say(`resumed landing queue "core"`))
	})

	It("resume-landing-queue says so when the queue is not paused", func() {
		atcServer.AppendHandlers(ghttp.CombineHandlers(
			ghttp.VerifyRequest("PUT", path(atc.ResumeLandingQueue)),
			ghttp.RespondWith(http.StatusConflict, `landing queue "core" is not paused`),
		))
		sess := fly("resume-landing-queue", "-q", "core")
		Expect(sess.ExitCode()).To(Equal(0))
		Expect(sess.Out).To(gbytes.Say(`landing queue "core" is not paused`))
	})

	It("resume-landing-queue fails naming a queue that is not set", func() {
		atcServer.AppendHandlers(ghttp.CombineHandlers(
			ghttp.VerifyRequest("PUT", path(atc.ResumeLandingQueue)),
			ghttp.RespondWith(http.StatusNotFound, ""),
		))
		sess := fly("resume-landing-queue", "-q", "core")
		Expect(sess.ExitCode()).To(Equal(1))
		Expect(sess.Err).To(gbytes.Say(`landing queue "core" not found`))
	})
})
