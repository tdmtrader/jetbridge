//go:build live
// +build live

package jetbridge_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
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
	body, err := podGet(ctx, clientset, deployed.webPod, port, "/metrics")
	if err != nil {
		t.Fatalf("GET %s:%s/metrics: %v", deployed.webPod.Name, port, err)
	}
	for _, family := range []string{"concourse_db_connections", "concourse_builds_running"} {
		if !strings.Contains(string(body), "# TYPE "+family+" ") {
			t.Errorf("web /metrics has no %s family; got %d bytes", family, len(body))
		}
	}

	prometheus := newLivePrometheus(t, ctx, clientset)
	up := prometheus.query(`up{namespace="` + deployed.namespace + `",pod="` + deployed.webPod.Name + `"}`)
	if len(up) == 0 {
		t.Fatalf("prometheus has no scrape target for web pod %s/%s: the ServiceMonitor is missing or not selected", deployed.namespace, deployed.webPod.Name)
	}
	for _, sample := range up {
		if sample.value() != "1" {
			t.Errorf("scrape target %v is down: %v", sample.Metric, sample.Value)
		}
	}

	// Two of the chart's unconditional rules; the Hangar output ones only
	// matter when that plane is on.
	loaded := prometheus.alertRules()
	for _, alert := range []string{"ConcourseWorkerStalled", "ConcourseDBConnectionPoolExhausted"} {
		if !loaded[alert] {
			t.Errorf("prometheus has not loaded alert %s: the PrometheusRule is missing or not selected", alert)
		}
	}
}

// TestLiveDaemonMetricsAreScraped is TestLiveWebMetricsAreScraped for the
// artifact daemon, and the failure it exists for is not hypothetical either.
// The daemon published artifact_daemon_* series for its whole life on its mTLS
// port, where Prometheus holds no client certificate, and the only
// ServiceMonitor selected the web: on 2026-09-24 this cluster's Prometheus
// held no artifact_daemon_ series at all. Every refusal the daemon counted
// was counted for nobody.
//
// So, per daemon pod: does its plain-HTTP metrics listener serve the daemon's
// families, does Prometheus hold an up target for it, and has Prometheus
// actually collected its series -- plus the daemon's alerting rules, which
// are only as loaded as the PrometheusRule is selected.
func TestLiveDaemonMetricsAreScraped(t *testing.T) {
	clientset, _ := kubeClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	port, ok := deployed.daemonFlag("metrics-port")
	if !ok || port == "" || port == "0" {
		t.Fatal("the deployed daemon runs no --metrics-port (artifactDaemon.metrics.port is unset): " +
			"its only /metrics is on the mTLS port, which Prometheus cannot scrape")
	}

	pods, err := clientset.CoreV1().Pods(deployed.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/component=artifact-daemon",
	})
	if err != nil {
		t.Fatalf("listing artifact daemon pods: %v", err)
	}
	var running []corev1.Pod
	for _, pod := range pods.Items {
		if pod.Status.Phase == corev1.PodRunning && pod.DeletionTimestamp == nil {
			running = append(running, pod)
		}
	}
	if len(running) == 0 {
		t.Fatalf("no running artifact daemon pod in %s", deployed.namespace)
	}

	prometheus := newLivePrometheus(t, ctx, clientset)
	for _, daemonPod := range running {
		pod := daemonPod.Name
		// Plain HTTP on the scheme and port Prometheus dials (see podGet for
		// the route), and a refusal here is a listener that is not there --
		// an image older than --metrics-port.
		body, err := podGet(ctx, clientset, daemonPod, port, "/metrics")
		if err != nil {
			t.Errorf("GET %s:%s/metrics: %v", pod, port, err)
			continue
		}
		// Only a family the daemon publishes from startup: the self-upgrade
		// restarts every daemon just before this tier runs, and a CounterVec
		// such as artifact_daemon_refusals_total has no TYPE line until its
		// first observation, so a healthy idle daemon would read as broken.
		for _, family := range []string{"artifact_daemon_peer_fetch_total"} {
			if !strings.Contains(string(body), "# TYPE "+family+" ") {
				t.Errorf("daemon %s /metrics has no %s family; got %d bytes", pod, family, len(body))
			}
		}

		target := `namespace="` + deployed.namespace + `",pod="` + pod + `"`
		up := prometheus.query(`up{` + target + `,service="` + deployed.service + `"}`)
		if len(up) == 0 {
			t.Errorf("prometheus has no scrape target for daemon pod %s/%s: the daemon ServiceMonitor is missing, not selected, or names a port its Service lacks", deployed.namespace, pod)
			continue
		}
		for _, sample := range up {
			if sample.value() != "1" {
				t.Errorf("daemon scrape target %v is down: %v", sample.Metric, sample.Value)
			}
		}

		// peer_fetch_total is initialized at startup, so a daemon that has
		// served nothing still has it; its absence is a scrape that never
		// landed, not an idle node.
		collected := prometheus.query(`count by (__name__) ({__name__=~"artifact_daemon_.*",` + target + `})`)
		names := map[string]bool{}
		for _, sample := range collected {
			names[sample.Metric["__name__"]] = true
		}
		if !names["artifact_daemon_peer_fetch_total"] {
			t.Errorf("prometheus holds %d artifact_daemon_ series names for daemon %s and not artifact_daemon_peer_fetch_total, which every daemon publishes from startup", len(names), pod)
		}
	}

	loaded := prometheus.alertRules()
	for _, alert := range []string{"ArtifactDaemonNotScraped", "ArtifactDaemonScrapeFailing", "ArtifactDaemonRefusingForLoad"} {
		if !loaded[alert] {
			t.Errorf("prometheus has not loaded alert %s: the PrometheusRule is missing, not selected, or rendered without artifactDaemon.metrics.port", alert)
		}
	}
}

