package tests

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/concourse/concourse/atc/atccmd"
	"github.com/jessevdk/go-flags"
)

// TestWebServiceMonitorScrapesAPortTheWebNodeActuallyServes closes the gap
// that made every web-side Prometheus metric silently absent: the monitor
// pointed at the API port and path /metrics, which the ATC does not route.
// Nothing about that is observable from the chart alone, so this walks the
// whole chain — ServiceMonitor endpoint -> Service port -> container port ->
// the --prometheus-bind-port the web process is actually given.
func TestWebServiceMonitorScrapesAPortTheWebNodeActuallyServes(t *testing.T) {
	const metricsPort = 9411
	manifests := renderChart(t,
		"serviceMonitor.enabled=true",
		"web.metrics.enabled=true",
		fmt.Sprintf("web.metrics.port=%d", metricsPort),
	)

	monitor := findServiceMonitor(t, manifests, "test-release-concourse-jetbridge")
	if len(monitor.Spec.Endpoints) != 1 {
		t.Fatalf("web ServiceMonitor endpoints = %d, want 1", len(monitor.Spec.Endpoints))
	}
	endpoint := monitor.Spec.Endpoints[0]
	if endpoint.Path != "/metrics" {
		t.Errorf("web endpoint path = %q, want /metrics", endpoint.Path)
	}

	// The endpoint names a Service port, so an endpoint naming a port the
	// Service does not publish produces a target with no address at all.
	service := findService(t, manifests, "-web")
	var servicePort *int
	var targetPort any
	for _, port := range service.Spec.Ports {
		if port.Name == endpoint.Port {
			value := port.Port
			servicePort = &value
			targetPort = port.TargetPort
		}
	}
	if servicePort == nil {
		var names []string
		for _, port := range service.Spec.Ports {
			names = append(names, port.Name)
		}
		t.Fatalf("web ServiceMonitor scrapes port %q, but the web Service publishes only %v", endpoint.Port, names)
	}

	// And the Service port must resolve to a container port the pod declares,
	// by name or by number.
	web := findDeployment(t, manifests, "-web")
	if len(web.Spec.Template.Spec.Containers) == 0 {
		t.Fatal("web Deployment has no containers")
	}
	container := web.Spec.Template.Spec.Containers[0]
	resolved := -1
	for _, port := range container.Ports {
		if fmt.Sprint(targetPort) == port.Name || fmt.Sprint(targetPort) == strconv.Itoa(port.ContainerPort) {
			resolved = port.ContainerPort
		}
	}
	if resolved < 0 {
		t.Fatalf("Service port %q targets %v, which is not among the web container ports %+v",
			endpoint.Port, targetPort, container.Ports)
	}

	// Finally, the ATC only opens that socket when it is handed both flags;
	// with either missing it silently registers no Prometheus emitter.
	wantIP := "--prometheus-bind-ip=0.0.0.0"
	wantPort := fmt.Sprintf("--prometheus-bind-port=%d", resolved)
	if !slices.Contains(container.Args, wantIP) || !slices.Contains(container.Args, wantPort) {
		t.Fatalf("web container is not started with %q and %q; the scraped port serves nothing. args=%v",
			wantIP, wantPort, container.Args)
	}
}

// TestWebServiceMonitorRefusesAnEndpointTheATCDoesNotServe pins the loud
// failure. Prometheus reports a target that returns the web UI as a scrape
// parse error and nothing else, so the broken combination must not render.
//
// It is scoped to an operator who ASKS for the web scrape. Failing the default
// too froze an entire GitOps deployment on 2026-08-03 over a metrics gap: Argo
// could not render any manifest, so unrelated changes stopped syncing while the
// cluster kept running the last good revision.
func TestWebServiceMonitorRefusesAnEndpointTheATCDoesNotServe(t *testing.T) {
	output := renderChartFailure(t, "serviceMonitor.enabled=true", "serviceMonitor.web.enabled=true")
	for _, want := range []string{"web.metrics.enabled", "serviceMonitor.web.enabled=false"} {
		if !strings.Contains(output, want) {
			t.Errorf("render failure does not mention %q:\n%s", want, output)
		}
	}
}

