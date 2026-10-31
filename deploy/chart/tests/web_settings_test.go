package tests

// Each web setting concourse.home used to pass through web.env has a typed
// value of its own (ADR-0008), rendered as the variable the web reads.

import (
	"os/exec"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func webEnv(t *testing.T, manifests string) map[string][]corev1.EnvVar {
	t.Helper()
	spec := strictWebDeployment(t, manifests).Spec.Template.Spec
	env := map[string][]corev1.EnvVar{}
	for _, container := range append(spec.InitContainers, spec.Containers...) {
		env[container.Name] = container.Env
	}
	return env
}

func envValue(env []corev1.EnvVar, name string) (string, bool) {
	for _, e := range env {
		if e.Name == name {
			return e.Value, true
		}
	}
	return "", false
}

var webSettings = []struct {
	set, variable, value string
}{
	{"web.authDuration=720h", "CONCOURSE_AUTH_DURATION", "720h"},
	{"web.caCertFile=/etc/ca/bundle.crt", "SSL_CERT_FILE", "/etc/ca/bundle.crt"},
	{"web.checks.interval=2m", "CONCOURSE_RESOURCE_CHECKING_INTERVAL", "2m"},
	{"web.checks.resourceTypeInterval=3m", "CONCOURSE_RESOURCE_TYPE_CHECKING_INTERVAL", "3m"},
	{"web.checks.webhookInterval=4m", "CONCOURSE_RESOURCE_WITH_WEBHOOK_CHECKING_INTERVAL", "4m"},
	{"web.checks.timeout=30m", "CONCOURSE_GLOBAL_RESOURCE_CHECK_TIMEOUT", "30m"},
	{"web.gc.oneOffGracePeriod=10m", "CONCOURSE_GC_ONE_OFF_GRACE_PERIOD", "10m"},
	{"web.gc.missingGracePeriod=15m", "CONCOURSE_GC_MISSING_GRACE_PERIOD", "15m"},
	{"web.secretRetry.attempts=7", "CONCOURSE_SECRET_RETRY_ATTEMPTS", "7"},
	{"web.secretRetry.interval=2s", "CONCOURSE_SECRET_RETRY_INTERVAL", "2s"},
}

// caMount brings a CA bundle into the web container, which web.caCertFile requires.
var caMount = []string{
	"web.extraVolumes[0].name=ca",
	"web.extraVolumes[0].configMap.name=ca",
	"web.extraVolumeMounts[0].name=ca",
	"web.extraVolumeMounts[0].mountPath=/etc/ca",
}

func TestEachWebSettingReachesTheWebContainer(t *testing.T) {
	for _, setting := range webSettings {
		t.Run(setting.set, func(t *testing.T) {
			env := webEnv(t, render(t, append([]string{setting.set}, caMount...)...))
			if got, ok := envValue(env["concourse-web"], setting.variable); !ok || got != setting.value {
				t.Fatalf("the web container's %s is %q (set: %v); want %q", setting.variable, got, ok, setting.value)
			}
		})
	}
}

// Left unset, a setting renders nothing and the binary keeps its default.
func TestAnUnsetWebSettingRendersNoVariable(t *testing.T) {
	env := webEnv(t, render(t))
	for _, setting := range webSettings {
		for container, vars := range env {
			if _, ok := envValue(vars, setting.variable); ok {
				t.Errorf("%s sets %s with nothing set; the binary's default must stand", container, setting.variable)
			}
		}
	}
}

func TestACACertFileOutsideEveryMountFailsTheRender(t *testing.T) {
	for name, sets := range map[string][]string{
		"no mount":         {"web.caCertFile=/etc/ca/bundle.crt"},
		"another mount":    append([]string{"web.caCertFile=/etc/cab/bundle.crt"}, caMount...),
		"the mount itself": append([]string{"web.caCertFile=/etc/ca"}, caMount...),
	} {
		t.Run(name, func(t *testing.T) {
			out := renderHangarError(t, sets...)
			if !strings.Contains(out, "web.caCertFile") || !strings.Contains(out, "web.extraVolumeMounts") {
				t.Fatalf("the refusal does not name web.caCertFile and web.extraVolumeMounts:\n%s", out)
			}
		})
	}
}

// The concourse.home values set every typed setting, and each reaches the
// web container.
func TestTheConcourseHomeSettingsRender(t *testing.T) {
	env := webEnv(t, renderRollout(t))["concourse-web"]
	for _, variable := range []string{
		"CONCOURSE_AUTH_DURATION", "SSL_CERT_FILE", "CONCOURSE_RESOURCE_CHECKING_INTERVAL",
		"CONCOURSE_RESOURCE_TYPE_CHECKING_INTERVAL", "CONCOURSE_RESOURCE_WITH_WEBHOOK_CHECKING_INTERVAL",
		"CONCOURSE_GLOBAL_RESOURCE_CHECK_TIMEOUT", "CONCOURSE_GC_ONE_OFF_GRACE_PERIOD",
		"CONCOURSE_GC_MISSING_GRACE_PERIOD", "CONCOURSE_SECRET_RETRY_ATTEMPTS", "CONCOURSE_SECRET_RETRY_INTERVAL",
	} {
		if _, ok := envValue(env, variable); !ok {
			t.Errorf("the concourse.home render does not set %s", variable)
		}
	}
}

// renderRollout renders concourse.home's values as Argo does.
func renderRollout(t *testing.T) string {
	t.Helper()
	cmd := exec.Command("helm", "template", "concourse", "deploy/chart", "--namespace", "cicd",
		"-f", "deploy/chart/tests/testdata/concourse-home-rollout.yaml")
	cmd.Dir = repoRoot(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the concourse.home rollout values do not render: %v\n%s", err, out)
	}
	return string(out)
}

// While web.env remains, an entry naming a variable a set typed value
// renders would hand the container two values; the render names the value.
func TestAWebEnvEntryATypedSettingRendersFailsTheRender(t *testing.T) {
	out := renderHangarError(t, "web.authDuration=720h",
		"web.env[0].name=CONCOURSE_AUTH_DURATION", "web.env[0].value=24h")
	if !strings.Contains(out, "web.authDuration") {
		t.Fatalf("the refusal does not name web.authDuration:\n%s", out)
	}
}
