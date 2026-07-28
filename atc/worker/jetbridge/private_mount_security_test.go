package jetbridge

import (
	"context"
	"fmt"
	"testing"

	"github.com/concourse/concourse/atc/runtime"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func privateMountTestSpec(path string) runtime.ContainerSpec {
	return runtime.ContainerSpec{
		ImageSpec: runtime.ImageSpec{ImageURL: "busybox:latest"},
		Dir:       "/work",
		PrivateFileMounts: []runtime.PrivateFileMount{{
			MountPath: path,
			Files:     map[string][]byte{"profile.yml": []byte("trusted")},
		}},
	}
}

func newPrivateMountTestContainer(clientset *fake.Clientset, spec runtime.ContainerSpec) *Container {
	clientset.PrependReactor("create", "pods", func(action ktesting.Action) (bool, kruntime.Object, error) {
		pod := action.(ktesting.CreateAction).GetObject().(*corev1.Pod)
		if pod.UID == "" {
			pod.UID = "pod-uid"
		}
		return false, nil, nil
	})
	return newContainer(
		"private-mount-test",
		taskMetadata(),
		spec,
		nil,
		clientset,
		permEmptyDirConfig(),
		"worker-1",
		nil,
		nil,
		nil,
		false,
	)
}

func TestPrivateMountCollisionIsFailClosedAndDoesNotOverwrite(t *testing.T) {
	ctx := context.Background()
	clientset := fake.NewSimpleClientset()
	c := newPrivateMountTestContainer(clientset, privateMountTestSpec("/run/concourse/dev-validation"))
	c.privateMountNameGenerator = func() (string, error) { return "concourse-private-collision", nil }

	// The pre-existing Secret simulates a concurrent or hostile substitution.
	// A private mount must never read, update, or adopt it.
	name := "concourse-private-collision"
	_, err := clientset.CoreV1().Secrets("test-ns").Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "test-ns"},
		Data:       map[string][]byte{"profile.yml": []byte("attacker")},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := c.createPod(ctx, runtime.ProcessSpec{Path: "/bin/sh"}); err == nil {
		t.Fatal("expected private Secret collision to fail closed")
	}
	secret, err := clientset.CoreV1().Secrets("test-ns").Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(secret.Data["profile.yml"]); got != "attacker" {
		t.Fatalf("collision Secret was overwritten: got %q", got)
	}
}

func TestPrivateMountTrustedSecretAlreadyExistsWhenPodBecomesVisible(t *testing.T) {
	ctx := context.Background()
	clientset := fake.NewSimpleClientset()
	c := newPrivateMountTestContainer(clientset, privateMountTestSpec("/run/concourse/dev-validation"))
	c.privateMountNameGenerator = func() (string, error) { return "concourse-private-race", nil }
	var exposedSecretName string
	clientset.PrependReactor("create", "pods", func(action ktesting.Action) (bool, kruntime.Object, error) {
		pod := action.(ktesting.CreateAction).GetObject().(*corev1.Pod)
		for _, volume := range pod.Spec.Volumes {
			if volume.Secret == nil || volume.Secret.SecretName != "concourse-private-race" {
				continue
			}
			exposedSecretName = volume.Secret.SecretName
		}
		return false, nil, nil
	})
	pod, err := c.createPod(ctx, runtime.ProcessSpec{Path: "/bin/sh"})
	if err != nil {
		t.Fatal(err)
	}
	if exposedSecretName == "" {
		t.Fatal("Pod did not expose the expected private Secret name")
	}
	// The fake reactor above is the adversarial observer that sees the Pod's
	// volume reference. Once that Pod is visible, its competing Create cannot
	// win because this implementation already created the immutable Secret.
	_, attackerErr := clientset.CoreV1().Secrets("test-ns").Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: exposedSecretName, Namespace: "test-ns"},
		Data:       map[string][]byte{"profile.yml": []byte("attacker")},
	}, metav1.CreateOptions{})
	if !apierrors.IsAlreadyExists(attackerErr) {
		t.Fatalf("attacker Create after Pod visibility = %v, want AlreadyExists", attackerErr)
	}
	secret, err := clientset.CoreV1().Secrets("test-ns").Get(ctx, privateSecretName(t, pod), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if string(secret.Data["profile.yml"]) != "trusted" || secret.Immutable == nil || !*secret.Immutable {
		t.Fatalf("Pod did not retain trusted immutable Secret: %#v", secret)
	}
}

