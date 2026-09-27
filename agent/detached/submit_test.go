package detached

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSubmitResumesFromItsReceipt(t *testing.T) {
	platform := newFakePlatform(t, "source", "outcome")
	ws := newTestWorkspace(t, "first")
	workload := testWorkload("implement")

	// The first attempt admits the Run, then loses the credential session.
	platform.credentialStatus = []int{http.StatusServiceUnavailable}
	first, err := platform.client(t).Submit(context.Background(), workload, ws.options())
	if err == nil || first.RunID != testRunID || first.Handle.Number != testNumber || first.Ready {
		t.Fatalf("interrupted submission lost its admitted Run: %+v %v", first, err)
	}
	fields, data := readReceipt(t, ws.receipt)
	if fields["version"] != receiptVersion || fields["workload"] != "implement" || fields["source_id"] != testSourceID || fields["run_id"] != float64(testRunID) || fields["number"] != float64(testNumber) || fields["invocation_key"] == "" {
		t.Fatalf("receipt did not record the admitted Run: %s", data)
	}
	if bytes.Contains(data, []byte("transient-bearer")) || bytes.Contains(data, []byte("synthetic-auth")) {
		t.Fatalf("receipt retained a secret: %s", data)
	}
	st, err := os.Stat(ws.receipt)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("receipt is not private: %v %v", st.Mode(), err)
	}

	// A fresh client resumes from the receipt: no second upload or admission.
	platform.credentialStates = []string{"ready"}
	resumed, err := platform.client(t).Submit(context.Background(), workload, ws.options())
	if err != nil || !resumed.Ready || resumed.State != "ready" || resumed.RunID != testRunID {
		t.Fatalf("resume did not observe readiness: %+v %v", resumed, err)
	}
	if uploads, creates, handoffs := platform.counts(); uploads != 1 || creates != 1 || handoffs != 0 {
		t.Fatalf("resume repeated work: uploads=%d creates=%d handoffs=%d", uploads, creates, handoffs)
	}
	if !bytes.Contains(platform.uploadedArchive, []byte("first")) {
		t.Fatal("upload did not stream the workload's archive")
	}
	request := platform.createRequests[0]
	if request.InvocationKey != fields["invocation_key"] || request.Inputs["source"].SourceID != testSourceID || request.Inputs["source"].Bearer != "transient-bearer" {
		t.Fatalf("admission did not carry the persisted key and fresh grant: %+v", request)
	}
}

func TestSubmitReplaysASavedSourceBeforeUploadingAgain(t *testing.T) {
	platform := newFakePlatform(t, "source", "outcome")
	ws := newTestWorkspace(t, "first")
	workload := testWorkload("implement")

	// Upload succeeds; admission's outcome is unknown.
	platform.createStatus = []int{http.StatusServiceUnavailable}
	if _, err := platform.client(t).Submit(context.Background(), workload, ws.options()); err == nil {
		t.Fatal("an uncertain admission reported success")
	}
	fields, _ := readReceipt(t, ws.receipt)
	if fields["source_id"] != testSourceID || fields["run_id"] != nil {
		t.Fatalf("receipt did not keep the uploaded source alone: %v", fields)
	}

	// The replay names the saved source without a bearer and does not re-upload.
	platform.credentialStates = []string{"ready"}
	result, err := platform.client(t).Submit(context.Background(), workload, ws.options())
	if err != nil || !result.Ready {
		t.Fatalf("replay failed: %+v %v", result, err)
	}
	if uploads, creates, _ := platform.counts(); uploads != 1 || creates != 2 {
		t.Fatalf("replay re-uploaded: uploads=%d creates=%d", uploads, creates)
	}
	replay := platform.createRequests[1]
	if replay.Inputs["source"].SourceID != testSourceID || replay.Inputs["source"].Bearer != "" || replay.InvocationKey != platform.createRequests[0].InvocationKey {
		t.Fatalf("replay changed its invocation intent: %+v", replay)
	}
}

func TestSubmitUploadsAgainOnlyAfterADefiniteGrantRefusal(t *testing.T) {
	platform := newFakePlatform(t, "source", "outcome")
	ws := newTestWorkspace(t, "first")
	workload := testWorkload("implement")
	platform.createStatus = []int{http.StatusServiceUnavailable}
	if _, err := platform.client(t).Submit(context.Background(), workload, ws.options()); err == nil {
		t.Fatal("an uncertain admission reported success")
	}
	// An expired grant is a definite 400: the client uploads again and admits
	// under the same invocation key.
	platform.createStatus = []int{http.StatusBadRequest}
	platform.credentialStates = []string{"ready"}
	result, err := platform.client(t).Submit(context.Background(), workload, ws.options())
	if err != nil || !result.Ready {
		t.Fatalf("resubmission failed: %+v %v", result, err)
	}
	if uploads, creates, _ := platform.counts(); uploads != 2 || creates != 3 {
		t.Fatalf("expected a second upload after a refused grant: uploads=%d creates=%d", uploads, creates)
	}
	keys := map[string]bool{}
	for _, request := range platform.createRequests {
		keys[request.InvocationKey] = true
	}
	if len(keys) != 1 {
		t.Fatalf("invocation key changed across attempts: %v", keys)
	}
}

