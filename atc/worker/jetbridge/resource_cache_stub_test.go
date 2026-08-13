package jetbridge_test

import (
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

// stubCache is a db.ResourceCache carrying only the two fields this package
// ever reads: the id, and the content key.
//
// It is a value, not a behavioural double. Nothing here records calls, returns
// canned results, or stands in for a database — the alternative would be
// dragging a Postgres harness into a package that has none, purely to supply two
// strings to a key formatter. Every method the code under test does NOT use
// panics rather than returning a zero value, so this cannot quietly grow into a
// stand-in for a real cache: the day something reaches for Version() or
// ResourceConfig(), the test fails loudly and the stub has to be reconsidered.
type stubCache struct {
	id         int
	durableKey string
}

func (c stubCache) ID() int            { return c.id }
func (c stubCache) DurableKey() string { return c.durableKey }

func (stubCache) Version() atc.Version {
	panic("stubCache.Version: not modelled — see the type comment")
}

func (stubCache) ResourceConfig() db.ResourceConfig {
	panic("stubCache.ResourceConfig: not modelled — see the type comment")
}

func (stubCache) Destroy(db.Tx) (bool, error) {
	panic("stubCache.Destroy: not modelled — see the type comment")
}

func (stubCache) BaseResourceType() *db.UsedBaseResourceType {
	panic("stubCache.BaseResourceType: not modelled — see the type comment")
}