func TestPrivateMountSecretIsImmutableBeforePodAndCASBoundAfterCreate(t *testing.T) {
	ctx := context.Background()
	clientset := fake.NewSimpleClientset()
	clientset.PrependReactor("create", "pods", func(action ktesting.Action) (bool, kruntime.Object, error) {
		pod := action.(ktesting.CreateAction).GetObject().(*corev1.Pod)
		pod.UID = "pod-uid"
		return false, nil, nil
	})
	var created *corev1.Secret
	clientset.PrependReactor("create", "secrets", func(action ktesting.Action) (bool, kruntime.Object, error) {
		created = action.(ktesting.CreateAction).GetObject().(*corev1.Secret).DeepCopy()
		return false, nil, nil
	})
	c := newPrivateMountTestContainer(clientset, privateMountTestSpec("/run/concourse/dev-validation"))

	pod, err := c.createPod(ctx, runtime.ProcessSpec{Path: "/bin/sh"})
	if err != nil {
		t.Fatal(err)
	}
	if created == nil {
		t.Fatal("expected private Secret Create")
	}
	if created.Immutable == nil || !*created.Immutable {
		t.Fatal("private Secret must be immutable at Create")
	}
	if len(created.OwnerReferences) != 0 || created.Labels[privateMountPodNameLabelKey] != c.podName {
		t.Fatalf("private Secret must be an ownerless pre-Pod object: %#v", created.ObjectMeta)
	}
	secretName := privateSecretName(t, pod)
	bound, err := clientset.CoreV1().Secrets("test-ns").Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(bound.OwnerReferences) != 1 || bound.OwnerReferences[0].UID != "pod-uid" || bound.Labels[privateMountPodUIDLabelKey] != "pod-uid" {
		t.Fatalf("private Secret was not CAS-bound to exact pod: %#v", bound.ObjectMeta)
	}
}

func TestPrivateMountCreatesSecretsBeforePodAndCleansPartialPrePodFailure(t *testing.T) {
	ctx := context.Background()
	clientset := fake.NewSimpleClientset()
	var order []string
	clientset.PrependReactor("create", "pods", func(action ktesting.Action) (bool, kruntime.Object, error) {
		order = append(order, "pod")
		pod := action.(ktesting.CreateAction).GetObject().(*corev1.Pod)
		pod.UID = "pod-uid"
		return false, nil, nil
	})
	clientset.PrependReactor("create", "secrets", func(action ktesting.Action) (bool, kruntime.Object, error) {
		order = append(order, "secret")
		if len(order) == 2 {
			return true, nil, apierrors.NewInternalError(fmt.Errorf("second Secret failed"))
		}
		return false, nil, nil
	})
	c := newPrivateMountTestContainer(clientset, runtime.ContainerSpec{
		ImageSpec: runtime.ImageSpec{ImageURL: "busybox:latest"},
		PrivateFileMounts: []runtime.PrivateFileMount{
			{MountPath: "/run/concourse/dev-validation", Files: map[string][]byte{"profile.yml": []byte("trusted")}},
			{MountPath: "/run/concourse/dev-validation-extra", Files: map[string][]byte{"config.yml": []byte("trusted")}},
		},
	})

	if _, err := c.createPod(ctx, runtime.ProcessSpec{Path: "/bin/sh"}); err == nil {
		t.Fatal("expected partial Secret creation failure")
	}
	if len(order) != 2 || order[0] != "secret" || order[1] != "secret" {
		t.Fatalf("trusted Secrets must be created before Pod visibility: %v", order)
	}
	// A fake client deletes a Pod immediately. Real clusters normally retain it
	// during termination; the implementation must not independently delete the
	// created Secret before the Pod is confirmed absent.
	secrets, err := clientset.CoreV1().Secrets("test-ns").List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(secrets.Items) != 0 {
		t.Fatalf("orphan cleanup after confirmed pod absence should remove Secret: %#v", secrets.Items)
	}
}