func TestSubmitCredentialStates(t *testing.T) {
	t.Run("available hands off the local auth file once", func(t *testing.T) {
		platform := newFakePlatform(t, "source", "outcome")
		ws := newTestWorkspace(t, "first")
		platform.credentialStates = []string{"available"}
		result, err := platform.client(t).Submit(context.Background(), testWorkload("implement"), ws.options())
		if err != nil || !result.Ready || result.State != "ready" {
			t.Fatalf("handoff did not reach readiness: %+v %v", result, err)
		}
		if _, _, handoffs := platform.counts(); handoffs != 1 || string(platform.handoffBody) != `{"synthetic-auth":true}` {
			t.Fatalf("handoff did not send the auth file exactly once: %d %q", handoffs, platform.handoffBody)
		}
	})
	t.Run("claimed is never retried", func(t *testing.T) {
		platform := newFakePlatform(t, "source", "outcome")
		ws := newTestWorkspace(t, "first")
		platform.credentialStates = []string{"claimed"}
		result, err := platform.client(t).Submit(context.Background(), testWorkload("implement"), ws.options())
		if err == nil || !strings.Contains(err.Error(), "already claimed") || result.Ready || result.State != "claimed" || result.RunID != testRunID {
			t.Fatalf("claimed delivery was not reported: %+v %v", result, err)
		}
		if _, _, handoffs := platform.counts(); handoffs != 0 {
			t.Fatal("claimed delivery resent credentials")
		}
	})
	t.Run("a handoff that reports claimed is not resent", func(t *testing.T) {
		platform := newFakePlatform(t, "source", "outcome")
		ws := newTestWorkspace(t, "first")
		platform.credentialStates = []string{"available"}
		platform.handoffState = "claimed"
		result, err := platform.client(t).Submit(context.Background(), testWorkload("implement"), ws.options())
		if err == nil || result.Ready || result.State != "claimed" {
			t.Fatalf("claimed handoff was not reported: %+v %v", result, err)
		}
		if _, _, handoffs := platform.counts(); handoffs != 1 {
			t.Fatalf("handoff was retried: %d", handoffs)
		}
	})
	t.Run("available without an auth file stops", func(t *testing.T) {
		platform := newFakePlatform(t, "source", "outcome")
		ws := newTestWorkspace(t, "first")
		options := ws.options()
		options.AuthFile = ""
		platform.credentialStates = []string{"available"}
		if _, err := platform.client(t).Submit(context.Background(), testWorkload("implement"), options); err == nil {
			t.Fatal("submission proceeded without credentials")
		}
	})
	t.Run("the handoff targets the workload's credential result", func(t *testing.T) {
		platform := newFakePlatform(t, "source", "elsewhere")
		ws := newTestWorkspace(t, "first")
		if _, err := platform.client(t).Submit(context.Background(), testWorkload("implement"), ws.options()); err == nil {
			t.Fatal("credential session found a result the workload does not name")
		}
	})
}

func TestSubmitRefusesAReceiptForAnotherSubmission(t *testing.T) {
	platform := newFakePlatform(t, "source", "outcome")
	ws := newTestWorkspace(t, "first")
	platform.credentialStates = []string{"ready"}
	if _, err := platform.client(t).Submit(context.Background(), testWorkload("implement"), ws.options()); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func() (Workload, SubmitOptions){
		"another workload": func() (Workload, SubmitOptions) { return testWorkload("review"), ws.options() },
		"another template": func() (Workload, SubmitOptions) {
			options := ws.options()
			options.Template = "other"
			return testWorkload("implement"), options
		},
		"another team": func() (Workload, SubmitOptions) {
			options := ws.options()
			options.Team = "other"
			return testWorkload("implement"), options
		},
		"a changed input": func() (Workload, SubmitOptions) {
			if err := os.WriteFile(filepath.Join(ws.input, "input.txt"), []byte("changed"), 0o600); err != nil {
				t.Fatal(err)
			}
			return testWorkload("implement"), ws.options()
		},
	}
	for _, name := range []string{"another workload", "another template", "another team", "a changed input"} {
		t.Run(name, func(t *testing.T) {
			workload, options := cases[name]()
			_, err := platform.client(t).Submit(context.Background(), workload, options)
			if err == nil || !strings.Contains(err.Error(), "saved receipt belongs to another") {
				t.Fatalf("receipt was reused: %v", err)
			}
		})
	}
	if uploads, creates, _ := platform.counts(); uploads != 1 || creates != 1 {
		t.Fatalf("a refused receipt reached the platform: uploads=%d creates=%d", uploads, creates)
	}
}

