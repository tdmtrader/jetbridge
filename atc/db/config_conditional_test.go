package db_test

import (
	"sync"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Conditional pipeline configuration", func() {
	var team db.Team
	var ref atc.PipelineRef
	BeforeEach(func() {
		var err error
		team, err = teamFactory.CreateTeam(atc.Team{Name: "conditional-team"})
		Expect(err).NotTo(HaveOccurred())
		ref = atc.PipelineRef{Name: "deploy"}
	})
	It("requires create-only or an exact existing version, including archived rows", func() {
		_, _, err := team.SavePipelineConditional(ref, atc.Config{}, 123, true)
		Expect(err).To(MatchError(db.ErrConfigPreconditionFailed))
		first, created, err := team.SavePipelineConditional(ref, atc.Config{}, 0, true)
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())
		_, _, err = team.SavePipelineConditional(ref, atc.Config{}, 0, true)
		Expect(err).To(MatchError(db.ErrConfigPreconditionFailed))
		second, created, err := team.SavePipelineConditional(ref, atc.Config{}, first.ConfigVersion(), true)
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeFalse())
		Expect(second.ConfigVersion()).To(BeNumerically(">", first.ConfigVersion()))
		_, _, err = team.SavePipelineConditional(ref, atc.Config{}, first.ConfigVersion(), true)
		Expect(err).To(MatchError(db.ErrConfigPreconditionFailed))
		Expect(second.Archive()).To(Succeed())
		_, _, err = team.SavePipelineConditional(ref, atc.Config{}, 0, true)
		Expect(err).To(MatchError(db.ErrConfigPreconditionFailed))
		// Legacy fly behavior still restores archived rows with version zero.
		_, _, err = team.SavePipeline(ref, atc.Config{}, 0, true)
		Expect(err).NotTo(HaveOccurred())
	})
	It("does not turn deletion or recreation into a successful stale update", func() {
		first, _, err := team.SavePipelineConditional(ref, atc.Config{}, 0, true)
		Expect(err).NotTo(HaveOccurred())
		Expect(first.Destroy()).To(Succeed())
		_, _, err = team.SavePipelineConditional(ref, atc.Config{}, first.ConfigVersion(), true)
		Expect(err).To(MatchError(db.ErrConfigPreconditionFailed))
		next, _, err := team.SavePipelineConditional(ref, atc.Config{}, 0, true)
		Expect(err).NotTo(HaveOccurred())
		_, _, err = team.SavePipelineConditional(ref, atc.Config{}, first.ConfigVersion(), true)
		Expect(err).To(MatchError(db.ErrConfigPreconditionFailed))
		Expect(next.ConfigVersion()).NotTo(Equal(first.ConfigVersion()))
	})
	It("has one winner among simultaneous creates and updates", func() {
		dbConn.SetMaxOpenConns(4)
		race := func(version db.ConfigVersion) db.Pipeline {
			type result struct {
				pipeline db.Pipeline
				err      error
			}
			results := make(chan result, 2)
			start := make(chan struct{})
			var wg sync.WaitGroup
			for i := 0; i < 2; i++ {
				wg.Add(1)
				go func() {
					defer GinkgoRecover()
					defer wg.Done()
					<-start
					p, _, err := team.SavePipelineConditional(ref, atc.Config{}, version, true)
					results <- result{p, err}
				}()
			}
			close(start)
			wg.Wait()
			close(results)
			var winner db.Pipeline
			losers := 0
			for r := range results {
				if r.err == nil {
					Expect(winner).To(BeNil())
					winner = r.pipeline
				} else {
					Expect(r.err).To(MatchError(db.ErrConfigPreconditionFailed))
					losers++
				}
			}
			Expect(winner).NotTo(BeNil())
			Expect(losers).To(Equal(1))
			return winner
		}
		first := race(0)
		second := race(first.ConfigVersion())
		Expect(second.ConfigVersion()).To(BeNumerically(">", first.ConfigVersion()))
	})
	It("reports a competing creation that commits first as a conflict", func() {
		dbConn.SetMaxOpenConns(4)
		competitor, err := dbConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		defer competitor.Rollback()
		_, err = competitor.Exec("INSERT INTO pipelines (name, team_id, secondary_ordering) VALUES ($1, $2, 1)", ref.Name, team.ID())
		Expect(err).NotTo(HaveOccurred())
		result := make(chan error, 1)
		go func() {
			defer GinkgoRecover()
			_, _, err := team.SavePipelineConditional(ref, atc.Config{}, 0, true)
			result <- err
		}()
		// The strict create sees no committed row, then blocks on the unique index.
		Eventually(func() int {
			var waiting int
			Expect(dbConn.QueryRow("SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock'").Scan(&waiting)).To(Succeed())
			return waiting
		}, 10*time.Second).Should(BeNumerically(">=", 1))
		Expect(competitor.Commit()).To(Succeed())
		Eventually(result, 10*time.Second).Should(Receive(MatchError(db.ErrConfigPreconditionFailed)))
	})
	It("keeps exact instance identities and immutable mutation receipts", func() {
		first, _, err := team.SavePipelineConditional(ref, atc.Config{}, 0, true)
		Expect(err).NotTo(HaveOccurred())
		receipt := first.ConfigVersion()
		inst := atc.PipelineRef{Name: ref.Name, InstanceVars: atc.InstanceVars{"branch": "main"}}
		other, created, err := team.SavePipelineConditional(inst, atc.Config{}, 0, true)
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())
		Expect(other.ID()).NotTo(Equal(first.ID()))
		_, _, err = team.SavePipelineConditional(ref, atc.Config{}, receipt, true)
		Expect(err).NotTo(HaveOccurred())
		Expect(first.ConfigVersion()).To(Equal(receipt))
	})
	It("rolls back the pipeline and receipt version when a later job write fails", func() {
		first, _, err := team.SavePipelineConditional(ref, atc.Config{}, 0, true)
		Expect(err).NotTo(HaveOccurred())
		_, err = dbConn.Exec(`CREATE FUNCTION strict_test_reject_job() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.name = 'fail' THEN RAISE EXCEPTION 'injected late save failure'; END IF; RETURN NEW; END $$`)
		Expect(err).NotTo(HaveOccurred())
		_, err = dbConn.Exec(`CREATE TRIGGER strict_test_reject_job BEFORE INSERT ON jobs FOR EACH ROW EXECUTE FUNCTION strict_test_reject_job()`)
		Expect(err).NotTo(HaveOccurred())
		bad := atc.Config{Jobs: atc.JobConfigs{{Name: "first"}, {Name: "fail"}}}
		_, _, err = team.SavePipelineConditional(ref, bad, first.ConfigVersion(), true)
		Expect(err).To(HaveOccurred())
		loaded, found, err := team.Pipeline(ref)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(loaded.ConfigVersion()).To(Equal(first.ConfigVersion()))
		jobs, err := loaded.Jobs()
		Expect(err).NotTo(HaveOccurred())
		Expect(jobs).To(BeEmpty())
	})
	It("conflicts when a deletion commits while a strict update waits for its row", func() {
		dbConn.SetMaxOpenConns(4)
		first, _, err := team.SavePipelineConditional(ref, atc.Config{}, 0, true)
		Expect(err).NotTo(HaveOccurred())
		tx, err := dbConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		defer tx.Rollback()
		_, err = tx.Exec("DELETE FROM pipelines WHERE id=$1", first.ID())
		Expect(err).NotTo(HaveOccurred())
		result := make(chan error, 1)
		go func() {
			_, _, err := team.SavePipelineConditional(ref, atc.Config{}, first.ConfigVersion(), true)
			result <- err
		}()
		Eventually(func() int {
			var n int
			_ = dbConn.QueryRow("SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock'").Scan(&n)
			return n
		}, 3*time.Second, 10*time.Millisecond).Should(BeNumerically(">", 0))
		Expect(tx.Commit()).To(Succeed())
		Eventually(result, 3*time.Second).Should(Receive(MatchError(db.ErrConfigPreconditionFailed)))
		_, found, err := team.Pipeline(ref)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())
	})

})
