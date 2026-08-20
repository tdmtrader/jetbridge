package db_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/event"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("run build admission", func() {
	var (
		factory  db.PipelineRunFactory
		run      db.PipelineRun
		payload  db.Pipeline
		entryJob db.Job
		workJob  db.Job
	)
	const (
		runNotRunningMessage = "pipeline run is not running"
		runOneOffMessage     = "pipeline run payload cannot create one-off builds"
	)
	type legacyJobBuildPipeline interface {
		CreateJobBuild(string) (db.Build, error)
	}

	BeforeEach(func() {
		factory = db.NewPipelineRunFactory(dbConn, lockFactory)
		template, _, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: "admission-template"}, atc.Config{
			Template: true,
			Resources: atc.ResourceConfigs{{
				Name: "source", Type: "some-base-resource-type", Source: atc.Source{"repository": "example"},
			}},
			Jobs: atc.JobConfigs{
				{Name: "entry"},
				{Name: "work", PlanSequence: []atc.Step{{Config: &atc.GetStep{Name: "source", Passed: []string{"entry"}, Trigger: true}}}},
			},
		}, 0, false)
		Expect(err).NotTo(HaveOccurred())

		creation, err := factory.CreateRun(context.Background(), template, db.RunParams{}, "creator")
		Expect(err).NotTo(HaveOccurred())
		run = creation.Run
		payloadID, found := run.InstancePipelineID()
		Expect(found).To(BeTrue())
		payload, found, err = defaultTeam.Pipeline(atc.PipelineRef{Name: template.Name(), InstanceVars: atc.InstanceVars{"run": float64(run.Number())}})
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(payload.ID()).To(Equal(payloadID))
		entryJob, found, err = payload.Job("entry")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		workJob, found, err = payload.Job("work")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
	})

	assertIdentity := func(build db.Build, name, key string) {
		GinkgoHelper()
		gotRunID, found := build.PipelineRunID()
		Expect(found).To(BeTrue())
		Expect(gotRunID).To(Equal(run.ID()))
		Expect(build.RunJobName()).To(Equal(name))
		Expect(build.RunJobKey()).To(Equal(key))
	}

	It("derives identical run build identity for entry, scheduler, manual, rerun, and legacy paths", func() {
		entryBuilds, err := entryJob.GetPendingBuilds()
		Expect(err).NotTo(HaveOccurred())
		Expect(entryBuilds).To(HaveLen(1))
		assertIdentity(entryBuilds[0], "entry", "entry")

		Expect(workJob.EnsurePendingBuildExists(context.Background())).To(Succeed())
		scheduled, err := workJob.GetPendingBuilds()
		Expect(err).NotTo(HaveOccurred())
		Expect(scheduled).To(HaveLen(1))
		assertIdentity(scheduled[0], "work", "work")

		manual, err := workJob.CreateBuild("manual-user")
		Expect(err).NotTo(HaveOccurred())
		assertIdentity(manual, "work", "work")

		rerun, err := workJob.RerunBuild(manual, "rerun-user")
		Expect(err).NotTo(HaveOccurred())
		assertIdentity(rerun, "work", "work")

		legacy, err := payload.(legacyJobBuildPipeline).CreateJobBuild("work")
		Expect(err).NotTo(HaveOccurred())
		assertIdentity(legacy, "work", "work")
	})

	It("derives labels from current rows rather than a stale hydrated job", func() {
		_, err := dbConn.Exec("ALTER TABLE jobs DISABLE TRIGGER USER")
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			_, enableErr := dbConn.Exec("ALTER TABLE jobs ENABLE TRIGGER USER")
			Expect(enableErr).NotTo(HaveOccurred())
		})
		_, err = dbConn.Exec("UPDATE jobs SET name = 'renamed-work', run_policy_key = 'renamed-policy' WHERE id = $1", workJob.ID())
		Expect(err).NotTo(HaveOccurred())
		_, err = dbConn.Exec("ALTER TABLE jobs ENABLE TRIGGER USER")
		Expect(err).NotTo(HaveOccurred())

		build, err := workJob.CreateBuild("manual-user")
		Expect(err).NotTo(HaveOccurred())
		assertIdentity(build, "renamed-work", "renamed-policy")
	})

	It("refuses non-manual terminal admission and defensively refuses a pending build start", func() {
		pending, err := workJob.CreateBuild("manual-user")
		Expect(err).NotTo(HaveOccurred())
		_, err = dbConn.Exec("UPDATE pipeline_runs SET status = 'succeeded', completed_at = now() WHERE id = $1", run.ID())
		Expect(err).NotTo(HaveOccurred())

		_, err = payload.(legacyJobBuildPipeline).CreateJobBuild("work")
		Expect(err).To(MatchError(runNotRunningMessage))
		Expect(workJob.EnsurePendingBuildExists(context.Background())).To(MatchError(runNotRunningMessage))

		started, err := pending.Start(atc.Plan{})
		Expect(err).To(MatchError(runNotRunningMessage))
		Expect(started).To(BeFalse())
	})

	It("serializes admission behind a terminal transaction", func() {
		tx, err := dbConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = tx.Rollback() })
		var lockedID int
		Expect(tx.QueryRow("SELECT id FROM pipeline_runs WHERE id = $1 FOR UPDATE", run.ID()).Scan(&lockedID)).To(Succeed())

		result := make(chan error, 1)
		go func() {
			result <- workJob.EnsurePendingBuildExists(context.Background())
		}()
		Consistently(result, 150*time.Millisecond).ShouldNot(Receive())
		_, err = tx.Exec("UPDATE pipeline_runs SET status = 'failed', completed_at = now() WHERE id = $1", run.ID())
		Expect(err).NotTo(HaveOccurred())
		Expect(tx.Commit()).To(Succeed())
		Eventually(result).WithTimeout(3 * time.Second).Should(Receive(MatchError(runNotRunningMessage)))
	})

	It("refuses a stale job after its terminal payload has been reclaimed", func() {
		_, err := dbConn.Exec("UPDATE pipeline_runs SET status = 'succeeded', completed_at = now() WHERE id = $1", run.ID())
		Expect(err).NotTo(HaveOccurred())
		Expect(payload.Destroy()).To(Succeed())

		_, err = workJob.CreateBuild("after-reclaim")
		Expect(err).To(HaveOccurred())
		Expect(workJob.EnsurePendingBuildExists(context.Background())).To(HaveOccurred())
	})

	It("rejects pipeline one-offs while team one-offs stay ordinary", func() {
		_, err := payload.CreateOneOffBuild()
		Expect(err).To(MatchError(runOneOffMessage))
		_, err = payload.CreateStartedBuild(atc.Plan{})
		Expect(err).To(MatchError(runOneOffMessage))

		oneOff, err := defaultTeam.CreateOneOffBuild()
		Expect(err).NotTo(HaveOccurred())
		_, stamped := oneOff.PipelineRunID()
		Expect(stamped).To(BeFalse())
	})

	It("keeps checks in a payload unstamped", func() {
		resource, found, err := payload.Resource("source")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		check, created, err := resource.CreateBuild(context.Background(), false, atc.Plan{})
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())
		_, stamped := check.PipelineRunID()
		Expect(stamped).To(BeFalse())
		Expect(check.RunJobName()).To(BeEmpty())
		Expect(check.RunJobKey()).To(BeEmpty())
		Expect(check.SaveEvent(event.Log{Payload: "check event"})).To(Succeed())
		var count int
		Expect(dbConn.QueryRow("SELECT count(*) FROM check_build_events WHERE build_id = $1", check.ID()).Scan(&count)).To(Succeed())
		Expect(count).To(Equal(2))
	})

	It("routes stamped run build events to the team partition before and after payload detachment", func() {
		entryBuilds, err := entryJob.GetPendingBuilds()
		Expect(err).NotTo(HaveOccurred())
		Expect(entryBuilds).To(HaveLen(1))
		build := entryBuilds[0]

		Expect(build.SaveEvent(event.Log{Payload: "before detach"})).To(Succeed())
		var count int
		Expect(dbConn.QueryRow(fmt.Sprintf("SELECT count(*) FROM team_build_events_%d WHERE build_id = $1", defaultTeam.ID()), build.ID()).Scan(&count)).To(Succeed())
		Expect(count).To(Equal(1))

		_, err = dbConn.Exec("UPDATE pipeline_runs SET status = 'succeeded', completed_at = now() WHERE id = $1", run.ID())
		Expect(err).NotTo(HaveOccurred())
		_, err = dbConn.Exec("UPDATE builds SET job_id = NULL, pipeline_id = NULL WHERE id = $1", build.ID())
		Expect(err).NotTo(HaveOccurred())
		Expect(payload.Destroy()).To(Succeed())

		Expect(build.SaveEvent(event.Log{Payload: "after detach"})).To(Succeed())
		Expect(dbConn.QueryRow(fmt.Sprintf("SELECT count(*) FROM team_build_events_%d WHERE build_id = $1", defaultTeam.ID()), build.ID()).Scan(&count)).To(Succeed())
		Expect(count).To(Equal(2))
	})
})

