//go:build live

package steps

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// A fixture prerequisite, not a replacement for the five daemon-backed Brine
// cases: real volume bytes persist after the writer pod dies, and the kubelet
// then removes the exact owned storage directory. A current production daemon
// serves the bytes through the actual node-IP route.
func TestLiveArtifactStorePersistsAndCleans(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	rec := new(brine.Recorder)
	defer func() {
		disposers := rec.DrainDisposers()
		for i := len(disposers) - 1; i >= 0; i-- {
			func() {
				defer func() {
					if p := recover(); p != nil {
						t.Errorf("disposer: %v", p)
					}
				}()
				disposers[i]()
			}()
		}
	}()
	d, err := newLiveArtifactDaemon(ctx, rec)
	if err != nil {
		t.Fatal(err)
	}
	s := d.store
	directory := corev1.HostPathDirectory
	writer := s.pod("artifact-store-writer", s.anchor.Spec.NodeName,
		[]corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: s.root, Type: &directory}}}},
		[]corev1.VolumeMount{{Name: "data", MountPath: "/store"}},
	)
	writer, err = s.cluster.Clientset.CoreV1().Pods(s.cluster.Namespace).Create(ctx, writer, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	createdWriter := writer.DeepCopy()
	rec.RegisterDisposer(func() {
		clean, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := deleteLiveStoragePod(clean, s, createdWriter); err != nil {
			panic(err)
		}
	})
	writer, err = awaitLiveStoragePod(ctx, s, writer)
	if err != nil {
		t.Fatal(err)
	}
	if err := validatePodMounts(writer); err != nil {
		t.Fatal(err)
	}
	expected := "real bytes survive producer deletion"
	if _, err := s.exec(ctx, writer.Name, []string{"sh", "-ec", `mkdir -p /store/steps/producer/result; printf '%s' "$1" > /store/steps/producer/result/output.txt; cp /store/.brine-owner /store/steps/producer/result/.brine-owner`, "write-artifact", expected}, nil); err != nil {
		t.Fatal(err)
	}
	volume := jetbridge.NewDeferredVolume("owned-storage-volume", "k8s-worker-1", s.executor, s.cluster.Namespace, "main", "/store/steps/producer/result")
	volume.SetPodName(writer.Name)
	read, err := volume.StreamOut(ctx, ".", compression.NewGzipCompression())
	if err != nil {
		t.Fatal(err)
	}
	files, readErr := filesInGzippedTar(read)
	closeErr := read.Close()
	if readErr != nil || closeErr != nil || files["output.txt"] != expected || files[".brine-owner"] != string(s.anchor.UID) || len(files) != 2 {
		t.Fatalf("real volume contents=%v read=%v close=%v", files, readErr, closeErr)
	}
	if err := deleteLiveStoragePod(ctx, s, writer); err != nil {
		t.Fatal(err)
	}
	got, err := s.exec(ctx, s.observer.Name, []string{"cat", "/store/steps/producer/result/output.txt"}, nil)
	if err != nil || got != expected {
		t.Fatalf("post-deletion artifact=%q err=%v", got, err)
	}
	fmt.Printf("real writer UID %s removed; independently mounted observer UID %s read %q\n", writer.UID, s.observer.UID, got)
	// The writer is absent, so the artifact must arrive from the real daemon.
	config := jetbridge.NewConfig(s.cluster.Namespace, "")
	config.ArtifactDaemonPort = int(d.port)
	artifact := jetbridge.NewDaemonSetVolume("steps/producer/result", "owned-artifact", "k8s-worker-1", nil, s.anchor.Spec.NodeName, config, jetbridge.NewNodeIPResolver(s.cluster.Clientset))
	stream, err := artifact.StreamOut(ctx, ".", compression.NewGzipCompression())
	if err != nil {
		t.Fatalf("daemon artifact read after producer deletion: %v", err)
	}
	files, readErr = filesInGzippedTar(stream)
	closeErr = stream.Close()
	if readErr != nil || closeErr != nil || len(files) != 2 || files["output.txt"] != expected || files[".brine-owner"] != string(s.anchor.UID) {
		t.Fatalf("real daemon artifact contents=%v read=%v close=%v", files, readErr, closeErr)
	}
	fmt.Printf("production DaemonSetVolume read exact bytes and ownership marker from real daemon UID %s after writer UID %s was deleted\n", d.pod.UID, writer.UID)
	if err := d.close(ctx); err != nil {
		t.Fatal(err)
	}
	conn, dialErr := net.DialTimeout("tcp", net.JoinHostPort(d.nodeIP, fmt.Sprint(d.port)), time.Second)
	if conn != nil {
		conn.Close()
	}
	if !errors.Is(dialErr, syscall.ECONNREFUSED) {
		t.Fatalf("closed daemon node port must refuse new connections: %v", dialErr)
	}
	fmt.Printf("closed real daemon node port %d and removed daemon UID %s\n", d.port, d.pod.UID)
	if !strings.HasPrefix(s.root, s.ownerRoot+"/") {
		t.Fatal("storage escaped owned pod directory")
	}
	// Make storage reclamation a bounded assertion, not merely a teardown
	// side effect. On failure, independently remove the owned anchor so the
	// remaining recorder disposers can still verify and finish cleanup.
	clean, stop := context.WithTimeout(context.Background(), 10*time.Second)
	err = s.close(clean)
	stop()
	if err != nil {
		recovery, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if cleanupErr := deleteLiveStoragePod(recovery, s, s.anchor); cleanupErr != nil {
			t.Errorf("remove owned anchor after failed cleanup assertion: %v", cleanupErr)
		}
		t.Fatalf("storage cleanup verification: %v", err)
	}
	if !s.cleaned {
		t.Fatal("storage cleanup was not verified")
	}
}
