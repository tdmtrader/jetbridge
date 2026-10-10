package ops_test

import (
	"testing"

	"github.com/concourse/concourse/integration/internal/dctest"
	"github.com/concourse/concourse/integration/internal/flytest"
)

func TestUpgradeOps(t *testing.T) {
	latestDC := dctest.Init(t, "../docker-compose.yml", "overrides/named.yml", "overrides/latest.yml")
	devDC := dctest.Init(t, "../docker-compose.yml", "overrides/named.yml")

	t.Run("test upgrade", func(t *testing.T) {
		t.Run("deploy latest release version", func(t *testing.T) {
			latestDC.Run(t, "up", "-d")
		})

		fly := flytest.Init(t, latestDC)
		setupUpgrade(t, fly)

		t.Run("upgrade to dev", func(t *testing.T) {
			devDC.Run(t, "up", "-d")
		})
		fly = flytest.Init(t, devDC)

		verifyUpgrade(t, fly)
	})

}
