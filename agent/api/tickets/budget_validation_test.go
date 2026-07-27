package tickets_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/api/tickets"
)

func TestCreateAndUpdateRejectNegativeBudget(t *testing.T) {
	h, store := newTestHandler("tdm")

	req := httptest.NewRequest("POST", "/api/v1/agent/tickets",
		strings.NewReader(`{"title":"t","repo":"r","budget_usd":-1}`))
	rec := httptest.NewRecorder()
	h.CreateTicket(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("create negative budget = %d, want 400", rec.Code)
	}

	store.Create(&tickets.Ticket{Title: "t", Repo: "r"})
	req = withParams(httptest.NewRequest("PUT", "/api/v1/agent/tickets/1",
		strings.NewReader(`{"budget_usd":-0.5}`)), url.Values{":ticket_id": {"1"}})
	rec = httptest.NewRecorder()
	h.UpdateTicket(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("update negative budget = %d, want 400", rec.Code)
	}
}
