package accessor_test

import (
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/accessor/accessorfakes"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("TeamsCacher", func() {
	It("refreshes persisted teams only after notification invalidates the cache", func() {
		teamFactory := useRealTeamFactory()
		_, err := teamFactory.CreateTeam(atc.Team{Name: "cached-team"})
		Expect(err).NotTo(HaveOccurred())

		signal := db.NewNotifySignal()
		fakeNotifications := new(accessorfakes.FakeNotifications)
		fakeNotifications.ListenSignalReturns(signal, nil)
		teamFetcher := accessor.NewTeamsCacher(
			lager.NewLogger("test"),
			fakeNotifications,
			teamFactory,
			time.Minute,
			time.Minute,
		)

		teamNames := func(teams []db.Team) []string {
			names := make([]string, 0, len(teams))
			for _, team := range teams {
				names = append(names, team.Name())
			}
			return names
		}

		cached, err := teamFetcher.GetTeams()
		Expect(err).NotTo(HaveOccurred())
		Expect(teamNames(cached)).To(ConsistOf("cached-team"))

		_, err = teamFactory.CreateTeam(atc.Team{Name: "new-team"})
		Expect(err).NotTo(HaveOccurred())

		persisted, err := teamFactory.GetTeams()
		Expect(err).NotTo(HaveOccurred())
		Expect(teamNames(persisted)).To(ConsistOf("cached-team", "new-team"))

		stillCached, err := teamFetcher.GetTeams()
		Expect(err).NotTo(HaveOccurred())
		Expect(teamNames(stillCached)).To(ConsistOf("cached-team"))

		signal.Signal()
		Eventually(func(g Gomega) []string {
			refreshed, err := teamFetcher.GetTeams()
			g.Expect(err).NotTo(HaveOccurred())
			return teamNames(refreshed)
		}, 5*time.Second, 10*time.Millisecond).Should(ConsistOf("cached-team", "new-team"))
	})
})