var _ = Describe("run build identity structural guard", func() {
	It("routes job-build writes through the canonical seam without absorbing check builds", func() {
		root := "."
		entries, err := os.ReadDir(root)
		Expect(err).NotTo(HaveOccurred())

		matchedWriteSites := 0
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}
			contents, err := os.ReadFile(filepath.Join(root, entry.Name()))
			Expect(err).NotTo(HaveOccurred())
			text := string(contents)
			matchedWriteSites += strings.Count(text, "createBuild(")
			matchedWriteSites += strings.Count(text, "INSERT INTO builds")
			matchedWriteSites += strings.Count(text, "Insert(\"builds\")")
		}
		Expect(matchedWriteSites).To(BeNumerically(">", 0), "guard must discover build write sites")

		jobSource, err := os.ReadFile("job.go")
		Expect(err).NotTo(HaveOccurred())
		Expect(string(jobSource)).NotTo(ContainSubstring("INSERT INTO builds"))
		Expect(string(jobSource)).NotTo(ContainSubstring("Insert(\"builds\")"))

		pipelineSource, err := os.ReadFile("pipeline.go")
		Expect(err).NotTo(HaveOccurred())
		legacyStart := strings.Index(string(pipelineSource), "func (p *pipeline) CreateJobBuild")
		Expect(legacyStart).To(BeNumerically(">=", 0))
		legacyEnd := strings.Index(string(pipelineSource)[legacyStart:], "\nfunc ")
		Expect(legacyEnd).To(BeNumerically(">", 0))
		legacyBody := string(pipelineSource)[legacyStart : legacyStart+legacyEnd]
		Expect(legacyBody).NotTo(ContainSubstring("Insert(\"builds\")"))

		admissionSource, err := os.ReadFile("pipeline_run_lock.go")
		Expect(err).NotTo(HaveOccurred())
		for _, label := range []string{"pipeline_run_id", "run_job_name", "run_job_key"} {
			Expect(string(admissionSource)).To(ContainSubstring("delete(values, \""+label+"\")"), "canonical admission must discard caller label %s", label)
		}

		resourceSource, err := os.ReadFile("resource.go")
		Expect(err).NotTo(HaveOccurred())
		Expect(string(resourceSource)).To(ContainSubstring("createStartedBuild("), "check paths must remain outside job admission")
		Expect(string(resourceSource)).NotTo(ContainSubstring("createJobBuild("), "check paths must remain ordinary")
	})
})

