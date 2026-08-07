package syslog_test

import (
	"fmt"
	"strconv"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/event"
	"github.com/concourse/concourse/atc/syslog"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func newDrainableBuild(team db.Team) db.Build {
	GinkgoHelper()

	build, err := team.CreateOneOffBuild()
	Expect(err).NotTo(HaveOccurred())

	timestamp := int64(1533744538)
	Expect(build.SaveEvent(event.Log{
		Time:    timestamp,
		Payload: fmt.Sprintf("build %d log", build.ID()),
	})).To(Succeed())
	Expect(build.SaveEvent(event.Status{
		Time:   timestamp,
		Status: atc.BuildStatus(fmt.Sprintf("build %d status", build.ID())),
	})).To(Succeed())
	Expect(build.SaveEvent(event.FinishGet{
		Time:           timestamp,
		FetchedVersion: atc.Version{"version": "0.0.1"},
		FetchedMetadata: atc.Metadata{
			{Name: "version", Value: "0.0.1"},
		},
	})).To(Succeed())
	Expect(build.SaveEvent(event.SelectedWorker{
		Time:       timestamp,
		WorkerName: "example-worker",
	})).To(Succeed())
	Expect(build.SaveEvent(event.InitializeTask{Time: timestamp})).To(Succeed())
	Expect(build.Finish(db.BuildStatusSucceeded)).To(Succeed())

	return build
}

var _ = Describe("Drainer", func() {
	var (
		buildFactory db.BuildFactory
		builds       []db.Build
		server       *testServer
	)

	BeforeEach(func() {
		conn := useRealDB()
		teamFactory := db.NewTeamFactory(conn, nil)
		team, err := teamFactory.CreateTeam(atc.Team{Name: atc.DefaultTeamName})
		Expect(err).NotTo(HaveOccurred())

		buildFactory = db.NewBuildFactory(conn, nil, 0, time.Hour)
		builds = []db.Build{newDrainableBuild(team), newDrainableBuild(team)}
	})

	AfterEach(func() {
		if server != nil {
			server.Close()
		}
	})

	Context("when there are builds that have not been drained", func() {
		Context("when tls is not set", func() {
			BeforeEach(func() {
				server = newTestServer(nil)
			})

			It("drains all build events by tcp", func(ctx SpecContext) {
				testDrainer := syslog.NewDrainer("tcp", server.Addr, "test", []string{}, buildFactory)
				err := testDrainer.Run(ctx)
				Expect(err).NotTo(HaveOccurred())

				got := <-server.Messages
				Expect(got).To(ContainSubstring("build " + strconv.Itoa(builds[0].ID()) + " log"))
				Expect(got).To(ContainSubstring("build " + strconv.Itoa(builds[1].ID()) + " log"))
				Expect(got).To(ContainSubstring(`get {"version": {"version":"0.0.1"}, "metadata": [{"name":"version","value":"0.0.1"}]`))
				Expect(got).To(ContainSubstring("build " + strconv.Itoa(builds[0].ID()) + " status"))
				Expect(got).To(ContainSubstring("build " + strconv.Itoa(builds[1].ID()) + " status"))
				Expect(got).To(ContainSubstring("selected worker: example-worker"))
				Expect(got).To(ContainSubstring("task initializing"))

				for _, build := range builds {
					persisted, found, err := buildFactory.Build(build.ID())
					Expect(err).NotTo(HaveOccurred())
					Expect(found).To(BeTrue())
					Expect(persisted.IsDrained()).To(BeTrue())
				}
				drainable, err := buildFactory.GetDrainableBuilds()
				Expect(err).NotTo(HaveOccurred())
				Expect(drainable).To(BeEmpty())
			}, NodeTimeout(5*time.Second))
		})

	})
})