// prometheusClient queries the cluster's Prometheus at the address the
// manifest names (see serviceGet for the route).
type prometheusClient struct {
	t         *testing.T
	ctx       context.Context
	clientset kubernetes.Interface
	target    livePrometheus
}

func newLivePrometheus(t *testing.T, ctx context.Context, clientset kubernetes.Interface) *prometheusClient {
	t.Helper()
	target := loadLiveManifest(t).Prometheus
	if target == nil {
		t.Fatal("the manifest names no prometheus; without one a scraped target cannot be told from an unscraped one")
	}
	return &prometheusClient{t: t, ctx: ctx, clientset: clientset, target: *target}
}

func (p *prometheusClient) get(path string, params url.Values) []byte {
	p.t.Helper()
	raw, err := serviceGet(p.ctx, p.clientset, &p.target, path, params)
	if err != nil {
		p.t.Fatalf("prometheus %s: %v", path, err)
	}
	return raw
}

type promSample struct {
	Metric map[string]string `json:"metric"`
	Value  []any             `json:"value"`
}

func (s promSample) value() any {
	if len(s.Value) != 2 {
		return nil
	}
	return s.Value[1]
}

// query runs an instant query and returns its vector.
func (p *prometheusClient) query(promql string) []promSample {
	p.t.Helper()
	var response struct {
		Data struct {
			Result []promSample `json:"result"`
		} `json:"data"`
	}
	raw := p.get("/api/v1/query", url.Values{"query": {promql}})
	if err := json.Unmarshal(raw, &response); err != nil {
		p.t.Fatalf("decoding %s: %v: %s", promql, err, raw)
	}
	return response.Data.Result
}

// alertRules returns the names of every alerting rule Prometheus has loaded.
func (p *prometheusClient) alertRules() map[string]bool {
	p.t.Helper()
	var rules struct {
		Data struct {
			Groups []struct {
				Rules []struct {
					Name string `json:"name"`
				} `json:"rules"`
			} `json:"groups"`
		} `json:"data"`
	}
	raw := p.get("/api/v1/rules", url.Values{"type": {"alert"}})
	if err := json.Unmarshal(raw, &rules); err != nil {
		p.t.Fatalf("decoding rules: %v", err)
	}
	loaded := map[string]bool{}
	for _, group := range rules.Data.Groups {
		for _, rule := range group.Rules {
			loaded[rule.Name] = true
		}
	}
	return loaded
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
		raw, err := podGet(ctx, clientset, deployed.webPod, port, path)
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

// Inside the cluster these requests dial the pod IP and the Service's DNS
// name directly; outside it (a workstation run) pod and Service addresses do
// not route, so they go through the API server's proxy instead.
//
// CI runs inside the cluster as the namespace's default ServiceAccount, which
// every task pod shares and which holds no pods/proxy or services/proxy right.
// Granting one to make these tests pass would hand it to every pipeline task,
// so the in-cluster path needs no permission beyond the network.
func inCluster() bool {
	_, err := os.Stat("/var/run/secrets/kubernetes.io/serviceaccount/token")
	return err == nil
}

// podGet fetches path from one of the deployment's pods on port.
func podGet(ctx context.Context, clientset kubernetes.Interface, pod corev1.Pod, port, path string) ([]byte, error) {
	if inCluster() {
		return directGet(ctx, "http://"+net.JoinHostPort(pod.Status.PodIP, port)+path)
	}
	return clientset.CoreV1().Pods(pod.Namespace).
		ProxyGet("http", pod.Name, port, path, nil).DoRaw(ctx)
}

// serviceGet fetches path, with query params, from a Service on its port.
func serviceGet(ctx context.Context, clientset kubernetes.Interface, svc *livePrometheus, path string, params url.Values) ([]byte, error) {
	if inCluster() {
		host := net.JoinHostPort(svc.Service+"."+svc.Namespace+".svc", svc.Port)
		return directGet(ctx, "http://"+host+path+"?"+params.Encode())
	}
	return clientset.CoreV1().Services(svc.Namespace).
		ProxyGet("http", svc.Service, svc.Port, path, flatten(params)).DoRaw(ctx)
}

// directGet is a plain GET that treats anything but 200 as an error.
func directGet(ctx context.Context, target string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s: %s", target, resp.Status, body)
	}
	return body, nil
}

func flatten(values url.Values) map[string]string {
	flat := map[string]string{}
	for key, value := range values {
		flat[key] = value[0]
	}
	return flat
}
