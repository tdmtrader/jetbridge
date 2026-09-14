package tests

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/yaml"
)

func TestWebCanAvoidKeylessOverlapDuringEncryptionEnablement(t *testing.T) {
	for _, tc := range []struct {
		name string
		sets []string
		want appsv1.DeploymentStrategyType
	}{
		{name: "existing default", want: ""},
		{name: "first encryption key", sets: []string{"web.strategy.type=Recreate"}, want: appsv1.RecreateDeploymentStrategyType},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifest := objectNamed(t, render(t, tc.sets...), "Deployment", "-web")
			var deployment appsv1.Deployment
			if err := yaml.Unmarshal([]byte(manifest.body), &deployment); err != nil {
				t.Fatal(err)
			}
			if deployment.Spec.Strategy.Type != tc.want || deployment.Spec.Strategy.RollingUpdate != nil {
				t.Fatalf("unexpected rollout strategy: %+v", deployment.Spec.Strategy)
			}
		})
	}
}
