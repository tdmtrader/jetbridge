package tests

// The database password and the local users are secrets, and a pod's args are
// not: anyone who may get the pod reads them, as does the artifact daemon's
// ServiceAccount, which gets pods. So they reach migrate-db and the web as
// environment variables from <fullname>-web-credentials, and the binary reads
// CONCOURSE_POSTGRES_PASSWORD and CONCOURSE_ADD_LOCAL_USER exactly as it read
// the flags. Only a web.env entry naming one of those variables keeps it a
// flag: the flag beat that entry, and still does.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"
)

// webCredentials returns the decoded data of <fullname>-web-credentials, or
// nil when the render carries none.
func webCredentials(t *testing.T, manifests string) map[string]string {
	t.Helper()
	for _, chunk := range splitDocuments(manifests) {
		var head renderedObject
		if yaml.Unmarshal([]byte(chunk), &head) != nil || head.Kind != "Secret" || !strings.HasSuffix(head.Metadata.Name, "-web-credentials") {
			continue
		}
		var secret corev1.Secret
		if err := yaml.UnmarshalStrict([]byte(chunk), &secret); err != nil {
			t.Fatalf("web credentials Secret does not decode: %v", err)
		}
		data := map[string]string{}
		for key, value := range secret.Data {
			data[key] = string(value)
		}
		return data
	}
	return nil
}

func webCredential(t *testing.T, manifests, key string) string {
	t.Helper()
	data := webCredentials(t, manifests)
	value, ok := data[key]
	if !ok {
		t.Fatalf("the web credentials Secret has no %s: %v", key, data)
	}
	return value
}

func envFromWebCredentials(container corev1.Container, name, key string) bool {
	return slices.ContainsFunc(container.Env, func(env corev1.EnvVar) bool {
		ref := env.ValueFrom
		return env.Name == name && ref != nil && ref.SecretKeyRef != nil &&
			strings.HasSuffix(ref.SecretKeyRef.Name, "-web-credentials") && ref.SecretKeyRef.Key == key
	})
}

func TestNoSecretReachesAPodArg(t *testing.T) {
	const password = "s3cret-db-password"
	manifests := render(t, "postgresql.password="+password, "web.localUsers=admin:s3cret-admin")
	spec := strictWebDeployment(t, manifests).Spec.Template.Spec
	migrate, web := spec.InitContainers[0], spec.Containers[0]
	for _, container := range []corev1.Container{migrate, web} {
		for _, arg := range append(slices.Clone(container.Command), container.Args...) {
			if strings.Contains(arg, "s3cret") {
				t.Errorf("%s args carry a secret: %s", container.Name, arg)
			}
		}
		if !envFromWebCredentials(container, "CONCOURSE_POSTGRES_PASSWORD", "postgres-password") {
			t.Errorf("%s does not read CONCOURSE_POSTGRES_PASSWORD from the web credentials: %+v", container.Name, container.Env)
		}
	}
	if !envFromWebCredentials(web, "CONCOURSE_ADD_LOCAL_USER", "add-local-user") {
		t.Errorf("the web does not read CONCOURSE_ADD_LOCAL_USER from the web credentials: %+v", web.Env)
	}
	if envFromWebCredentials(migrate, "CONCOURSE_ADD_LOCAL_USER", "add-local-user") {
		t.Error("migrate-db reads the local users, which it has no flag for")
	}
	if got := webCredential(t, manifests, "postgres-password"); got != password {
		t.Errorf("the Secret's postgres-password is %q, want %q", got, password)
	}
	if got := webCredential(t, manifests, "add-local-user"); got != "admin:s3cret-admin" {
		t.Errorf("the Secret's add-local-user is %q", got)
	}
	// Non-secret settings stay flags.
	for _, want := range []string{"--postgres-user=concourse", "--main-team-local-user=test"} {
		if !slices.Contains(web.Args, want) {
			t.Errorf("web args lost %s", want)
		}
	}
}

// The kubelet turns $$ into $ in args and never in a value from a Secret, so
// the Secret holds what the flag used to deliver.
func TestTheSecretHoldsWhatTheArgDelivered(t *testing.T) {
	manifests := render(t, "postgresql.password=a$$b$c", "web.localUsers=admin:x$$y")
	if got := webCredential(t, manifests, "postgres-password"); got != "a$b$c" {
		t.Errorf("postgres-password = %q, want a$b$c", got)
	}
	if got := webCredential(t, manifests, "add-local-user"); got != "admin:x$y" {
		t.Errorf("add-local-user = %q, want admin:x$y", got)
	}
}

// A changed password or local user rolled the web when it was an arg; the
// pod template's checksum keeps that.
func TestAChangedCredentialRollsTheWeb(t *testing.T) {
	checksum := func(sets ...string) string {
		return strictWebDeployment(t, render(t, sets...)).Spec.Template.Annotations["checksum/web-credentials"]
	}
	base := checksum()
	if base == "" {
		t.Fatal("the web pod template carries no checksum/web-credentials")
	}
	for _, set := range []string{"postgresql.password=other", "web.localUsers=test:other"} {
		if checksum(set) == base {
			t.Errorf("%s leaves the web pod template unchanged", set)
		}
	}
}

