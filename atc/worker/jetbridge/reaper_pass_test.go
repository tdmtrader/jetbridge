package jetbridge_test

// Whole reaper passes over destroying containers in a real database: what one
// pod or one step Job the web may not delete does to the rest of the pass, in
// either step runtime.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/gc"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
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

var _ = Describe("a reaper pass", func() {
	const namespace = "test-ns"
	const workerName = "k8s-test-ns"

	var (
		ctx       context.Context
		database  jetbridgeDB
		dbWorker  db.Worker
		clientset *fake.Clientset
		logger    *lagertest.TestLogger
	)

	BeforeEach(func() {
		ctx = context.Background()
		database = useJetbridgeDB()
		var err error
		dbWorker, err = persistNamedWorker(database, workerName)
		Expect(err).ToNot(HaveOccurred())
		clientset = fake.NewSimpleClientset()
		logger = lagertest.NewTestLogger("reaper")
	})

	newReaper := func(stepRuntime string) *jetbridge.Reaper {
		cfg := jetbridge.NewConfig(namespace, "")
		cfg.StepRuntime = stepRuntime
		destroyer := gc.NewDestroyer(lagertest.NewTestLogger("destroyer"), database.ContainerRepository, database.VolumeRepository)
		return jetbridge.NewReaper(logger, clientset, cfg, database.ContainerRepository, destroyer)
	}

	// destroying records a container the DB has marked for destruction and
	// returns its handle.
	containers := 0
	destroying := func() string {
		GinkgoHelper()
		containers++
		handle := fmt.Sprintf("step-%d", containers)
		creating, err := dbWorker.CreateContainer(db.NewFixedHandleContainerOwner(handle), db.ContainerMetadata{Type: db.ContainerTypeTask})
		Expect(err).ToNot(HaveOccurred())
		created, err := creating.Created()
		Expect(err).ToNot(HaveOccurred())
		_, err = created.Destroying()
		Expect(err).ToNot(HaveOccurred())
		return created.Handle()
	}

	stepPod := func(handle string, owners ...metav1.OwnerReference) {
		GinkgoHelper()
		_, err := clientset.CoreV1().Pods(namespace).Create(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
			Name:            handle,
			Namespace:       namespace,
			Labels:          map[string]string{"concourse.ci/worker": workerName, "concourse.ci/handle": handle},
			OwnerReferences: owners,
		}}, metav1.CreateOptions{})
		Expect(err).ToNot(HaveOccurred())
	}

	// leftoverStepJob is a step Job a web under the job runtime left behind,
	// with the pod it owns, in destroying.
	leftoverStepJob := func() string {
		GinkgoHelper()
		handle := destroying()
		_, err := clientset.BatchV1().Jobs(namespace).Create(ctx, &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
			Name: handle, Namespace: namespace, UID: types.UID("uid-" + handle),
			Labels: map[string]string{"concourse.ci/worker": workerName},
		}}, metav1.CreateOptions{})
		Expect(err).ToNot(HaveOccurred())
		stepPod(handle, metav1.OwnerReference{APIVersion: "batch/v1", Kind: "Job", Name: handle, UID: types.UID("uid-" + handle)})
		return handle
	}

	forbidJobs := func() {
		forbidden := func(k8stesting.Action) (bool, k8sruntime.Object, error) {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "batch", Resource: "jobs"}, "", errors.New("the Role grants no batch/jobs"))
		}
		clientset.PrependReactor("list", "jobs", forbidden)
		clientset.PrependReactor("delete", "jobs", forbidden)
	}

	podExists := func(name string) bool {
		_, err := clientset.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
		return err == nil
	}

	logged := func(level lager.LogLevel, message string) int {
		n := 0
		for _, entry := range logger.Logs() {
			if entry.LogLevel == level && strings.HasSuffix(entry.Message, message) {
				n++
			}
		}
		return n
	}

	errorsLogged := func() []string {
		var messages []string
		for _, entry := range logger.Logs() {
			if entry.LogLevel == lager.ERROR {
				messages = append(messages, entry.Message)
			}
		}
		return messages
	}

	Context("under the exec runtime with no batch/jobs grant", func() {
		It("reaps every other container, and says once that step Jobs are not collected", func() {
			forbidJobs()
			leftover := leftoverStepJob()
			first, second := destroying(), destroying()
			stepPod(first)
			stepPod(second)
			reaper := newReaper(jetbridge.StepRuntimeExec)

			Expect(reaper.Run(ctx)).To(Succeed())
			Expect(reaper.Run(ctx)).To(Succeed())

			Expect(podExists(first)).To(BeFalse())
			Expect(podExists(second)).To(BeFalse())
			Expect(podExists(leftover)).To(BeTrue(), "a Job's pod is never deleted on its own")
			Expect(errorsLogged()).To(BeEmpty())
			Expect(logged(lager.INFO, "step-jobs-not-collected")).To(Equal(1))
		})
	})

	Context("under the exec runtime with batch/jobs list and delete", func() {
		It("collects a step Job left behind by the job runtime", func() {
			leftover := leftoverStepJob()
			var propagation *metav1.DeletionPropagation
			clientset.PrependReactor("delete", "jobs", func(action k8stesting.Action) (bool, k8sruntime.Object, error) {
				propagation = action.(k8stesting.DeleteAction).GetDeleteOptions().PropagationPolicy
				return false, nil, nil
			})

			Expect(newReaper(jetbridge.StepRuntimeExec).Run(ctx)).To(Succeed())

			_, err := clientset.BatchV1().Jobs(namespace).Get(ctx, leftover, metav1.GetOptions{})
			Expect(apierrors.IsNotFound(err)).To(BeTrue(), "the leftover Job survived: %v", err)
			// The pod goes with its Job: the cluster's garbage collector takes
			// it, which the fake clientset does not run.
			Expect(propagation).ToNot(BeNil())
			Expect(*propagation).To(Equal(metav1.DeletePropagationBackground))
			Expect(errorsLogged()).To(BeEmpty())
		})
	})

	Context("when one pod cannot be deleted", func() {
		It("reaps the others and reports the failure at the end of the pass", func() {
			stuck, first, second := destroying(), destroying(), destroying()
			for _, handle := range []string{stuck, first, second} {
				stepPod(handle)
			}
			clientset.PrependReactor("delete", "pods", func(action k8stesting.Action) (bool, k8sruntime.Object, error) {
				if action.(k8stesting.DeleteAction).GetName() == stuck {
					return true, nil, apierrors.NewInternalError(errors.New("etcd is unwell"))
				}
				return false, nil, nil
			})

			err := newReaper(jetbridge.StepRuntimeExec).Run(ctx)

			Expect(err).To(MatchError(ContainSubstring(stuck)))
			Expect(podExists(first)).To(BeFalse())
			Expect(podExists(second)).To(BeFalse())
			Expect(podExists(stuck)).To(BeTrue())
			Expect(logged(lager.ERROR, "failed-to-delete-pod")).To(Equal(1))
		})
	})

	Context("under the job runtime", func() {
		It("reaps a pod-less step Job as before", func() {
			_, err := clientset.BatchV1().Jobs(namespace).Create(ctx, &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
				Name: "stale-no-pod", Namespace: namespace, UID: "u1",
				CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Hour)),
				Labels:            map[string]string{"concourse.ci/worker": workerName},
			}}, metav1.CreateOptions{})
			Expect(err).ToNot(HaveOccurred())

			Expect(newReaper(jetbridge.StepRuntimeJob).Run(ctx)).To(Succeed())

			_, err = clientset.BatchV1().Jobs(namespace).Get(ctx, "stale-no-pod", metav1.GetOptions{})
			Expect(apierrors.IsNotFound(err)).To(BeTrue(), "the pod-less Job survived: %v", err)
		})

		It("keeps a batch/jobs refusal an error", func() {
			forbidJobs()
			leftover := leftoverStepJob()
			other := destroying()
			stepPod(other)

			err := newReaper(jetbridge.StepRuntimeJob).Run(ctx)

			Expect(err).To(MatchError(ContainSubstring(leftover)))
			Expect(podExists(other)).To(BeFalse())
			Expect(logged(lager.ERROR, "failed-to-delete-pod")).To(Equal(1))
			Expect(logged(lager.ERROR, "failed-to-list-step-jobs")).To(Equal(1))
			Expect(logged(lager.INFO, "step-jobs-not-collected")).To(Equal(0))
		})
	})
})
