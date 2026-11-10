package jetbridge

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
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

// An abandoned job step deletes its own Job and only that: a Job of the
// same name created since (another invocation on the handle) has another
// UID, the API server refuses the delete on the UID precondition, and the
// refusal is not an error -- it was never this step's Job.
func TestAnUnfinishedJobStepDeletesOnlyItsOwnJob(t *testing.T) {
	for _, tc := range []struct {
		name     string
		replaced bool
	}{
		{"its own Job", false},
		{"a Job of the same name made since", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clientset := fake.NewSimpleClientset()
			jobControllerDouble(clientset, func(job *batchv1.Job) *corev1.Pod {
				return &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{Name: job.Name + "-abcde", UID: types.UID("pod-uid-" + job.Name)},
					Spec:       corev1.PodSpec{NodeName: "node-1", Containers: []corev1.Container{{Name: mainContainerName}}},
					Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{
						Name: mainContainerName, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
					}}},
				}
			})
			// The API server's UID precondition, which the fake clientset
			// ignores (as in reaper_pass_test.go).
			var preconditions []string
			clientset.PrependReactor("delete", "jobs", func(action k8stesting.Action) (bool, k8sruntime.Object, error) {
				del := action.(k8stesting.DeleteAction)
				uid := ""
				if p := del.GetDeleteOptions().Preconditions; p != nil && p.UID != nil {
					uid = string(*p.UID)
				}
				preconditions = append(preconditions, uid)
				obj, err := clientset.Tracker().Get(batchv1.SchemeGroupVersion.WithResource("jobs"), del.GetNamespace(), del.GetName())
				if err == nil && uid != "" && string(obj.(*batchv1.Job).UID) != uid {
					return true, nil, apierrors.NewConflict(schema.GroupResource{Group: "batch", Resource: "jobs"}, del.GetName(),
						fmt.Errorf("Precondition failed: UID in precondition: %s, UID in object meta: %s", uid, obj.(*batchv1.Job).UID))
				}
				return false, nil, nil
			})

			container := jobRuntimeContainer(t, clientset, db.ContainerTypeTask, runtime.ContainerSpec{Dir: "/tmp/build/task"})
			logger := lagertest.NewTestLogger("cleanup")
			ctx, abort := context.WithCancel(lagerctx.NewContext(context.Background(), logger))
			defer abort()
			process, err := container.Run(ctx, runtime.ProcessSpec{Path: "sh", Args: []string{"-c", "sleep 60"}},
				runtime.ProcessIO{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			jobs := batchv1.SchemeGroupVersion.WithResource("jobs")
			if tc.replaced {
				// Another invocation replaced the Job under the same name.
				if err := clientset.Tracker().Delete(jobs, "test-ns", container.podName); err != nil {
					t.Fatal(err)
				}
				replacement := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: container.podName, Namespace: "test-ns", UID: "uid-replacement"}}
				if err := clientset.Tracker().Add(replacement); err != nil {
					t.Fatal(err)
				}
			}

			done := make(chan error, 1)
			go func() {
				_, err := process.Wait(ctx)
				done <- err
			}()
			time.Sleep(200 * time.Millisecond)
			abort()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("Wait did not return after the abort")
			}

			if len(preconditions) != 1 || preconditions[0] != "uid-"+container.podName {
				t.Errorf("delete preconditions = %q, want the UID of the Job this step created", preconditions)
			}
			job, err := clientset.BatchV1().Jobs("test-ns").Get(context.Background(), container.podName, metav1.GetOptions{})
			if tc.replaced {
				if err != nil || job.UID != "uid-replacement" {
					t.Errorf("the replacement Job was deleted (%v)", err)
				}
			} else if !apierrors.IsNotFound(err) {
				t.Errorf("the step's own Job survived its abandonment (%v)", err)
			}
			for _, entry := range logger.Logs() {
				if entry.LogLevel == lager.ERROR && strings.HasSuffix(entry.Message, "failed-to-delete-unfinished-step-job") {
					t.Errorf("a Job that was not the step's was reported as a failed delete: %v", entry.Data)
				}
			}
		})
	}
}
