package api_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/hangar/output"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Pipeline Run cancellation API", func() {
	var (
		database *realDB
		run      db.PipelineRun
	)

	// The requester is read from the verified claims, which only the real
	// accessor carries, so these specs replace the suite's fake access.
	callAs := func(claims map[string]any) {
		team, found, err := database.Deps.teamFactory.FindTeam(atc.DefaultTeamName)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		fakeAccessor.CreateReturns(accessor.NewAccessor(accessor.Verification{
			HasToken: true, IsTokenValid: true, RawClaims: claims,
		}, accessor.OperatorRole, "aud", []string{"concourse-worker"}, []db.Team{team}, displayName{}), nil)
	}

	operator := func(subject string) map[string]any {
		claims := map[string]any{"name": "operator", "federated_claims": map[string]any{"connector_id": "test", "user_id": "operator"}}
		if subject != "" {
			claims["sub"] = subject
		}
		return claims
	}

	cancel := func(number int) *http.Response {
		GinkgoHelper()
		request, err := http.NewRequest(http.MethodPost, pipelineRunsURL(server, "review")+"/"+strconv.Itoa(number)+"/cancel", bytes.NewBufferString(`{"reason":"operator stop"}`))
		Expect(err).NotTo(HaveOccurred())
		request.Header.Set("Content-Type", "application/json")
		response, err := client.Do(request)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(response.Body.Close)
		return response
	}

	requester := func() sql.NullString {
		GinkgoHelper()
		var by sql.NullString
		Expect(database.Conn.QueryRow(`SELECT cancel_requested_by FROM pipeline_runs WHERE id=$1`, run.ID()).Scan(&by)).To(Succeed())
		return by
	}

	BeforeEach(func() {
		database = useRealDB()
		Expect(database.Main.UpdateProviderAuth(atc.TeamAuth{accessor.OperatorRole: {"users": {"test:operator"}}})).To(Succeed())
		template := database.SavePipeline(database.Main, "review", atc.Config{Template: true, Jobs: atc.JobConfigs{{Name: "entry"}}})
		run = createV2Run(database, template)
		server = database.Serve()
	})

	It("records the caller's verified subject as the requester", func() {
		// This fails if the requester is a display name: two principals can
		// share one, and the first writer's identity must be the verified one.
		callAs(operator("Cg9vcGVyYXRvci1zdWJqZWN0EgR0ZXN0"))

		response := cancel(run.Number())

		Expect(response.StatusCode).To(Equal(http.StatusOK))
		var body atc.CancelPipelineRunResponse
		Expect(json.NewDecoder(response.Body).Decode(&body)).To(Succeed())
		Expect(body.Outcome).To(Equal(atc.RunCancelAccepted))
		Expect(requester().String).To(Equal("Cg9vcGVyYXRvci1zdWJqZWN0EgR0ZXN0"))
	})

	It("refuses a caller subject in the reserved server namespace before Run lookup", func() {
		// The system: prefix names server requesters, such as a deadline. A
		// caller that could claim one could pass for the server in the record.
		callAs(operator("system:run-deadline"))

		Expect(cancel(run.Number()).StatusCode).To(Equal(http.StatusForbidden))
		Expect(cancel(run.Number() + 1000).StatusCode).To(Equal(http.StatusForbidden))
		Expect(requester().Valid).To(BeFalse())
	})

	It("refuses a caller without a verified subject", func() {
		callAs(operator(""))

		Expect(cancel(run.Number()).StatusCode).To(Equal(http.StatusUnauthorized))
		Expect(requester().Valid).To(BeFalse())
	})
})

// displayName is the configured display identity, which is not the subject.
type displayName struct{}

func (displayName) DisplayUserId(_, _, username, _, _ string) string { return "display:" + username }

// createV2Run opens one Run contract activation epoch and admits a v2 Run of
// template under it, which is the only contract cancellation accepts.
func createV2Run(database *realDB, template db.Pipeline) db.PipelineRun {
	GinkgoHelper()
	ctx := context.Background()
	_, err := database.Conn.Exec(`
		INSERT INTO hangar_output_activation_epochs
			(epoch_id, base_state, output_state, base_attestation, output_attestation,
			 receipt_public_key_id, receipt_key_valid_from, receipt_key_valid_until,
			 materialization_key_id, bucket_fingerprint, derived_namespace)
		VALUES (1, 'enabled', 'enabled', '{}', '{}', 'receipt-key-1',
			now() - interval '1 day', now() + interval '30 days',
			'materialize-key-1', 'gs://output-bucket', 'deployment/ns')`)
	Expect(err).NotTo(HaveOccurred())
	consumer, err := db.HangarConsumerPrefixHeld("cancel-api-test")
	Expect(err).NotTo(HaveOccurred())
	tx, err := database.Conn.Begin()
	Expect(err).NotTo(HaveOccurred())
	defer db.Rollback(tx)
	Expect(db.NewHangarOutputRepository(consumer).RecordPolicyAttestation(ctx, tx, output.PolicySnapshot{
		ProtocolVersion: output.ProtocolVersion, ActivationEpoch: 1, BucketFingerprint: "gs://output-bucket",
		Metageneration: 3, PolicyHash: "policy-hash-1", State: output.PolicySafe,
		ObservedAt: output.NewTimestamp(time.Now()),
	}, nil)).To(Succeed())
	Expect(tx.Commit()).To(Succeed())
	_, err = database.Conn.Exec(`UPDATE pipeline_run_activation SET epoch=1, admission_enabled=true WHERE singleton`)
	Expect(err).NotTo(HaveOccurred())

	tx, err = database.Conn.Begin()
	Expect(err).NotTo(HaveOccurred())
	defer db.Rollback(tx)
	creation, err := db.NewPipelineRunFactory(database.Conn, database.LockFactory).CreateRunInTx(ctx, tx, template, db.RunParams{}, "creator", db.RunCreationOpts{ActivationEpoch: 1})
	Expect(err).NotTo(HaveOccurred())
	Expect(tx.Commit()).To(Succeed())
	return creation.Run
}
