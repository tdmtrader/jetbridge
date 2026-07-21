package outcomes_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/api/outcomes"
	"github.com/concourse/concourse/agent/deliverydiff"
	"github.com/concourse/concourse/agent/deliverydiff/deliverydifffakes"
	"github.com/concourse/concourse/agent/gitcheck"
)

// stubProvider returns a canned DiffPage, recording the args it was asked for.
type stubProvider struct {
	page      gitcheck.DiffPage
	err       error
	gotRepo   string
	gotBase   string
	gotPushed string
	gotOffset int
	gotLimit  int
	calls     int
}

func (s *stubProvider) Diff(repo, base, pushed string, offset, limit int) (gitcheck.DiffPage, error) {
	s.calls++
	s.gotRepo, s.gotBase, s.gotPushed, s.gotOffset, s.gotLimit = repo, base, pushed, offset, limit
	return s.page, s.err
}

func TestDiffHandlerServesWindow(t *testing.T) {
	os := outcomes.NewMemoryStore()
	_ = os.Ensure(&outcomes.Outcome{TicketID: 1, Repo: "tdmtrader/concourse", Branch: "agent/ticket-1", PushedSha: "head", BaseSha: "base"})
	prov := &stubProvider{page: gitcheck.DiffPage{Files: []gitcheck.DiffFile{{Path: "a.go", Patch: "diff"}}, TotalFiles: 1}}
	h := outcomes.NewDiffHandler(os, nil, prov)

	req := httptest.NewRequest("GET", "/x?offset=0&limit=50", nil)
	req.Form = map[string][]string{":ticket_id": {"1"}}
	rec := httptest.NewRecorder()
	h.GetDiff(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", rec.Code, rec.Body)
	}
	if prov.gotRepo != "tdmtrader/concourse" || prov.gotBase != "base" || prov.gotPushed != "head" {
		t.Fatalf("provider args: %+v", prov)
	}
	var page gitcheck.DiffPage
	_ = json.Unmarshal(rec.Body.Bytes(), &page)
	if len(page.Files) != 1 {
		t.Fatalf("page: %+v", page)
	}
}

func TestDiffHandlerDefaultsAndCapsWindow(t *testing.T) {
	os := outcomes.NewMemoryStore()
	_ = os.Ensure(&outcomes.Outcome{TicketID: 1, Repo: "r", Branch: "b", PushedSha: "head", BaseSha: "base"})
	prov := &stubProvider{}
	h := outcomes.NewDiffHandler(os, nil, prov)

	// no params → the frozen §1.11.1 default window (50 files)
	req := httptest.NewRequest("GET", "/x", nil)
	req.Form = map[string][]string{":ticket_id": {"1"}}
	h.GetDiff(httptest.NewRecorder(), req)
	if prov.gotOffset != 0 || prov.gotLimit != 50 {
		t.Fatalf("defaults: offset=%d limit=%d, want 0/50", prov.gotOffset, prov.gotLimit)
	}

	// oversized limit is capped at 200
	req = httptest.NewRequest("GET", "/x?offset=10&limit=9999", nil)
	req.Form = map[string][]string{":ticket_id": {"1"}}
	h.GetDiff(httptest.NewRecorder(), req)
	if prov.gotOffset != 10 || prov.gotLimit != 200 {
		t.Fatalf("cap: offset=%d limit=%d, want 10/200", prov.gotOffset, prov.gotLimit)
	}
}

func TestDiffHandler404WhenBaseUnknown(t *testing.T) {
	// fallback-seeded rows (watcher backstop) carry base_sha == "" — the
	// EXCEPTION post-B4, since harvest seeds base_sha at push time.
	os := outcomes.NewMemoryStore()
	_ = os.Ensure(&outcomes.Outcome{TicketID: 1, Repo: "r", Branch: "b", PushedSha: "head"}) // base_sha == ""
	h := outcomes.NewDiffHandler(os, nil, &stubProvider{})
	req := httptest.NewRequest("GET", "/x", nil)
	req.Form = map[string][]string{":ticket_id": {"1"}}
	rec := httptest.NewRecorder()
	h.GetDiff(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", rec.Code)
	}
}

func TestDiffHandler404WhenNoRow(t *testing.T) {
	h := outcomes.NewDiffHandler(outcomes.NewMemoryStore(), nil, &stubProvider{})
	req := httptest.NewRequest("GET", "/x", nil)
	req.Form = map[string][]string{":ticket_id": {"1"}}
	rec := httptest.NewRecorder()
	h.GetDiff(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", rec.Code)
	}
}

