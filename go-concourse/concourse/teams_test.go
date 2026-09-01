package concourse_test

import (
	"net/http"

	"github.com/concourse/concourse/go-concourse/concourse"
	"github.com/concourse/concourse/go-concourse/concourse/internal"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/ghttp"
)

var _ = Describe("ATC Handler Teams", func() {
	Describe("FindTeam", func() {
		teamName := "myTeam"
		expectedURL := "/api/v1/teams/myTeam"

		Context("when an unhandled HTTP status code is returned", func() {
			BeforeEach(func() {
				atcServer.AppendHandlers(
					ghttp.CombineHandlers(
						ghttp.VerifyRequest("GET", expectedURL),
						ghttp.RespondWith(http.StatusInternalServerError, "server issue"),
					),
				)
			})
			It("returns an UnexpectedResponseError", func() {
				_, err := client.FindTeam(teamName)
				Expect(err).To(Equal(internal.UnexpectedResponseError{
					StatusCode: http.StatusInternalServerError,
					Status:     "500 Internal Server Error",
					Body:       "server issue",
				}))
			})
		})
	})
	Describe("Destroy", func() {
		var (
			expectedURL string
			err         error
		)

		BeforeEach(func() {
			expectedURL = "/api/v1/teams/enron"
			team = client.Team("not-super-important")
		})

		JustBeforeEach(func() {
			err = team.DestroyTeam("enron")
		})

		Context("when passed a team that you can't delete", func() {
			BeforeEach(func() {
				atcServer.AppendHandlers(
					ghttp.CombineHandlers(
						ghttp.VerifyRequest("DELETE", expectedURL),
						ghttp.RespondWith(http.StatusForbidden, nil),
					),
				)
			})

			It("returns back true for created, and false for updated", func() {
				Expect(err).To(Equal(concourse.ErrDestroyRefused))
			})
		})

		Context("when the server blows up", func() {
			BeforeEach(func() {
				atcServer.AppendHandlers(
					ghttp.CombineHandlers(
						ghttp.VerifyRequest("DELETE", expectedURL),
						ghttp.RespondWith(http.StatusInternalServerError, nil),
					),
				)
			})

			It("returns back false for created, and true for updated", func() {
				Expect(err).To(HaveOccurred())
				Expect(err).NotTo(Equal(concourse.ErrDestroyRefused))
			})
		})
	})
})
