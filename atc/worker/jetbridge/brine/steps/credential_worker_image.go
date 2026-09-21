package steps

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runs"
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

// digestReviewImage turns the CI-supplied review image into the
// digest-qualified reference a credential pin requires. A digest reference is
// used as given. A tag is resolved against the disposable kubelet that
// imported it, from the runtime's own record of the image's repo digests, so
// the pin names exactly the bytes that kubelet will run. Anything else fails:
// a credential delivery into a tag is refused, and skipping would hide that.
func digestReviewImage(ctx context.Context, image string) (string, error) {
	if runs.ValidateCredentialWorkerImage(image) == nil {
		return image, nil
	}
	cluster := os.Getenv("BRINE_K3S_CONTAINER")
	if cluster == "" {
		return "", fmt.Errorf("BRINE_REVIEW_IMAGE %q is not digest-qualified (repo@sha256:...) and "+
			"BRINE_K3S_CONTAINER names no kubelet to resolve it on; credentials are only delivered into "+
			"a digest-pinned worker image", image)
	}
	out, err := exec.CommandContext(ctx, "docker", "exec", cluster, "crictl", "inspecti", "-o", "json", image).Output()
	if err != nil {
		return "", fmt.Errorf("resolving BRINE_REVIEW_IMAGE %q to a digest on kubelet %s: %w", image, cluster, err)
	}
	var inspected struct {
		Status struct {
			RepoDigests []string `json:"repoDigests"`
		} `json:"status"`
	}
	if err = json.Unmarshal(out, &inspected); err != nil {
		return "", fmt.Errorf("reading kubelet %s's record of %q: %w", cluster, image, err)
	}
	repository := image
	if at := strings.LastIndex(repository, ":"); at > strings.LastIndex(repository, "/") {
		repository = repository[:at]
	}
	for _, digested := range inspected.Status.RepoDigests {
		if strings.HasPrefix(digested, repository+"@") && runs.ValidateCredentialWorkerImage(digested) == nil {
			return digested, nil
		}
	}
	return "", fmt.Errorf("kubelet %s records no %s@sha256 digest for BRINE_REVIEW_IMAGE %q (repo digests %v)",
		cluster, repository, image, inspected.Status.RepoDigests)
}
