package atc

import "fmt"

// TaskCacheIdentity identifies either an ordinary job cache or a cache shared
// by materialized jobs from numbered runs of one template.
type TaskCacheIdentity struct {
	JobID              int
	TeamID             int
	TemplatePipelineID int
	RunJobName         string
}

func (identity TaskCacheIdentity) Validate() error {
	ordinary := identity.JobID > 0 && identity.TeamID == 0 && identity.TemplatePipelineID == 0 && identity.RunJobName == ""
	run := identity.JobID == 0 && identity.TeamID > 0 && identity.TemplatePipelineID > 0 && identity.RunJobName != ""
	if ordinary || run {
		return nil
	}

	return fmt.Errorf("task cache identity must contain exactly one complete ordinary or run scope")
}