func TestAnExistingDatabaseSecretKeepsThePasswordOutOfTheChart(t *testing.T) {
	manifests := render(t, "postgresql.existingSecret=operator-db", "postgresql.passwordSecretKey=pw")
	if _, ok := webCredentials(t, manifests)["postgres-password"]; ok {
		t.Error("the chart copies postgresql.password although postgresql.existingSecret names the operator's Secret")
	}
	web := strictWebDeployment(t, manifests).Spec.Template.Spec.Containers[0]
	if envFromWebCredentials(web, "CONCOURSE_POSTGRES_PASSWORD", "postgres-password") {
		t.Error("the web reads the database password from the chart's Secret, not postgresql.existingSecret")
	}

	none := render(t, "postgresql.existingSecret=operator-db", "web.localUsers=",
		"web.mainTeamLocalUser=", "web.env[0].name=CONCOURSE_MAIN_TEAM_OIDC_GROUP", "web.env[0].value=admins")
	if data := webCredentials(t, none); data != nil {
		t.Errorf("a render with no inline credential renders a web credentials Secret: %v", data)
	}
	if _, ok := strictWebDeployment(t, none).Spec.Template.Annotations["checksum/web-credentials"]; ok {
		t.Error("a render with no web credentials Secret carries its checksum")
	}
}

// A web.env entry naming CONCOURSE_POSTGRES_PASSWORD or
// CONCOURSE_ADD_LOCAL_USER lost to the flag, so the flag stays and the web
// container renders no second variable of that name.
func TestAWebEnvCredentialKeepsTheFlagThatBeatIt(t *testing.T) {
	manifests := render(t,
		"web.env[0].name=CONCOURSE_ADD_LOCAL_USER", "web.env[0].value=other:x",
		"web.env[1].name=CONCOURSE_POSTGRES_PASSWORD", "web.env[1].value=other")
	spec := strictWebDeployment(t, manifests).Spec.Template.Spec
	web := spec.Containers[0]
	for _, want := range []string{"--add-local-user=test:test", "--postgres-password=concourse"} {
		if !slices.Contains(web.Args, want) {
			t.Errorf("web args lack %s", want)
		}
	}
	for _, name := range []string{"CONCOURSE_ADD_LOCAL_USER", "CONCOURSE_POSTGRES_PASSWORD"} {
		count := 0
		for _, env := range web.Env {
			if env.Name == name {
				count++
			}
		}
		if count != 1 {
			t.Errorf("the web container renders %s %d times, want once (the web.env entry)", name, count)
		}
	}
	if _, ok := webCredentials(t, manifests)["add-local-user"]; ok {
		t.Error("the Secret holds local users no container reads")
	}
	// migrate-db has no web.env, so it reads the chart's Secret as before.
	if !envFromWebCredentials(spec.InitContainers[0], "CONCOURSE_POSTGRES_PASSWORD", "postgres-password") {
		t.Error("migrate-db lost its database password")
	}
}

// renderWithValues renders with a values file holding exactly these strings,
// so a value's spaces, # and quotes reach the chart untouched by --set.
func renderWithValues(t *testing.T, values map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	// Not t.TempDir: a subtest's name can hold a % helm reads as a URL escape.
	dir, err := os.MkdirTemp("", "chart-values")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "values.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("helm", "template", "jb", "deploy/chart",
		"-f", "deploy/chart/tests/testdata/required-values.yaml", "-f", path)
	cmd.Dir = repoRoot(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helm template failed: %v\n%s", err, out)
	}
	return string(out)
}

// oldDelivered is what the chart's old unquoted arg `- --<flag>=<value>`
// handed the binary: the YAML scalar, then the kubelet's $$ -> $. It is
// written here independently of the chart, and only for values with no $(,
// which the chart keeps as flags. ok is false when YAML did not read the arg
// as one string, so the old manifest carried no usable arg at all.
func oldDelivered(t *testing.T, flag, value string) (string, bool) {
	t.Helper()
	if strings.Contains(value, "$(") {
		t.Fatalf("oldDelivered cannot model the kubelet's $(VAR) expansion: %q", value)
	}
	var parsed []string
	if err := yaml.Unmarshal([]byte("- --"+flag+"="+value), &parsed); err != nil || len(parsed) != 1 {
		return "", false
	}
	return strings.ReplaceAll(strings.TrimPrefix(parsed[0], "--"+flag+"="), "$$", "$"), true
}

