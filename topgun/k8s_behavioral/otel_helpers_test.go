package behavioral_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// collectOTelEnabled returns true when the COLLECT_OTEL env var is set.
func collectOTelEnabled() bool {
	return os.Getenv("COLLECT_OTEL") == "1"
}

// deployOTelCollector applies the OTel collector manifests into the test namespace.
func deployOTelCollector() {
	GinkgoHelper()
	manifestPath := filepath.Join(mustRepoRoot(), "topgun", "k8s_behavioral", "testdata", "otel-collector.yaml")
	cmd := exec.Command("kubectl",
		"--kubeconfig", config.Kubeconfig,
		"-n", config.Namespace,
		"apply", "-f", manifestPath,
	)
	cmd.Stdout = GinkgoWriter
	cmd.Stderr = GinkgoWriter
	Expect(cmd.Run()).To(Succeed(), "failed to deploy OTel collector")

	// Wait for collector pod to be ready.
	waitCmd := exec.Command("kubectl",
		"--kubeconfig", config.Kubeconfig,
		"-n", config.Namespace,
		"wait", "--for=condition=ready", "pod",
		"-l", "app=otel-collector",
		"--timeout=120s",
	)
	waitCmd.Stdout = GinkgoWriter
	waitCmd.Stderr = GinkgoWriter
	Expect(waitCmd.Run()).To(Succeed(), "OTel collector pod not ready")
}

// otelCollectorAddress returns the in-cluster address of the OTel collector.
func otelCollectorAddress() string {
	return fmt.Sprintf("otel-collector.%s.svc.cluster.local:4317", config.Namespace)
}

// collectOTelMetricsFromCollector reads the metrics file from the collector pod
// and returns raw JSON lines.
func collectOTelMetricsFromCollector() []map[string]interface{} {
	GinkgoHelper()
	var out bytes.Buffer
	cmd := exec.Command("kubectl",
		"--kubeconfig", config.Kubeconfig,
		"-n", config.Namespace,
		"exec", "deploy/otel-collector", "--",
		"cat", "/var/otel/metrics.json",
	)
	cmd.Stdout = &out
	cmd.Stderr = GinkgoWriter

	// The file may not exist yet if no metrics have been exported.
	if err := cmd.Run(); err != nil {
		return nil
	}

	var results []map[string]interface{}
	for _, line := range strings.Split(out.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var entry map[string]interface{}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		results = append(results, entry)
	}
	return results
}

// printStepPerformanceReport prints a formatted report of step durations
// from the collected OTel metrics to GinkgoWriter.
func printStepPerformanceReport(metrics []map[string]interface{}) {
	fmt.Fprintln(GinkgoWriter, "")
	fmt.Fprintln(GinkgoWriter, strings.Repeat("=", 60))
	fmt.Fprintln(GinkgoWriter, "  OTel Metrics Collected from Integration Test")
	fmt.Fprintln(GinkgoWriter, strings.Repeat("=", 60))
	fmt.Fprintf(GinkgoWriter, "  Total metric entries: %d\n", len(metrics))

	// Look for step duration metrics in the collected data.
	stepCount := 0
	for _, entry := range metrics {
		name, _ := entry["Name"].(string)
		if strings.Contains(name, "step") || strings.Contains(name, "duration") {
			stepCount++
			fmt.Fprintf(GinkgoWriter, "  [%d] %s\n", stepCount, name)
		}
	}

	if stepCount == 0 {
		fmt.Fprintln(GinkgoWriter, "  No step-related metrics found in collected data.")
		fmt.Fprintln(GinkgoWriter, "  This is expected for short-lived test builds.")
	}
	fmt.Fprintln(GinkgoWriter, strings.Repeat("=", 60))
	fmt.Fprintln(GinkgoWriter, "")
}

// waitForOTelMetrics polls the collector until at least one metric entry appears.
func waitForOTelMetrics(timeout time.Duration) []map[string]interface{} {
	GinkgoHelper()
	var metrics []map[string]interface{}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		metrics = collectOTelMetricsFromCollector()
		if len(metrics) > 0 {
			return metrics
		}
		time.Sleep(2 * time.Second)
	}
	return metrics
}

// cleanupOTelCollector removes the OTel collector resources.
func cleanupOTelCollector() {
	_ = exec.Command("kubectl",
		"--kubeconfig", config.Kubeconfig,
		"-n", config.Namespace,
		"delete", "deploy/otel-collector", "svc/otel-collector", "configmap/otel-collector-config",
		"--ignore-not-found",
	).Run()

	// Wait for pod termination with a context timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, "kubectl",
		"--kubeconfig", config.Kubeconfig,
		"-n", config.Namespace,
		"wait", "--for=delete", "pod", "-l", "app=otel-collector",
		"--timeout=30s",
	).Run()
}