// TestWebServiceMonitorFollowsMetricsWhenUnset is the other half of that
// contract: left alone, the web scrape tracks web.metrics.enabled, so enabling
// metrics is the only step an operator takes and leaving them off yields the
// daemon monitor by itself rather than a failed render.
func TestWebServiceMonitorFollowsMetricsWhenUnset(t *testing.T) {
	off := renderChart(t, "serviceMonitor.enabled=true", "artifactDaemon.enabled=true")
	if _, found := lookupServiceMonitor(off, "test-release-concourse-jetbridge"); found {
		t.Error("the web ServiceMonitor rendered while web.metrics.enabled was false")
	}
	if _, found := lookupServiceMonitor(off, "-artifact-daemon"); !found {
		t.Error("the artifact-daemon ServiceMonitor must still render when the web scrape is off")
	}

	on := renderChart(t, "serviceMonitor.enabled=true", "artifactDaemon.enabled=true", "web.metrics.enabled=true")
	monitor := findServiceMonitor(t, on, "test-release-concourse-jetbridge")
	if len(monitor.Spec.Endpoints) != 1 || monitor.Spec.Endpoints[0].Port != "metrics" {
		t.Errorf("web ServiceMonitor endpoints = %+v, want a single port named metrics", monitor.Spec.Endpoints)
	}
}

// TestWebMetricsPortIsAbsentUnlessEnabled keeps the unauthenticated exposition
// endpoint opt-in: no container port, no Service port, no flags by default.
func TestWebMetricsPortIsAbsentUnlessEnabled(t *testing.T) {
	manifests := renderChart(t)
	web := findDeployment(t, manifests, "-web")
	container := web.Spec.Template.Spec.Containers[0]
	for _, port := range container.Ports {
		if port.Name == "metrics" {
			t.Error("web container declares a metrics port without web.metrics.enabled")
		}
	}
	for _, arg := range container.Args {
		if strings.HasPrefix(arg, "--prometheus-bind") {
			t.Errorf("web container renders %q without web.metrics.enabled", arg)
		}
	}
	for _, port := range findService(t, manifests, "-web").Spec.Ports {
		if port.Name == "metrics" {
			t.Error("web Service publishes a metrics port without web.metrics.enabled")
		}
	}
}

// TestWebMetricsPortRejectsCollisions covers the failure the ATC reports only
// as a startup error: its Prometheus listener is an independent net.Listen, so
// reusing the API, TSA, or TLS port fails to bind.
func TestWebMetricsPortRejectsCollisions(t *testing.T) {
	for _, sets := range [][]string{
		{"web.metrics.enabled=true", "web.metrics.port=8080"},
		{"web.metrics.enabled=true", "web.metrics.port=2222"},
		{"web.metrics.enabled=true", "web.tls.enabled=true", "web.metrics.port=443"},
		{"web.metrics.enabled=true", "web.metrics.port=0"},
		{"web.metrics.enabled=true", "web.metrics.bindIP="},
	} {
		if output := renderChartFailure(t, sets...); output == "" {
			t.Errorf("chart accepted %v", sets)
		}
	}
}

// emittedSeries maps every metric name the chart's alerting rules select to
// the instrument that proves the series exists. Prometheus evaluates a
// selector matching nothing as an empty vector forever — no error, no target,
// no signal — so a renamed, re-united, or deleted instrument must break this
// test rather than quietly disarm an alert.
//
// registration is a regexp that must still match file. For the OTel pipeline
// the exported name is the instrument name with dots replaced by underscores
// plus the suffix the OTLP-to-Prometheus translation appends, so the unit is
// pinned alongside the name: dropping WithUnit("s") silently renames
// ..._duration_seconds_count to ..._duration_count.
var emittedSeries = map[string]struct{ file, registration string }{
	"concourse_k8s_image_pull_failures_total": {
		"atc/metric/emitter/prometheus.go",
		`Subsystem:\s+"k8s",\s*\n\s*Name:\s+"image_pull_failures_total"`,
	},
	"concourse_k8s_pod_startup_duration_milliseconds_bucket": {
		"atc/metric/emitter/prometheus.go",
		`prometheus\.NewHistogram\((?s:.*?)Subsystem:\s+"k8s",\s*\n\s*Name:\s+"pod_startup_duration_milliseconds"`,
	},
	"concourse_db_connections": {
		"atc/metric/emitter/prometheus.go",
		`prometheus\.NewGaugeVec\((?s:.*?)Subsystem:\s+"db",\s*\n\s*Name:\s+"connections"(?s:.*?)\[\]string\{"dbname"\}`,
	},
	"concourse_workers_registered": {
		"atc/metric/emitter/prometheus.go",
		`Subsystem:\s+"workers",\s*\n\s*Name:\s+"registered"(?s:.*?)\[\]string\{"state"\}`,
	},
	"artifact_daemon_snapshot_operations_total": {
		"cmd/artifact-daemon/metrics.go",
		`Namespace:\s+"artifact_daemon",\s*\n\s*Name:\s+"snapshot_operations_total"`,
	},
}

