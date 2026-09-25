package tests

import (
	"fmt"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// A ServiceMonitor names a Service port, that port forwards to a container
// port, and something must be listening there. Nothing validates any link in
// that chain: prometheus-operator will happily build a target for a port that
// serves something else entirely, and the only symptom is one target marked
// down in a UI nobody opens while nothing is firing.
//
// This chart shipped `- port: http`, which is a perfectly real Service port --
// the 8080 web UI. So Prometheus scraped the Elm SPA, reported `unsupported
// Content-Type "text/html"`, and the `concourse` PrometheusRule (Hangar
// residency, artifact daemon failures, the lot) evaluated against no data for
// as long as it had existed.
//
// Checking that the scraped port merely EXISTS does not catch that -- my first
// version of this test passed against the very bug it was written for. The
// invariant worth asserting is the whole chain: the port Prometheus scrapes
// must resolve to the port the ATC binds its metrics listener to.
func TestServiceMonitorScrapesTheMetricsListener(t *testing.T) {
	rendered := renderChart(t,
		"serviceMonitor.enabled=true",
		"metrics.enabled=true",
		"metrics.port=9391",
		"artifactDaemon.metrics.port=9392",
		"kubernetes.artifactHelperImage=alpine@sha256:aaaa",
	)

	docs := splitYAMLDocs(rendered)

	// Two: the web and the artifact daemon. The daemon went unscraped for as
	// long as it published metrics because the only ServiceMonitor selected
	// the web's Service, so a monitor going missing is itself the failure.
	monitors := serviceMonitors(docs)
	if len(monitors) != 2 {
		t.Fatalf("serviceMonitor.enabled=true with artifactDaemon.metrics.port rendered %d "+
			"ServiceMonitors, want 2 (web and artifact daemon). If the gate moved, move this "+
			"test with it -- a test that finds nothing to check is how the http/metrics "+
			"mismatch survived in the first place.", len(monitors))
	}

	for _, m := range monitors {
		svc := serviceMatching(t, docs, m.selector)
		if svc.name == "" {
			t.Errorf("ServiceMonitor selector %v matched no rendered Service; it can "+
				"never produce a target", m.selector)
			continue
		}

		bindPort, containerPorts := metricsListenerBehind(t, docs, svc.podSelector)
		if bindPort == "" {
			t.Errorf("nothing behind Service %s binds a metrics listener: the ATC "+
				"registers its Prometheus emitter only when both bind flags are set "+
				"(IsConfigured in atc/metric/emitter/prometheus.go), and the artifact "+
				"daemon opens its plain-HTTP listener only with --metrics-port, so "+
				"nothing is listening for this ServiceMonitor to scrape", svc.name)
			continue
		}

		for _, scraped := range m.ports {
			target, ok := svc.ports[scraped]
			if !ok {
				t.Errorf("ServiceMonitor scrapes port %q, which Service %s does not "+
					"define (%v); prometheus-operator drops the endpoint silently",
					scraped, svc.name, svc.portNames())
				continue
			}

			// targetPort is a name or a number; resolve names through the
			// container's own port list, the way kube-proxy does.
			resolved := target
			if named, isName := containerPorts[target]; isName {
				resolved = named
			}

			if resolved != bindPort {
				t.Errorf("ServiceMonitor scrapes Service %s port %q -> container port %s, "+
					"but the workload binds its metrics listener on %s. Prometheus will "+
					"scrape whatever else answers there (the web UI returns HTML, the "+
					"daemon's own port is mTLS) and every alerting rule in this chart "+
					"evaluates against no data.",
					svc.name, scraped, resolved, bindPort)
			}
		}
	}
}

// metricsListenerBehind finds the Deployment or DaemonSet whose pods the
// Service selects and returns the port it binds its metrics listener on -- the
// ATC's CONCOURSE_PROMETHEUS_BIND_PORT or the daemon's --metrics-port -- and a
// map of container port names to their numbers.
func metricsListenerBehind(t *testing.T, docs []string, podSelector map[string]string) (bindPort string, byName map[string]string) {
	t.Helper()
	byName = map[string]string{}

	for _, doc := range docs {
		var obj struct {
			Kind string `yaml:"kind"`
			Spec struct {
				Template struct {
					Metadata struct {
						Labels map[string]string `yaml:"labels"`
					} `yaml:"metadata"`
					Spec struct {
						Containers []struct {
							Name    string   `yaml:"name"`
							Command []string `yaml:"command"`
							Args    []string `yaml:"args"`
							Env     []struct {
								Name  string `yaml:"name"`
								Value string `yaml:"value"`
							} `yaml:"env"`
							Ports []struct {
								Name          string `yaml:"name"`
								ContainerPort int    `yaml:"containerPort"`
							} `yaml:"ports"`
						} `yaml:"containers"`
					} `yaml:"spec"`
				} `yaml:"template"`
			} `yaml:"spec"`
		}
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
			continue
		}
		if obj.Kind != "Deployment" && obj.Kind != "DaemonSet" {
			continue
		}
		if !labelsMatch(obj.Spec.Template.Metadata.Labels, podSelector) {
			continue
		}
		for _, c := range obj.Spec.Template.Spec.Containers {
			for _, e := range c.Env {
				if e.Name == "CONCOURSE_PROMETHEUS_BIND_PORT" {
					bindPort = e.Value
				}
			}
			for _, arg := range append(append([]string{}, c.Command...), c.Args...) {
				if v, ok := strings.CutPrefix(arg, "--metrics-port="); ok {
					bindPort = v
				}
			}
			for _, p := range c.Ports {
				if p.Name != "" {
					byName[p.Name] = fmt.Sprint(p.ContainerPort)
				}
			}
		}
		return bindPort, byName
	}
	return bindPort, byName
}

