package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"

	"github.com/concourse/concourse/atc"
)

// ResourceCaptureBuildAssociation is the server-derived proof that a build is
// executing inside a server-owned resource-capture run. It carries no
// caller-supplied data: every field is read back out of the ownership chain
// the capture adapter created through CreateRunForServerTemplate.
type ResourceCaptureBuildAssociation struct {
	TemplatePipelineID int
	TemplateName       string
}

var resourceCaptureTemplateNamePattern = regexp.MustCompile(
	`^` + atc.ResourceCaptureTemplatePrefix + `[0-9a-f]{24}-[0-9a-f]{12}$`,
)

func (association ResourceCaptureBuildAssociation) Validate() error {
	if association.TemplatePipelineID <= 0 {
		return fmt.Errorf("db: resource-capture association has no template pipeline")
	}
	if !resourceCaptureTemplateNamePattern.MatchString(association.TemplateName) {
		return fmt.Errorf("db: resource-capture association template name is not the reserved server-owned shape")
	}
	return nil
}

// ResourceCaptureTemplateAssociation re-establishes, for this build, the same
// ownership chain that FindResourceCaptureOutput requires before it will hand
// back a capture's snapshot: the build's pipeline is the run instance of a
// template pipeline registered in agent_workflow_run_templates, under the
// reserved capture name, with no durable workflow attached.
//
// Nothing here is authored: agent_workflow_run_templates is written only by
// trusted server composition, ordinary CreateRun refuses workflow-owned
// pipelines, and pipeline_runs has no public write surface. A user who copies
// the rendered capture job into their own pipeline therefore gets no
// association at all, and the capture authority in their plan stays inert.
func (b *build) ResourceCaptureTemplateAssociation() (ResourceCaptureBuildAssociation, bool, error) {
	var association ResourceCaptureBuildAssociation
	err := b.conn.QueryRowContext(context.Background(), `
		SELECT template.id, template.name
		FROM builds b
		JOIN jobs job ON job.id = b.job_id
		JOIN pipelines instance ON instance.id = b.pipeline_id
		JOIN pipeline_runs run ON run.instance_pipeline_id = instance.id
		JOIN pipelines template ON template.id = run.template_pipeline_id
		JOIN agent_workflow_run_templates owned ON owned.pipeline_id = template.id
		WHERE b.id = $1
		  AND job.name = $2
		  AND template.team_id = b.team_id
		  AND template.template = true
		  AND template.instance_vars IS NULL
		  AND template.archived = false
		  AND instance.name = template.name
		  AND instance.team_id = template.team_id
		  AND instance.instance_vars IS NOT NULL
		  AND template.name ~ ('^' || $3 || '[0-9a-f]{24}-[0-9a-f]{12}$')
		  AND NOT EXISTS (
		    SELECT 1 FROM agent_workflow_runs workflow_run
		    WHERE workflow_run.template_pipeline_id = template.id
		  )
	`, b.id, atc.ResourceCaptureJobName, atc.ResourceCaptureTemplatePrefix).Scan(
		&association.TemplatePipelineID, &association.TemplateName,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ResourceCaptureBuildAssociation{}, false, nil
	}
	if err != nil {
		return ResourceCaptureBuildAssociation{}, false, err
	}
	if err := association.Validate(); err != nil {
		return ResourceCaptureBuildAssociation{}, false, err
	}
	return association, true, nil
}
