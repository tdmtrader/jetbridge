package runclient

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/atctest"
)

// bounded is a submission's deadline: a client that never stops polling fails
// the test instead of hanging it.
func bounded(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	t.Cleanup(cancel)
	return ctx
}

func isCreate(r *http.Request) bool {
	return r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/v2/") && strings.HasSuffix(r.URL.Path, "/runs")
}

func isCredential(r *http.Request, method string) bool {
	return r.Method == method && strings.Contains(r.URL.Path, "/credentials/")
}

func TestSubmitResumesFromItsReceipt(t *testing.T) {
	p := newPlatform(t, "outcome")
	ws := newTestWorkspace(t, "first")
	workload := testWorkload("implement")

	// The first attempt admits the Run, then loses the credential session. The
	// decorator also keeps the grant the platform issued, to look for it later.
	var lost atomic.Bool
	var grant atomic.Value
	p.decorate(t, func(w http.ResponseWriter, r *http.Request, next http.Handler) {
		switch {
		case isCredential(r, http.MethodGet) && lost.CompareAndSwap(false, true):
			w.WriteHeader(http.StatusServiceUnavailable)
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/run-inputs/"):
			recorder := httptest.NewRecorder()
			next.ServeHTTP(recorder, r)
			var source atc.RunInputSource
			_ = json.Unmarshal(recorder.Body.Bytes(), &source)
			grant.Store(source.Bearer)
			for name, values := range recorder.Header() {
				w.Header()[name] = values
			}
			w.WriteHeader(recorder.Code)
			_, _ = w.Write(recorder.Body.Bytes())
		default:
			next.ServeHTTP(w, r)
		}
	})
	first, err := p.client(t).Submit(bounded(t), workload, ws.options(p))
	if err == nil || first.RunID < 1 || first.Handle.Number != 1 || first.Ready {
		t.Fatalf("interrupted submission lost its admitted Run: %+v %v", first, err)
	}
	fields, data := readReceipt(t, ws.receipt)
	sourceID, _ := fields["source_id"].(string)
	if fields["version"] != receiptVersion || fields["workload"] != "implement" || !strings.HasPrefix(sourceID, "input-v1-") ||
		fields["run_id"] != float64(first.RunID) || fields["number"] != float64(1) || fields["invocation_key"] == "" {
		t.Fatalf("receipt did not record the admitted Run: %s", data)
	}
	bearer, _ := grant.Load().(string)
	if bearer == "" || bytes.Contains(data, []byte(bearer)) || bytes.Contains(data, []byte("synthetic-auth")) {
		t.Fatalf("receipt retained a secret: %s", data)
	}
	st, err := os.Stat(ws.receipt)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("receipt is not private: %v %v", st.Mode(), err)
	}

	// The producer starts; a fresh client resumes from the receipt and hands
	// off, with no second upload or admission.
	p.Start(t, p.team, testTemplate, 1, "outcome")
	resumed, err := p.client(t).Submit(bounded(t), workload, ws.options(p))
	if err != nil || !resumed.Ready || resumed.State != "ready" || resumed.RunID != first.RunID {
		t.Fatalf("resume did not reach readiness: %+v %v", resumed, err)
	}
	if uploads, runs := p.counts(t); uploads != 1 || runs != 1 {
		t.Fatalf("resume repeated work: uploads=%d runs=%d", uploads, runs)
	}
	if delivered, attempts := p.Delivered(first.RunID); string(delivered) != `{"synthetic-auth":true}` || attempts != 1 {
		t.Fatalf("the producer did not receive the auth file once: %q after %d deliveries", delivered, attempts)
	}
	if uploaded, err := os.ReadFile(filepath.Join(p.Input(t, p.team, testTemplate, 1, "source"), "input.txt")); err != nil || string(uploaded) != "first" {
		t.Fatalf("the Run's input is not the workload's archive: %q %v", uploaded, err)
	}
	// The Run was admitted under the persisted key: presenting it replays it.
	key, _ := fields["invocation_key"].(string)
	replayed, err := p.client(t).CreateRun(context.Background(), p.team, testTemplate,
		atc.CreatePipelineRunV2Request{InvocationKey: key, Inputs: map[string]atc.RunInputSource{"source": {SourceID: sourceID}}})
	if err != nil || replayed.ID != first.RunID || replayed.AdmissionOutcome != atc.RunAdmissionReplayed {
		t.Fatalf("the Run was not admitted under the receipt's key: %+v %v", replayed, err)
	}
}

