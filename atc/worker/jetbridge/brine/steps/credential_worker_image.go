package steps

import (
	"fmt"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

// brineCredentialWorkerImage is the operator pin the local credential and
// review fixtures run under. A credential is delivered only into a result
// producer whose snapshotted rootfs_uri is exactly docker:///<pin>, so the
// fixtures pin their producer to it before the Run is created. Nothing pulls
// it: envtest runs no kubelet.
const brineCredentialWorkerImage = "registry.brine.test/review-worker@sha256:" +
	"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// pinProducerImage re-saves template with its first job's first task -- the
// fixtures' one result producer -- running from the pinned worker image, so a
// Run created afterwards snapshots it.
func pinProducerImage(teams db.TeamFactory, template db.Pipeline, image string) (db.Pipeline, error) {
	team, found, err := teams.FindTeam(template.TeamName())
	if err != nil || !found {
		return nil, fmt.Errorf("find template team: %v", err)
	}
	config, err := template.Config()
	if err != nil {
		return nil, err
	}
	if len(config.Jobs) == 0 || len(config.Jobs[0].PlanSequence) == 0 {
		return nil, fmt.Errorf("template %s has no producer to pin", template.Name())
	}
	task, ok := config.Jobs[0].PlanSequence[0].Config.(*atc.TaskStep)
	if !ok || task.Config == nil || task.RunResult == nil {
		return nil, fmt.Errorf("template %s's first step is not an inline result producer", template.Name())
	}
	task.Config.RootfsURI = "docker:///" + image
	pinned, _, err := team.SavePipeline(template.PipelineRef(), config, template.ConfigVersion(), false)
	if err != nil {
		return nil, err
	}
	if got := atc.RunTaskImage(config, task.TaskID); got != "docker:///"+image {
		return nil, fmt.Errorf("pinned producer reports image %q", got)
	}
	return pinned, nil
}
