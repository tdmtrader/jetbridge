package jetbridge_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/gc/gcfakes"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// The locator is keyed by VOLUME handle -- RecordOutputs stores
// "<container handle>-output-<name>", "<container handle>-dir" and friends --
// while GC hands the Reaper bare CONTAINER handles. Looking a container handle
// up directly can therefore never hit, so no DELETE was ever issued and the
// locator entries were never reclaimed. Reclamation fell back entirely to the
// daemon's mtime TTL sweep, hours behind container destroy.
var _ = Describe("Reaper artifact cleanup", func() {
	It("deletes daemon artifacts recorded under volume handles", func() {
		const (
			containerHandle = "build-42"
			nodeName        = "node-3"
		)

		database := useJetbridgeDB()
		dbWorker, err := persistNamedWorker(database, "k8s-test-namespace")
		Expect(err).ToNot(HaveOccurred())
		creating, err := dbWorker.CreateContainer(
			db.NewFixedHandleContainerOwner(containerHandle),
			db.ContainerMetadata{Type: db.ContainerTypeTask},
		)
		Expect(err).ToNot(HaveOccurred())
		created, err := creating.Created()
		Expect(err).ToNot(HaveOccurred())
		_, err = created.Destroying()
		Expect(err).ToNot(HaveOccurred())

		var (
			mu      sync.Mutex
			deleted []string
		)
		daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodDelete {
				mu.Lock()
				deleted = append(deleted, r.URL.Path)
				mu.Unlock()
			}
			w.WriteHeader(http.StatusNoContent)
		}))
		DeferCleanup(daemon.Close)

		host, port, err := net.SplitHostPort(strings.TrimPrefix(daemon.URL, "http://"))
		Expect(err).ToNot(HaveOccurred())
		daemonPort, err := strconv.Atoi(port)
		Expect(err).ToNot(HaveOccurred())

		ctx := context.Background()
		clientset := fake.NewSimpleClientset(&corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: nodeName},
			Status: corev1.NodeStatus{
				Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: host}},
			},
		})

		cfg := jetbridge.NewConfig("test-namespace", "")
		cfg.ArtifactDaemonPort = daemonPort
		cfg.ArtifactDaemonHostPath = "/var/lib/concourse-artifacts"

		reaper := jetbridge.NewReaper(
			lagertest.NewTestLogger("reaper"), clientset, cfg, database.ContainerRepository, new(gcfakes.FakeDestroyer))

		locator := jetbridge.NewArtifactLocator()
		// Exactly what DaemonSetBackend.RecordOutputs writes: volume handle as the
		// key, "<container handle>/<output name>" as the daemon-side HostDir.
		locator.Record(jetbridge.ArtifactKey(containerHandle+"-output-result"), nodeName, containerHandle+"/result")
		locator.Record(jetbridge.ArtifactKey(containerHandle+"-dir"), nodeName, containerHandle+"/dir")
		// A resource cache records a HostDir with no container handle in it. It
		// outlives the container that populated it and must survive this sweep.
		locator.Record(jetbridge.ArtifactKey("rc-7"), nodeName, "rc-7")
		// Another build's output on the same node must not be swept either.
		locator.Record(jetbridge.ArtifactKey("build-43-output-result"), nodeName, "build-43/result")

		reaper.SetArtifactLocator(locator)

		Expect(reaper.Run(ctx)).To(Succeed())
		var state, workerName string
		Expect(database.Conn.QueryRow(
			`SELECT state, worker_name FROM containers WHERE handle = $1`,
			containerHandle,
		).Scan(&state, &workerName)).To(Succeed())
		Expect(state).To(Equal("destroying"))
		Expect(workerName).To(Equal("k8s-test-namespace"))

		mu.Lock()
		deletedPaths := append([]string(nil), deleted...)
		mu.Unlock()
		Expect(deletedPaths).To(Equal([]string{"/artifacts/steps/" + containerHandle}),
			"the destroyed container's step directory was never deleted from the daemon")

		_, stillThere := locator.Locate(jetbridge.ArtifactKey(containerHandle + "-output-result"))
		Expect(stillThere).To(BeFalse(), "locator entry leaked: this is the only reclaim path in the process")
		_, stillThere = locator.Locate(jetbridge.ArtifactKey(containerHandle + "-dir"))
		Expect(stillThere).To(BeFalse(), "locator entry leaked")

		_, cacheKept := locator.Locate(jetbridge.ArtifactKey("rc-7"))
		Expect(cacheKept).To(BeTrue(), "a resource cache was reclaimed along with the container that populated it")
		_, otherKept := locator.Locate(jetbridge.ArtifactKey("build-43-output-result"))
		Expect(otherKept).To(BeTrue(), "another build's artifact was swept")
	})
})

func TestArtifactLocatorLocateStepMatchesOnlyTheStepsOwnEntries(t *testing.T) {
	locator := jetbridge.NewArtifactLocator()
	locator.Record("build-4-output-a", "node-1", "build-4/a")
	locator.Record("build-4-dir", "node-1", "build-4/dir")
	// A prefix that is not a path segment boundary must not match.
	locator.Record("build-40-output-a", "node-2", "build-40/a")
	locator.Record("rc-9", "node-1", "rc-9")

	got := locator.LocateStep("build-4")
	require.Len(t, got, 1)
	require.ElementsMatch(t, []string{"build-4-output-a", "build-4-dir"}, got["node-1"])

	require.Empty(t, locator.LocateStep("build-999"))
	require.Empty(t, locator.LocateStep(""))
}
