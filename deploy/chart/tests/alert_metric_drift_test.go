package tests

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// An alerting rule that names a metric the binary never emits is not a broken
// alert -- it is a silent absence of one. PromQL has no notion of an unknown
// series: the expression evaluates to the empty vector, the rule sits in
// Inactive forever, and the operator sees a named alert on the Prometheus rules
// page that looks exactly like working coverage.
//
// Three of this chart's four rules were in that state. They asked for
// concourse_db_connections_open, concourse_db_connections_max,
// concourse_k8s_pod_startup_duration_bucket and concourse_worker_heartbeat_age.
// The ATC emits concourse_db_connections{dbname}, no maximum at all,
// concourse_k8s_pod_startup_duration_milliseconds_bucket, and
// concourse_workers_registered{state}. Every one of those names was plausible.
// None of them existed.
//
// So resolve the names against their actual definition rather than against
// anyone's memory of them. Every Prometheus metric this project publishes is
// declared as a prometheus.*Opts literal in atc/metric/emitter/prometheus.go,
// so the AST of that file is the oracle -- the same move
// TestChartRendersOnlyFlagsTheBinaryAccepts makes by reading --help instead of
// trusting the chart.
func TestAlertRulesReferenceMetricsTheBinaryEmits(t *testing.T) {
	declared := declaredPrometheusMetrics(t)
	if len(declared) < 20 {
		t.Fatalf("only %d metric declarations parsed out of prometheus.go; the "+
			"literals moved and this test is no longer reading anything. Fix the "+
			"parse -- an oracle that finds nothing passes everything.", len(declared))
	}

	rendered := renderChart(t,
		"alertingRules.enabled=true",
		"kubernetes.artifactHelperImage=alpine@sha256:aaaa",
	)

	exprs := alertExpressions(rendered)
	if len(exprs) == 0 {
		t.Fatal("alertingRules.enabled=true rendered no alert expressions; if the " +
			"gate or template moved, move this test with it")
	}

	// Histograms publish _bucket/_sum/_count; counters conventionally end
	// _total and are declared that way. Strip only the histogram suffixes.
	histogramSuffix := regexp.MustCompile(`_(bucket|sum|count)$`)
	metricRef := regexp.MustCompile(`\bconcourse_[a-zA-Z0-9_]+`)

	for alert, expr := range exprs {
		for _, ref := range metricRef.FindAllString(expr, -1) {
			base := histogramSuffix.ReplaceAllString(ref, "")
			if declared[ref] || declared[base] {
				continue
			}
			t.Errorf("alert %s references %q, which the ATC never emits.\n"+
				"  expression: %s\n"+
				"  This rule cannot fire: PromQL returns an empty vector for an "+
				"unknown series, so the alert stays Inactive forever while looking "+
				"like coverage.\n"+
				"  Closest declared names: %v",
				alert, ref, expr, nearestMetrics(declared, ref))
		}
	}
}

// declaredPrometheusMetrics builds the set of fully-qualified metric names from
// the prometheus.*Opts composite literals, joining Namespace, Subsystem and
// Name the way the client library does.
func declaredPrometheusMetrics(t *testing.T) map[string]bool {
	t.Helper()

	path := filepath.Join(repoRoot(t), "atc", "metric", "emitter", "prometheus.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}

	declared := map[string]bool{}

	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}

		parts := map[string]string{}
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := kv.Key.(*ast.Ident)
			if !ok {
				continue
			}
			basic, ok := kv.Value.(*ast.BasicLit)
			if !ok || basic.Kind != token.STRING {
				continue
			}
			val, err := strconv.Unquote(basic.Value)
			if err != nil {
				continue
			}
			parts[key.Name] = val
		}

		if parts["Name"] == "" {
			return true
		}

		segments := []string{}
		for _, k := range []string{"Namespace", "Subsystem", "Name"} {
			if parts[k] != "" {
				segments = append(segments, parts[k])
			}
		}
		declared[strings.Join(segments, "_")] = true
		return true
	})

	return declared
}

// alertExpressions maps alert name to expression from the rendered chart. The
// rules are plain YAML lists, so a line scan is enough and avoids depending on
// the rule schema.
func alertExpressions(rendered string) map[string]string {
	out := map[string]string{}
	var current string
	for _, line := range strings.Split(rendered, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "- alert:"):
			current = strings.TrimSpace(strings.TrimPrefix(trimmed, "- alert:"))
		case strings.HasPrefix(trimmed, "expr:") && current != "":
			out[current] = strings.TrimSpace(strings.TrimPrefix(trimmed, "expr:"))
			current = ""
		}
	}
	return out
}

// nearestMetrics offers the declared names sharing the longest prefix, so the
// failure names the metric that was probably meant.
func nearestMetrics(declared map[string]bool, want string) []string {
	type scored struct {
		name  string
		score int
	}
	var all []scored
	for name := range declared {
		n := 0
		for n < len(name) && n < len(want) && name[n] == want[n] {
			n++
		}
		all = append(all, scored{name, n})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].score != all[j].score {
			return all[i].score > all[j].score
		}
		return all[i].name < all[j].name
	})

	var out []string
	for i := 0; i < len(all) && i < 3; i++ {
		out = append(out, all[i].name)
	}
	return out
}
