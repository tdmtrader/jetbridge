package main

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The walk's DaemonSet adapter, read as a pure function of what the API
// returns. Which status fields it takes and how it reads a pod's readiness are
// the adapter's whole decision, and the walk's own tests see only its answer.

func daemonSet(desired, updated, ready, available int32) appsv1.DaemonSet {
	return appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: "concourse-hangar-output-daemon"},
		Status: appsv1.DaemonSetStatus{
			DesiredNumberScheduled: desired,
			UpdatedNumberScheduled: updated,
			NumberReady:            ready,
			NumberAvailable:        available,
		},
	}
}

func pod(name string, conditions ...corev1.PodCondition) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status:     corev1.PodStatus{Conditions: conditions},
	}
}

func condition(kind corev1.PodConditionType, status corev1.ConditionStatus) corev1.PodCondition {
	return corev1.PodCondition{Type: kind, Status: status}
}

func TestTheDaemonSetCountsAreTheDesiredUpdatedAndReadyOnes(t *testing.T) {
	// Available is deliberately different from every other count: it adds
	// minReadySeconds, a pacing knob, and must not stand in for ready.
	readiness := daemonSetReadiness(daemonSet(3, 2, 1, 0), nil)
	if readiness.Name != "concourse-hangar-output-daemon" {
		t.Errorf("the readiness names %q", readiness.Name)
	}
	if readiness.Desired != 3 || readiness.Updated != 2 || readiness.Ready != 1 {
		t.Errorf("read desired=%d updated=%d ready=%d from desiredNumberScheduled=3 "+
			"updatedNumberScheduled=2 numberReady=1", readiness.Desired, readiness.Updated,
			readiness.Ready)
	}
}

// The generation pair is carried as it is, so the walk can tell a status that
// describes the current template from one left over from the previous.
func TestTheDaemonSetGenerationAndItsObservedGenerationAreBothCarried(t *testing.T) {
	set := daemonSet(2, 2, 2, 2)
	set.Generation = 5
	set.Status.ObservedGeneration = 4
	readiness := daemonSetReadiness(set, nil)
	if readiness.Generation != 5 || readiness.ObservedGeneration != 4 {
		t.Errorf("read generation %d observed %d from metadata.generation=5 "+
			"status.observedGeneration=4", readiness.Generation, readiness.ObservedGeneration)
	}
}

func TestAMemberIsReadyOnlyByItsReadyConditionAndNotWhileTerminating(t *testing.T) {
	terminating := pod("leaving", condition(corev1.PodReady, corev1.ConditionTrue))
	now := metav1.Now()
	terminating.DeletionTimestamp = &now

	readiness := daemonSetReadiness(daemonSet(2, 2, 2, 2), []corev1.Pod{
		pod("ready", condition(corev1.PodScheduled, corev1.ConditionTrue),
			condition(corev1.PodReady, corev1.ConditionTrue)),
		pod("not-ready", condition(corev1.PodReady, corev1.ConditionFalse)),
		pod("scheduled-only", condition(corev1.PodScheduled, corev1.ConditionTrue)),
		pod("no-conditions"),
		terminating,
	})

	want := map[string]bool{"ready": true, "not-ready": false, "scheduled-only": false,
		"no-conditions": false, "leaving": false}
	if len(readiness.Members) != len(want) {
		t.Fatalf("read %d members from %d pods", len(readiness.Members), len(want))
	}
	for _, member := range readiness.Members {
		if member.Ready != want[member.Pod] {
			t.Errorf("pod %s read as Ready=%v", member.Pod, member.Ready)
		}
	}
}
