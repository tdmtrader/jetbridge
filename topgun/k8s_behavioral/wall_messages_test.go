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
		Expect(sess.ExitCode()).To(Equal(0), string(sess.Err.Contents()))

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
