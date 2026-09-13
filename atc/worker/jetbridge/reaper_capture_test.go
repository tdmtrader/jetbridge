package jetbridge

// What the Reaper does with an answer it did not expect.
//
// `cleanupDaemonSetArtifacts` DELETEs a destroyed step's artifacts from the
// daemon on the node that holds them, and then dropped the locator entry --
// unconditionally, whatever the daemon said. The daemon now answers 409 for a
// source a durable output capture still holds, which is the refusal Phase 3
// built and its own suite pins with M-A and M-B. What nothing pinned is THIS
// side of it: a refused delete used to make the ATC forget where the bytes are,
// so the one caller that could come back and retry no longer knew the handle.
//
// The bytes are not leaked -- once the capture releases, the classifier answers
// unmanaged and the sweeper's TTL reclaims the step directory -- but "the
// Reaper will clean it up" stops being true for exactly the sources that most
// need it, and the operator has no way to tell.
//
// The daemon here is an http.Server answering a status code, deliberately. What
// the daemon ANSWERS is pinned by cmd/artifact-daemon's own suite against its
// real ledger; what is unpinned, and what this asserts, is the ATC's reaction
// to that answer. A whole daemon would test the half that is already tested.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"code.cloudfoundry.org/lager/v3/lagertest"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestARefusedArtifactDeleteKeepsTheLocatorEntrySoTheNextSweepRetries(t *testing.T) {
	for name, row := range map[string]struct {
		status    int
		forgotten bool
	}{
		// The controls. A delete that worked, and a delete of something that
		// was already gone, both retire the entry -- without these rows a
		// Reaper that never forgot anything would pass.
		"a delete that succeeded":     {status: http.StatusNoContent, forgotten: true},
		"a delete of something gone":  {status: http.StatusNotFound, forgotten: true},
		"a capture-held source (409)": {status: http.StatusConflict, forgotten: false},
		"a daemon that failed (500)":  {status: http.StatusInternalServerError, forgotten: false},
	} {
		var asked []string
		daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			asked = append(asked, r.Method+" "+r.URL.Path)
			w.WriteHeader(row.status)
		}))

		parsed, err := url.Parse(daemon.URL)
		if err != nil {
			daemon.Close()
			t.Fatalf("%s: parsing the daemon URL: %v", name, err)
		}
		port, err := strconv.Atoi(parsed.Port())
		if err != nil {
			daemon.Close()
			t.Fatalf("%s: the daemon has no port: %v", name, err)
		}

		clientset := fake.NewSimpleClientset(&corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
			Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{
				{Type: corev1.NodeInternalIP, Address: parsed.Hostname()},
			}},
		})

		locator := NewArtifactLocator()
		locator.Record("held-handle", "node-1", "held-handle/result")

		reaper := &Reaper{
			logger:          lagertest.NewTestLogger("reaper"),
			clientset:       clientset,
			cfg:             Config{Namespace: "test-ns", ArtifactDaemonPort: port},
			artifactLocator: locator,
			nodeIPResolver:  NewNodeIPResolver(clientset),
			httpClient:      daemon.Client(),
		}
		reaper.cleanupDaemonSetArtifacts(context.Background(),
			lagertest.NewTestLogger("cleanup"), []string{"held-handle"})
		daemon.Close()

		if len(asked) != 1 || !strings.HasPrefix(asked[0], "DELETE ") {
			t.Errorf("%s: the reaper asked %v", name, asked)
		}

		_, stillKnown := locator.LocateNode(ArtifactKey("held-handle"))
		if row.forgotten && stillKnown {
			t.Errorf("%s: the locator still names the handle after a delete that finished; "+
				"the entry is never retired and the map grows forever", name)
		}
		if !row.forgotten && !stillKnown {
			t.Errorf("%s: the locator forgot where the bytes are after a delete the daemon "+
				"REFUSED. Nothing retries, and the handle the next sweep needs is gone", name)
		}
	}
}
