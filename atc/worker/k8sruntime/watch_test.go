package k8sruntime_test

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/concourse/concourse/atc/worker/k8sruntime"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

var _ = Describe("watchPod", func() {
	var (
		fakeClientset *fake.Clientset
		ctx           context.Context
	)

	BeforeEach(func() {
		ctx = context.Background()
		fakeClientset = fake.NewSimpleClientset()
	})

	It("returns a watch.Interface filtered to a specific pod by field selector", func() {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "my-pod",
				Namespace: "test-namespace",
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{Name: "main", Image: "busybox"},
				},
			},
		}
		_, err := fakeClientset.CoreV1().Pods("test-namespace").Create(ctx, pod, metav1.CreateOptions{})
		Expect(err).ToNot(HaveOccurred())

		watcher, err := k8sruntime.WatchPod(ctx, fakeClientset, "test-namespace", "my-pod", "")
		Expect(err).ToNot(HaveOccurred())
		Expect(watcher).ToNot(BeNil())
		defer watcher.Stop()

		pod.Status.Phase = corev1.PodRunning
		_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
		Expect(err).ToNot(HaveOccurred())

		event := <-watcher.ResultChan()
		Expect(event.Type).To(Equal(watch.Modified))

		receivedPod, ok := event.Object.(*corev1.Pod)
		Expect(ok).To(BeTrue())
		Expect(receivedPod.Name).To(Equal("my-pod"))
		Expect(receivedPod.Status.Phase).To(Equal(corev1.PodRunning))
	})

	It("passes resourceVersion to the watch options", func() {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "rv-pod",
				Namespace: "test-namespace",
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{Name: "main", Image: "busybox"},
				},
			},
		}
		_, err := fakeClientset.CoreV1().Pods("test-namespace").Create(ctx, pod, metav1.CreateOptions{})
		Expect(err).ToNot(HaveOccurred())

		watcher, err := k8sruntime.WatchPod(ctx, fakeClientset, "test-namespace", "rv-pod", "12345")
		Expect(err).ToNot(HaveOccurred())
		Expect(watcher).ToNot(BeNil())
		watcher.Stop()
	})
})

