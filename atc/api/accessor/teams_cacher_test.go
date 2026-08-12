package accessor_test

import (
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("TeamsCacher", func() {
	It("refreshes persisted teams only after notification invalidates the cache", func() {
		fixture := useRealTeamFixture()
		teamFactory := fixture.TeamFactory
		_, err := teamFactory.CreateTeam(atc.Team{Name: "cached-team"})
		Expect(err).NotTo(HaveOccurred())

		teamFetcher := accessor.NewTeamsCacher(
			lager.NewLogger("test"),
			fixture.Conn.Bus(),
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

		// The cacher subscribes from its own goroutine, so a NOTIFY issued
		// before it reaches LISTEN goes nowhere. Re-notifying is what makes
		// the handoff observable; the assertion above already proved the
		// cache does not refresh on its own.
		Eventually(func(g Gomega) []string {
			g.Expect(teamFactory.NotifyCacher()).To(Succeed())

			refreshed, err := teamFetcher.GetTeams()
			g.Expect(err).NotTo(HaveOccurred())
			return teamNames(refreshed)
		}, 10*time.Second, 100*time.Millisecond).Should(ConsistOf("cached-team", "new-team"))
	})
})
