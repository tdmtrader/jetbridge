package commands

import (
	"bytes"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/pagination"
)

func TestAgentHistoryCursorIsValidatedForRequestsAndReportedForNextPages(t *testing.T) {
	cursor, err := pagination.Encode(pagination.Cursor{
		CreatedAt: time.Date(2026, time.July, 22, 12, 0, 0, 123456000, time.UTC),
		ID:        42,
	})
	if err != nil {
		t.Fatal(err)
	}
	query := url.Values{"limit": {"2"}}
	if err := addAgentHistoryCursor(query, cursor); err != nil {
		t.Fatal(err)
	}
	if query.Get("cursor") != cursor {
		t.Fatalf("cursor query = %q, want %q", query.Get("cursor"), cursor)
	}
	var output bytes.Buffer
	if err := reportNextAgentHistoryCursor(&output, "snapshot", cursor); err != nil {
		t.Fatal(err)
	}
	if output.String() != "# next cursor: "+cursor+"\n" {
		t.Fatalf("cursor output = %q", output.String())
	}
}

func TestAgentHistoryCursorRejectsMalformedCallerAndServerValues(t *testing.T) {
	query := url.Values{}
	if err := addAgentHistoryCursor(query, "secret"); err == nil {
		t.Fatal("malformed request cursor accepted")
	}
	var output bytes.Buffer
	err := reportNextAgentHistoryCursor(&output, "workflow-run", "secret")
	if err == nil || !strings.Contains(err.Error(), "server returned invalid workflow-run cursor") {
		t.Fatalf("server cursor error = %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("malformed server cursor was reported: %q", output.String())
	}
}
