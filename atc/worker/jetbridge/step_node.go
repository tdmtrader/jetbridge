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
//
// Steering a step toward a node also admits it there: every step pod tolerates
// the NoSchedule taint of the same key and value. That is how a node is
// reserved for steps -- label it, taint it key=value:NoSchedule, and name it
// here -- so the cluster's own services schedule elsewhere while builds still
// reach it. Without the taint the toleration matches nothing.
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

func (l StepNodeLabel) toleration() corev1.Toleration {
	return corev1.Toleration{Key: l.Key, Operator: corev1.TolerationOpEqual, Value: l.Value, Effect: corev1.TaintEffectNoSchedule}
}

// buildTolerations is what a step pod tolerates: the taints of the nodes it
// is steered to, then Config.StepTolerations. Nil when there are none.
func (c *Container) buildTolerations() []corev1.Toleration {
	var tolerations []corev1.Toleration
	add := func(t corev1.Toleration) {
		for _, have := range tolerations {
			if have.MatchToleration(&t) {
				return
			}
		}
		tolerations = append(tolerations, t)
	}
	for _, label := range []*StepNodeLabel{c.config.PreferredStepNode, c.config.RequiredStepNode} {
		if label != nil {
			add(label.toleration())
		}
	}
	for _, t := range c.config.StepTolerations {
		add(t)
	}
	return tolerations
}
