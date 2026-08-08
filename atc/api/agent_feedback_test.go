package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/concourse/concourse/agent/api/reviews"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const agentFeedbackReviewSnapshotID = snapshot.SnapshotID(9007199254740993)

var _ = Describe("agent feedback routes", func() {
	var (
		realdb          *realDB
		deps            apiDBDeps
		research        db.Team
		feedbackFactory db.AgentFeedbackFactory
		productionID    snapshot.DatabaseID
		server          *httptest.Server
	)

	BeforeEach(func() {
		fakeAccess.IsAuthenticatedReturns(true)
		fakeAccess.IsAuthorizedReturns(true)
		fakeAccess.UserInfoReturns(atc.UserInfo{DisplayUserId: "canonical-reviewer"})
		fakeAccess.ClaimsReturns(accessor.Claims{PreferredUsername: "mutable-reviewer"})

		realdb = useRealDB()
		deps = realdb.Deps

		var err error
		research, err = deps.teamFactory.CreateTeam(atc.Team{Name: "research"})
		Expect(err).NotTo(HaveOccurred())
		Expect(research.ID()).NotTo(Equal(realdb.Main.ID()))
		Expect(research.Name()).To(Equal("research"))
		Expect(realdb.Main.Name()).To(Equal(atc.DefaultTeamName))

		feedbackFactory = db.NewAgentFeedbackFactory(realdb.Conn)
		deps.feedbackStore = feedbackFactory
		productionID = seedAgentFeedbackReviewIdentity(realdb, research, agentFeedbackReviewSnapshotID)

		// Finalize all endpoint-local dependencies before constructing the
		// production router. The handler and fixture readers now share this
		// spec's unique database clone.
		realdb.Deps = deps
		server = realdb.Serve()
	})

	It("persists feedback in the requested non-main team", func() {
		submit := func() *http.Response {
			GinkgoHelper()

			request, err := http.NewRequest(
				http.MethodPost,
				server.URL+"/api/v1/teams/research/agent/feedback",
				strings.NewReader(`{"review_snapshot_id":"9007199254740993","finding_id":"ISS-1","finding_type":"proven_issue","finding_snapshot":{"severity":"high"},"verdict":"accurate","confidence":0.9,"notes":"durable finding","reviewer":"forged","source":"interactive"}`),
			)
			Expect(err).NotTo(HaveOccurred())
			request.Header.Set("Content-Type", "application/json")

			response, err := client.Do(request)
			Expect(err).NotTo(HaveOccurred())
			return response
		}

		// The router is already bound to the real factory, but the review has
		// not been projected yet. The database authorization join must reject
		// this otherwise-valid request instead of silently accepting it in a
		// memory store.
		response := submit()
		_, _ = io.Copy(io.Discard, response.Body)
		Expect(response.Body.Close()).To(Succeed())
		Expect(response.StatusCode).To(Equal(http.StatusNotFound))
		expectAgentFeedbackRowCount(realdb, 0)

		projectAgentFeedbackReview(realdb, research, agentFeedbackReviewSnapshotID, productionID)

		response = submit()
		defer response.Body.Close()
		Expect(response.StatusCode).To(Equal(http.StatusCreated))
		Expect(fakeAccess.IsAuthorizedArgsForCall(0)).To(Equal("research"))

		// Read through a newly constructed database factory rather than the
		// object passed to the handler, proving the canonical row is durable.
		stored, err := db.NewAgentFeedbackFactory(realdb.Conn).GetByReviewSnapshot(
			agentFeedbackReviewSnapshotID,
			research.Name(),
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(stored).To(HaveLen(1))
		record := stored[0]
		Expect(record.ReviewSnapshotID).To(Equal(agentFeedbackReviewSnapshotID))
		Expect(record.ReviewTeamName).To(Equal(research.Name()))
		Expect(record.FindingID).To(Equal("ISS-1"))
		Expect(record.FindingType).To(Equal("proven_issue"))
		Expect(record.FindingSnapshot).To(MatchJSON(`{"severity":"high"}`))
		Expect(record.Verdict).To(Equal("accurate"))
		Expect(record.Confidence).To(Equal(0.9))
		Expect(record.Notes).To(Equal("durable finding"))
		Expect(record.Reviewer).To(Equal("canonical-reviewer"))
		Expect(record.Reviewer).NotTo(Equal("forged"))
		Expect(record.Source).To(Equal("interactive"))

		mainRows, err := feedbackFactory.GetByReviewSnapshot(agentFeedbackReviewSnapshotID, realdb.Main.Name())
		Expect(err).NotTo(HaveOccurred())
		Expect(mainRows).To(BeEmpty())
		wrongSnapshotRows, err := feedbackFactory.GetByReviewSnapshot(agentFeedbackReviewSnapshotID+1, research.Name())
		Expect(err).NotTo(HaveOccurred())
		Expect(wrongSnapshotRows).To(BeEmpty())
		expectAgentFeedbackRowCount(realdb, 1)
	})

	It("rejects a blank canonical identity before persistence", func() {
		projectAgentFeedbackReview(realdb, research, agentFeedbackReviewSnapshotID, productionID)

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

		stored, err := feedbackFactory.GetByReviewSnapshot(agentFeedbackReviewSnapshotID, research.Name())
		Expect(err).NotTo(HaveOccurred())
		Expect(stored).To(BeEmpty())
		expectAgentFeedbackRowCount(realdb, 0)
	})

	It("fails closed when the requested team does not resolve", func() {
		projectAgentFeedbackReview(realdb, research, agentFeedbackReviewSnapshotID, productionID)

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

		stored, err := feedbackFactory.GetByReviewSnapshot(agentFeedbackReviewSnapshotID, research.Name())
		Expect(err).NotTo(HaveOccurred())
		Expect(stored).To(BeEmpty())
		expectAgentFeedbackRowCount(realdb, 0)
	})
})

func seedAgentFeedbackReviewIdentity(realdb *realDB, team db.Team, id snapshot.SnapshotID) snapshot.DatabaseID {
	GinkgoHelper()

	_, err := realdb.Conn.Exec(`
		INSERT INTO agent_snapshots
			(id, team_id, type_name, type_version, digest, byte_size, file_count, representation, content_state)
		VALUES ($1, $2, 'review', 1, $3, 1024, 1, 'filesystem-tree-v1', 'available')
	`, int64(id), team.ID(), "sha256:"+strings.Repeat("f", 64))
	Expect(err).NotTo(HaveOccurred())

	var productionID snapshot.DatabaseID
	err = realdb.Conn.QueryRow(`
		INSERT INTO agent_snapshot_productions
			(snapshot_id, occurrence_kind, team_id, team_name, created_by, upload_idempotency_key)
		VALUES ($1, 'upload', $2, $3, 'feedback-fixture', 'api-feedback-review')
		RETURNING id
	`, int64(id), team.ID(), team.Name()).Scan(&productionID)
	Expect(err).NotTo(HaveOccurred())
	return productionID
}

func projectAgentFeedbackReview(
	realdb *realDB,
	team db.Team,
	id snapshot.SnapshotID,
	productionID snapshot.DatabaseID,
) {
	GinkgoHelper()

	Expect(db.NewAgentReviewsFactory(realdb.Conn).UpsertReviewProjection(
		context.Background(),
		&reviews.StoredReview{
			SnapshotID:     id,
			ProductionID:   &productionID,
			TeamName:       team.Name(),
			Conclusion:     "accept",
			Summary:        "feedback fixture review",
			SeverityCounts: map[string]int{"high": 1},
			Review:         json.RawMessage(`{"record_version":"1.0.0"}`),
			SubmittedBy:    "feedback-fixture",
		},
	)).To(Succeed())
}

func expectAgentFeedbackRowCount(realdb *realDB, expected int) {
	GinkgoHelper()

	var count int
	Expect(realdb.Conn.QueryRow(`SELECT count(*) FROM agent_feedback`).Scan(&count)).To(Succeed())
	Expect(count).To(Equal(expected))
}
