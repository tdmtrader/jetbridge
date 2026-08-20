package db_test

import (
	"context"
	"fmt"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/event"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Run build log query", func() {
	var (
		template db.Pipeline
		factory  db.PipelineRunFactory
	)

	BeforeEach(func() {
		factory = db.NewPipelineRunFactory(dbConn, lockFactory)
		var err error
		template, _, err = defaultTeam.SavePipeline(atc.PipelineRef{Name: "run-log-template"}, atc.Config{
			Template: true,
			Params:   []atc.ParamSchema{{Name: "environment", Type: atc.ParamTypeString, Required: true}},
			Jobs: atc.JobConfigs{
				{Name: "deploy-((environment))"},
				{Name: "unrelated"},
			},
		}, 0, false)
		Expect(err).NotTo(HaveOccurred())
	})

	createRun := func(environment string) (db.PipelineRun, map[string]db.Build) {
		GinkgoHelper()
		creation, err := factory.CreateRun(context.Background(), template, db.RunParams{
			Vars: atc.RunParams{"environment": environment},
		}, "creator")
		Expect(err).NotTo(HaveOccurred())
		builds := make(map[string]db.Build, len(creation.EntryBuilds))
		for _, build := range creation.EntryBuilds {
			builds[build.RunJobKey()] = build
		}
		return creation.Run, builds
	}

	It("paginates all live and reclaimed builds by immutable policy key and hydrates detached display names", func() {
		firstRun, first := createRun("staging")
		_, second := createRun("production")
		_, third := createRun("canary")

		reclaimRunPayloadForTest(template, firstRun)

		from := first["deploy-((environment))"].ID()
		builds, pagination, err := template.ChronoRunBuilds("deploy-((environment))", db.Page{From: &from, Limit: 2})
		Expect(err).NotTo(HaveOccurred())
		Expect(buildIDs(builds)).To(Equal([]int{
			second["deploy-((environment))"].ID(),
			first["deploy-((environment))"].ID(),
		}))
		Expect(buildJobNames(builds)).To(Equal([]string{"deploy-production", "deploy-staging"}))
		Expect(pagination.Newer).To(Equal(&db.Page{From: db.NewIntPtr(third["deploy-((environment))"].ID()), Limit: 2}))

		newer, next, err := template.ChronoRunBuilds("deploy-((environment))", *pagination.Newer)
		Expect(err).NotTo(HaveOccurred())
		Expect(buildIDs(newer)).To(Equal([]int{third["deploy-((environment))"].ID()}))
		Expect(buildJobNames(newer)).To(Equal([]string{"deploy-canary"}))
		Expect(next.Newer).To(BeNil())
	})

	It("isolates templates and policy keys", func() {
		_, own := createRun("staging")
		otherTemplate, _, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: "other-run-log-template"}, atc.Config{
			Template: true,
			Params:   []atc.ParamSchema{{Name: "environment", Type: atc.ParamTypeString, Required: true}},
			Jobs:     atc.JobConfigs{{Name: "deploy-((environment))"}},
		}, 0, false)
		Expect(err).NotTo(HaveOccurred())
		other, err := factory.CreateRun(context.Background(), otherTemplate, db.RunParams{Vars: atc.RunParams{"environment": "other"}}, "creator")
		Expect(err).NotTo(HaveOccurred())

		builds, _, err := template.ChronoRunBuilds("deploy-((environment))", db.Page{Limit: 20})
		Expect(err).NotTo(HaveOccurred())
		Expect(buildIDs(builds)).To(Equal([]int{own["deploy-((environment))"].ID()}))
		Expect(buildIDs(builds)).NotTo(ContainElement(other.EntryBuilds[0].ID()))

		builds, _, err = template.ChronoRunBuilds("unrelated", db.Page{Limit: 20})
		Expect(err).NotTo(HaveOccurred())
		Expect(buildIDs(builds)).To(Equal([]int{own["unrelated"].ID()}))
	})
})

var _ = Describe("team-partition run events", func() {
	It("deletes only selected run events from the base team and reaps only those builds", func() {
		template, _, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: "event-template"}, atc.Config{
			Template: true,
			Jobs:     atc.JobConfigs{{Name: "entry"}},
		}, 0, false)
		Expect(err).NotTo(HaveOccurred())
		creation, err := db.NewPipelineRunFactory(dbConn, lockFactory).CreateRun(context.Background(), template, db.RunParams{}, "creator")
		Expect(err).NotTo(HaveOccurred())
		target := creation.EntryBuilds[0]
		Expect(target.SaveEvent(event.Log{Payload: "target"})).To(Succeed())

		otherTeam, err := teamFactory.CreateTeam(atc.Team{Name: "other-run-log-team"})
		Expect(err).NotTo(HaveOccurred())
		otherTemplate, _, err := otherTeam.SavePipeline(atc.PipelineRef{Name: "event-template"}, atc.Config{
			Template: true,
			Jobs:     atc.JobConfigs{{Name: "entry"}},
		}, 0, false)
		Expect(err).NotTo(HaveOccurred())
		otherCreation, err := db.NewPipelineRunFactory(dbConn, lockFactory).CreateRun(context.Background(), otherTemplate, db.RunParams{}, "creator")
		Expect(err).NotTo(HaveOccurred())
		foreign := otherCreation.EntryBuilds[0]
		Expect(foreign.SaveEvent(event.Log{Payload: "foreign"})).To(Succeed())

		Expect(template.DeleteRunBuildEventsByBuildIDs([]int{target.ID(), foreign.ID()})).To(Succeed())
		Expect(runEventCount(defaultTeam.ID(), target.ID())).To(BeZero())
		Expect(runEventCount(otherTeam.ID(), foreign.ID())).To(Equal(1))
		Expect(buildWasReaped(target.ID())).To(BeTrue())
		Expect(buildWasReaped(foreign.ID())).To(BeFalse())
	})
})

func buildIDs(builds []db.BuildForAPI) []int {
	ids := make([]int, len(builds))
	for i, build := range builds {
		ids[i] = build.ID()
	}
	return ids
}

func buildJobNames(builds []db.BuildForAPI) []string {
	names := make([]string, len(builds))
	for i, build := range builds {
		names[i] = build.JobName()
	}
	return names
}

func runEventCount(teamID, buildID int) int {
	GinkgoHelper()
	var count int
	Expect(dbConn.QueryRow(fmt.Sprintf("SELECT count(*) FROM team_build_events_%d WHERE build_id = $1", teamID), buildID).Scan(&count)).To(Succeed())
	return count
}

func buildWasReaped(buildID int) bool {
	GinkgoHelper()
	var reaped bool
	Expect(dbConn.QueryRow("SELECT reap_time IS NOT NULL FROM builds WHERE id = $1", buildID).Scan(&reaped)).To(Succeed())
	return reaped
}
