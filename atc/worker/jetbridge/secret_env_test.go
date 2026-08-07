package jetbridge_test

import (
	"context"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/vars"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

var _ = Describe("SecretEnv in Pod Spec", func() {
	var (
		fakeClientset *fake.Clientset
		worker        *jetbridge.Worker
		ctx           context.Context
		delegate      runtime.BuildStepDelegate
		database      jetbridgeDB
	)

	BeforeEach(func() {
		database = useJetbridgeDB()
		ctx = context.Background()
		dbWorker, err := persistNamedWorker(database, "k8s-worker-1")
		Expect(err).NotTo(HaveOccurred())
		fakeClientset = fake.NewSimpleClientset()
		cfg := jetbridge.NewConfig("test-namespace", "")
		delegate = &noopDelegate{}
		worker = jetbridge.NewWorker(dbWorker, fakeClientset, cfg)
	})

	It("emits ValueFrom.SecretKeyRef for env vars in SecretEnv", func() {
		handle := "secret-env-handle"

		container, _, err := worker.FindOrCreateContainer(
			ctx,
			db.NewFixedHandleContainerOwner(handle),
			db.ContainerMetadata{Type: db.ContainerTypeTask},
			runtime.ContainerSpec{
				TeamID:   1,
				TeamName: "main",
				Dir:      "/workdir",
				ImageSpec: runtime.ImageSpec{
					ImageURL: "docker:///busybox",
				},
				Env: []string{"DB_PASS=s3cret", "STATIC=hello"},
				SecretEnv: map[string]vars.SecretRef{
					"DB_PASS": {Namespace: "concourse-main", Name: "db-password", Key: "value"},
				},
			},
			delegate,
		)
		Expect(err).ToNot(HaveOccurred())
		expectPersistedContainer(database, "k8s-worker-1", handle)

		_, err = container.Run(ctx, runtime.ProcessSpec{
			Path: "/bin/sh",
			Args: []string{"-c", "echo test"},
		}, runtime.ProcessIO{})
		Expect(err).ToNot(HaveOccurred())

		pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
		Expect(err).ToNot(HaveOccurred())
		Expect(pods.Items).To(HaveLen(1))

		mainContainer := pods.Items[0].Spec.Containers[0]

		// DB_PASS should use ValueFrom.SecretKeyRef, not a literal Value
		var dbPassEnv *corev1.EnvVar
		var staticEnv *corev1.EnvVar
		for i := range mainContainer.Env {
			switch mainContainer.Env[i].Name {
			case "DB_PASS":
				dbPassEnv = &mainContainer.Env[i]
			case "STATIC":
				staticEnv = &mainContainer.Env[i]
			}
		}

		Expect(dbPassEnv).ToNot(BeNil(), "DB_PASS env var should exist")
		Expect(dbPassEnv.Value).To(BeEmpty(), "DB_PASS should not have literal Value")
		Expect(dbPassEnv.ValueFrom).ToNot(BeNil(), "DB_PASS should have ValueFrom")
		Expect(dbPassEnv.ValueFrom.SecretKeyRef).ToNot(BeNil())
		Expect(dbPassEnv.ValueFrom.SecretKeyRef.Name).To(Equal("db-password"))
		Expect(dbPassEnv.ValueFrom.SecretKeyRef.Key).To(Equal("value"))

		Expect(staticEnv).ToNot(BeNil(), "STATIC env var should exist")
		Expect(staticEnv.Value).To(Equal("hello"), "STATIC should have literal Value")
		Expect(staticEnv.ValueFrom).To(BeNil(), "STATIC should not have ValueFrom")
	})

	It("keeps all env vars as literal values when SecretEnv is nil", func() {
		handle := "no-secret-env-handle"

		container, _, err := worker.FindOrCreateContainer(
			ctx,
			db.NewFixedHandleContainerOwner(handle),
			db.ContainerMetadata{Type: db.ContainerTypeTask},
			runtime.ContainerSpec{
				TeamID:   1,
				TeamName: "main",
				Dir:      "/workdir",
				ImageSpec: runtime.ImageSpec{
					ImageURL: "docker:///busybox",
				},
				Env: []string{"FOO=bar"},
			},
			delegate,
		)
		Expect(err).ToNot(HaveOccurred())
		expectPersistedContainer(database, "k8s-worker-1", handle)

		_, err = container.Run(ctx, runtime.ProcessSpec{
			Path: "/bin/sh",
			Args: []string{"-c", "echo test"},
		}, runtime.ProcessIO{})
		Expect(err).ToNot(HaveOccurred())

		pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
		Expect(err).ToNot(HaveOccurred())
		Expect(pods.Items).To(HaveLen(1))

		mainContainer := pods.Items[0].Spec.Containers[0]
		for _, envVar := range mainContainer.Env {
			if envVar.Name == "FOO" {
				Expect(envVar.Value).To(Equal("bar"))
				Expect(envVar.ValueFrom).To(BeNil())
			}
		}
	})
})
