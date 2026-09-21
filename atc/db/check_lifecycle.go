package db

import "code.cloudfoundry.org/lager/v3"

var CheckDeleteBatchSize = 500

type CheckLifecycle interface {
	DeleteCompletedChecks(logger lager.Logger) error
}

type checkLifecycle struct {
	conn DbConn
}

func NewCheckLifecycle(conn DbConn) CheckLifecycle {
	return &checkLifecycle{
		conn: conn,
	}
}

func (cl *checkLifecycle) DeleteCompletedChecks(logger lager.Logger) error {
	var counter int
	var err1 error
	for {
		var numChecksDeleted int
		// An executed Run check is pinned by its execution evidence, which
		// only deleteClosedRunChecks may remove. Exclude those before batching,
		// so an executed check can neither fail this set-based batch on its
		// evidence foreign key nor starve ordinary check GC. Every other Run
		// check follows the ordinary rules below. The exclusion is race-free:
		// an execution is admitted only to an uncompleted build under that
		// build's row lock, and only completed builds are deleted.
		err1 = cl.conn.QueryRow(`
      WITH resource_builds AS (
        SELECT distinct(last_check_build_id) as build_id
        FROM resource_config_scopes
        WHERE last_check_build_id IS NOT NULL
      ),
      deleted_builds AS (
        DELETE FROM builds USING (
          (SELECT id
          FROM builds b
          WHERE completed AND resource_id IS NOT NULL
          AND NOT EXISTS ( SELECT 1 FROM pipeline_run_executions e WHERE e.build_id = b.id )
          AND NOT EXISTS ( SELECT 1 FROM resource_builds WHERE build_id = b.id )
					LIMIT $1)
            UNION ALL
          SELECT id
          FROM builds b
          WHERE completed AND resource_type_id IS NOT NULL
          AND NOT EXISTS ( SELECT 1 FROM pipeline_run_executions e WHERE e.build_id = b.id )
          AND EXISTS (SELECT * FROM builds b2 WHERE b.resource_type_id = b2.resource_type_id AND b.id < b2.id)
    ) AS deletable_builds WHERE builds.id = deletable_builds.id
      RETURNING builds.id
      ), deleted_events AS (
        DELETE FROM check_build_events USING deleted_builds WHERE build_id = deleted_builds.id
      )
      SELECT COUNT(*) FROM deleted_builds
    `, CheckDeleteBatchSize).Scan(&numChecksDeleted)
		if err1 != nil {
			break
		}
		logger.Debug("deleted-check-builds", lager.Data{"count": numChecksDeleted, "batch": counter})

		if numChecksDeleted < CheckDeleteBatchSize {
			break
		}
		counter++
	}

	errRunChecks := cl.deleteClosedRunChecks(logger)

	// A build whose events these are is still fetchable through the API for as
	// long as it is a resource's current in-memory build or a scope's last
	// check -- `inMemoryCheckBuildForApi.EventPage` resolves it by either. The
	// resource's `in_memory_build_id` advances one transaction before the
	// scope's `last_check_build_id` does, and a scope outlives the resource
	// that ran its last check, so the two are not interchangeable: deleting on
	// the strength of one alone leaves the other resolving to a build with no
	// events, reported as a clean empty `finished` page rather than
	// OUTPUT_RETAINED_AWAY. Keep the events while either still points here.
	_, err2 := cl.conn.Exec(`
      WITH expired_imb_ids AS (
          SELECT distinct(build_id) AS build_id
              FROM check_build_events cbe
              WHERE NOT EXISTS (SELECT 1 FROM resources WHERE in_memory_build_id = cbe.build_id)
                AND NOT EXISTS (SELECT 1 FROM resource_config_scopes WHERE last_check_build_id = cbe.build_id)
                AND NOT EXISTS (SELECT 1 FROM builds WHERE id = cbe.build_id)
      )
      DELETE FROM check_build_events cbe2 USING expired_imb_ids 
          WHERE cbe2.build_id = expired_imb_ids.build_id;
    `)

	if err1 != nil {
		return err1
	}

	if errRunChecks != nil {
		return errRunChecks
	}

	if err2 != nil {
		return err2
	}

	return nil
}
