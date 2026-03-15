package accessor

import (
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/patrickmn/go-cache"
)

//counterfeiter:generate . Notifications
type Notifications interface {
	ListenSignal(string) (*db.NotifySignal, error)
	UnlistenSignal(string, *db.NotifySignal) error
}

type teamsCacher struct {
	logger        lager.Logger
	cache         *cache.Cache
	notifications Notifications
	teamFactory   db.TeamFactory
}

func NewTeamsCacher(
	logger lager.Logger,
	notifications Notifications,
	teamFactory db.TeamFactory,
	expiration time.Duration,
	cleanupInterval time.Duration,
) *teamsCacher {
	c := &teamsCacher{
		logger:        logger,
		cache:         cache.New(expiration, cleanupInterval),
		notifications: notifications,
		teamFactory:   teamFactory,
	}

	go c.waitForNotifications()

	return c
}

func (c *teamsCacher) GetTeams() ([]db.Team, error) {
	if teams, found := c.cache.Get(atc.TeamCacheName); found {
		return teams.([]db.Team), nil
	}

	teams, err := c.teamFactory.GetTeams()
	if err != nil {
		return nil, err
	}

	c.cache.Set(atc.TeamCacheName, teams, cache.DefaultExpiration)

	return teams, nil
}

func (c *teamsCacher) waitForNotifications() {
	signal, err := c.notifications.ListenSignal(atc.TeamCacheChannel)
	if err != nil {
		c.logger.Error("failed-to-listen-for-team-cache", err)
		return
	}

	defer c.notifications.UnlistenSignal(atc.TeamCacheChannel, signal)

	for {
		<-signal.C()
		c.cache.Delete(atc.TeamCacheName)
	}
}
