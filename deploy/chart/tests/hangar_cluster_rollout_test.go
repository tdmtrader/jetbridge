package tests

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The runbook of hangar_stores_enabled_in_cluster, one overlay per step,
// cumulative, rendered over a credential-free stand-in for concourse.home's
// values. Each step must render, and the invariants the runbook relies on must
// hold at every step.
var rolloutSteps = []struct {
	name string
	sets []string
}{
	{"S0 pre-flight", nil},
	{"S1 bootstrap", []string{
		"hangarBootstrap.enabled=true", "hangarBootstrap.database.enabled=true",
		"hangarOutput.activationEpoch=1",
		"hangarOutput.executionControl.keySecret=concourse-hangar-control-key-e1",
		"hangarOutput.executionControl.keyID=control-1",
		"hangarOutput.capabilityKeySecret=concourse-hangar-capability-key",
		"hangarOutput.daemon.tls.existingSecret=concourse-hangar-output-tls",
		"hangarOutput.daemon.tls.clientSecret=concourse-hangar-output-client-tls",
		"hangarOutput.receipt.keyID=receipt-1",
		"hangarOutput.receipt.privateKeySecret=concourse-hangar-receipt-key-e1",
		"hangarOutput.materializationKeySecret=concourse-hangar-materialize-key",
		"hangarOutput.database.existingSecret=concourse-hangar-activation-dsn",
		"hangarStorage.disk.tls.existingSecret=concourse-hangar-store-tls",
		"hangarStorage.disk.credentials.existingSecret=concourse-hangar-store-credentials",
		"artifactDaemon.hangar.keySecret=concourse-hangar-warrant-key",
	}},
	{"S2 disk init", []string{"hangarStorage.disk.enabled=true", "hangarStorage.disk.storeID=concourse-home-1",
		"hangarStorage.disk.storageClass=local-path", "hangarStorage.disk.initialize=true"}},
	{"S3 store up", []string{"hangarStorage.disk.initialize=false"}},
	{"S4 strict inputs on the daemon", []string{"artifactDaemon.hangar.enabled=true",
		"artifactDaemon.hangar.store=disk", "artifactDaemon.hangar.bucket=inputs"}},
	{"S5 strict inputs on web", []string{"artifactDaemon.hangar.webEnabled=true"}},
	{"S6 base workloads", []string{"hangarOutput.executionControl.enabled=true", "hangarOutput.daemon.scratch.sizeLimit=32Gi"}},
	{"S7 walk to base", []string{"hangarOutput.activation.target=base"}},
	{"S10 output workloads", []string{
		"hangarOutput.enabled=true", "hangarOutput.store=disk", "hangarOutput.bucket=outputs", "hangarOutput.tenant=concourse-home",
		"hangarOutput.readControlURL=https://concourse.home",
		"hangarOutput.readControlCA.configMap=concourse-home-ca", "hangarOutput.readControlCA.key=concourse.home.crt"}},
	{"S11 walk to output", []string{"hangarOutput.activation.target=output"}},
	{"S13 capture and Run results", []string{
		"hangarOutput.webEnabled=true", "web.runInputSigningKeySecret=concourse-run-input-signing-key"}},
}

func TestTheRolloutRendersAtEveryStep(t *testing.T) {
	var sets []string
	for index, step := range rolloutSteps {
		sets = append(sets, step.sets...)
		args := []string{"template", "concourse", "deploy/chart", "--namespace", "cicd",
			"-f", "deploy/chart/tests/testdata/concourse-home-rollout.yaml"}
		for _, set := range sets {
			args = append(args, "--set", set)
		}
		cmd := exec.Command("helm", args...)
		cmd.Dir = repoRoot(t)
		raw, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s does not render: %v\n%s", step.name, err, firstLines(string(raw), 5))
		}
		out := string(raw)
		// The walk Job renders from S6, where execution control comes on, and
		// not before: every later step changes only its target.
		walking := currentValue(sets, "hangarOutput.executionControl.enabled=") == "true"
		target := currentValue(sets, "hangarOutput.activation.target=")
		if target == "" {
			target = "off"
		}

		jobs := 0
		for _, doc := range documentsIn(t, out) {
			if doc.kind != "Job" || !strings.Contains(doc.body, "hangar-output-activate") {
				continue
			}
			jobs++
			if !strings.Contains(doc.body, "ttlSecondsAfterFinished: 86400") {
				t.Errorf("%s: the activation walk Job renders without its TTL", step.name)
			}
			if !strings.Contains(doc.body, "--target="+target) {
				t.Errorf("%s: the activation walk Job does not walk to %s", step.name, target)
			}
		}
		if want := map[bool]int{true: 1, false: 0}[walking]; jobs != want {
			t.Errorf("%s: %d activation walk Jobs rendered, want %d", step.name, jobs, want)
		}

		if index >= 2 && (!strings.Contains(out, "kind: PersistentVolumeClaim") || !strings.Contains(out, "concourse-home-1")) {
			t.Errorf("%s: the disk store's volume or store ID is not rendered", step.name)
		}

		image := ""
		for _, doc := range documentsIn(t, out) {
			_, pod := podOf(t, doc)
			for _, container := range append(append(pod.InitContainers[:0:0], pod.InitContainers...), pod.Containers...) {
				if !strings.Contains(container.Image, "concourse") || container.Name == "postgres" {
					continue
				}
				if image == "" {
					image = container.Image
				}
				if container.Image != image && strings.Contains(doc.name, "hangar") {
					t.Errorf("%s: %s runs %s, web runs %s", step.name, doc.name, container.Image, image)
				}
			}
		}

		capture := strings.Contains(out, "--kubernetes-hangar-output-capture-enabled") || strings.Contains(out, "--run-result-scratch-dir")
		if capture != (step.name == "S13 capture and Run results") {
			t.Errorf("%s: capture and Run-result flags rendered = %v", step.name, capture)
		}

		web := webDeployment(t, out)
		for _, user := range []string{"--main-team-local-user=admin", "--main-team-local-user=live-tests"} {
			if !strings.Contains(web, user) {
				t.Errorf("%s: web lacks %s", step.name, user)
			}
		}
	}
}

// The fixture carries placeholders only.
func TestTheRolloutFixtureHoldsNoCredential(t *testing.T) {
	body, err := os.ReadFile(repoRoot(t) + "/deploy/chart/tests/testdata/concourse-home-rollout.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(body), "\n") {
		if strings.Contains(line, "localUsers:") && strings.Count(line, "placeholder") != 2 {
			t.Errorf("the rollout fixture's local users are not placeholders: %s", line)
		}
		if strings.Contains(strings.ToLower(line), "password:") {
			t.Errorf("the rollout fixture names a password: %s", line)
		}
	}
}

func currentValue(sets []string, prefix string) string {
	value := ""
	for _, set := range sets {
		if strings.HasPrefix(set, prefix) {
			value = strings.TrimPrefix(set, prefix)
		}
	}
	return value
}
