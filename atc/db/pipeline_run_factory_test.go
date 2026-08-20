package db_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("PipelineRunFactory", func() {
	var factory db.PipelineRunFactory

	BeforeEach(func() {
		factory = db.NewPipelineRunFactory(dbConn, lockFactory)
	})

	It("creates a complete run graph in the caller transaction", func() {
		template, _, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: "template"}, atc.Config{
			Template:  true,
			Params:    []atc.ParamSchema{{Name: "value", Type: atc.ParamTypeString, Required: true}},
			Resources: atc.ResourceConfigs{{Name: "input", Type: "some-base-resource-type", Source: atc.Source{"repository": "example"}}},
			Jobs:      atc.JobConfigs{{Name: "entry-((value))"}, {Name: "downstream", PlanSequence: []atc.Step{{Config: &atc.GetStep{Name: "input", Passed: []string{"entry-((value))"}, Trigger: true}}}}},
		}, 0, false)
		Expect(err).NotTo(HaveOccurred())

		tx, err := dbConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		defer tx.Rollback()

		creation, err := factory.CreateRunInTx(context.Background(), tx, template, db.RunParams{Vars: atc.RunParams{"value": "one"}}, "creator", db.RunCreationOpts{
			BeforeCommit: func(callbackTx db.Tx, got db.RunCreation) error {
				var headers, payloads, builds, nextBuildID, payloadID, payloadRunID int
				var payloadTemplate bool
				var scheduleRequestedAfterLast bool
				Expect(callbackTx.QueryRow("SELECT count(*) FROM pipeline_runs WHERE id = $1", got.Run.ID()).Scan(&headers)).To(Succeed())
				Expect(callbackTx.QueryRow("SELECT count(*) FROM pipelines WHERE pipeline_run_id = $1", got.Run.ID()).Scan(&payloads)).To(Succeed())
				Expect(callbackTx.QueryRow("SELECT count(*) FROM builds WHERE pipeline_run_id = $1", got.Run.ID()).Scan(&builds)).To(Succeed())
				Expect(callbackTx.QueryRow("SELECT id, template, pipeline_run_id FROM pipelines WHERE pipeline_run_id = $1", got.Run.ID()).Scan(&payloadID, &payloadTemplate, &payloadRunID)).To(Succeed())
				Expect(callbackTx.QueryRow("SELECT next_build_id, schedule_requested > last_scheduled FROM jobs WHERE id = $1", got.EntryBuilds[0].JobID()).Scan(&nextBuildID, &scheduleRequestedAfterLast)).To(Succeed())
				Expect(headers).To(Equal(1))
				Expect(payloads).To(Equal(1))
				Expect(builds).To(Equal(1))
				Expect(payloadTemplate).To(BeFalse())
				Expect(payloadRunID).To(Equal(got.Run.ID()))
				Expect(nextBuildID).To(Equal(got.EntryBuilds[0].ID()))
				Expect(scheduleRequestedAfterLast).To(BeTrue())

				rows, err := callbackTx.Query("SELECT name, run_expected, run_policy_key FROM jobs WHERE pipeline_id = $1 ORDER BY name", payloadID)
				Expect(err).NotTo(HaveOccurred())
				defer rows.Close()
				flags := map[string]runJobFlags{}
				for rows.Next() {
					var name, key string
					var expected bool
					Expect(rows.Scan(&name, &expected, &key)).To(Succeed())
					flags[name] = runJobFlags{Expected: expected, PolicyKey: key}
				}
				Expect(rows.Err()).NotTo(HaveOccurred())
				Expect(flags).To(Equal(map[string]runJobFlags{
					"downstream": {Expected: true, PolicyKey: "downstream"},
					"entry-one":  {Expected: true, PolicyKey: "entry-((value))"},
				}))
				return nil
			},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(creation.Run.Number()).To(Equal(1))
		Expect(creation.Config.Template).To(BeFalse())
		childID, found := creation.Run.InstancePipelineID()
		Expect(found).To(BeTrue())
		Expect(childID).To(BeNumerically(">", 0))
		Expect(creation.Config.Jobs[0].Name).To(Equal("entry-one"))
		Expect(creation.EntryJobs).To(Equal([]string{"entry-one"}))
		Expect(creation.EntryBuilds).To(HaveLen(1))
		Expect(creation.EntryBuilds[0].RunJobName()).To(Equal("entry-one"))
		Expect(creation.EntryBuilds[0].RunJobKey()).To(Equal("entry-((value))"))
		Expect(creation.Run.CompletedAt()).To(BeNil())
		expectedHash := fmt.Sprintf("%x", sha256.Sum256(append([]byte("run-instance-config/v1\x00"), creation.CanonicalJSON...)))
		Expect(creation.ConfigHash).To(Equal(expectedHash))

		Expect(tx.Commit()).To(Succeed())
		stored, found, err := factory.GetRun(template, creation.Run.Number())
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(stored.CompletedAt()).To(BeNil())
	})

	It("paginates runs newest first with inclusive run-number cursors", func() {
		template, _, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: "paged-runs"}, atc.Config{Template: true, Jobs: atc.JobConfigs{{Name: "entry"}}}, 0, false)
		Expect(err).NotTo(HaveOccurred())
		for range 5 {
			_, err = factory.CreateRun(context.Background(), template, db.RunParams{}, "creator")
			Expect(err).NotTo(HaveOccurred())
		}

		runs, pagination, err := factory.Runs(template, db.Page{Limit: 2})
		Expect(err).NotTo(HaveOccurred())
		Expect(runNumbers(runs)).To(Equal([]int{5, 4}))
		Expect(pagination.Older).To(Equal(&db.Page{To: db.NewIntPtr(3), Limit: 2}))
		Expect(pagination.Newer).To(BeNil())

		runs, pagination, err = factory.Runs(template, *pagination.Older)
		Expect(err).NotTo(HaveOccurred())
		Expect(runNumbers(runs)).To(Equal([]int{3, 2}))
		Expect(pagination.Newer).To(Equal(&db.Page{From: db.NewIntPtr(4), Limit: 2}))

		runs, _, err = factory.Runs(template, *pagination.Newer)
		Expect(err).NotTo(HaveOccurred())
		Expect(runNumbers(runs)).To(Equal([]int{5, 4}))

		runs, _, err = factory.Runs(template, db.Page{From: db.NewIntPtr(2), To: db.NewIntPtr(4), Limit: 3})
		Expect(err).NotTo(HaveOccurred())
		Expect(runNumbers(runs)).To(Equal([]int{4, 3, 2}))
		runs, pagination, err = factory.Runs(template, db.Page{From: db.NewIntPtr(2), To: db.NewIntPtr(4), Limit: 3})
		Expect(err).NotTo(HaveOccurred())
		Expect(runNumbers(runs)).To(Equal([]int{4, 3, 2}))
		Expect(pagination.Newer).To(Equal(&db.Page{From: db.NewIntPtr(5), Limit: 3}))
		Expect(pagination.Older).To(Equal(&db.Page{To: db.NewIntPtr(1), Limit: 3}))
		_, _, err = factory.Runs(template, db.Page{From: db.NewIntPtr(5), To: db.NewIntPtr(3), Limit: 2})
		Expect(err).To(MatchError("invalid range boundaries"))
	})

	It("does not allocate a number when validation fails", func() {
		template, _, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: "invalid-run"}, atc.Config{
			Template: true,
			Params:   []atc.ParamSchema{{Name: "required", Type: atc.ParamTypeString, Required: true}},
			Jobs:     atc.JobConfigs{{Name: "entry"}},
		}, 0, false)
		Expect(err).NotTo(HaveOccurred())

		tx, err := dbConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		defer tx.Rollback()
		_, err = factory.CreateRunInTx(context.Background(), tx, template, db.RunParams{}, "creator", db.RunCreationOpts{})
		Expect(err).To(MatchError(ContainSubstring("required")))
		var number, runs int
		Expect(tx.QueryRow("SELECT last_run_number FROM pipelines WHERE id = $1", template.ID()).Scan(&number)).To(Succeed())
		Expect(tx.QueryRow("SELECT count(*) FROM pipeline_runs WHERE template_pipeline_id = $1", template.ID()).Scan(&runs)).To(Succeed())
		Expect(number).To(Equal(0))
		Expect(runs).To(Equal(0))
	})

	It("rejects paused, archived, non-template, and instanced bases without allocation", func() {
		assertNotAllocated := func(pipeline db.Pipeline, expected error) {
			tx, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer tx.Rollback()
			_, err = factory.CreateRunInTx(context.Background(), tx, pipeline, db.RunParams{}, "creator", db.RunCreationOpts{})
			Expect(err).To(MatchError(expected))

			var number, runs int
			Expect(tx.QueryRow("SELECT last_run_number FROM pipelines WHERE id = $1", pipeline.ID()).Scan(&number)).To(Succeed())
			Expect(tx.QueryRow("SELECT count(*) FROM pipeline_runs WHERE template_pipeline_id = $1", pipeline.ID()).Scan(&runs)).To(Succeed())
			Expect(number).To(Equal(0))
			Expect(runs).To(Equal(0))
			Expect(tx.Rollback()).To(Succeed())
		}

		paused, _, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: "paused-run"}, atc.Config{Template: true, Jobs: atc.JobConfigs{{Name: "entry"}}}, 0, false)
		Expect(err).NotTo(HaveOccurred())
		Expect(paused.Pause("creator")).To(Succeed())
		assertNotAllocated(paused, db.ErrPipelineRunPaused)

		archived, _, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: "archived-run"}, atc.Config{Template: true, Jobs: atc.JobConfigs{{Name: "entry"}}}, 0, false)
		Expect(err).NotTo(HaveOccurred())
		Expect(archived.Archive()).To(Succeed())
		assertNotAllocated(archived, db.ErrPipelineRunArchived)

		ordinary, _, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: "ordinary-run"}, atc.Config{Jobs: atc.JobConfigs{{Name: "entry"}}}, 0, false)
		Expect(err).NotTo(HaveOccurred())
		assertNotAllocated(ordinary, db.ErrPipelineRunNotTemplate)

		instanced, _, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: "instanced-run", InstanceVars: atc.InstanceVars{"run": float64(1)}}, atc.Config{Jobs: atc.JobConfigs{{Name: "entry"}}}, 0, false)
		Expect(err).NotTo(HaveOccurred())
		assertNotAllocated(instanced, db.ErrPipelineRunInstanced)
	})

	It("allocates concurrent run numbers monotonically", func() {
		template, _, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: "concurrent-runs"}, atc.Config{Template: true, Jobs: atc.JobConfigs{{Name: "entry"}}}, 0, false)
		Expect(err).NotTo(HaveOccurred())

		results := make(chan runResult, 6)
		var wg sync.WaitGroup
		for range 6 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				creation, err := factory.CreateRun(context.Background(), template, db.RunParams{}, "creator")
				results <- runResult{Creation: creation, Err: err}
			}()
		}
		wg.Wait()
		close(results)

		numbers := make([]int, 0, 6)
		for result := range results {
			Expect(result.Err).NotTo(HaveOccurred())
			numbers = append(numbers, result.Creation.Run.Number())
		}
		sort.Ints(numbers)
		Expect(numbers).To(Equal([]int{1, 2, 3, 4, 5, 6}))
		Expect(template.Reload()).To(BeTrue())
		Expect(template.LastRunNumber()).To(Equal(6))
	})

	It("skips an occupied ordinary run-shaped instance", func() {
		template, _, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: "occupied-run"}, atc.Config{Template: true, Jobs: atc.JobConfigs{{Name: "entry"}}}, 0, false)
		Expect(err).NotTo(HaveOccurred())
		_, _, err = defaultTeam.SavePipeline(atc.PipelineRef{Name: "occupied-run", InstanceVars: atc.InstanceVars{"run": float64(1)}}, atc.Config{Jobs: atc.JobConfigs{{Name: "entry"}}}, 0, false)
		Expect(err).NotTo(HaveOccurred())

		creation, err := factory.CreateRun(context.Background(), template, db.RunParams{}, "creator")
		Expect(err).NotTo(HaveOccurred())
		Expect(creation.Run.Number()).To(Equal(2))
		Expect(template.Reload()).To(BeTrue())
		Expect(template.LastRunNumber()).To(Equal(2))
	})

	It("hydrates the durable run number only for payload pipelines", func() {
		template, _, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: "run-number"}, atc.Config{Template: true, Jobs: atc.JobConfigs{{Name: "entry"}}}, 0, false)
		Expect(err).NotTo(HaveOccurred())
		creation, err := factory.CreateRun(context.Background(), template, db.RunParams{}, "creator")
		Expect(err).NotTo(HaveOccurred())

		payload, found, err := defaultTeam.Pipeline(atc.PipelineRef{Name: "run-number", InstanceVars: atc.InstanceVars{"run": float64(creation.Run.Number())}})
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		number, hasNumber := payload.RunNumber()
		Expect(hasNumber).To(BeTrue())
		Expect(number).To(Equal(creation.Run.Number()))

		ordinary, _, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: "ordinary-run-number", InstanceVars: atc.InstanceVars{"run": float64(99)}}, atc.Config{Jobs: atc.JobConfigs{{Name: "entry"}}}, 0, false)
		Expect(err).NotTo(HaveOccurred())
		_, hasNumber = ordinary.RunNumber()
		Expect(hasNumber).To(BeFalse())
	})

	It("materializes the durable ID and stores normalized parameters", func() {
		template, _, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: "run-id-and-params"}, atc.Config{
			Template: true,
			Params:   []atc.ParamSchema{{Name: "count", Type: atc.ParamTypeNumber, Required: true}},
			Jobs:     atc.JobConfigs{{Name: "entry-((run_id))-((count))"}},
		}, 0, false)
		Expect(err).NotTo(HaveOccurred())

		creation, err := factory.CreateRun(context.Background(), template, db.RunParams{Vars: atc.RunParams{"count": "42"}}, "creator")
		Expect(err).NotTo(HaveOccurred())
		Expect(creation.Config.Jobs[0].Name).To(Equal(fmt.Sprintf("entry-%d-42", creation.Run.ID())))
		Expect(creation.Run.Params()).To(Equal(atc.Params{"count": float64(42)}))
		var params string
		Expect(dbConn.QueryRow("SELECT params FROM pipeline_runs WHERE id = $1", creation.Run.ID()).Scan(&params)).To(Succeed())
		Expect(params).To(MatchJSON(`{"count":42}`))
	})

	It("uses an authoritative config override for validation and materialization", func() {
		template, _, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: "override-run"}, atc.Config{
			Template: true,
			Params:   []atc.ParamSchema{{Name: "base", Type: atc.ParamTypeString, Required: true}},
			Jobs:     atc.JobConfigs{{Name: "base-((base))"}},
		}, 0, false)
		Expect(err).NotTo(HaveOccurred())
		override := atc.Config{
			Params: []atc.ParamSchema{{Name: "authoritative", Type: atc.ParamTypeString, Required: true}},
			Jobs:   atc.JobConfigs{{Name: "override-((authoritative))"}},
		}

		tx, err := dbConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		defer tx.Rollback()
		creation, err := factory.CreateRunInTx(context.Background(), tx, template, db.RunParams{Vars: atc.RunParams{"authoritative": "value"}}, "creator", db.RunCreationOpts{Config: &override})
		Expect(err).NotTo(HaveOccurred())
		Expect(creation.Config.Jobs[0].Name).To(Equal("override-value"))
		Expect(creation.EntryJobs).To(Equal([]string{"override-value"}))
		Expect(tx.Commit()).To(Succeed())
	})

	It("persists ordinary pipeline defaults and template metadata across reloads and updates", func() {
		keepLast, ttlDays := 2, 3
		ref := atc.PipelineRef{Name: "metadata-run"}
		config := atc.Config{
			Template:     true,
			Params:       []atc.ParamSchema{{Name: "value", Type: atc.ParamTypeString, Default: "default"}},
			RunRetention: &atc.RunRetentionConfig{KeepLast: &keepLast, TTLDays: &ttlDays},
			Jobs:         atc.JobConfigs{{Name: "entry-((value))"}},
		}
		template, _, err := defaultTeam.SavePipeline(ref, config, 0, false)
		Expect(err).NotTo(HaveOccurred())
		Expect(template.Reload()).To(BeTrue())
		Expect(template.Template()).To(BeTrue())
		Expect(template.Params()).To(Equal(config.Params))
		Expect(template.RunRetention()).To(Equal(config.RunRetention))
		Expect(template.LastRunNumber()).To(Equal(0))

		withoutMetadata := atc.Config{Template: true, Jobs: atc.JobConfigs{{Name: "entry"}}}
		updated, _, err := defaultTeam.SavePipeline(ref, withoutMetadata, template.ConfigVersion(), false)
		Expect(err).NotTo(HaveOccurred())
		Expect(template.Reload()).To(BeTrue())
		Expect(template.Params()).To(BeEmpty())
		Expect(template.RunRetention()).To(BeNil())
		Expect(updated.Template()).To(BeTrue())

		creation, err := factory.CreateRun(context.Background(), updated, db.RunParams{}, "creator")
		Expect(err).NotTo(HaveOccurred())
		Expect(creation.Run.Number()).To(Equal(1))
		resaved, _, err := defaultTeam.SavePipeline(ref, withoutMetadata, updated.ConfigVersion(), false)
		Expect(err).NotTo(HaveOccurred())
		Expect(resaved.LastRunNumber()).To(Equal(1))
		_, _, err = defaultTeam.SavePipeline(ref, atc.Config{Jobs: atc.JobConfigs{{Name: "ordinary"}}}, resaved.ConfigVersion(), false)
		Expect(err).To(MatchError(db.ErrPipelineTemplateHasRuns))

		ordinary, _, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: "ordinary-defaults"}, atc.Config{Jobs: atc.JobConfigs{{Name: "entry"}}}, 0, false)
		Expect(err).NotTo(HaveOccurred())
		Expect(ordinary.Template()).To(BeFalse())
		Expect(ordinary.Params()).To(BeEmpty())
		Expect(ordinary.RunRetention()).To(BeNil())
		Expect(ordinary.LastRunNumber()).To(Equal(0))
	})

	It("keeps a committed creation successful when post-commit notification fails", func() {
		template, _, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: "notification-run"}, atc.Config{Template: true, Jobs: atc.JobConfigs{{Name: "entry"}}}, 0, false)
		Expect(err).NotTo(HaveOccurred())
		failingFactory := db.NewPipelineRunFactory(notificationFailingConn{DbConn: dbConn}, lockFactory)

		creation, err := failingFactory.CreateRun(context.Background(), template, db.RunParams{}, "creator")
		Expect(err).NotTo(HaveOccurred())
		stored, found, err := factory.GetRun(template, creation.Run.Number())
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(stored.ID()).To(Equal(creation.Run.ID()))
		Expect(failingFactory.AfterRunCreated(context.Background(), creation)).To(MatchError("notification unavailable"))
		Expect(failingFactory.AfterRunCreated(context.Background(), creation)).To(MatchError("notification unavailable"))
	})

	It("attempts both post-commit wakeups when one notification fails", func() {
		template, _, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: "partial-notification-run"}, atc.Config{Template: true, Jobs: atc.JobConfigs{{Name: "entry"}}}, 0, false)
		Expect(err).NotTo(HaveOccurred())
		bus := &notificationRecordingBus{NotificationsBus: dbConn.Bus(), failChannel: atc.ComponentLidarScanner}
		recordingFactory := db.NewPipelineRunFactory(notificationRecordingConn{DbConn: dbConn, bus: bus}, lockFactory)

		creation, err := recordingFactory.CreateRun(context.Background(), template, db.RunParams{}, "creator")
		Expect(err).NotTo(HaveOccurred())
		Expect(bus.notifications).To(Equal([]string{atc.ComponentLidarScanner, atc.ComponentScheduler}))
		Expect(recordingFactory.AfterRunCreated(context.Background(), creation)).To(MatchError("notification unavailable"))
		Expect(bus.notifications).To(Equal([]string{atc.ComponentLidarScanner, atc.ComponentScheduler, atc.ComponentLidarScanner, atc.ComponentScheduler}))
	})

	It("wakes the scanner and scheduler after creation, repeatedly", func() {
		template, _, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: "wake-run"}, atc.Config{Template: true, Jobs: atc.JobConfigs{{Name: "entry"}}}, 0, false)
		Expect(err).NotTo(HaveOccurred())
		bus := dbConn.Bus()
		scanner, err := bus.ListenSignal(atc.ComponentLidarScanner)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(bus.UnlistenSignal(atc.ComponentLidarScanner, scanner)).To(Succeed()) })
		scheduler, err := bus.ListenSignal(atc.ComponentScheduler)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(bus.UnlistenSignal(atc.ComponentScheduler, scheduler)).To(Succeed()) })

		creation, err := factory.CreateRun(context.Background(), template, db.RunParams{}, "creator")
		Expect(err).NotTo(HaveOccurred())
		Eventually(scanner.C(), 10*time.Second).Should(Receive())
		Eventually(scheduler.C(), 10*time.Second).Should(Receive())

		Expect(factory.AfterRunCreated(context.Background(), creation)).To(Succeed())
		Eventually(scanner.C(), 10*time.Second).Should(Receive())
		Eventually(scheduler.C(), 10*time.Second).Should(Receive())
	})

	It("rolls back all run rows when BeforeCommit rejects the creation", func() {
		template, _, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: "rollback-run"}, atc.Config{Template: true, Jobs: atc.JobConfigs{{Name: "entry"}}}, 0, false)
		Expect(err).NotTo(HaveOccurred())
		tx, err := dbConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		defer tx.Rollback()
		_, err = factory.CreateRunInTx(context.Background(), tx, template, db.RunParams{}, "creator", db.RunCreationOpts{BeforeCommit: func(db.Tx, db.RunCreation) error { return fmt.Errorf("stop") }})
		Expect(err).To(MatchError("stop"))
		var visibleRows, one int
		Expect(tx.QueryRow("SELECT count(*) FROM pipeline_runs WHERE template_pipeline_id = $1", template.ID()).Scan(&visibleRows)).To(Succeed())
		Expect(tx.QueryRow("SELECT 1").Scan(&one)).To(Succeed())
		Expect(visibleRows).To(Equal(1))
		Expect(one).To(Equal(1))
		Expect(tx.Rollback()).To(Succeed())
		var runs, payloads int
		Expect(dbConn.QueryRow("SELECT count(*) FROM pipeline_runs WHERE template_pipeline_id = $1", template.ID()).Scan(&runs)).To(Succeed())
		Expect(dbConn.QueryRow("SELECT count(*) FROM pipelines WHERE name = 'rollback-run' AND pipeline_run_id IS NOT NULL").Scan(&payloads)).To(Succeed())
		Expect(runs).To(Equal(0))
		Expect(payloads).To(Equal(0))
	})
})

func runNumbers(runs []db.PipelineRun) []int {
	numbers := make([]int, len(runs))
	for i, run := range runs {
		numbers[i] = run.Number()
	}
	return numbers
}

type runJobFlags struct {
	Expected  bool
	PolicyKey string
}

type runResult struct {
	Creation db.RunCreation
	Err      error
}

type notificationFailingConn struct{ db.DbConn }

func (c notificationFailingConn) Bus() db.NotificationsBus {
	return notificationFailingBus{NotificationsBus: c.DbConn.Bus()}
}

type notificationFailingBus struct{ db.NotificationsBus }

func (notificationFailingBus) Notify(string) error { return fmt.Errorf("notification unavailable") }

type notificationRecordingConn struct {
	db.DbConn
	bus *notificationRecordingBus
}

func (c notificationRecordingConn) Bus() db.NotificationsBus { return c.bus }

type notificationRecordingBus struct {
	db.NotificationsBus
	notifications []string
	failChannel   string
}

func (b *notificationRecordingBus) Notify(channel string) error {
	b.notifications = append(b.notifications, channel)
	if channel == b.failChannel {
		return fmt.Errorf("notification unavailable")
	}
	return nil
}
