package db

import (
	"database/sql"

	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/atc"
)

type usedTaskCache struct {
	id       int
	identity atc.TaskCacheIdentity
	stepName string
	path     string
}

type UsedTaskCache interface {
	ID() int
	Identity() atc.TaskCacheIdentity
	JobID() int
	StepName() string
	Path() string
}

func (tc *usedTaskCache) ID() int                         { return tc.id }
func (tc *usedTaskCache) Identity() atc.TaskCacheIdentity { return tc.identity }
func (tc *usedTaskCache) JobID() int                      { return tc.identity.JobID }
func (tc *usedTaskCache) StepName() string                { return tc.stepName }
func (tc *usedTaskCache) Path() string                    { return tc.path }

func (f usedTaskCache) findOrCreate(tx Tx) (UsedTaskCache, error) {
	utc, found, err := f.find(tx)
	if err != nil || found {
		return utc, err
	}

	columns := []string{"step_name", "path"}
	values := []any{f.stepName, f.path}
	conflict := ""
	if f.identity.JobID != 0 {
		columns = append([]string{"job_id"}, columns...)
		values = append([]any{f.identity.JobID}, values...)
		conflict = "(job_id, step_name, path) WHERE job_id IS NOT NULL DO UPDATE SET job_id = EXCLUDED.job_id"
	} else {
		columns = append([]string{"template_pipeline_id", "run_job_name"}, columns...)
		values = append([]any{f.identity.TemplatePipelineID, f.identity.RunJobName}, values...)
		conflict = "(template_pipeline_id, run_job_name, step_name, path) WHERE template_pipeline_id IS NOT NULL DO UPDATE SET template_pipeline_id = EXCLUDED.template_pipeline_id"
	}

	var id int
	err = psql.Insert("task_caches").Columns(columns...).Values(values...).
		Suffix("ON CONFLICT " + conflict + " RETURNING id").RunWith(tx).QueryRow().Scan(&id)
	if err != nil {
		return nil, err
	}

	utc, _, findErr := f.find(tx)
	return utc, findErr
}

func (f usedTaskCache) find(runner sq.Runner) (UsedTaskCache, bool, error) {
	query := psql.Select("tc.id").From("task_caches tc").Where(sq.Eq{"tc.step_name": f.stepName, "tc.path": f.path})
	if f.identity.JobID != 0 {
		query = query.Where(sq.Eq{"tc.job_id": f.identity.JobID})
	} else {
		query = query.LeftJoin("pipelines template ON template.id = tc.template_pipeline_id").
			Columns("template.team_id").
			Where(sq.Eq{"tc.template_pipeline_id": f.identity.TemplatePipelineID, "tc.run_job_name": f.identity.RunJobName})
	}

	var id int
	var teamID int
	args := []any{&id}
	if f.identity.JobID == 0 {
		args = append(args, &teamID)
	}
	err := query.RunWith(runner).QueryRow().Scan(args...)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, false, nil
		}
		return nil, false, err
	}

	f.id = id
	if f.identity.JobID == 0 {
		f.identity.TeamID = teamID
	}
	return &f, true, nil
}
