package pipelinerunserver

import (
	"net/http/httptest"
	"testing"

	"github.com/concourse/concourse/atc"
)

// The runs listing is reachable by an unauthenticated viewer on an exposed
// template, and the caller-supplied limit sizes the response, the batched
// payload lookup's IN list, and the rows scanned for it, so it must be bounded
// before it reaches the DB layer.
func TestPipelineRunPageBoundsCallerSuppliedLimit(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		target    string
		wantLimit int
		wantErr   bool
	}{
		{name: "absent limit uses the runs default", target: "/runs", wantLimit: defaultPipelineRunLimit},
		{name: "modest limit is honoured", target: "/runs?limit=7", wantLimit: 7},
		{name: "limit exactly at the cap is honoured", target: "/runs?limit=500", wantLimit: atc.PaginationAPIMaxLimit},
		{name: "absurd limit clamps to the cap", target: "/runs?limit=100000", wantLimit: atc.PaginationAPIMaxLimit},
		{name: "malformed limit is rejected", target: "/runs?limit=banana", wantErr: true},
		{name: "non-positive limit is rejected", target: "/runs?limit=-1", wantErr: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			page, err := pipelineRunPage(httptest.NewRequest("GET", testCase.target, nil))
			if testCase.wantErr {
				if err == nil {
					t.Fatalf("%s: expected an error, got page %+v", testCase.target, page)
				}
				return
			}
			if err != nil {
				t.Fatalf("%s: unexpected error: %v", testCase.target, err)
			}
			if page.Limit != testCase.wantLimit {
				t.Fatalf("%s: limit = %d, want %d", testCase.target, page.Limit, testCase.wantLimit)
			}
		})
	}
}

func TestPaginationCapExceedsTheRunsDefault(t *testing.T) {
	// A cap at or below the default page would silently shrink normal pages.
	if atc.PaginationAPIMaxLimit <= defaultPipelineRunLimit {
		t.Fatalf("PaginationAPIMaxLimit (%d) must exceed defaultPipelineRunLimit (%d)", atc.PaginationAPIMaxLimit, defaultPipelineRunLimit)
	}
}
