package gc

import (
	"context"
	"time"

	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/metric"
)

type deprecatedScopeCollector struct {
	conn        db.DbConn
	gracePeriod time.Duration
}

func NewDeprecatedScopeCollector(
	conn db.DbConn,
	gracePeriod time.Duration,
) *deprecatedScopeCollector {
	return &deprecatedScopeCollector{
		conn:        conn,
		gracePeriod: gracePeriod,
	}
}

func (dsc *deprecatedScopeCollector) Run(ctx context.Context) error {
	logger := lagerctx.FromContext(ctx).Session("deprecated-scope-collector")

	logger.Debug("start")
	defer logger.Debug("done")

	start := time.Now()
	defer func() {
		metric.DeprecatedScopeCollectorDuration{
			Duration: time.Since(start),
		}.Emit(logger)
	}()

	_, err := dsc.conn.Exec(`
		DELETE FROM resource_config_scopes
		WHERE deprecated_at IS NOT NULL
		AND deprecated_at < now() - $1::interval
	`, dsc.gracePeriod.String())

	return err
}
