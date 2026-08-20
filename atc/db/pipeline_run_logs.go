package db

import (
	"fmt"

	sq "github.com/Masterminds/squirrel"
)

func (p *pipeline) ChronoRunBuilds(runJobKey string, page Page) ([]BuildForAPI, Pagination, error) {
	query := buildsQuery.
		JoinClause("JOIN pipeline_runs log_run ON log_run.id = b.pipeline_run_id").
		Where(sq.Eq{
			"log_run.template_pipeline_id": p.id,
			"b.run_job_key":                runJobKey,
		})
	return getBuildsWithPagination(query, page, p.conn, p.lockFactory, true)
}

func (p *pipeline) DeleteRunBuildEventsByBuildIDs(buildIDs []int) error {
	if len(buildIDs) == 0 {
		return nil
	}

	tx, err := p.conn.Begin()
	if err != nil {
		return err
	}
	defer Rollback(tx)

	filter := `
		SELECT b.id
		FROM builds b
		JOIN pipeline_runs run ON run.id = b.pipeline_run_id
		WHERE b.id = ANY($1) AND b.team_id = $2 AND run.template_pipeline_id = $3
	`
	if _, err = tx.Exec(fmt.Sprintf(`DELETE FROM team_build_events_%d WHERE build_id IN (%s)`, p.teamID, filter), buildIDs, p.teamID, p.id); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE builds SET reap_time = now() WHERE id IN (`+filter+`)`, buildIDs, p.teamID, p.id); err != nil {
		return err
	}
	return tx.Commit()
}
