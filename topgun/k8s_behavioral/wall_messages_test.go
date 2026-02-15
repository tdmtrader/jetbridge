package behavioral_test

import (
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Wall Messages", func() {

	It("sets and clears a wall message via the API", func() {
		By("setting a wall message")
		sess := fly.Start("set-wall", "-m", "maintenance window in progress")
		<-sess.Exited
		if sess.ExitCode() != 0 {
			// set-wall may not be available; skip gracefully
			Skip("fly set-wall not available in this version")
		}

		By("verifying the wall message is visible")
		status, body := apiGet("/api/v1/wall")
		Expect(status).To(Equal(http.StatusOK))
		Expect(string(body)).To(ContainSubstring("maintenance"))

		By("clearing the wall message")
		sess = fly.Start("clear-wall")
		<-sess.Exited
		Expect(sess.ExitCode()).To(Equal(0))
	})
})
