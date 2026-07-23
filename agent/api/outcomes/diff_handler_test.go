package outcomes_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/concourse/concourse/agent/api/outcomes"
	"github.com/concourse/concourse/agent/gitcheck"
	"github.com/concourse/concourse/agent/projection"
	"github.com/concourse/concourse/agent/snapshot"
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
}

func (s *stubProvider) Diff(repo, base, pushed string, offset, limit int) (gitcheck.DiffPage, error) {
	s.gotRepo, s.gotBase, s.gotPushed, s.gotOffset, s.gotLimit = repo, base, pushed, offset, limit
	return s.page, s.err
}

type stubTicketProjectionResolver struct {
	resolution outcomes.TicketRepositoryChangeResolution
	err        error
	ticketID   int
}

func (resolver *stubTicketProjectionResolver) ResolveTicketRepositoryChange(_ context.Context, ticketID int) (outcomes.TicketRepositoryChangeResolution, error) {
	resolver.ticketID = ticketID
	return resolver.resolution, resolver.err
}

func TestDiffHandlerPrefersDurableRepositoryChangeWithoutLiveMirror(t *testing.T) {
	resolver := &stubTicketProjectionResolver{resolution: outcomes.TicketRepositoryChangeResolution{
		Found: true,
		Change: projection.RepositoryChange{
			SnapshotID: snapshot.SnapshotID(71), Status: projection.RepositoryChangeProjectionReady,
			Files: []gitcheck.ChangedFile{
				{Path: "a.go", Patch: "diff-a"}, {Path: "b.go", Patch: "diff-b", Truncated: true},
			},
			FileCount: 2,
		},
	}}
	handler := outcomes.NewDiffHandlerWithProjection(outcomes.NewMemoryStore(), nil, resolver)
	request := httptest.NewRequest(http.MethodGet, "/x?offset=1&limit=1", nil)
	request.Form = map[string][]string{":ticket_id": {"17"}}
	recorder := httptest.NewRecorder()
	handler.GetDiff(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var page gitcheck.DiffPage
	if err := json.Unmarshal(recorder.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if resolver.ticketID != 17 || page.Offset != 1 || page.Limit != 1 || page.TotalFiles != 2 || page.HasMore ||
		len(page.Files) != 1 || page.Files[0].Path != "b.go" || !page.Files[0].Truncated {
		t.Fatalf("page = %#v resolver ticket=%d", page, resolver.ticketID)
	}
}

func TestDiffHandlerNeverFallsBackForProjectionNativeAttempt(t *testing.T) {
	store := outcomes.NewMemoryStore()
	_ = store.Ensure(&outcomes.Outcome{TicketID: 18, Repo: "r", BaseSha: "base", PushedSha: "head"})
	provider := &stubProvider{page: gitcheck.DiffPage{TotalFiles: 99}}
	resolver := &stubTicketProjectionResolver{resolution: outcomes.TicketRepositoryChangeResolution{Legacy: false}}
	handler := outcomes.NewDiffHandlerWithProjection(store, provider, resolver)
	request := httptest.NewRequest(http.MethodGet, "/x", nil)
	request.Form = map[string][]string{":ticket_id": {"18"}}
	recorder := httptest.NewRecorder()
	handler.GetDiff(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("code = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if provider.gotRepo != "" {
		t.Fatalf("projection-native attempt reached live mirror: %#v", provider)
	}
}

func TestDiffHandlerFallsBackToLiveMirrorOnlyWhenResolverMarksLegacy(t *testing.T) {
	store := outcomes.NewMemoryStore()
	_ = store.Ensure(&outcomes.Outcome{TicketID: 19, Repo: "legacy", BaseSha: "base", PushedSha: "head"})
	provider := &stubProvider{page: gitcheck.DiffPage{TotalFiles: 1}}
	resolver := &stubTicketProjectionResolver{resolution: outcomes.TicketRepositoryChangeResolution{Legacy: true}}
	handler := outcomes.NewDiffHandlerWithProjection(store, provider, resolver)
	request := httptest.NewRequest(http.MethodGet, "/x", nil)
	request.Form = map[string][]string{":ticket_id": {"19"}}
	recorder := httptest.NewRecorder()
	handler.GetDiff(recorder, request)
	if recorder.Code != http.StatusOK || provider.gotRepo != "legacy" {
		t.Fatalf("code=%d provider=%#v body=%s", recorder.Code, provider, recorder.Body.String())
	}
}

func TestDiffHandlerServesWindow(t *testing.T) {
	os := outcomes.NewMemoryStore()
	_ = os.Ensure(&outcomes.Outcome{TicketID: 1, Repo: "tdmtrader/concourse", Branch: "agent/ticket-1", PushedSha: "head", BaseSha: "base"})
	prov := &stubProvider{page: gitcheck.DiffPage{Files: []gitcheck.DiffFile{{Path: "a.go", Patch: "diff"}}, TotalFiles: 1}}
	h := outcomes.NewDiffHandler(os, prov)

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
	h := outcomes.NewDiffHandler(os, prov)

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
	h := outcomes.NewDiffHandler(os, &stubProvider{})
	req := httptest.NewRequest("GET", "/x", nil)
	req.Form = map[string][]string{":ticket_id": {"1"}}
	rec := httptest.NewRecorder()
	h.GetDiff(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", rec.Code)
	}
}

func TestDiffHandler404WhenNoRow(t *testing.T) {
	h := outcomes.NewDiffHandler(outcomes.NewMemoryStore(), &stubProvider{})
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
	// nil provider == diff API disabled (no --agent-outcome-git-dir)
	h := outcomes.NewDiffHandler(os, nil)
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
	h := outcomes.NewDiffHandler(os, &stubProvider{err: errors.New("git exploded")})
	req := httptest.NewRequest("GET", "/x", nil)
	req.Form = map[string][]string{":ticket_id": {"1"}}
	rec := httptest.NewRecorder()
	h.GetDiff(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("git error: code = %d, want 502", rec.Code)
	}
}
