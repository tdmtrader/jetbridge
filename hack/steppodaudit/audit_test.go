// Package steppodaudit tests hack/step-pod-audit offline: a fake kubectl on
// PATH serves the web Deployment's args and the step pods, and the audit's
// verdict on each pod is read from its output.
package steppodaudit

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const deployment = `{"spec":{"template":{"spec":{"containers":[{"args":[
"--kubernetes-service-account=step-default",
"--kubernetes-artifact-daemon-host-path=/var/a",
"--kubernetes-step-pod-grant=name=dind-checks,owner=main/ci/resource-type/dind,privileged=true",
"--kubernetes-step-pod-grant=name=elsewhere,owner=main/elsewhere/resource-type/dind,privileged=true",
"--kubernetes-step-pod-grant=name=dotted,owner=main/cxi/resource-type/dind,privileged=true",
"--kubernetes-step-pod-grant=name=job-grant,owner=main/ci/unit,privileged=true"
]}]}}}}`

// checkPod is a privileged pod of a check build, as the runtime labels it.
func checkPod(name, kind, pipeline, grant string) string {
	annotations := "{}"
	if grant != "" {
		annotations = `{"concourse.dev/step-pod-grant":"` + grant + `"}`
	}
	return `{"metadata":{"name":"` + name + `","labels":{"concourse.ci/handle":"` + name + `","concourse.ci/type":"` + kind +
		`","concourse.ci/build":"check","concourse.ci/pipeline":"` + pipeline + `"},"annotations":` + annotations + `},
 "spec":{"serviceAccountName":"step-default","automountServiceAccountToken":false,"containers":[{"name":"main","securityContext":{"privileged":true}}]}}`
}

func TestTheAuditHoldsACheckPodToItsResourceTypeGrant(t *testing.T) {
	for _, tool := range []string{"sh", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			// In CI (HANGAR_CI set) a missing tool is a failure, never a
			// skip: a skipped audit test passes the tier vacuously.
			if os.Getenv("HANGAR_CI") != "" {
				t.Fatalf("no %s on PATH in CI: %v", tool, err)
			}
			t.Skipf("no %s on PATH: %v", tool, err)
		}
	}
	dir := t.TempDir()
	pods := `{"items":[` + strings.Join([]string{
		checkPod("granted-check", "check", "ci", "dind-checks"),
		checkPod("image-get", "get", "ci", "dind-checks"),
		checkPod("other-pipelines-grant", "check", "ci", "elsewhere"),
		// A pipeline name is compared as a string: c.i is not cxi.
		checkPod("dotted-pipeline", "check", "c.i", "dotted"),
		checkPod("a-job-grant", "check", "ci", "job-grant"),
		checkPod("no-annotation", "check", "ci", ""),
		checkPod("unknown-grant", "check", "ci", "nobody"),
	}, ",") + `]}`
	for name, body := range map[string]string{"deployment.json": deployment, "pods.json": pods} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	kubectl := "#!/bin/sh\ncase \"$*\" in\n*deployment*) cat " + filepath.Join(dir, "deployment.json") +
		" ;;\n*pods*) cat " + filepath.Join(dir, "pods.json") + " ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(kubectl), 0o700); err != nil {
		t.Fatal(err)
	}

	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", filepath.Join(root, "hack", "step-pod-audit"), "pods")
	cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Errorf("the audit passed a run with failing pods:\n%s", out)
	}
	verdicts := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		verdict, rest, ok := strings.Cut(line, ":")
		if !ok || (verdict != "ok" && verdict != "FAIL") {
			continue
		}
		verdicts[strings.Fields(rest)[0]] = verdict
	}
	for pod, want := range map[string]string{
		"granted-check":         "ok",
		"image-get":             "ok",
		"other-pipelines-grant": "FAIL",
		"dotted-pipeline":       "FAIL",
		"a-job-grant":           "FAIL",
		"no-annotation":         "FAIL",
		"unknown-grant":         "FAIL",
	} {
		if verdicts[pod] != want {
			t.Errorf("%s: the audit says %q, want %q\n%s", pod, verdicts[pod], want, out)
		}
	}
}