type renderedService struct {
	name        string
	podSelector map[string]string
	ports       map[string]string // port name -> targetPort (name or number)
}

func (s renderedService) portNames() []string {
	names := make([]string, 0, len(s.ports))
	for n := range s.ports {
		names = append(names, n)
	}
	return names
}

func serviceMatching(t *testing.T, docs []string, selector map[string]string) renderedService {
	t.Helper()
	for _, doc := range docs {
		var obj struct {
			Kind     string `yaml:"kind"`
			Metadata struct {
				Name   string            `yaml:"name"`
				Labels map[string]string `yaml:"labels"`
			} `yaml:"metadata"`
			Spec struct {
				Selector map[string]string `yaml:"selector"`
				Ports    []struct {
					Name       string    `yaml:"name"`
					TargetPort yaml.Node `yaml:"targetPort"`
				} `yaml:"ports"`
			} `yaml:"spec"`
		}
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil || obj.Kind != "Service" {
			continue
		}
		if !labelsMatch(obj.Metadata.Labels, selector) {
			continue
		}
		svc := renderedService{name: obj.Metadata.Name, podSelector: obj.Spec.Selector, ports: map[string]string{}}
		for _, p := range obj.Spec.Ports {
			svc.ports[p.Name] = p.TargetPort.Value
		}
		return svc
	}
	return renderedService{}
}

type renderedMonitor struct {
	selector map[string]string
	ports    []string
}

func serviceMonitors(docs []string) []renderedMonitor {
	var out []renderedMonitor
	for _, doc := range docs {
		var obj struct {
			Kind string `yaml:"kind"`
			Spec struct {
				Selector struct {
					MatchLabels map[string]string `yaml:"matchLabels"`
				} `yaml:"selector"`
				Endpoints []struct {
					Port string `yaml:"port"`
				} `yaml:"endpoints"`
			} `yaml:"spec"`
		}
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil || obj.Kind != "ServiceMonitor" {
			continue
		}
		m := renderedMonitor{selector: obj.Spec.Selector.MatchLabels}
		for _, e := range obj.Spec.Endpoints {
			m.ports = append(m.ports, e.Port)
		}
		out = append(out, m)
	}
	return out
}

// The emitter needs both bind flags or it never registers at all, which is the
// difference between an exposed port and a served one.
func TestMetricsPortIsActuallyServed(t *testing.T) {
	rendered := renderChart(t,
		"metrics.enabled=true",
		"metrics.port=9391",
		"kubernetes.artifactHelperImage=alpine@sha256:aaaa",
	)

	if !strings.Contains(rendered, "CONCOURSE_PROMETHEUS_BIND_PORT") {
		t.Error("metrics.enabled=true exposes a metrics port but never sets " +
			"CONCOURSE_PROMETHEUS_BIND_PORT, so the ATC never opens the listener " +
			"and the port forwards to nothing")
	}
	if !strings.Contains(rendered, "CONCOURSE_PROMETHEUS_BIND_IP") {
		t.Error("CONCOURSE_PROMETHEUS_BIND_IP is unset; the emitter requires both " +
			"bind flags and stays unregistered without it")
	}
	if !strings.Contains(rendered, `value: "9391"`) {
		t.Error("the bind port does not carry metrics.port, so the Service and the " +
			"listener can disagree")
	}
}

// --metrics-port is a flag a daemon image older than it exits on, and this
// chart can sync ahead of its image (the home cluster's Argo ignores the image
// field). So nothing about the daemon's metrics listener may render until an
// operator asks for it: not the flag, not the container or Service port, not
// the ServiceMonitor, not the rules that would evaluate against its series.
func TestDaemonMetricsListenerIsOptIn(t *testing.T) {
	rendered := renderChart(t,
		"serviceMonitor.enabled=true",
		"alertingRules.enabled=true",
		"artifactDaemon.networkPolicy.enabled=true",
		"kubernetes.artifactHelperImage=alpine@sha256:aaaa",
	)

	if strings.Contains(rendered, "--metrics-port") {
		t.Error("artifactDaemon.metrics.port is unset but --metrics-port rendered; a daemon " +
			"image older than the flag exits on it and every node loses its artifact store")
	}
	if strings.Contains(rendered, "alert: ArtifactDaemon") {
		t.Error("artifact daemon alerts rendered with no metrics listener to feed them; " +
			"ArtifactDaemonNotScraped would fire forever and the rest can never fire")
	}
	docs := splitYAMLDocs(rendered)
	if n := len(serviceMonitors(docs)); n != 1 {
		t.Errorf("rendered %d ServiceMonitors without artifactDaemon.metrics.port, want only the web's", n)
	}
	daemonSvc := serviceMatching(t, docs, map[string]string{"app.kubernetes.io/component": "artifact-daemon"})
	if daemonSvc.name == "" {
		t.Fatal("no artifact daemon Service rendered")
	}
	if _, ok := daemonSvc.ports["metrics"]; ok {
		t.Error("the daemon Service has a metrics port with no listener behind it")
	}
}

func splitYAMLDocs(rendered string) []string {
	var docs []string
	for _, d := range strings.Split(rendered, "\n---") {
		if strings.TrimSpace(d) != "" {
			docs = append(docs, d)
		}
	}
	return docs
}

func labelsMatch(have, want map[string]string) bool {
	if len(want) == 0 {
		return false
	}
	for k, v := range want {
		if have[k] != v {
			return false
		}
	}
	return true
}
