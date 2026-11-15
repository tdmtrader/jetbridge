package tests

import (
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// Every pod that runs the chart's own image pulls it from the same registry, so
// each needs image.pullSecrets. A pod left out renders, passes every other test
// here, and then sits in ImagePullBackOff on a private registry.
func TestEveryPodThatRunsTheChartsImagePullsWithItsPullSecrets(t *testing.T) {
	want := []corev1.LocalObjectReference{{Name: "registry-a"}, {Name: "registry-b"}}
	seen := map[string]bool{}

	for _, mode := range typedModes {
		t.Run(mode.name, func(t *testing.T) {
			sets := append(append([]string{}, mode.sets...), "image.pullSecrets[0]=registry-a", "image.pullSecrets[1]=registry-b")
			for _, doc := range documentsIn(t, render(t, sets...)) {
				component, pod := podOf(t, doc)
				if !runsTheChartsImage(pod) {
					continue
				}
				seen[component] = true
				if !slices.Equal(pod.ImagePullSecrets, want) {
					t.Errorf("%s %s runs the chart's image with pull secrets %v, want %v", doc.kind, doc.name, pod.ImagePullSecrets, want)
				}
			}
		})
	}

	for _, component := range []string{"web", "artifact-daemon", "hangar-store", "hangar-bootstrap"} {
		if !seen[component] {
			t.Errorf("no mode rendered a %s pod, so its pull secrets went unchecked", component)
		}
	}
}

func TestNoPodNamesAPullSecretWhenNoneIsSet(t *testing.T) {
	for _, mode := range typedModes {
		t.Run(mode.name, func(t *testing.T) {
			for _, doc := range documentsIn(t, render(t, mode.sets...)) {
				if _, pod := podOf(t, doc); runsTheChartsImage(pod) && pod.ImagePullSecrets != nil {
					t.Errorf("%s %s names pull secrets %v with image.pullSecrets empty", doc.kind, doc.name, pod.ImagePullSecrets)
				}
			}
		})
	}
}

// The required test values set image.repository to concourse-local.
func runsTheChartsImage(pod corev1.PodSpec) bool {
	for _, container := range slices.Concat(pod.InitContainers, pod.Containers) {
		if strings.HasPrefix(container.Image, "concourse-local:") {
			return true
		}
	}
	return false
}
