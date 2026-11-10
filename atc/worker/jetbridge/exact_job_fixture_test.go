package jetbridge

import (
	"errors"
	"fmt"
	"sync"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// nthJobPod is the name of the pod of the nth Job of a step's name.
func nthJobPod(job string, n int) string {
	if n == 1 {
		return job + "-abcde"
	}
	return fmt.Sprintf("%s-r%d", job, n)
}

// runningJobPod is a step Job's pod running on node, its UID "uid-<name>",
// which is what tokenReviewDouble binds a token to.
func runningJobPod(name, node string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID("uid-" + name)},
		Spec:       corev1.PodSpec{NodeName: node, Containers: []corev1.Container{{Name: mainContainerName}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, StartTime: &metav1.Time{Time: time.Now()}, ContainerStatuses: []corev1.ContainerStatus{{
			Name: mainContainerName, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
		}}},
	}
}

// unschedulableJobPod is a step Job's pod the scheduler could not place.
func unschedulableJobPod(name string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID("uid-" + name)},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: mainContainerName}}},
		Status: corev1.PodStatus{Phase: corev1.PodPending, Conditions: []corev1.PodCondition{{
			Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: "Unschedulable",
			Message: "0/2 nodes are available: node(s) didn't match Pod's node affinity",
		}}},
	}
}

// countingJobController is jobControllerDouble for a step whose Job is made
// more than once: the nth Job of a name gets UID "uid-<name>" for the first
// and "uid-<name>-<n>" after, and the pod podFor describes. A delete honours
// a UID precondition, as the API server does, and takes the Job's pods.
func countingJobController(clientset *fake.Clientset, podFor func(n int, job *batchv1.Job) *corev1.Pod) {
	jobs := schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}
	pods := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	var mu sync.Mutex
	made := map[string]int{}
	clientset.PrependReactor("create", "jobs", func(action k8stesting.Action) (bool, k8sruntime.Object, error) {
		job := action.(k8stesting.CreateAction).GetObject().(*batchv1.Job)
		if _, err := clientset.Tracker().Get(jobs, job.Namespace, job.Name); err == nil {
			return true, nil, apierrors.NewAlreadyExists(schema.GroupResource{Group: "batch", Resource: "jobs"}, job.Name)
		}
		mu.Lock()
		made[job.Name]++
		n := made[job.Name]
		mu.Unlock()
		job.UID = types.UID("uid-" + job.Name)
		if n > 1 {
			job.UID = types.UID(fmt.Sprintf("uid-%s-%d", job.Name, n))
		}
		if err := clientset.Tracker().Add(job); err != nil {
			return true, nil, err
		}
		if pod := podFor(n, job); pod != nil {
			pod.Namespace = job.Namespace
			pod.Labels = map[string]string{jobNameLabelKey: job.Name, jobUIDLabelKey: string(job.UID), runtimeLabelKey: StepRuntimeJob}
			controller := true
			pod.OwnerReferences = []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: job.UID, Controller: &controller}}
			if err := clientset.Tracker().Add(pod); err != nil {
				return true, nil, err
			}
		}
		return true, job, nil
	})
	clientset.PrependReactor("delete", "jobs", func(action k8stesting.Action) (bool, k8sruntime.Object, error) {
		del := action.(k8stesting.DeleteAction)
		obj, err := clientset.Tracker().Get(jobs, del.GetNamespace(), del.GetName())
		if err != nil {
			return false, nil, nil
		}
		uid := obj.(*batchv1.Job).UID
		if p := del.GetDeleteOptions().Preconditions; p != nil && p.UID != nil && *p.UID != uid {
			return true, nil, apierrors.NewConflict(schema.GroupResource{Group: "batch", Resource: "jobs"}, del.GetName(), errors.New("UID precondition failed"))
		}
		listed, _ := clientset.Tracker().List(pods, schema.GroupVersionKind{Version: "v1", Kind: "Pod"}, del.GetNamespace(), metav1.ListOptions{})
		if list, ok := listed.(*corev1.PodList); ok {
			for _, pod := range list.Items {
				if pod.Labels[jobUIDLabelKey] == string(uid) {
					_ = clientset.Tracker().Delete(pods, pod.Namespace, pod.Name)
				}
			}
		}
		return false, nil, nil
	})
}
