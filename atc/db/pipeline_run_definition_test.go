package db_test

import (
	"context"
	"strings"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("A Run's retained definition attestation", func() {
	var (
		factory db.PipelineRunFactory
		run     db.PipelineRun
	)

	BeforeEach(func() {
		template, _, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: "attested"}, atc.Config{
			Template: true,
			Params:   []atc.ParamSchema{{Name: "message", Type: atc.ParamTypeString, Default: "hello"}},
			Jobs: atc.JobConfigs{{Name: "entry", PlanSequence: []atc.Step{
				{Config: &atc.TaskStep{Name: "say", Config: &atc.TaskConfig{
					Platform: "linux", Run: atc.TaskRunConfig{Path: "echo", Args: []string{"((message))"}},
				}}},
			}}},
		}, 0, false)
		Expect(err).NotTo(HaveOccurred())
		factory = db.NewPipelineRunFactory(dbConn, lockFactory)
		tx, err := dbConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		defer db.Rollback(tx)
		creation, err := factory.CreateRunInTx(context.Background(), tx, template, db.RunParams{}, "creator", db.RunCreationOpts{})
		Expect(err).NotTo(HaveOccurred())
		Expect(tx.Commit()).To(Succeed())
		run = creation.Run

		_, found, err := factory.Definition(run.ID())
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
	})

	// rewrite replaces the stored definition with the decrypted one changed
	// by change, stored under seal: a writer that holds the key, or that writes
	// a row without a nonce, which Decrypt reads as plaintext.
	rewrite := func(seal func([]byte) (string, *string)) {
		GinkgoHelper()
		var config string
		var nonce *string
		Expect(dbConn.QueryRow(`SELECT config, nonce FROM pipeline_run_definitions WHERE run_id = $1`, run.ID()).Scan(&config, &nonce)).To(Succeed())
		Expect(nonce).NotTo(BeNil(), "the definition is stored encrypted")
		payload, err := dbConn.EncryptionStrategy().Decrypt(config, nonce)
		Expect(err).NotTo(HaveOccurred())
		changed := strings.Replace(string(payload), "hello", "corrupted", 1)
		Expect(changed).NotTo(Equal(string(payload)))
		config, nonce = seal([]byte(changed))
		_, err = dbConn.Exec(`UPDATE pipeline_run_definitions SET config = $2, nonce = $3 WHERE run_id = $1`, run.ID(), config, nonce)
		Expect(err).NotTo(HaveOccurred())
	}

	It("refuses a changed definition re-encrypted under the key", func() {
		rewrite(func(payload []byte) (string, *string) {
			config, nonce, err := dbConn.EncryptionStrategy().Encrypt(payload)
			Expect(err).NotTo(HaveOccurred())
			return config, nonce
		})
		_, _, err := factory.Definition(run.ID())
		Expect(err).To(MatchError(ContainSubstring("attestation mismatch")))
	})

	It("refuses a changed definition written as plaintext with no nonce", func() {
		rewrite(func(payload []byte) (string, *string) { return string(payload), nil })
		_, _, err := factory.Definition(run.ID())
		Expect(err).To(MatchError(ContainSubstring("attestation mismatch")))
	})

	It("refuses altered ciphertext", func() {
		_, err := dbConn.Exec(`UPDATE pipeline_run_definitions SET config = overlay(config placing (CASE WHEN substr(config, 1, 1) = '0' THEN '1' ELSE '0' END) from 1 for 1) WHERE run_id = $1`, run.ID())
		Expect(err).NotTo(HaveOccurred())
		_, _, err = factory.Definition(run.ID())
		Expect(err).To(HaveOccurred())
	})
})