func TestPrivateMountPrePodPartialFailureCleansOnlyCreatedSecrets(t *testing.T) {
	ctx := context.Background()
	clientset := fake.NewSimpleClientset()
	clientset.PrependReactor("create", "secrets", func(action ktesting.Action) (bool, kruntime.Object, error) {
		if action.(ktesting.CreateAction).GetObject().(*corev1.Secret).Name == "concourse-private-second" {
			return true, nil, apierrors.NewInternalError(fmt.Errorf("second Secret failed"))
		}
		return false, nil, nil
	})
	c := newPrivateMountTestContainer(clientset, runtime.ContainerSpec{
		ImageSpec: runtime.ImageSpec{ImageURL: "busybox:latest"},
		PrivateFileMounts: []runtime.PrivateFileMount{
			{MountPath: "/run/concourse/dev-validation", Files: map[string][]byte{"profile.yml": []byte("trusted")}},
			{MountPath: "/run/concourse/dev-validation-extra", Files: map[string][]byte{"config.yml": []byte("trusted")}},
		},
	})
	names := []string{"concourse-private-first", "concourse-private-second"}
	index := 0
	c.privateMountNameGenerator = func() (string, error) {
		name := names[index]
		index++
		return name, nil
	}

	if _, err := c.createPod(ctx, runtime.ProcessSpec{Path: "/bin/sh"}); err == nil {
		t.Fatal("expected partial Secret creation failure")
	}
	if _, err := clientset.CoreV1().Secrets("test-ns").Get(ctx, "concourse-private-first", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("pre-Pod partial failure leaked or replaced Secret: %v", err)
	}
}

func TestPrivateMountBindFailureNeverDeletesSecretWhilePodMayBeLive(t *testing.T) {
	ctx := context.Background()
	clientset := fake.NewSimpleClientset()
	c := newPrivateMountTestContainer(clientset, privateMountTestSpec("/run/concourse/dev-validation"))
	c.privateMountNameGenerator = func() (string, error) { return "concourse-private-bind-failure", nil }
	clientset.PrependReactor("update", "secrets", func(ktesting.Action) (bool, kruntime.Object, error) {
		return true, nil, apierrors.NewInternalError(fmt.Errorf("bind failed"))
	})
	clientset.PrependReactor("delete", "pods", func(ktesting.Action) (bool, kruntime.Object, error) {
		return true, nil, apierrors.NewInternalError(fmt.Errorf("pod delete failed"))
	})
	if _, err := c.createPod(ctx, runtime.ProcessSpec{Path: "/bin/sh"}); err == nil {
		t.Fatal("expected bind failure")
	}
	secret, err := clientset.CoreV1().Secrets("test-ns").Get(ctx, "concourse-private-bind-failure", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Secret deleted although pod cleanup failed: %v", err)
	}
	if secret.Immutable == nil || !*secret.Immutable || string(secret.Data["profile.yml"]) != "trusted" || len(secret.OwnerReferences) != 0 {
		t.Fatalf("bind failure Secret is not the original ownerless trusted object: %#v", secret)
	}
}

func TestPrivateMountRejectsUnsafeAndOverlappingPaths(t *testing.T) {
	cases := []string{
		"", "/", "relative", "/run/concourse/../dev-validation", "/work", "/work/private", "/tmp/elsewhere",
	}
	for _, path := range cases {
		t.Run(path, func(t *testing.T) {
			c := newPrivateMountTestContainer(fake.NewSimpleClientset(), privateMountTestSpec(path))
			if _, err := c.createPod(context.Background(), runtime.ProcessSpec{Path: "/bin/sh"}); err == nil {
				t.Fatalf("expected unsafe private mount path %q to be rejected", path)
			}
		})
	}
}

func TestPrivateMountRejectsUnsafeFileNamesAndOversizeData(t *testing.T) {
	badFiles := []map[string][]byte{
		{"../profile.yml": []byte("trusted")},
		{"profile\\.yml": []byte("trusted")},
		{".": []byte("trusted")},
		{"profile.yml": nil},
		{"profile.yml": make([]byte, 1024*1024+1)},
	}
	for _, files := range badFiles {
		c := newPrivateMountTestContainer(fake.NewSimpleClientset(), runtime.ContainerSpec{
			ImageSpec: runtime.ImageSpec{ImageURL: "busybox:latest"},
			PrivateFileMounts: []runtime.PrivateFileMount{{
				MountPath: "/run/concourse/dev-validation",
				Files:     files,
			}},
		})
		if _, err := c.createPod(context.Background(), runtime.ProcessSpec{Path: "/bin/sh"}); err == nil {
			t.Fatalf("expected unsafe private mount files %q to be rejected", files)
		}
	}
}