func TestLegacyReviewReceiptResumesAsReview(t *testing.T) {
	platform := newFakePlatform(t, "source", "outcome")
	ws := newTestWorkspace(t, "first")
	input, err := loadFileInput(ws.input)
	if err != nil {
		t.Fatal(err)
	}
	legacy := `{"version":"review-invocation/v1","server":"` + platform.server.URL + `","team":"` + testTeam + `","template":"` + testTemplate + `","input_digest":"` + input.Digest() + `","invocation_key":"legacy-key","source_id":"` + testSourceID + `","run_id":41,"number":3}`
	if err := os.WriteFile(ws.receipt, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := platform.client(t).Submit(context.Background(), testWorkload("implement"), ws.options()); err == nil || !strings.Contains(err.Error(), "another workload") {
		t.Fatalf("a legacy review receipt resumed as another workload: %v", err)
	}
	platform.credentialStates = []string{"ready"}
	result, err := platform.client(t).Submit(context.Background(), testWorkload("review"), ws.options())
	if err != nil || !result.Ready || result.RunID != testRunID || result.Handle.Number != testNumber {
		t.Fatalf("legacy review receipt did not resume: %+v %v", result, err)
	}
	if uploads, creates, _ := platform.counts(); uploads != 0 || creates != 0 {
		t.Fatalf("legacy resume repeated admission: uploads=%d creates=%d", uploads, creates)
	}
}

func TestReceiptLoadRefusals(t *testing.T) {
	cases := map[string]string{
		"unknown version":          `{"version":"other/v1","workload":"review","server":"s","team":"t","template":"p","input_digest":"d","invocation_key":"k"}`,
		"current without workload": `{"version":"detached-invocation/v1","server":"s","team":"t","template":"p","input_digest":"d","invocation_key":"k"}`,
		"legacy with workload":     `{"version":"review-invocation/v1","workload":"implement","server":"s","team":"t","template":"p","input_digest":"d","invocation_key":"k"}`,
		"unknown field":            `{"version":"detached-invocation/v1","workload":"review","server":"s","team":"t","template":"p","input_digest":"d","invocation_key":"k","bearer":"b"}`,
		"run without source":       `{"version":"detached-invocation/v1","workload":"review","server":"s","team":"t","template":"p","input_digest":"d","invocation_key":"k","run_id":1,"number":1}`,
		"run without number":       `{"version":"detached-invocation/v1","workload":"review","server":"s","team":"t","template":"p","input_digest":"d","invocation_key":"k","source_id":"x","run_id":1}`,
		"missing key":              `{"version":"detached-invocation/v1","workload":"review","server":"s","team":"t","template":"p","input_digest":"d"}`,
		"trailing value":           `{"version":"detached-invocation/v1","workload":"review","server":"s","team":"t","template":"p","input_digest":"d","invocation_key":"k"} {}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "request.json"), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			file, err := openReceipt(context.Background(), filepath.Join(dir, "request.json"), t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			if _, found, err := file.Load(); err == nil || found {
				t.Fatalf("receipt loaded: %v", err)
			}
		})
	}
	t.Run("world-readable receipt", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "request.json")
		if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		file, err := openReceipt(context.Background(), path, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		if _, _, err := file.Load(); err == nil {
			t.Fatal("a world-readable receipt loaded")
		}
	})
}

func TestReceiptMustBeOutsideTheInput(t *testing.T) {
	platform := newFakePlatform(t, "source", "outcome")
	ws := newTestWorkspace(t, "first")
	options := ws.options()
	options.Receipt = filepath.Join(ws.input, "request.json")
	if _, err := platform.client(t).Submit(context.Background(), testWorkload("implement"), options); err == nil {
		t.Fatal("a receipt inside the input was accepted")
	}
	if uploads, creates, _ := platform.counts(); uploads != 0 || creates != 0 {
		t.Fatal("an invalid receipt location reached the platform")
	}
	if _, err := os.Stat(options.Receipt); !os.IsNotExist(err) {
		t.Fatalf("receipt was written inside the input: %v", err)
	}
}

func TestSubmitRequiresACompleteWorkload(t *testing.T) {
	platform := newFakePlatform(t, "source", "outcome")
	ws := newTestWorkspace(t, "first")
	for name, workload := range map[string]Workload{
		"no name":              {Input: "source", CredentialResult: "outcome", Load: loadFileInput},
		"no input":             {Name: "w", CredentialResult: "outcome", Load: loadFileInput},
		"no credential result": {Name: "w", Input: "source", Load: loadFileInput},
		"no loader":            {Name: "w", Input: "source", CredentialResult: "outcome"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := platform.client(t).Submit(context.Background(), workload, ws.options()); err == nil {
				t.Fatal("an incomplete workload submitted")
			}
		})
	}
	if _, err := os.Stat(ws.receipt); !os.IsNotExist(err) {
		t.Fatal("an incomplete workload wrote a receipt")
	}
}
