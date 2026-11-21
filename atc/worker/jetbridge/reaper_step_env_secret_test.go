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
	"github.com/concourse/concourse/atc/worker/jetbridge"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
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
		stepPod(literal, nil)
		destroying(db.ContainerTypeCheck)
		live := container(db.ContainerTypeTask).Handle()
		stepPod(live, map[string]string{secretAnnotation: "step-env-" + live})

		Expect(reaper.Run(ctx)).To(Succeed())

		Expect(secretCalls()).To(BeEmpty())
	})

	It("asks for a departing handle's Secret once, never lists one, and reads none", func() {
		podless := destroying(db.ContainerTypeTask)
		// The destroying row of a podless container is dropped in the same
		// pass; a long-missing one stays for the collector.
		long := container(db.ContainerTypeTask).Handle()
		missingFor(long, 10*time.Minute)

		Expect(reaper.Run(ctx)).To(Succeed())
		Expect(reaper.Run(ctx)).To(Succeed())

		Expect(secretCalls()).To(Equal(map[string]int{"delete": 2}), "podless %s, long-missing %s", podless, long)
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
