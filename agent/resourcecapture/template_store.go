package resourcecapture

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/agent/workflowrun"
)

type ImmutableTemplateSaver interface {
	SaveOrReuse(context.Context, workflowrun.AdmissionContext, workflowrun.ImmutableTemplateSpec) (workflowrun.WorkflowRunTemplateRef, error)
}

// ImmutableTemplateStore adapts resource captures to the existing
// server-owned immutable-template registry. The registry's historical name is
// workflow-specific; its ownership and create-only guarantees are generic.
type ImmutableTemplateStore struct {
	saver ImmutableTemplateSaver
}

func NewTemplateStore(saver ImmutableTemplateSaver) (*ImmutableTemplateStore, error) {
	if isNil(saver) {
		return nil, fmt.Errorf("resource capture: immutable template saver is required")
	}
	return &ImmutableTemplateStore{saver: saver}, nil
}

func (store *ImmutableTemplateStore) SaveOrReuse(ctx context.Context, spec TemplateSpec) (TemplateRef, error) {
	if err := contextError(ctx); err != nil {
		return TemplateRef{}, err
	}
	if spec.TeamID <= 0 || spec.TeamName == "" || spec.Name == "" || spec.OperationKey == "" || len(spec.CanonicalJSON) == 0 {
		return TemplateRef{}, fmt.Errorf("resource capture: invalid immutable template spec")
	}
	canonical, err := spec.Config.CanonicalJSON()
	if err != nil || !bytes.Equal(canonical, spec.CanonicalJSON) {
		return TemplateRef{}, fmt.Errorf("resource capture: immutable template canonical bytes mismatch")
	}
	digest := sha256.Sum256(canonical)
	if spec.FullHash != hex.EncodeToString(digest[:]) {
		return TemplateRef{}, fmt.Errorf("resource capture: immutable template hash mismatch")
	}
	fullHash, err := workflow.TargetConfigHash(spec.Config)
	if err != nil {
		return TemplateRef{}, fmt.Errorf("resource capture: immutable template hash: %w", err)
	}
	ref, err := store.saver.SaveOrReuse(ctx, workflowrun.AdmissionContext{
		TeamID: spec.TeamID, TeamName: spec.TeamName, CreatedBy: "jetbridge",
		Origin: workflowrun.Origin{Kind: AdapterResourceVersion, Reference: spec.OperationKey},
	}, workflowrun.ImmutableTemplateSpec{
		Name: spec.Name, FullHash: fullHash, CanonicalJSON: append([]byte(nil), canonical...), Config: spec.Clone().Config,
	})
	if err != nil {
		return TemplateRef{}, err
	}
	if ref.PipelineID <= 0 || ref.TeamID != spec.TeamID || ref.Name != spec.Name || ref.FullHash != fullHash || ref.ConfigVersion <= 0 {
		return TemplateRef{}, fmt.Errorf("resource capture: immutable template saver returned a mismatched template")
	}
	return TemplateRef{ID: ref.PipelineID, TeamID: ref.TeamID, Name: ref.Name, ConfigVersion: ref.ConfigVersion, FullHash: ref.FullHash}, nil
}