var _ = Describe("template checking and scheduling", func() {
	It("excludes base templates while preserving payloads and ordinary run-shaped instances", func() {
		config := atc.Config{
			Template: true,
			Resources: atc.ResourceConfigs{{
				Name: "source", Type: "custom", Source: atc.Source{"repository": "example"},
			}},
			ResourceTypes: atc.ResourceTypes{{
				Name: "custom", Type: "some-base-resource-type", Source: atc.Source{"repository": "type"},
			}},
			Jobs: atc.JobConfigs{{
				Name: "entry", PlanSequence: []atc.Step{{Config: &atc.GetStep{Name: "source", Trigger: true}}},
			}},
		}
		template, _, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: "checking-template"}, config, 0, false)
		Expect(err).NotTo(HaveOccurred())
		creation, err := db.NewPipelineRunFactory(dbConn, lockFactory).CreateRun(context.Background(), template, db.RunParams{}, "creator")
		Expect(err).NotTo(HaveOccurred())
		payloadID, found := creation.Run.InstancePipelineID()
		Expect(found).To(BeTrue())

		ordinaryConfig := config
		ordinaryConfig.Template = false
		ordinary, _, err := defaultTeam.SavePipeline(atc.PipelineRef{
			Name: "ordinary-shaped", InstanceVars: atc.InstanceVars{"run": float64(41)},
		}, ordinaryConfig, 0, false)
		Expect(err).NotTo(HaveOccurred())
		ordinaryJob, found, err := ordinary.Job("entry")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(ordinaryJob.RequestSchedule()).To(Succeed())

		resources, err := checkFactory.Resources()
		Expect(err).NotTo(HaveOccurred())
		resourcePipelineIDs := make([]int, 0, len(resources))
		for _, resource := range resources {
			resourcePipelineIDs = append(resourcePipelineIDs, resource.PipelineID())
		}
		Expect(resourcePipelineIDs).To(ContainElements(payloadID, ordinary.ID()))
		Expect(resourcePipelineIDs).NotTo(ContainElement(template.ID()))

		resourceTypes, err := checkFactory.ResourceTypesByPipeline()
		Expect(err).NotTo(HaveOccurred())
		Expect(resourceTypes).To(HaveKey(payloadID))
		Expect(resourceTypes).To(HaveKey(ordinary.ID()))
		Expect(resourceTypes).NotTo(HaveKey(template.ID()))

		jobs, err := db.NewJobFactory(dbConn, lockFactory).JobsToSchedule()
		Expect(err).NotTo(HaveOccurred())
		jobPipelineIDs := make([]int, 0, len(jobs))
		for _, job := range jobs {
			jobPipelineIDs = append(jobPipelineIDs, job.PipelineID())
		}
		Expect(jobPipelineIDs).To(ContainElements(payloadID, ordinary.ID()))
		Expect(jobPipelineIDs).NotTo(ContainElement(template.ID()))

		pipelines, err := db.NewPipelineFactory(dbConn, lockFactory).PipelinesToSchedule()
		Expect(err).NotTo(HaveOccurred())
		pipelineIDs := make([]int, 0, len(pipelines))
		for _, pipeline := range pipelines {
			pipelineIDs = append(pipelineIDs, pipeline.ID())
		}
		Expect(pipelineIDs).To(ContainElements(payloadID, ordinary.ID()))
		Expect(pipelineIDs).NotTo(ContainElement(template.ID()))
	})
})

