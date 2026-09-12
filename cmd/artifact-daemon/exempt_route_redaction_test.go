package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/hangar/output/ledger"
)

// What the mTLS-EXEMPT routes may say.
//
// The output daemon holds itself to this rule already: redaction_test.go drives
// its real routes and greps every response body for the control directory, the
// managed steps root, the bucket, the prefix and the key paths. The artifact
// daemon's exempt routes were never held to it, and they are the ones an
// unauthenticated caller can reach -- the exemption is deliberate and
// documented ("it is a question a pod on this node must be able to ask"), and
// the same file records that the transport is not the boundary either, because
// NetworkPolicy enforcement is optional and CNI-dependent. So the audience for
// these bodies is every pod on the node, task pods included.
//
// Measured before the fix: with the control directory replaced by a regular
// file, so the ledger read answers ENOTDIR rather than "does not exist",
// GET /capture-held answered 200 with the node's storage path and the raw OS
// error in the JSON body.
func TestTheExemptRoutesNameNoNodeLocalPath(t *testing.T) {
	server, storage := capturedServer(t)
	handler := server.Handler()

	// Make the ledger unreadable in the way that produces the detailed text:
	// a regular file where the control directory should be.
	control := filepath.Join(storage, ledger.ControlDirName)
	if err := os.RemoveAll(control); err != nil {
		t.Fatalf("removing the control directory: %v", err)
	}
	if err := os.WriteFile(control, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("replacing the control directory with a file: %v", err)
	}

	forbidden := map[string]string{
		storage:                         "the node's artifact storage root",
		filepath.Join(storage, "steps"): "the managed steps root on this node",
		control:                         "the daemon's control directory on this node",
		"not a directory":               "the raw OS error",
	}

	for _, target := range []string{
		"/capture-held/steps/" + heldStepDir(),
		"/capture-held/steps/unheld-handle",
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
		body := recorder.Body.String()

		for value, what := range forbidden {
			if value != "" && strings.Contains(body, value) {
				t.Errorf("GET %s answered %d with %s in the body:\n\n%s\n\nThis route requires "+
					"no client certificate, so that value is readable by any pod on the node.",
					target, recorder.Code, what, body)
			}
		}

		// And it still says something: a redaction that emptied the answer
		// would be a cleanup init container with nothing to act on.
		var answer struct {
			Class  string `json:"class"`
			Handle string `json:"handle"`
			Reason string `json:"reason"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
			t.Fatalf("GET %s did not answer JSON: %v", target, err)
		}
		if answer.Class == "" {
			t.Errorf("GET %s answered no class", target)
		}
		if answer.Class != string(ledger.Unmanaged) && answer.Reason == "" {
			t.Errorf("GET %s answered class %q with no reason at all; a refusal nobody can read "+
				"is a stuck operation nobody can diagnose", target, answer.Class)
		}
	}
}