var _ = Describe("PodWatcher", func() {
	var (
		fakeClientset *fake.Clientset
		ctx           context.Context
	)

	BeforeEach(func() {
		ctx = context.Background()
		fakeClientset = fake.NewSimpleClientset()
	})

	Describe("Next", func() {
		It("returns initial pod state from Get() on first call", func() {
			// Create the pod in the fake store.
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "watch-pod",
					Namespace:       "test-namespace",
					ResourceVersion: "1",
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "main", Image: "busybox"}},
				},
				Status: corev1.PodStatus{Phase: corev1.PodPending},
			}
			_, err := fakeClientset.CoreV1().Pods("test-namespace").Create(ctx, pod, metav1.CreateOptions{})
			Expect(err).ToNot(HaveOccurred())

			pw := k8sruntime.NewPodWatcher(fakeClientset, "test-namespace", "watch-pod")
			defer pw.Stop()

			// First call should return the current state via Get().
			receivedPod, err := pw.Next(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(receivedPod.Name).To(Equal("watch-pod"))
			Expect(receivedPod.Status.Phase).To(Equal(corev1.PodPending))
		})

		It("returns pod events from watch channel on subsequent calls", func() {
			// Create the pod in the fake store.
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "watch-pod",
					Namespace:       "test-namespace",
					ResourceVersion: "1",
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "main", Image: "busybox"}},
				},
				Status: corev1.PodStatus{Phase: corev1.PodPending},
			}
			_, err := fakeClientset.CoreV1().Pods("test-namespace").Create(ctx, pod, metav1.CreateOptions{})
			Expect(err).ToNot(HaveOccurred())

			// Use a controlled fake watcher for subsequent calls.
			fakeW := watch.NewRaceFreeFake()
			fakeClientset.PrependWatchReactor("pods", func(action k8stesting.Action) (bool, watch.Interface, error) {
				return true, fakeW, nil
			})

			pw := k8sruntime.NewPodWatcher(fakeClientset, "test-namespace", "watch-pod")
			defer pw.Stop()

			// First call returns initial state from Get().
			_, err = pw.Next(ctx)
			Expect(err).ToNot(HaveOccurred())

			// Send an event on the watch channel.
			pod.ResourceVersion = "2"
			pod.Status.Phase = corev1.PodRunning
			fakeW.Modify(pod)

			// Second call should get the event from watch channel.
			receivedPod, err := pw.Next(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(receivedPod.Name).To(Equal("watch-pod"))
			Expect(receivedPod.Status.Phase).To(Equal(corev1.PodRunning))
		})

		It("re-establishes the watch when the channel closes", func() {
			// Create the pod in the fake store.
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "reconnect-pod",
					Namespace:       "test-namespace",
					ResourceVersion: "100",
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "main", Image: "busybox"}},
				},
				Status: corev1.PodStatus{Phase: corev1.PodPending},
			}
			_, err := fakeClientset.CoreV1().Pods("test-namespace").Create(ctx, pod, metav1.CreateOptions{})
			Expect(err).ToNot(HaveOccurred())

			var watchCallCount int32
			fakeWatcher1 := watch.NewRaceFreeFake()
			fakeWatcher2 := watch.NewRaceFreeFake()
			fakeClientset.PrependWatchReactor("pods", func(action k8stesting.Action) (bool, watch.Interface, error) {
				n := atomic.AddInt32(&watchCallCount, 1)
				if n == 1 {
					return true, fakeWatcher1, nil
				}
				return true, fakeWatcher2, nil
			})

			pw := k8sruntime.NewPodWatcher(fakeClientset, "test-namespace", "reconnect-pod")
			defer pw.Stop()

			// First call returns initial state from Get().
			receivedPod, err := pw.Next(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(receivedPod.Status.Phase).To(Equal(corev1.PodPending))

			// Send an event on the first watcher.
			pod.ResourceVersion = "101"
			pod.Status.Phase = corev1.PodRunning
			fakeWatcher1.Modify(pod)

			receivedPod, err = pw.Next(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(receivedPod.Status.Phase).To(Equal(corev1.PodRunning))

			// Close the first watcher to simulate disconnect.
			fakeWatcher1.Stop()

			// Send event on the second watcher (after reconnection).
			pod.ResourceVersion = "102"
			pod.Status.Phase = corev1.PodSucceeded
			fakeWatcher2.Modify(pod)

			// Next() should transparently reconnect and return the new event.
			receivedPod, err = pw.Next(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(receivedPod.Status.Phase).To(Equal(corev1.PodSucceeded))

			// Verify watch was called twice (initial + reconnection).
			Expect(atomic.LoadInt32(&watchCallCount)).To(BeNumerically(">=", 2))
		})

		It("falls back to Get() if watch re-establishment fails consecutively", func() {
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "fallback-pod",
					Namespace:       "test-namespace",
					ResourceVersion: "200",
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "main", Image: "busybox"},
					},
				},
				Status: corev1.PodStatus{Phase: corev1.PodPending},
			}
			_, err := fakeClientset.CoreV1().Pods("test-namespace").Create(ctx, pod, metav1.CreateOptions{})
			Expect(err).ToNot(HaveOccurred())

			// First watcher works, then all subsequent watches fail.
			var watchCallCount int32
			fakeWatcher1 := watch.NewRaceFreeFake()
			fakeClientset.PrependWatchReactor("pods", func(action k8stesting.Action) (bool, watch.Interface, error) {
				n := atomic.AddInt32(&watchCallCount, 1)
				if n == 1 {
					return true, fakeWatcher1, nil
				}
				return true, nil, errors.New("watch unavailable")
			})

			pw := k8sruntime.NewPodWatcher(fakeClientset, "test-namespace", "fallback-pod")
			defer pw.Stop()

			// First call returns initial state from Get().
			receivedPod, err := pw.Next(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(receivedPod.Status.Phase).To(Equal(corev1.PodPending))

			// Send event on watch, then close watcher.
			pod.ResourceVersion = "201"
			pod.Status.Phase = corev1.PodRunning
			fakeWatcher1.Modify(pod)

			receivedPod, err = pw.Next(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(receivedPod.Status.Phase).To(Equal(corev1.PodRunning))

			// Close watcher to trigger reconnection attempts.
			fakeWatcher1.Stop()

			// Update the pod in the fake store so Get() returns new state.
			pod.ResourceVersion = "205"
			pod.Status.Phase = corev1.PodSucceeded
			_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
			Expect(err).ToNot(HaveOccurred())

			// Next() should fall back to Get() after consecutive watch failures.
			receivedPod, err = pw.Next(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(receivedPod.Status.Phase).To(Equal(corev1.PodSucceeded))
		})

		It("returns error when context is cancelled", func() {
			// Create the pod in the fake store.
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "cancel-pod",
					Namespace:       "test-namespace",
					ResourceVersion: "1",
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "main", Image: "busybox"}},
				},
				Status: corev1.PodStatus{Phase: corev1.PodPending},
			}
			_, err := fakeClientset.CoreV1().Pods("test-namespace").Create(ctx, pod, metav1.CreateOptions{})
			Expect(err).ToNot(HaveOccurred())

			fakeW := watch.NewRaceFreeFake()
			fakeClientset.PrependWatchReactor("pods", func(action k8stesting.Action) (bool, watch.Interface, error) {
				return true, fakeW, nil
			})

			pw := k8sruntime.NewPodWatcher(fakeClientset, "test-namespace", "cancel-pod")
			defer pw.Stop()

			// First call returns initial state from Get().
			_, err = pw.Next(ctx)
			Expect(err).ToNot(HaveOccurred())

			// Cancel context and try to get next event.
			cancelCtx, cancel := context.WithCancel(ctx)
			cancel()

			_, err = pw.Next(cancelCtx)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("context canceled"))
		})
	})
})
