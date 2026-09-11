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

	"github.com/concourse/concourse/hangar/output"
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

	// The output plane's rules are rendered too, and that matters more than it
	// looks: they are behind `{{- if .Values.hangarOutput.enabled }}`, so a
	// default render would check none of them and this guard would report
	// coverage it does not have -- which is the exact shape of the defect it
	// was written for, one level up.
	rendered := renderChart(t,
		append([]string{
			"alertingRules.enabled=true",
			"kubernetes.artifactHelperImage=alpine@sha256:aaaa",
		}, outputSets...)...,
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

// ---------------------------------------------------------------------------
// Alert COVERAGE: the other direction
// ---------------------------------------------------------------------------
//
// The rule above runs the arrow rule -> metric: no alert may name a series
// nothing emits. This is the arrow the Phase 8 box actually asked for and
// nothing implemented -- state -> rule: no way for the plane to go at risk may
// exist with no alert behind it.
//
// They are different failures. The first is a rule that can never fire; this is
// a state that can never be alerted on, and it is the worse of the two, because
// the plane is FAIL-CLOSED from detection onward. An at-risk epoch blocks new
// captures, claim acquisitions, managed-output grants, orphan adoption and
// reclaim admission; a class with no alert behind it is builds refusing to run
// with nothing on the Prometheus rules page to say why.
//
// `output.PolicyViolations()` is a closed vocabulary, so the coverage question
// is answerable rather than approximate.
func TestEveryAtRiskTransitionIsCoveredByARenderedAlert(t *testing.T) {
	classes := output.PolicyViolations()
	if len(classes) < 10 {
		t.Fatalf("the policy-violation vocabulary is %d classes; it collapsed and this rule "+
			"would pass over almost nothing", len(classes))
	}

	rendered := renderChart(t,
		append([]string{
			"alertingRules.enabled=true",
			"kubernetes.artifactHelperImage=alpine@sha256:aaaa",
		}, outputSets...)...,
	)
	exprs := alertExpressions(rendered)

	outputRules := 0
	for _, expr := range exprs {
		if strings.Contains(expr, "concourse_hangar_output_") {
			outputRules++
		}
	}
	if outputRules < 4 {
		t.Fatalf("only %d rendered alerts mention an output-plane metric; the output rules "+
			"did not render and every claim below would be about the wrong document",
			outputRules)
	}

	// The catch-all, found rather than assumed. `Status.AtRisk` is
	// `len(reasons) != 0` over every open violation of any class, and
	// TestEveryPolicyViolationClassPutsThePlaneAtRisk proves that against the
	// real schema for all eleven -- which is what makes one aggregate rule
	// legitimate coverage rather than a blanket excuse.
	var catchAll []string
	for alert, expr := range exprs {
		if strings.Contains(expr, "concourse_hangar_output_at_risk") {
			catchAll = append(catchAll, alert)
		}
	}
	sort.Strings(catchAll)
	if len(catchAll) != 1 {
		t.Fatalf("%d rendered alerts are over concourse_hangar_output_at_risk (%v).\n\n"+
			"Exactly one is the catch-all that covers every violation class. With none, a "+
			"class with no rule of its own has no alert at all and the plane refuses work "+
			"silently; with several, the coverage claim below names no particular rule.",
			len(catchAll), catchAll)
	}

	for _, class := range classes {
		specific := ""
		for alert, expr := range exprs {
			if strings.Contains(expr, string(class)) {
				specific = alert
			}
		}
		if specific != "" {
			t.Logf("%s: covered directly by %s", class, specific)

			continue
		}
		t.Logf("%s: covered by the catch-all %s", class, catchAll[0])
	}

	// The two at-risk reasons that are NOT violation rows. `evidence_stale` is
	// set by the reader from the age bound with no row anywhere, and a policy
	// state that does not admit new work contributes `policy_<state>`. The
	// first has a rule of its own, on the AGE rather than on a failed read,
	// because past the bound "we have not checked" and "the check failed" are
	// the same amount of evidence.
	if !hasExpressionOver(exprs, "concourse_hangar_output_policy_evidence_age_seconds") {
		t.Error("no rendered alert is over the policy evidence age.\n\n" +
			"Stale evidence puts the plane at risk with no violation row anywhere, so the " +
			"per-class coverage above says nothing about it. Past the 15-minute bound the " +
			"monitor is not watching, and nothing else notices.")
	}
}

func hasExpressionOver(exprs map[string]string, metric string) bool {
	for _, expr := range exprs {
		if strings.Contains(expr, metric) {
			return true
		}
	}

	return false
}
