//go:build live
// +build live

package jetbridge_test

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestLiveWebMetricsAreScraped proves the deployed web's metrics reach the
// place alerts are evaluated, not just that a port is open.
//
// The failure this exists for already happened once: the ServiceMonitor and
// PrometheusRule sat in the namespace for 62 days without the label the
// cluster's Prometheus selects on, so no Concourse alert could ever fire and
// every one of them read as green. The web served metrics the whole time. So
// the test asks three things in order -- does the web serve Prometheus text,
// does Prometheus hold an up target for it, and are the Concourse alerting
// rules loaded -- and the second and third are the ones that were false.
func TestLiveWebMetricsAreScraped(t *testing.T) {
	clientset, _ := kubeClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	port, err := deployed.webPort("metrics")
	if err != nil {
		t.Fatal(err)
	}
	body, err := clientset.CoreV1().Pods(deployed.namespace).
		ProxyGet("http", deployed.webPod.Name, port, "/metrics", nil).DoRaw(ctx)
	if err != nil {
		t.Fatalf("GET %s:%s/metrics: %v", deployed.webPod.Name, port, err)
	}
	for _, family := range []string{"concourse_db_connections", "concourse_builds_running"} {
		if !strings.Contains(string(body), "# TYPE "+family+" ") {
			t.Errorf("web /metrics has no %s family; got %d bytes", family, len(body))
		}
	}

	prometheus := loadLiveManifest(t).Prometheus
	if prometheus == nil {
		t.Fatal("the manifest names no prometheus; without one this test cannot tell a scraped web from an unscraped one")
	}
	query := func(path string, params url.Values) []byte {
		t.Helper()
		raw, err := clientset.CoreV1().Services(prometheus.Namespace).
			ProxyGet("http", prometheus.Service, prometheus.Port, path, flatten(params)).DoRaw(ctx)
		if err != nil {
			t.Fatalf("prometheus %s: %v", path, err)
		}
		return raw
	}

	var up struct {
		Data struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
				Value  []any             `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	raw := query("/api/v1/query", url.Values{"query": {`up{namespace="` + deployed.namespace + `",pod="` + deployed.webPod.Name + `"}`}})
	if err := json.Unmarshal(raw, &up); err != nil {
		t.Fatalf("decoding up query: %v: %s", err, raw)
	}
	if len(up.Data.Result) == 0 {
		t.Fatalf("prometheus has no scrape target for web pod %s/%s: the ServiceMonitor is missing or not selected", deployed.namespace, deployed.webPod.Name)
	}
	for _, sample := range up.Data.Result {
		if len(sample.Value) != 2 || sample.Value[1] != "1" {
			t.Errorf("scrape target %v is down: %v", sample.Metric, sample.Value)
		}
	}

	var rules struct {
		Data struct {
			Groups []struct {
				Rules []struct {
					Name string `json:"name"`
				} `json:"rules"`
			} `json:"groups"`
		} `json:"data"`
	}
	raw = query("/api/v1/rules", url.Values{"type": {"alert"}})
	if err := json.Unmarshal(raw, &rules); err != nil {
		t.Fatalf("decoding rules: %v", err)
	}
	loaded := map[string]bool{}
	for _, group := range rules.Data.Groups {
		for _, rule := range group.Rules {
			loaded[rule.Name] = true
		}
	}
	// Two of the chart's unconditional rules; the Hangar output ones only
	// matter when that plane is on.
	for _, alert := range []string{"ConcourseWorkerStalled", "ConcourseDBConnectionPoolExhausted"} {
		if !loaded[alert] {
			t.Errorf("prometheus has not loaded alert %s: the PrometheusRule is missing or not selected", alert)
		}
	}
}

// TestLiveWebMCPIsMounted proves the deployed web serves MCP: the protected
// resource and its authorization server both publish metadata naming the
// deployment's own external URL.
//
// This covers the wiring only -- the flag, the client registration and the
// external URL agreeing -- not an authorized call, which needs an OAuth
// consent this tier has no user for.
func TestLiveWebMCPIsMounted(t *testing.T) {
	clientset, _ := kubeClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	externalURL, ok := deployed.webFlag("external-url")
	if !ok || externalURL == "" {
		t.Fatal("the deployed web has no --external-url; MCP refuses to start without one")
	}
	base := strings.TrimRight(externalURL, "/")
	port, err := deployed.webPort("http")
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string) map[string]any {
		t.Helper()
		raw, err := clientset.CoreV1().Pods(deployed.namespace).
			ProxyGet("http", deployed.webPod.Name, port, path, nil).DoRaw(ctx)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("GET %s: not JSON: %v: %s", path, err, raw)
		}
		return doc
	}

	resource := get("/.well-known/oauth-protected-resource/api/v1/mcp")
	if resource["resource"] != base+"/api/v1/mcp" {
		t.Errorf("protected resource = %v, want %s/api/v1/mcp", resource["resource"], base)
	}
	server := get("/.well-known/oauth-authorization-server/mcp/oauth")
	if server["issuer"] != base+"/mcp/oauth" {
		t.Errorf("authorization server issuer = %v, want %s/mcp/oauth", server["issuer"], base)
	}
}

func flatten(values url.Values) map[string]string {
	flat := map[string]string{}
	for key, value := range values {
		flat[key] = value[0]
	}
	return flat
}