func TestPrivateMountRejectsEveryRuntimeMountOverlap(t *testing.T) {
	private := runtime.PrivateFileMount{MountPath: "/run/concourse/authority", Files: map[string][]byte{"profile.yml": []byte("trusted")}}
	cases := []struct {
		name string
		spec runtime.ContainerSpec
	}{
		{"working directory parent", runtime.ContainerSpec{ImageSpec: runtime.ImageSpec{ImageURL: "busybox:latest"}, Dir: "/run/concourse", PrivateFileMounts: []runtime.PrivateFileMount{private}}},
		{"candidate input child", runtime.ContainerSpec{ImageSpec: runtime.ImageSpec{ImageURL: "busybox:latest"}, Inputs: []runtime.Input{{DestinationPath: "/run/concourse/authority/candidate"}}, PrivateFileMounts: []runtime.PrivateFileMount{private}}},
		{"output parent", runtime.ContainerSpec{ImageSpec: runtime.ImageSpec{ImageURL: "busybox:latest"}, Outputs: map[string]string{"out": "/run/concourse"}, PrivateFileMounts: []runtime.PrivateFileMount{private}}},
		{"cache child", runtime.ContainerSpec{ImageSpec: runtime.ImageSpec{ImageURL: "busybox:latest"}, Caches: []string{"/run/concourse/authority/cache"}, PrivateFileMounts: []runtime.PrivateFileMount{private}}},
		{"scratch child", runtime.ContainerSpec{ImageSpec: runtime.ImageSpec{ImageURL: "busybox:latest"}, ScratchPaths: []string{"/run/concourse/authority/scratch"}, PrivateFileMounts: []runtime.PrivateFileMount{private}}},
		{"ordinary Secret parent", runtime.ContainerSpec{ImageSpec: runtime.ImageSpec{ImageURL: "busybox:latest"}, SecretMounts: []runtime.SecretMount{{SecretName: "ordinary", MountPath: "/run/concourse"}}, PrivateFileMounts: []runtime.PrivateFileMount{private}}},
		{"other private child", runtime.ContainerSpec{ImageSpec: runtime.ImageSpec{ImageURL: "busybox:latest"}, PrivateFileMounts: []runtime.PrivateFileMount{private, {MountPath: "/run/concourse/authority/child", Files: map[string][]byte{"config.yml": []byte("trusted")}}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newPrivateMountTestContainer(fake.NewSimpleClientset(), tc.spec)
			if _, err := c.createPod(context.Background(), runtime.ProcessSpec{Path: "/bin/sh"}); err == nil {
				t.Fatal("expected runtime mount overlap to be rejected")
			}
		})
	}
}

func TestPrivateMountNewPodsUseDifferentSecretNamesAndAttachDoesNotAllocate(t *testing.T) {
	ctx := context.Background()
	clientset := fake.NewSimpleClientset()
	spec := privateMountTestSpec("/run/concourse/dev-validation")
	c := newPrivateMountTestContainer(clientset, spec)
	first, err := c.createPod(ctx, runtime.ProcessSpec{Path: "/bin/sh"})
	if err != nil {
		t.Fatal(err)
	}
	firstSecret := privateSecretName(t, first)
	if err := clientset.CoreV1().Pods("test-ns").Delete(ctx, first.Name, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	// Simulate owner-reference GC before another execution attempt.
	if err := clientset.CoreV1().Secrets("test-ns").Delete(ctx, firstSecret, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	second, err := c.createPod(ctx, runtime.ProcessSpec{Path: "/bin/sh"})
	if err != nil {
		t.Fatal(err)
	}
	secondSecret := privateSecretName(t, second)
	if firstSecret == secondSecret {
		t.Fatal("fresh pod reused a predictable private Secret name")
	}
	reloaded := newContainer(c.handle, taskMetadata(), spec, nil, clientset, c.config, c.workerName, nil, nil, nil, true)
	if _, err := reloaded.Attach(ctx, "task", runtime.ProcessIO{}); err != nil {
		t.Fatal(err)
	}
	secrets, err := clientset.CoreV1().Secrets("test-ns").List(ctx, metav1.ListOptions{})
	if err != nil || len(secrets.Items) != 1 || secrets.Items[0].Name != secondSecret {
		t.Fatalf("attach allocated or mutated private authority: %#v, %v", secrets.Items, err)
	}
}

func privateSecretName(t *testing.T, pod *corev1.Pod) string {
	t.Helper()
	for _, volume := range pod.Spec.Volumes {
		if volume.Secret != nil && volume.Secret.SecretName != "" {
			return volume.Secret.SecretName
		}
	}
	t.Fatal("pod has no private Secret volume")
	return ""
}