var _ = Describe("pending build before schedule consumption", func() {
	It("advances only to the observed token and preserves a newer request", func() {
		type scheduleRequestConsumer interface {
			ConsumeScheduleRequest(time.Time, bool) error
		}

		Expect(defaultJob.RequestSchedule()).To(Succeed())
		found, err := defaultJob.Reload()
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		observed := defaultJob.ScheduleRequestedTime()

		Eventually(func() time.Time {
			Expect(defaultJob.RequestSchedule()).To(Succeed())
			var requested time.Time
			Expect(dbConn.QueryRow("SELECT schedule_requested FROM jobs WHERE id = $1", defaultJob.ID()).Scan(&requested)).To(Succeed())
			return requested
		}).Should(BeTemporally(">", observed))

		consumer, ok := defaultJob.(scheduleRequestConsumer)
		Expect(ok).To(BeTrue(), "jobs must expose atomic observed-token consumption")
		Expect(consumer.ConsumeScheduleRequest(observed, false)).To(Succeed())

		var requested, lastScheduled time.Time
		Expect(dbConn.QueryRow("SELECT schedule_requested, last_scheduled FROM jobs WHERE id = $1", defaultJob.ID()).Scan(&requested, &lastScheduled)).To(Succeed())
		Expect(lastScheduled).To(Equal(observed))
		Expect(requested).To(BeTemporally(">", lastScheduled))

		Expect(consumer.ConsumeScheduleRequest(requested, false)).To(Succeed())
		Expect(consumer.ConsumeScheduleRequest(observed, true)).To(Succeed())
		Expect(dbConn.QueryRow("SELECT last_scheduled FROM jobs WHERE id = $1", defaultJob.ID()).Scan(&lastScheduled)).To(Succeed())
		Expect(lastScheduled).To(Equal(requested))
	})
})
