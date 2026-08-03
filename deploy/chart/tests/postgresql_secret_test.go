package tests

import "testing"

func TestBundledPostgresExistingSecretIsSharedByEveryDatabaseConsumer(t *testing.T) {
	manifests := renderChart(t,
		"postgresql.existingSecret=external-db-password",
		"postgresql.passwordSecretKey=custom-password",
		"postgresql.password=must-not-render",
	)

	db := findDeployment(t, manifests, "-db")
	web := findDeployment(t, manifests, "-web")

	assertSecretEnv(t, db.Spec.Template.Spec.Containers[0].Env, "POSTGRES_PASSWORD", "external-db-password", "custom-password")
	assertSecretEnv(t, findInitContainerEnv(t, web, "migrate-db"), "CONCOURSE_POSTGRES_PASSWORD", "external-db-password", "custom-password")
	assertSecretEnv(t, findContainerEnv(t, web, "concourse-web"), "CONCOURSE_POSTGRES_PASSWORD", "external-db-password", "custom-password")
}

func findContainerEnv(t *testing.T, workload deployment, name string) []envVar {
	t.Helper()
	for _, container := range workload.Spec.Template.Spec.Containers {
		if container.Name == name {
			return container.Env
		}
	}
	t.Fatalf("container %q not found", name)
	return nil
}

func findInitContainerEnv(t *testing.T, workload deployment, name string) []envVar {
	t.Helper()
	for _, container := range workload.Spec.Template.Spec.InitContainers {
		if container.Name == name {
			return container.Env
		}
	}
	t.Fatalf("init container %q not found", name)
	return nil
}

func assertSecretEnv(t *testing.T, env []envVar, name, secretName, secretKey string) {
	t.Helper()
	for _, variable := range env {
		if variable.Name != name {
			continue
		}
		if variable.Value != "" {
			t.Fatalf("%s uses literal value %q instead of Secret %s/%s", name, variable.Value, secretName, secretKey)
		}
		if variable.ValueFrom == nil || variable.ValueFrom.SecretKeyRef == nil {
			t.Fatalf("%s does not use a Secret reference", name)
		}
		if got := variable.ValueFrom.SecretKeyRef.Name; got != secretName {
			t.Fatalf("%s Secret name = %q, want %q", name, got, secretName)
		}
		if got := variable.ValueFrom.SecretKeyRef.Key; got != secretKey {
			t.Fatalf("%s Secret key = %q, want %q", name, got, secretKey)
		}
		return
	}
	t.Fatalf("environment variable %q not found", name)
}
