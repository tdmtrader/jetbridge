package jetbridge

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

// PreferredStepNodeWeight is the weight of the preferred-node term on every
// step pod. It is the scheduler's maximum and equal to the input-locality term
// BuildAffinity adds, deliberately: when the two disagree -- an input was
// produced on another node -- neither wins on node affinity alone, and the
// resource-fit score (the emptier node) breaks the tie. A lower weight would let
// input locality keep every build chain on whichever node its first get landed.
const PreferredStepNodeWeight = 100

// StepNodeLabel is a node label step pods are steered by: preferred (see
// Config.PreferredStepNode) or required (Config.RequiredStepNode).
type StepNodeLabel struct {
	Key   string
	Value string
}

// ParseStepNodeLabel parses `key=value`, for example
// `kubernetes.io/hostname=k3s-agent-y`.
func ParseStepNodeLabel(value string) (StepNodeLabel, error) {
	key, labelValue, ok := strings.Cut(value, "=")
	if !ok || key == "" {
		return StepNodeLabel{}, fmt.Errorf("want key=value, got %q", value)
	}
	if errs := validation.IsQualifiedName(key); len(errs) > 0 {
		return StepNodeLabel{}, fmt.Errorf("label key %q: %s", key, strings.Join(errs, "; "))
	}
	if errs := validation.IsValidLabelValue(labelValue); len(errs) > 0 {
		return StepNodeLabel{}, fmt.Errorf("label value %q: %s", labelValue, strings.Join(errs, "; "))
	}
	return StepNodeLabel{Key: key, Value: labelValue}, nil
}

func (l StepNodeLabel) requirement() corev1.NodeSelectorRequirement {
	return corev1.NodeSelectorRequirement{Key: l.Key, Operator: corev1.NodeSelectorOpIn, Values: []string{l.Value}}
}

func (l StepNodeLabel) preferredTerm() corev1.PreferredSchedulingTerm {
	return corev1.PreferredSchedulingTerm{
		Weight:     PreferredStepNodeWeight,
		Preference: corev1.NodeSelectorTerm{MatchExpressions: []corev1.NodeSelectorRequirement{l.requirement()}},
	}
}