// The Secret hands the binary exactly what the old arg did, however the
// unquoted arg's YAML parse and the kubelet's escaping reshaped it.
func TestTheSecretHoldsWhatTheOldArgDelivered(t *testing.T) {
	values := []string{
		"abc #def",    // a YAML comment
		"trailing   ", // trailing spaces
		"  leading",   // leading spaces, inside the scalar, stay
		"$2a$10$59/qbls9PKuG8WySHZWDkOa3xYHVzjj234TSm9wj9X2uUnTXDiGY.", // bcrypt
		"a$$b$c", // the kubelet's $$
		"*star", "&anchor", "!tag", "%pct", "@at", "`tick", "|pipe", ">gt",
		"'quote", `"dq`, "[bracket", "{brace", "#hash", "-dash", "?q", "~", "null", "123",
	}
	for _, value := range values {
		t.Run(value, func(t *testing.T) {
			// An external database: the bundled one's unquoted env value
			// breaks the manifest outright on several of these.
			manifests := renderWithValues(t, map[string]any{
				"postgresql": map[string]any{"enabled": false, "host": "db.example", "password": value},
				"web":        map[string]any{"localUsers": "admin:" + value},
			})
			data := webCredentials(t, manifests)
			podChecked := true
			for _, c := range []struct{ key, raw string }{
				{"postgres-password", value},
				{"add-local-user", strings.TrimSpace("admin:" + value)},
			} {
				key, raw := c.key, c.raw
				got, held := data[key]
				want, delivered := oldDelivered(t, key, raw)
				switch {
				case !delivered && held:
					t.Errorf("%s: the old arg was no string, yet the Secret holds %q", key, got)
				case delivered && !held:
					t.Errorf("%s: the Secret leaves out a value the old arg delivered as %q", key, want)
				case delivered && got != want:
					t.Errorf("%s = %q, the old arg delivered %q", key, got, want)
				}
				if !delivered {
					// The flag stays, as malformed as before; there is no pod to check.
					podChecked = false
				}
			}
			if !podChecked {
				return
			}
			web := strictWebDeployment(t, manifests).Spec.Template.Spec.Containers[0]
			for _, arg := range web.Args {
				if strings.HasPrefix(arg, "--postgres-password") || strings.HasPrefix(arg, "--add-local-user") {
					t.Errorf("web still passes %s as an arg", arg)
				}
			}
		})
	}
}

// The bundled PostgreSQL reads its password from an unquoted env value, which
// YAML parses the same way: a " #" comment or trailing spaces left both it and
// the old web arg with the same password, and still do.
func TestTheWebAndTheBundledDatabaseAgreeOnThePassword(t *testing.T) {
	for _, value := range []string{"abc #def", "trailing   "} {
		manifests := renderWithValues(t, map[string]any{"postgresql": map[string]any{"password": value}})
		var database string
		for _, chunk := range splitDocuments(manifests) {
			var head renderedObject
			if yaml.Unmarshal([]byte(chunk), &head) != nil || head.Kind != "Deployment" || !strings.HasSuffix(head.Metadata.Name, "-db") {
				continue
			}
			var deployment appsv1.Deployment
			if err := yaml.Unmarshal([]byte(chunk), &deployment); err != nil {
				t.Fatal(err)
			}
			for _, env := range deployment.Spec.Template.Spec.Containers[0].Env {
				if env.Name == "POSTGRES_PASSWORD" {
					database = env.Value
				}
			}
		}
		if got := webCredential(t, manifests, "postgres-password"); got != database {
			t.Errorf("for %q the web reads %q and the bundled database was initialised with %q", value, got, database)
		}
	}
}

// The kubelet expanded $(VAR) in the old args, so a local user's or the
// database's password could be a reference to a web.env variable fed from a
// Secret. A Secret value is never expanded, so such a value stays a flag,
// rendered exactly as before, and the Secret leaves it out.
func TestAKubeletReferenceKeepsTheFlag(t *testing.T) {
	manifests := renderWithValues(t, map[string]any{
		"postgresql": map[string]any{"password": "$(DB_PW)"},
		"web": map[string]any{
			"localUsers": "admin:$(ADMIN_PW),test:test",
			"env": []any{
				map[string]any{"name": "ADMIN_PW", "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "admin", "key": "pw"}}},
			},
		},
	})
	spec := strictWebDeployment(t, manifests).Spec.Template.Spec
	migrate, web := spec.InitContainers[0], spec.Containers[0]
	for _, want := range []string{"--postgres-password=$(DB_PW)", "--add-local-user=admin:$(ADMIN_PW)", "--add-local-user=test:test"} {
		if !slices.Contains(web.Args, want) {
			t.Errorf("web args lack %s: %v", want, web.Args)
		}
	}
	if !slices.Contains(migrate.Args, "--postgres-password=$(DB_PW)") {
		t.Errorf("migrate-db args lack the database password flag: %v", migrate.Args)
	}
	for _, container := range []corev1.Container{migrate, web} {
		if envFromWebCredentials(container, "CONCOURSE_POSTGRES_PASSWORD", "postgres-password") ||
			envFromWebCredentials(container, "CONCOURSE_ADD_LOCAL_USER", "add-local-user") {
			t.Errorf("%s reads a credential the Secret cannot reproduce", container.Name)
		}
	}
	if data := webCredentials(t, manifests); data != nil {
		t.Errorf("the Secret holds values whose flags stayed: %v", data)
	}

	// $$( was the kubelet's escape for a literal $(, and stays a flag too.
	escaped := renderWithValues(t, map[string]any{"postgresql": map[string]any{"password": "a$$(b)"}})
	if _, ok := webCredentials(t, escaped)["postgres-password"]; ok {
		t.Error("a password holding $$( went into the Secret")
	}
}