func TestDiffHandler404WhenDisabled(t *testing.T) {
	os := outcomes.NewMemoryStore()
	_ = os.Ensure(&outcomes.Outcome{TicketID: 1, Repo: "r", Branch: "b", PushedSha: "head", BaseSha: "base"})
	// With no stored attempt and no historical mirror, no diff is available.
	h := outcomes.NewDiffHandler(os, nil, nil)
	req := httptest.NewRequest("GET", "/x", nil)
	req.Form = map[string][]string{":ticket_id": {"1"}}
	rec := httptest.NewRecorder()
	h.GetDiff(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("disabled: code = %d, want 404", rec.Code)
	}
}

func TestDiffHandler502OnGitError(t *testing.T) {
	os := outcomes.NewMemoryStore()
	_ = os.Ensure(&outcomes.Outcome{TicketID: 1, Repo: "r", Branch: "b", PushedSha: "head", BaseSha: "base"})
	h := outcomes.NewDiffHandler(os, nil, &stubProvider{err: errors.New("git exploded")})
	req := httptest.NewRequest("GET", "/x", nil)
	req.Form = map[string][]string{":ticket_id": {"1"}}
	rec := httptest.NewRecorder()
	h.GetDiff(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("git error: code = %d, want 502", rec.Code)
	}
}

func TestDiffHandlerServesStoredDiffWithoutMirror(t *testing.T) {
	diffs := new(deliverydifffakes.FakeStore)
	diffs.GetLatestReturns(deliverydiff.DeliveryDiff{
		Files:      []gitcheck.DiffFile{{Path: "stored.go", Patch: "stored patch"}},
		TotalFiles: 1, CapturedFiles: 1,
	}, true, nil)
	h := outcomes.NewDiffHandler(outcomes.NewMemoryStore(), diffs, nil)

	rec := requestDiff(t, h, "/x", "42")
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", rec.Code, rec.Body)
	}
	var page gitcheck.DiffPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Files) != 1 || page.Files[0].Path != "stored.go" {
		t.Fatalf("page = %+v", page)
	}
}

func TestDiffHandlerStoredDiffWinsOverMirror(t *testing.T) {
	diffs := new(deliverydifffakes.FakeStore)
	diffs.GetLatestReturns(deliverydiff.DeliveryDiff{
		Files: []gitcheck.DiffFile{{Path: "stored.go", Patch: "stored"}}, TotalFiles: 1, CapturedFiles: 1,
	}, true, nil)
	provider := &stubProvider{page: gitcheck.DiffPage{Files: []gitcheck.DiffFile{{Path: "mirror.go"}}}}
	h := outcomes.NewDiffHandler(outcomes.NewMemoryStore(), diffs, provider)

	rec := requestDiff(t, h, "/x", "42")
	if rec.Code != http.StatusOK || provider.calls != 0 {
		t.Fatalf("code = %d provider calls = %d", rec.Code, provider.calls)
	}
	if !strings.Contains(rec.Body.String(), "stored.go") || strings.Contains(rec.Body.String(), "mirror.go") {
		t.Fatalf("body = %s", rec.Body)
	}
}

func TestDiffHandlerStoredPagingUsesCompleteTotal(t *testing.T) {
	diffs := new(deliverydifffakes.FakeStore)
	diffs.GetLatestReturns(deliverydiff.DeliveryDiff{
		Files:      []gitcheck.DiffFile{{Path: "a"}, {Path: "b"}, {Path: "c"}},
		TotalFiles: 5, CapturedFiles: 3, Truncated: true,
	}, true, nil)
	h := outcomes.NewDiffHandler(outcomes.NewMemoryStore(), diffs, nil)

	rec := requestDiff(t, h, "/x?offset=1&limit=999", "42")
	var page gitcheck.DiffPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || page.Offset != 1 || page.Limit != deliverydiff.MaxFiles || page.TotalFiles != 5 || !page.HasMore || len(page.Files) != 2 {
		t.Fatalf("page = %+v", page)
	}
}

func TestDiffHandler500OnStoredReadErrorWithoutMirrorFallback(t *testing.T) {
	diffs := new(deliverydifffakes.FakeStore)
	diffs.GetLatestReturns(deliverydiff.DeliveryDiff{}, false, errors.New("postgres exploded"))
	provider := &stubProvider{}
	h := outcomes.NewDiffHandler(outcomes.NewMemoryStore(), diffs, provider)

	rec := requestDiff(t, h, "/x", "42")
	if rec.Code != http.StatusInternalServerError || provider.calls != 0 {
		t.Fatalf("code = %d provider calls = %d", rec.Code, provider.calls)
	}
}

func requestDiff(t *testing.T, h *outcomes.DiffHandler, target, ticketID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", target, nil)
	req.Form = map[string][]string{":ticket_id": {ticketID}}
	rec := httptest.NewRecorder()
	h.GetDiff(rec, req)
	return rec
}
