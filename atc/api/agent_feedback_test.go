package api_test

import (
	"io"
	"net/http"
	"strings"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("agent feedback routes", func() {
	BeforeEach(func() {
		fakeAccess.IsAuthenticatedReturns(true)
		fakeAccess.IsAuthorizedReturns(true)
		fakeAccess.UserInfoReturns(atc.UserInfo{DisplayUserId: "canonical-reviewer"})
		fakeAccess.ClaimsReturns(accessor.Claims{PreferredUsername: "mutable-reviewer"})
		dbTeam.NameReturns("research")
	})

	It("persists feedback in the requested non-main team", func() {
		request, err := http.NewRequest(
			http.MethodPost,
			server.URL+"/api/v1/teams/research/agent/feedback",
			strings.NewReader(`{"review_snapshot_id":"9007199254740993","finding_id":"ISS-1","verdict":"accurate","reviewer":"forged"}`),
		)
		Expect(err).NotTo(HaveOccurred())
		request.Header.Set("Content-Type", "application/json")

		response, err := client.Do(request)
		Expect(err).NotTo(HaveOccurred())
		defer response.Body.Close()
		Expect(response.StatusCode).To(Equal(http.StatusCreated))
		Expect(fakeAccess.IsAuthorizedArgsForCall(0)).To(Equal("research"))
		Expect(dbTeamFactory.FindTeamArgsForCall(0)).To(Equal("research"))

		stored, err := apiFeedbackStore.GetByReviewSnapshot(snapshot.SnapshotID(9007199254740993), "research")
		Expect(err).NotTo(HaveOccurred())
		Expect(stored).To(HaveLen(1))
		Expect(stored[0].Reviewer).To(Equal("canonical-reviewer"))
	})

	It("rejects a blank canonical identity before persistence", func() {
		fakeAccess.UserInfoReturns(atc.UserInfo{})
		request, err := http.NewRequest(
			http.MethodPost,
			server.URL+"/api/v1/teams/research/agent/feedback",
			strings.NewReader(`{"review_snapshot_id":"9007199254740993","finding_id":"ISS-1","verdict":"accurate","reviewer":"forged"}`),
		)
		Expect(err).NotTo(HaveOccurred())
		request.Header.Set("Content-Type", "application/json")

		response, err := client.Do(request)
		Expect(err).NotTo(HaveOccurred())
		defer response.Body.Close()
		Expect(response.StatusCode).To(Equal(http.StatusForbidden))

		stored, err := apiFeedbackStore.GetByReviewSnapshot(snapshot.SnapshotID(9007199254740993), "research")
		Expect(err).NotTo(HaveOccurred())
		Expect(stored).To(BeEmpty())
	})

	It("fails closed when the requested team does not resolve", func() {
		dbTeamFactory.FindTeamReturns(nil, false, nil)
		request, err := http.NewRequest(
			http.MethodPost,
			server.URL+"/api/v1/teams/missing/agent/feedback",
			strings.NewReader(`{"review_snapshot_id":"9007199254740993","finding_id":"ISS-1","verdict":"accurate","reviewer":"reviewer"}`),
		)
		Expect(err).NotTo(HaveOccurred())
		request.Header.Set("Content-Type", "application/json")

		response, err := client.Do(request)
		Expect(err).NotTo(HaveOccurred())
		defer response.Body.Close()
		_, _ = io.Copy(io.Discard, response.Body)
		Expect(response.StatusCode).To(Equal(http.StatusNotFound))

		stored, err := apiFeedbackStore.GetByReviewSnapshot(snapshot.SnapshotID(9007199254740993), "missing")
		Expect(err).NotTo(HaveOccurred())
		Expect(stored).To(BeEmpty())
	})
})
