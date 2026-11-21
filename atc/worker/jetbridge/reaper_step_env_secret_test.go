package jetbridge_test

// The reaper's backstop for a task step's params Secret: the one a web left
// without an owner, by dying between making it and making the pod, or
// between the pod and pointing the Secret at it.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagertest"
	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/gc"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

var _ = Describe("the reaper and a task step's params Secret", func() {
	const namespace = "test-ns"
	const workerName = "k8s-test-ns"
	const secretAnnotation = "concourse.ci/step-env-secret"

	var (
		ctx       context.Context
		database  jetbridgeDB
		dbWorker  db.Worker
		clientset *fake.Clientset
		logger    *lagertest.TestLogger
		reaper    *jetbridge.Reaper
		made      int
	)

	BeforeEach(func() {
		ctx = context.Background()
		database = useJetbridgeDB()
		var err error
		dbWorker, err = persistNamedWorker(database, workerName)
		Expect(err).ToNot(HaveOccurred())
		clientset = fake.NewSimpleClientset()
		logger = lagertest.NewTestLogger("reaper")
		destroyer := gc.NewDestroyer(lagertest.NewTestLogger("destroyer"), database.ContainerRepository, database.VolumeRepository)
		reaper = jetbridge.NewReaper(logger, clientset, jetbridge.NewConfig(namespace, ""), database.ContainerRepository, destroyer)
	})

	container := func(kind db.ContainerType) db.CreatedContainer {
		GinkgoHelper()
		made++
		creating, err := dbWorker.CreateContainer(db.NewFixedHandleContainerOwner(fmt.Sprintf("step-%d", made)), db.ContainerMetadata{Type: kind})
		Expect(err).ToNot(HaveOccurred())
		created, err := creating.Created()
		Expect(err).ToNot(HaveOccurred())
		return created
	}
	destroying := func(kind db.ContainerType) string {
		GinkgoHelper()
		created := container(kind)
		_, err := created.Destroying()
		Expect(err).ToNot(HaveOccurred())
		return created.Handle()
	}
	missingFor := func(handle string, d time.Duration) {
		GinkgoHelper()
		_, err := sq.StatementBuilder.PlaceholderFormat(sq.Dollar).Update("containers").
			Set("missing_since", time.Now().Add(-d)).
			Where(sq.Eq{"handle": handle}).RunWith(database.Conn).Exec()
		Expect(err).ToNot(HaveOccurred())
	}
	secret := func(handle string) {
		GinkgoHelper()
		_, err := clientset.CoreV1().Secrets(namespace).Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
			Name: "step-env-" + handle, Namespace: namespace,
		}}, metav1.CreateOptions{})
		Expect(err).ToNot(HaveOccurred())
	}
	secretExists := func(handle string) bool {
		_, err := clientset.CoreV1().Secrets(namespace).Get(ctx, "step-env-"+handle, metav1.GetOptions{})
		return err == nil
	}
	stepPod := func(handle string, annotations map[string]string) {
		GinkgoHelper()
		_, err := clientset.CoreV1().Pods(namespace).Create(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
			Name: handle, Namespace: namespace, Annotations: annotations,
			Labels: map[string]string{"concourse.ci/worker": workerName, "concourse.ci/handle": handle},
		}}, metav1.CreateOptions{})
		Expect(err).ToNot(HaveOccurred())
	}
	secretCalls := func() map[string]int {
		calls := map[string]int{}
		for _, action := range clientset.Actions() {
			if action.GetResource().Resource == "secrets" {
				calls[action.GetVerb()]++
			}
		}
		return calls
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

	It("deletes the Secret of a destroying task container that has no pod, and of one whose pod never came to own it", func() {
		podless := destroying(db.ContainerTypeTask)
		secret(podless)
		unowned := destroying(db.ContainerTypeTask)
		secret(unowned)
		stepPod(unowned, map[string]string{secretAnnotation: "step-env-" + unowned})

		Expect(reaper.Run(ctx)).To(Succeed())

		Expect(secretExists(podless)).To(BeFalse(), "a Secret whose pod was never made outlived its container")
		Expect(secretExists(unowned)).To(BeFalse(), "an unowned Secret outlived its container")
		Expect(errorsLogged()).To(BeEmpty())
	})

	It("deletes the Secret of a task container missing for longer than the grace, which the database drops without a destroying pass", func() {
		long := container(db.ContainerTypeTask).Handle()
		secret(long)
		missingFor(long, 10*time.Minute)
		// Missing only a moment: its pod may be being made right now.
		recent := container(db.ContainerTypeTask).Handle()
		secret(recent)
		missingFor(recent, time.Second)

		Expect(reaper.Run(ctx)).To(Succeed())

		Expect(secretExists(long)).To(BeFalse())
		Expect(secretExists(recent)).To(BeTrue(), "the Secret of a container whose pod may be being made was taken")
	})

	It("calls nothing for a pod that reads no Secret, a check, or a live task container", func() {
		literal := destroying(db.ContainerTypeTask)
		stepPod(literal, map[string]string{secretAnnotation: ""})
		destroying(db.ContainerTypeCheck)
		live := container(db.ContainerTypeTask).Handle()
		stepPod(live, map[string]string{secretAnnotation: "step-env-" + live})

		Expect(reaper.Run(ctx)).To(Succeed())

		Expect(secretCalls()).To(BeEmpty())
	})

	It("asks again on every pass while a handle departs, so a Secret made after an earlier delete is still taken, and never lists or reads one", func() {
		long := container(db.ContainerTypeTask).Handle()
		missingFor(long, 10*time.Minute)
		secret(long)

		Expect(reaper.Run(ctx)).To(Succeed())
		Expect(secretExists(long)).To(BeFalse())

		// A web makes the Secret again, and dies before the pod.
		secret(long)
		Expect(reaper.Run(ctx)).To(Succeed())

		Expect(secretExists(long)).To(BeFalse(), "a Secret made after the first pass's delete outlived its departing container")
		Expect(secretCalls()).To(Equal(map[string]int{"create": 2, "delete": 2, "get": 2}), "only the test's own creates and reads")
	})

	It("never takes the Secret of a handle that has a step Job, which owns it while its pod is still to be made", func() {
		pending := container(db.ContainerTypeTask).Handle()
		missingFor(pending, 5*time.Minute)
		secret(pending)
		_, err := clientset.BatchV1().Jobs(namespace).Create(ctx, &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
			Name: "job-" + pending, Namespace: namespace, CreationTimestamp: metav1.Now(),
			Labels: map[string]string{"concourse.ci/worker": workerName, "concourse.ci/handle": pending},
		}}, metav1.CreateOptions{})
		Expect(err).ToNot(HaveOccurred())

		Expect(reaper.Run(ctx)).To(Succeed())

		Expect(secretExists(pending)).To(BeTrue(), "the Secret of a step Job whose pod is late was taken")
	})

	It("costs no call for a departing container whose pod it saw read no Secret, and asks for one it never saw", func() {
		seen := container(db.ContainerTypeTask).Handle()
		stepPod(seen, map[string]string{secretAnnotation: ""})
		Expect(reaper.Run(ctx)).To(Succeed())
		Expect(clientset.CoreV1().Pods(namespace).Delete(ctx, seen, metav1.DeleteOptions{})).To(Succeed())
		missingFor(seen, 10*time.Minute)
		unseen := container(db.ContainerTypeTask).Handle()
		missingFor(unseen, 10*time.Minute)
		clientset.ClearActions()

		Expect(reaper.Run(ctx)).To(Succeed())

		var deleted []string
		for _, action := range clientset.Actions() {
			if action.GetResource().Resource == "secrets" && action.GetVerb() == "delete" {
				deleted = append(deleted, action.(k8stesting.DeleteAction).GetName())
			}
		}
		Expect(deleted).To(Equal([]string{"step-env-" + unseen}))
	})

	It("asks for the Secret of a handle whose pod an older web made, which says nothing about params", func() {
		old := container(db.ContainerTypeTask).Handle()
		stepPod(old, nil)
		Expect(reaper.Run(ctx)).To(Succeed())
		Expect(clientset.CoreV1().Pods(namespace).Delete(ctx, old, metav1.DeleteOptions{})).To(Succeed())
		missingFor(old, 10*time.Minute)
		// This web replaces the older one's pod, makes the Secret, and dies.
		secret(old)

		Expect(reaper.Run(ctx)).To(Succeed())

		Expect(secretExists(old)).To(BeFalse(), "an older web's pod was taken to say its handle reads no Secret")
	})

	It("keeps a pod-less destroying container for another pass when the step Jobs cannot be listed", func() {
		podless := destroying(db.ContainerTypeTask)
		secret(podless)
		failed := false
		clientset.PrependReactor("list", "jobs", func(k8stesting.Action) (bool, k8sruntime.Object, error) {
			if failed {
				return false, nil, nil
			}
			failed = true
			return true, nil, apierrors.NewServiceUnavailable("the API server is restarting")
		})

		Expect(reaper.Run(ctx)).To(Succeed())
		Expect(secretExists(podless)).To(BeTrue())
		Expect(reaper.Run(ctx)).To(Succeed())

		Expect(secretExists(podless)).To(BeFalse(), "the destroying row was dropped in the pass that could not tell, and its Secret with it")
	})

	It("never lets a task container a web is making a Secret for depart", func() {
		handle := "step-returning"
		worker := jetbridge.NewWorker(dbWorker, clientset, jetbridge.NewConfig(namespace, ""), jetbridge.WorkerDeps{})
		step, _, err := worker.FindOrCreateContainer(ctx, db.NewFixedHandleContainerOwner(handle),
			db.ContainerMetadata{Type: db.ContainerTypeTask},
			runtime.ContainerSpec{TeamID: 1, Type: db.ContainerTypeTask, ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
				Params: []runtime.Param{{Name: "TOKEN", Value: "s3cr3t"}}}, nil)
		Expect(err).ToNot(HaveOccurred())
		// Its pod went while no web watched; the build resumes minutes later.
		missingFor(handle, 10*time.Minute)

		_, err = step.Run(ctx, runtime.ProcessSpec{Path: "/bin/true"}, runtime.ProcessIO{})
		Expect(err).ToNot(HaveOccurred())

		departing, err := database.ContainerRepository.FindDepartingContainers(workerName, db.ContainerTypeTask, time.Minute)
		Expect(err).ToNot(HaveOccurred())
		Expect(departing).ToNot(ContainElement(handle), "a container given a Secret is still departing, so a reaper pass may take it")
	})

	It("says once that it may not delete them, and goes on with the pass", func() {
		clientset.PrependReactor("delete", "secrets", func(k8stesting.Action) (bool, k8sruntime.Object, error) {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "", errors.New("the Role grants no secrets"))
		})
		first, second := destroying(db.ContainerTypeTask), destroying(db.ContainerTypeTask)

		Expect(reaper.Run(ctx)).To(Succeed())

		notes := 0
		for _, entry := range logger.Logs() {
			if strings.HasSuffix(entry.Message, "step-env-secrets-not-collected") {
				notes++
			}
		}
		Expect(notes).To(Equal(1), "%s and %s", first, second)
		Expect(errorsLogged()).To(BeEmpty())
	})
})
