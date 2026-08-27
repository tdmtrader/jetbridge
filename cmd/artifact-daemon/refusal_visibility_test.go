package main

import (
	"bytes"
	"go/ast"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"code.cloudfoundry.org/lager/v3/lagertest"
)

func refusalCount(t *testing.T, s *Server) float64 {
	t.Helper()
	var total float64
	families, err := s.metrics.registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() != "artifact_daemon_refusals_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			total += m.GetCounter().GetValue()
		}
	}
	return total
}

func labelsOf(t *testing.T, s *Server) []string {
	t.Helper()
	var out []string
	families, _ := s.metrics.registry.Gather()
	for _, f := range families {
		if f.GetName() != "artifact_daemon_refusals_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			var route, reason string
			for _, l := range m.GetLabel() {
				switch l.GetName() {
				case "route":
					route = l.GetValue()
				case "reason":
					reason = l.GetValue()
				}
			}
			out = append(out, route+" "+reason)
		}
	}
	return out
}

// A refused request must leave a trace on the daemon. Before this, 24 of ~30
// refusal sites wrote an http.Error and nothing else: a build failed and the
// daemon recorded nothing an operator could look at.
func TestRefusalsAreCountedAndLogged(t *testing.T) {
	storage := t.TempDir()
	logger := lagertest.NewTestLogger("refusal")
	s := newServerT(t, logger, storage, "node")
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	before := refusalCount(t, s)

	// A non-canonical key: refused by validateRequestKey.
	resp, err := http.Get(ts.URL + "/artifacts/steps%2f%2e%2fbuild-1/out")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}

	// An out-of-root dest on the mTLS-exempt resolve route.
	body := bytes.NewReader([]byte(`{"key":"a/b","dest":"/etc/passwd"}`))
	resp, err = http.Post(ts.URL+"/resolve", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if got := refusalCount(t, s) - before; got != 2 {
		t.Errorf("refusals_total rose by %v, want 2 — labels seen: %v", got, labelsOf(t, s))
	}
	if !strings.Contains(string(logger.Buffer().Contents()), "refused") {
		t.Error("nothing was logged for either refusal")
	}
}

// Labels must stay a BOUNDED set. A reason or route derived from a key, path or
// error string gives this metric one series per request, which eventually takes
// the scrape down — the classic way a well-meant metric becomes an outage.
func TestRefusalLabelsAreBounded(t *testing.T) {
	storage := t.TempDir()
	s := newServerT(t, lagertest.NewTestLogger("refusal"), storage, "node")
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	for _, key := range []string{"alpha", "beta", "gamma", "delta"} {
		// Each key differs, and each is non-canonical the same way.
		resp, err := http.Get(ts.URL + "/artifacts/steps%2f%2e%2f" + key + "/out")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}

	series := labelsOf(t, s)
	if len(series) != 1 {
		t.Errorf("four distinct keys produced %d series, want 1 — a label is derived from the request: %v",
			len(series), series)
	}
}

// Every handler refusal goes through s.refuse. Structural, because the property
// is "no future site forgets", which no behavioural test can assert.
func TestArchitecture_HandlersRefuseThroughOnePath(t *testing.T) {
	files := productionFiles(t)

	// Sites that may write a 4xx directly, with the reason they are not
	// refusals a caller can act on.
	known := map[string]string{
		"server.go:Server.refuse":                         "IS the one path",
		"durable_handlers.go:Server.handleDurableRestore": "its 404 is a durable-store MISS, a normal outcome, not a refused request",
	}

	scanned := 0
	matched := map[string]bool{}
	for name, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				return true
			}
			scanned++
			recv := ""
			if fn.Recv != nil && len(fn.Recv.List) > 0 {
				if st, ok := fn.Recv.List[0].Type.(*ast.StarExpr); ok {
					if id, ok := st.X.(*ast.Ident); ok {
						recv = id.Name + "."
					}
				}
			}
			where := name + ":" + recv + fn.Name.Name
			ast.Inspect(fn.Body, func(m ast.Node) bool {
				call, ok := m.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				id, ok := sel.X.(*ast.Ident)
				if !ok || id.Name != "http" || sel.Sel.Name != "Error" {
					return true
				}
				// Only 4xx matters; 5xx is an internal fault, not a refusal.
				status := ""
				if len(call.Args) == 3 {
					if s, ok := call.Args[2].(*ast.SelectorExpr); ok {
						status = s.Sel.Name
					}
				}
				// A COMPUTED status is flagged, not skipped. Skipping it left a
				// hole big enough for the one path itself to fall through:
				// s.refuse passes a variable, so the guard never saw it.
				if strings.HasPrefix(status, "StatusInternal") {
					return true
				}
				if reason, ok := known[where]; ok {
					matched[where] = true
					t.Logf("known: %s — %s", where, reason)
					return true
				}
				t.Errorf("%s writes a %s with http.Error. Refusals go through s.refuse so they are "+
					"counted and logged; add it above if it is genuinely not a refusal.", where, status)
				return true
			})
			return true
		})
	}
	if scanned == 0 {
		t.Fatal("scanned no production functions — this guard cannot fail")
	}
	for where := range known {
		if !matched[where] {
			t.Errorf("known entry %q matched nothing; the exemption is stale", where)
		}
	}
}
