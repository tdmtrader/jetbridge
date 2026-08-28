package steps

import (
	"fmt"

	"github.com/brine-dev/brine-go/pkg/brine"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"
)

// ReaperGapDefinitions closes the three places where reaper_test.go was
// STRONGER than brine, found by mutating reaper.go and watching only the
// ginkgo suite go red, plus one place where neither suite looked.
//
// Every double here is a working one. The API server really does return
// NotFound for a pod that is already gone, and really does refuse to list when
// it cannot be reached; nothing below asserts on a call, only on what the
// reaper does afterwards.

func ReaperGapDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		// Blocker 1. Retention has two halves: the pod survives, and the row
		// that tracks it survives. Dropping
		// `remainingPods = append(remainingPods, retained...)` keeps the pod
		// and loses the row, and brine asserted only the pod.
		//
		// Not a combinator, the negative ones included: the parameter names a
		// POD, while any collection a CheckNotMember could search is container
		// handles, and the two are joined only by a label this step has to
		// read off the pod first. It also asserts three things in sequence —
		// the pod is there, its row is there, its row is not marked missing —
		// so a failure can say which one went, and each of the last two
		// carries the consequence a resumed build pays for it.
		brine.DefineCheck[ReaperOutcome](
			"the container behind the pod {string} is not marked as missing",
			func(in ReaperOutcome, p brine.Params, _ *brine.Recorder) error {
				name, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a pod name parameter")
				}
				pod, err := in.Ready.Clientset.CoreV1().Pods(in.Ready.Config.Namespace).
					Get(in.Ready.Ctx, name, metav1.GetOptions{})
				if err != nil {
					return fmt.Errorf("expected pod %q to still be there: %w", name, err)
				}
				handle := pod.Labels["concourse.ci/handle"]
				if handle == "" {
					return fmt.Errorf("pod %q carries no handle label", name)
				}
				row, found, err := in.Ready.containerRow(handle)
				if err != nil {
					return err
				}
				if !found {
					return fmt.Errorf(
						"the container row for pod %q (handle %q) is gone while the pod is still "+
							"there — a resumed build reads the exit-status annotation off that pod "+
							"and has no row to attach to, so it re-executes a step that already "+
							"finished", name, handle)
				}
				if row.MissingSince.Valid {
					return fmt.Errorf(
						"the container behind pod %q (handle %q) is marked missing while its pod "+
							"is still running — the next sweep destroys the row", name, handle)
				}
				return nil
			},
		),

		// Namespaces are shared. Listing every pod rather than this worker's
		// own is not a counting error: an unrecognised pod has no container
		// row, so DestroyUnknownContainers marks it destroying and the delete
		// loop removes it. That is one Concourse worker deleting another
		// worker's running builds.
		brine.DefineMap[ReaperReady, ReaperReady](
			"a pod {string} belonging to another worker is running in the same namespace",
			func(in ReaperReady, p brine.Params, _ *brine.Recorder) (ReaperReady, error) {
				name, ok := p.GetString(0)
				if !ok {
					return ReaperReady{}, fmt.Errorf("expected a pod name parameter")
				}
				pod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      name,
						Namespace: in.Config.Namespace,
						Labels:    map[string]string{"concourse.ci/worker": "k8s-somebody-else"},
					},
					Status: corev1.PodStatus{Phase: corev1.PodRunning},
				}
				_, err := in.Clientset.CoreV1().Pods(in.Config.Namespace).
					Create(in.Ctx, pod, metav1.CreateOptions{})
				if err != nil {
					return ReaperReady{}, fmt.Errorf("create foreign pod %q: %w", name, err)
				}
				return in, nil
			},
		),

		// Blocker 3. The pod vanished between the list and the delete. NotFound
		// is what the API server returns, and the reaper has to treat it as
		// routine rather than failing the whole sweep.
		brine.DefineMap[ReaperReady, ReaperReady](
			"the pod is deleted by someone else before the reaper gets to it",
			func(in ReaperReady, _ brine.Params, _ *brine.Recorder) (ReaperReady, error) {
				in.Clientset.PrependReactor("delete", "pods",
					func(action k8stesting.Action) (bool, apiruntime.Object, error) {
						name := ""
						if d, ok := action.(k8stesting.DeleteAction); ok {
							name = d.GetName()
						}
						return true, nil, apierrors.NewNotFound(corev1.Resource("pods"), name)
					})
				return in, nil
			},
		),

		// Neither suite looked here: a sweep that cannot list pods has swept
		// nothing, and reporting success makes it look healthy to the component
		// runner while the cluster fills up.
		brine.DefineMap[ReaperReady, ReaperReady](
			"the cluster stops answering when the reaper lists pods",
			func(in ReaperReady, _ brine.Params, _ *brine.Recorder) (ReaperReady, error) {
				in.Clientset.PrependReactor("list", "pods",
					func(k8stesting.Action) (bool, apiruntime.Object, error) {
						return true, nil, fmt.Errorf("connection refused")
					})
				return in, nil
			},
		),

		CheckThat[ReaperOutcome]("the reaper reports that it could not sweep",
			func(in ReaperOutcome) error {
				if in.Err == nil {
					return fmt.Errorf(
						"expected the reaper to report a failure, it reported success — a sweep " +
							"that silently did nothing looks healthy to the component runner while " +
							"pods accumulate unreclaimed")
				}
				return nil
			}),
	}
}