// promQLFunctions are the PromQL identifiers an expression may contain that
// are not metric names.
var promQLFunctions = map[string]bool{
	"increase": true, "rate": true, "histogram_quantile": true, "or": true,
	"and": true, "unless": true, "sum": true, "avg": true, "max": true, "min": true,
	"by": true, "without": true, "count": true, "absent": true,
}

var promQLIdentifier = regexp.MustCompile(`[a-zA-Z_][a-zA-Z0-9_]*`)

// TestEveryAlertSelectsAnEmittedSeries walks each rendered alert expression,
// extracts the metric names it selects, and requires every one to be backed
// by an instrument that still exists in the Go sources.
func TestEveryAlertSelectsAnEmittedSeries(t *testing.T) {
	repoRoot, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	sources := map[string]string{}
	for _, series := range emittedSeries {
		if _, loaded := sources[series.file]; loaded {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(repoRoot, series.file))
		if err != nil {
			t.Fatalf("read %s: %v", series.file, err)
		}
		sources[series.file] = string(raw)
	}
	for name, series := range emittedSeries {
		matched, err := regexp.MatchString(series.registration, sources[series.file])
		if err != nil {
			t.Fatalf("%s: bad registration pattern: %v", name, err)
		}
		if !matched {
			t.Errorf("%s: %s no longer registers it as %s; every alert selecting that series can never fire",
				name, series.file, series.registration)
		}
	}

	rule := findPrometheusRule(t, renderChart(t, "alertingRules.enabled=true"))
	alerts := 0
	for _, group := range rule.Spec.Groups {
		for _, alert := range group.Rules {
			alerts++
			// Strip label matchers, durations, and numeric literals so only
			// metric names and PromQL functions remain.
			expr := regexp.MustCompile(`\{[^}]*\}`).ReplaceAllString(alert.Expr, " ")
			expr = regexp.MustCompile(`\[[^]]*\]`).ReplaceAllString(expr, " ")
			for _, identifier := range promQLIdentifier.FindAllString(expr, -1) {
				if promQLFunctions[identifier] {
					continue
				}
				if _, known := emittedSeries[identifier]; !known {
					t.Errorf("alert %s selects %q, which no exporter emits (see emittedSeries)",
						alert.Alert, identifier)
				}
			}
		}
	}
	if alerts == 0 {
		t.Fatal("rendered PrometheusRule contains no alerts")
	}
}

