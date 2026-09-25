//go:build live
// +build live

package jetbridge_test

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestLiveHostPathTaskCacheSurvivesPodRestart writes into a task cache from
// one pod, deletes that pod, and reads the file back from a new pod for the
// same job and step, with the deployed web's cache store.
//
// That is the whole promise of a hostpath cache store over emptydir, and the
// fake clientset cannot keep it: it has no node filesystem. The cluster has one
// node, so the second pod necessarily lands where the first wrote.
//
// The job id is this run's own, far above any real job's, so the cache
// directory it keys is never one a pipeline uses. The test empties it when it
// is done; the empty directory itself stays on the node, as every cache
// directory does.
func TestLiveHostPathTaskCacheSurvivesPodRestart(t *testing.T) {
	if store := liveFeatures["web.taskCache"](deployed); store != "hostpath" {
		t.Fatalf("the deployed web's task cache store is %q, not hostpath; the manifest should say so", store)
	}

	stamp := time.Now()
	identity := &atc.TaskCacheIdentity{JobID: int(stamp.Unix())}
	marker := fmt.Sprintf("cached-at-%d", stamp.UnixNano())

	run := func(handle, script string) string {
		t.Helper()
		worker, delegate, database := setupLiveWorkerWithDatabase(t, handle)
		clientset, cfg := kubeClient(t)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		cleanupPod(t, clientset, cfg.Namespace, handle)

		container, _, err := worker.FindOrCreateContainer(
			ctx,
			db.NewFixedHandleContainerOwner(handle),
			db.ContainerMetadata{Type: db.ContainerTypeTask, StepName: "live-cache"},
			runtime.ContainerSpec{
				TeamID:            1,
				Dir:               "/tmp/build/live-cache",
				Caches:            []string{"cache"},
				TaskCacheIdentity: identity,
				ImageSpec:         runtime.ImageSpec{ImageURL: "docker:///busybox", Privileged: true},
			},
			delegate,
		)
		if err != nil {
			t.Fatalf("FindOrCreateContainer %s: %v", handle, err)
		}
		requirePersistedContainer(t, database, "live-k8s-worker", handle)

		var stdout, stderr bytes.Buffer
		process, err := container.Run(ctx, runtime.ProcessSpec{
			Path: "/bin/sh",
			Args: []string{"-c", script},
			Dir:  "/tmp/build/live-cache",
		}, runtime.ProcessIO{Stdout: &stdout, Stderr: &stderr})
		if err != nil {
			t.Fatalf("Run in %s: %v", handle, err)
		}
		result, err := process.Wait(ctx)
		if err != nil {
			t.Fatalf("Wait in %s: %v", handle, err)
		}
		if result.ExitStatus != 0 {
			t.Fatalf("%s exited %d: %s", handle, result.ExitStatus, stderr.String())
		}

		// A new pod, not the same pod run twice: the second read must come
		// from the node, not from a container filesystem that outlived it.
		if err := clientset.CoreV1().Pods(cfg.Namespace).Delete(ctx, handle, metav1.DeleteOptions{}); err != nil {
			t.Fatalf("deleting %s: %v", handle, err)
		}
		for {
			_, err := clientset.CoreV1().Pods(cfg.Namespace).Get(ctx, handle, metav1.GetOptions{})
			if k8serrors.IsNotFound(err) {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatalf("pod %s was not deleted: %v", handle, ctx.Err())
			case <-time.After(time.Second):
			}
		}
		return strings.TrimSpace(stdout.String())
	}

	suffix := stamp.Format("150405")
	run("live-cache-write-"+suffix, "echo "+marker+" > cache/marker")
	t.Cleanup(func() { run("live-cache-clean-"+suffix, "rm -rf cache/* cache/.[!.]*; true") })

	if got := run("live-cache-read-"+suffix, "cat cache/marker"); got != marker {
		t.Fatalf("a new pod for the same job and step read %q from the cache, want %q: the cache did not survive the pod", got, marker)
	}
}