func TestSubmitReplaysASavedSourceBeforeUploadingAgain(t *testing.T) {
	p := newPlatform(t, "outcome")
	ws := newTestWorkspace(t, "first")
	workload := testWorkload("implement")

	// Upload succeeds; admission happens but its answer is lost.
	var lost atomic.Bool
	p.decorate(t, func(w http.ResponseWriter, r *http.Request, next http.Handler) {
		if isCreate(r) && lost.CompareAndSwap(false, true) {
			atctest.Lose(next, r)
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		next.ServeHTTP(w, r)
	})
	if _, err := p.client(t).Submit(bounded(t), workload, ws.options(p)); err == nil {
		t.Fatal("an uncertain admission reported success")
	}
	fields, _ := readReceipt(t, ws.receipt)
	if fields["source_id"] == nil || fields["run_id"] != nil {
		t.Fatalf("receipt did not keep the uploaded source alone: %v", fields)
	}

	// The replay names the saved source, finds the Run already admitted, and
	// does not upload again.
	p.Schedule(t, p.team, testTemplate, "outcome")
	result, err := p.client(t).Submit(bounded(t), workload, ws.options(p))
	if err != nil || !result.Ready || result.Handle.Number != 1 {
		t.Fatalf("replay failed: %+v %v", result, err)
	}
	if uploads, runs := p.counts(t); uploads != 1 || runs != 1 {
		t.Fatalf("replay re-uploaded or re-admitted: uploads=%d runs=%d", uploads, runs)
	}
}

func TestSubmitUploadsAgainOnlyAfterADefiniteGrantRefusal(t *testing.T) {
	p := newPlatform(t, "outcome")
	ws := newTestWorkspace(t, "first")
	workload := testWorkload("implement")
	// The first admission never reaches the platform, so the saved source is
	// bound to nothing and its grant is gone with the answer.
	var refused atomic.Bool
	p.decorate(t, func(w http.ResponseWriter, r *http.Request, next http.Handler) {
		if isCreate(r) && refused.CompareAndSwap(false, true) {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		next.ServeHTTP(w, r)
	})
	if _, err := p.client(t).Submit(bounded(t), workload, ws.options(p)); err == nil {
		t.Fatal("an uncertain admission reported success")
	}
	// Replaying the saved source alone is a definite refusal from the platform:
	// the client uploads again and admits under the same invocation key.
	p.Schedule(t, p.team, testTemplate, "outcome")
	result, err := p.client(t).Submit(bounded(t), workload, ws.options(p))
	if err != nil || !result.Ready {
		t.Fatalf("resubmission failed: %+v %v", result, err)
	}
	if uploads, runs := p.counts(t); uploads != 2 || runs != 1 {
		t.Fatalf("expected a second upload after a refused grant: uploads=%d runs=%d", uploads, runs)
	}
	fields, _ := readReceipt(t, ws.receipt)
	key, _ := fields["invocation_key"].(string)
	source, _ := fields["source_id"].(string)
	replayed, err := p.client(t).CreateRun(context.Background(), p.team, testTemplate,
		atc.CreatePipelineRunV2Request{InvocationKey: key, Inputs: map[string]atc.RunInputSource{"source": {SourceID: source}}})
	if err != nil || replayed.ID != result.RunID {
		t.Fatalf("the invocation key changed across attempts: %+v %v", replayed, err)
	}
}

// countHandoffs routes the test's clients through a decorator counting the
// credential handoffs they send, running before ahead of each. Not resending
// is the client's own behaviour: the platform would deliver nothing either
// way, so no resulting state can show it.
func countHandoffs(t *testing.T, p *platform, before func(r *http.Request)) *atomic.Int32 {
	var handoffs atomic.Int32
	p.decorate(t, func(w http.ResponseWriter, r *http.Request, next http.Handler) {
		if isCredential(r, http.MethodPost) {
			handoffs.Add(1)
			if before != nil {
				before(r)
			}
		}
		next.ServeHTTP(w, r)
	})
	return &handoffs
}

func TestSubmitCredentialStates(t *testing.T) {
	t.Run("available hands off the local auth file once", func(t *testing.T) {
		p := newPlatform(t, "outcome")
		ws := newTestWorkspace(t, "first")
		p.Schedule(t, p.team, testTemplate, "outcome")
		handoffs := countHandoffs(t, p, nil)
		result, err := p.client(t).Submit(bounded(t), testWorkload("implement"), ws.options(p))
		if err != nil || !result.Ready || result.State != "ready" {
			t.Fatalf("handoff did not reach readiness: %+v %v", result, err)
		}
		if delivered, attempts := p.Delivered(result.RunID); attempts != 1 || string(delivered) != `{"synthetic-auth":true}` {
			t.Fatalf("handoff did not send the auth file exactly once: %d %q", attempts, delivered)
		}
		// Resuming a ready Run observes readiness and sends nothing.
		again, err := p.client(t).Submit(bounded(t), testWorkload("implement"), ws.options(p))
		if err != nil || !again.Ready || again.RunID != result.RunID || handoffs.Load() != 1 {
			t.Fatalf("a ready Run was handed off again: %+v %v %d", again, err, handoffs.Load())
		}
	})
	t.Run("claimed is never retried", func(t *testing.T) {
		p := newPlatform(t, "outcome")
		ws := newTestWorkspace(t, "first")
		p.Schedule(t, p.team, testTemplate, "outcome")
		p.RefuseDelivery(p.team)
		handoffs := countHandoffs(t, p, nil)
		if _, err := p.client(t).Submit(bounded(t), testWorkload("implement"), ws.options(p)); err == nil {
			t.Fatal("a severed delivery reported success")
		}
		result, err := p.client(t).Submit(bounded(t), testWorkload("implement"), ws.options(p))
		if err == nil || !strings.Contains(err.Error(), "already claimed") || result.Ready || result.State != "claimed" || result.RunID < 1 {
			t.Fatalf("claimed delivery was not reported: %+v %v", result, err)
		}
		if _, attempts := p.Delivered(result.RunID); handoffs.Load() != 1 || attempts != 1 {
			t.Fatalf("claimed delivery resent credentials: %d handoffs, %d deliveries", handoffs.Load(), attempts)
		}
	})
	t.Run("a handoff that reports claimed is not resent", func(t *testing.T) {
		p := newPlatform(t, "outcome")
		ws := newTestWorkspace(t, "first")
		p.Schedule(t, p.team, testTemplate, "outcome")
		p.RefuseDelivery(p.team)
		// Between the client reading "available" and handing off, another
		// handoff of the same Run claims the delivery and is severed.
		var once sync.Once
		handoffs := countHandoffs(t, p, func(r *http.Request) {
			once.Do(func() {
				competing, err := http.NewRequest(http.MethodPost, p.URL+r.URL.Path, strings.NewReader(`{"competing":true}`))
				if err != nil {
					t.Error(err)
					return
				}
				competing.Header.Set("Content-Type", "application/json")
				if response, err := p.Client(t).Do(competing); err == nil {
					_, _ = io.Copy(io.Discard, response.Body)
					response.Body.Close()
				}
			})
		})
		result, err := p.client(t).Submit(bounded(t), testWorkload("implement"), ws.options(p))
		if err == nil || result.Ready || result.State != "claimed" {
			t.Fatalf("claimed handoff was not reported: %+v %v", result, err)
		}
		if _, attempts := p.Delivered(result.RunID); handoffs.Load() != 1 || attempts != 1 {
			t.Fatalf("handoff was retried: %d handoffs, %d deliveries", handoffs.Load(), attempts)
		}
	})
	t.Run("available without an auth file stops", func(t *testing.T) {
		p := newPlatform(t, "outcome")
		ws := newTestWorkspace(t, "first")
		options := ws.options(p)
		options.AuthFile = ""
		p.Schedule(t, p.team, testTemplate, "outcome")
		result, err := p.client(t).Submit(bounded(t), testWorkload("implement"), options)
		if err == nil || result.State != "available" {
			t.Fatalf("submission proceeded without credentials: %+v %v", result, err)
		}
		if _, attempts := p.Delivered(result.RunID); attempts != 0 {
			t.Fatal("something was delivered without an auth file")
		}
	})
	t.Run("the handoff targets the workload's credential result", func(t *testing.T) {
		p := newPlatform(t, "elsewhere")
		ws := newTestWorkspace(t, "first")
		p.Schedule(t, p.team, testTemplate, "elsewhere")
		if _, err := p.client(t).Submit(bounded(t), testWorkload("implement"), ws.options(p)); err == nil {
			t.Fatal("credential session found a result the workload does not name")
		}
	})
}

func TestSubmitRefusesAReceiptForAnotherSubmission(t *testing.T) {
	p := newPlatform(t, "outcome")
	ws := newTestWorkspace(t, "first")
	p.Schedule(t, p.team, testTemplate, "outcome")
	if _, err := p.client(t).Submit(bounded(t), testWorkload("implement"), ws.options(p)); err != nil {
		t.Fatal(err)
	}
	other := p.NewTeam(t)
	cases := map[string]func() (Workload, SubmitOptions){
		"another workload": func() (Workload, SubmitOptions) { return testWorkload("review"), ws.options(p) },
		"another template": func() (Workload, SubmitOptions) {
			options := ws.options(p)
			options.Template = "other"
			return testWorkload("implement"), options
		},
		"another team": func() (Workload, SubmitOptions) {
			options := ws.options(p)
			options.Team = other
			return testWorkload("implement"), options
		},
		"a changed input": func() (Workload, SubmitOptions) {
			if err := os.WriteFile(filepath.Join(ws.input, "input.txt"), []byte("changed"), 0o600); err != nil {
				t.Fatal(err)
			}
			return testWorkload("implement"), ws.options(p)
		},
	}
	for _, name := range []string{"another workload", "another template", "another team", "a changed input"} {
		t.Run(name, func(t *testing.T) {
			workload, options := cases[name]()
			_, err := p.client(t).Submit(bounded(t), workload, options)
			if err == nil || !strings.Contains(err.Error(), "saved receipt belongs to another") {
				t.Fatalf("receipt was reused: %v", err)
			}
		})
	}
	if uploads, runs := p.counts(t); uploads != 1 || runs != 1 {
		t.Fatalf("a refused receipt reached the platform: uploads=%d runs=%d", uploads, runs)
	}
}

func TestLegacyReviewReceiptResumesAsReview(t *testing.T) {
	p := newPlatform(t, "outcome")
	ws := newTestWorkspace(t, "first")
	p.Schedule(t, p.team, testTemplate, "outcome")
	// A review submitted and ready, then its receipt rewritten in the legacy
	// review format the current client must still resume.
	submitted, err := p.client(t).Submit(bounded(t), testWorkload("review"), ws.options(p))
	if err != nil || !submitted.Ready {
		t.Fatalf("submission: %+v %v", submitted, err)
	}
	fields, _ := readReceipt(t, ws.receipt)
	legacy, _ := json.Marshal(map[string]any{"version": "review-invocation/v1", "server": p.url, "team": p.team, "template": testTemplate,
		"input_digest": fields["input_digest"], "invocation_key": "legacy-key", "source_id": fields["source_id"],
		"run_id": submitted.RunID, "number": submitted.Handle.Number})
	if err := os.WriteFile(ws.receipt, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := p.client(t).Submit(bounded(t), testWorkload("implement"), ws.options(p)); err == nil || !strings.Contains(err.Error(), "another workload") {
		t.Fatalf("a legacy review receipt resumed as another workload: %v", err)
	}
	result, err := p.client(t).Submit(bounded(t), testWorkload("review"), ws.options(p))
	if err != nil || !result.Ready || result.RunID != submitted.RunID || result.Handle.Number != submitted.Handle.Number {
		t.Fatalf("legacy review receipt did not resume: %+v %v", result, err)
	}
	if uploads, runs := p.counts(t); uploads != 1 || runs != 1 {
		t.Fatalf("legacy resume repeated admission: uploads=%d runs=%d", uploads, runs)
	}
}

func TestReceiptLoadRefusals(t *testing.T) {
	cases := map[string]string{
		"unknown version":          `{"version":"other/v1","workload":"review","server":"s","team":"t","template":"p","input_digest":"d","invocation_key":"k"}`,
		"current without workload": `{"version":"run-invocation/v1","server":"s","team":"t","template":"p","input_digest":"d","invocation_key":"k"}`,
		"legacy with workload":     `{"version":"review-invocation/v1","workload":"implement","server":"s","team":"t","template":"p","input_digest":"d","invocation_key":"k"}`,
		"unknown field":            `{"version":"run-invocation/v1","workload":"review","server":"s","team":"t","template":"p","input_digest":"d","invocation_key":"k","bearer":"b"}`,
		"run without source":       `{"version":"run-invocation/v1","workload":"review","server":"s","team":"t","template":"p","input_digest":"d","invocation_key":"k","run_id":1,"number":1}`,
		"run without number":       `{"version":"run-invocation/v1","workload":"review","server":"s","team":"t","template":"p","input_digest":"d","invocation_key":"k","source_id":"x","run_id":1}`,
		"missing key":              `{"version":"run-invocation/v1","workload":"review","server":"s","team":"t","template":"p","input_digest":"d"}`,
		"trailing value":           `{"version":"run-invocation/v1","workload":"review","server":"s","team":"t","template":"p","input_digest":"d","invocation_key":"k"} {}`,
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
	p := newPlatform(t, "outcome")
	ws := newTestWorkspace(t, "first")
	options := ws.options(p)
	options.Receipt = filepath.Join(ws.input, "request.json")
	if _, err := p.client(t).Submit(bounded(t), testWorkload("implement"), options); err == nil {
		t.Fatal("a receipt inside the input was accepted")
	}
	if uploads, runs := p.counts(t); uploads != 0 || runs != 0 {
		t.Fatal("an invalid receipt location reached the platform")
	}
	if _, err := os.Stat(options.Receipt); !os.IsNotExist(err) {
		t.Fatalf("receipt was written inside the input: %v", err)
	}
}

func TestSubmitRequiresACompleteWorkload(t *testing.T) {
	p := newPlatform(t, "outcome")
	ws := newTestWorkspace(t, "first")
	for name, workload := range map[string]Workload{
		"no name":              {Input: "source", CredentialResult: "outcome", Load: loadFileInput},
		"no input":             {Name: "w", CredentialResult: "outcome", Load: loadFileInput},
		"no credential result": {Name: "w", Input: "source", Load: loadFileInput},
		"no loader":            {Name: "w", Input: "source", CredentialResult: "outcome"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := p.client(t).Submit(bounded(t), workload, ws.options(p)); err == nil {
				t.Fatal("an incomplete workload submitted")
			}
		})
	}
	if _, err := os.Stat(ws.receipt); !os.IsNotExist(err) {
		t.Fatal("an incomplete workload wrote a receipt")
	}
	if uploads, runs := p.counts(t); uploads != 0 || runs != 0 {
		t.Fatal("an incomplete workload reached the platform")
	}
}