// TestDBPoolAlertComparesAgainstTheConfiguredMaximum covers the alert with no
// equivalent series: nothing publishes a pool maximum, so the threshold comes
// from the same values that size the pools. The fallbacks must track the
// binary's own flag defaults, because the Deployment omits the flag entirely
// when the value is unset.
func TestDBPoolAlertComparesAgainstTheConfiguredMaximum(t *testing.T) {
	cmd := &atccmd.ATCCommand{}
	parser := flags.NewParser(cmd, flags.Default)
	parser.NamespaceDelimiter = "-"
	runCmd := parser.Find("run")
	if runCmd == nil {
		t.Fatal("ATC run command is missing")
	}
	binaryDefault := func(flagName string) string {
		option := runCmd.FindOptionByLongName(flagName)
		if option == nil {
			t.Fatalf("web binary no longer defines --%s", flagName)
		}
		if len(option.Default) != 1 {
			t.Fatalf("--%s default = %v, want exactly one value", flagName, option.Default)
		}
		return option.Default[0]
	}

	// With the pool sizes unset the Deployment renders no --*-max-conns flag,
	// so the alert must fall back to whatever the binary itself would use.
	rule := findPrometheusRule(t, renderChart(t,
		"alertingRules.enabled=true",
		"web.apiMaxConns=null",
		"web.backendMaxConns=null",
	))
	alert, found := rule.alert("ConcourseDBConnectionPoolExhausted")
	if !found {
		t.Fatal("PrometheusRule does not contain ConcourseDBConnectionPoolExhausted")
	}
	want := fmt.Sprintf(`concourse_db_connections{dbname="api"} >= %s or concourse_db_connections{dbname="backend"} >= %s`,
		binaryDefault("api-max-conns"), binaryDefault("backend-max-conns"))
	if alert.Expr != want {
		t.Errorf("unset-pool expression = %q, want %q", alert.Expr, want)
	}

	// And an operator who resizes the pools must get an alert that tracks it.
	rule = findPrometheusRule(t, renderChart(t,
		"alertingRules.enabled=true",
		"web.apiMaxConns=33",
		"web.backendMaxConns=77",
	))
	alert, _ = rule.alert("ConcourseDBConnectionPoolExhausted")
	want = `concourse_db_connections{dbname="api"} >= 33 or concourse_db_connections{dbname="backend"} >= 77`
	if alert.Expr != want {
		t.Errorf("resized-pool expression = %q, want %q", alert.Expr, want)
	}
	if !strings.Contains(alert.Annotations["description"], "api 33") ||
		!strings.Contains(alert.Annotations["description"], "backend 77") {
		t.Errorf("alert description does not name the thresholds it fires on: %q", alert.Annotations["description"])
	}
}

// TestAlertingRulesWarnWhenNoExporterFeedsThem covers the residual gap the
// expressions alone cannot close: correct selectors still produce nothing when
// the pipeline behind them is not wired, and Prometheus reports an empty
// vector exactly like a healthy one. The install output has to say so.
func TestAlertingRulesWarnWhenNoExporterFeedsThem(t *testing.T) {
	notes := renderNotes(t, "alertingRules.enabled=true")
	for _, want := range []string{
		"alertingRules.enabled is true but web.metrics.enabled is false",
		"Set web.metrics.enabled=true",
		"alertingRules.enabled is true but otelMetrics.otlpAddress is empty",
	} {
		if !strings.Contains(notes, want) {
			t.Errorf("NOTES does not warn %q:\n%s", want, notes)
		}
	}

	wired := renderNotes(t,
		"alertingRules.enabled=true",
		"web.metrics.enabled=true",
		"otelMetrics.otlpAddress=collector.monitoring.svc:4317",
	)
	for _, unwanted := range []string{"web.metrics.enabled is false", "otelMetrics.otlpAddress is empty"} {
		if strings.Contains(wired, unwanted) {
			t.Errorf("NOTES still warns %q after the source is configured:\n%s", unwanted, wired)
		}
	}
}

// TestRetiredAlertSelectorsAreGone names the four expressions that could never
// fire. Each is listed with why no rename could rescue it, so a future edit
// cannot reintroduce one believing it is merely a naming preference.
func TestRetiredAlertSelectorsAreGone(t *testing.T) {
	rule := findPrometheusRule(t, renderChart(t, "alertingRules.enabled=true"))
	// Asserted against the parsed expressions, not the rendered text: the
	// template comments deliberately name these selectors to explain why they
	// were replaced, and that prose must not make this test pass or fail.
	var exprs []string
	for _, group := range rule.Spec.Groups {
		for _, alert := range group.Rules {
			exprs = append(exprs, alert.Expr)
		}
	}
	joined := strings.Join(exprs, "\n")
	for selector, reason := range map[string]string{
		"concourse_db_connections_open":                   "no instrument of that name exists; the gauge is concourse_db_connections",
		"concourse_db_connections_max":                    "no exporter publishes a pool maximum at all, so no rename could rescue this",
		"concourse_k8s_pod_startup_duration_bucket":       "the histogram is registered as pod_startup_duration_milliseconds",
		"concourse_worker_heartbeat_age":                  "the OTel gauge has no production call site, so it is never emitted under any name",
		"concourse_agent_recovery_restore_duration_count": `the instrument is WithUnit("s"), so the exported counter is ..._seconds_count`,
	} {
		if strings.Contains(joined, selector) {
			t.Errorf("an alert still selects %s: %s", selector, reason)
		}
	}
}
