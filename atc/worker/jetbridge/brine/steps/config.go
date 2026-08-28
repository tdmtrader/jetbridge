package steps

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/worker/jetbridge"
)

// ConfigDefinitions migrates config_test.go — how the worker's configuration
// resolves defaults, how resource-type image overrides merge, and how a
// clientset is built. All exported, no cluster, no database.

const testKubeconfig = `apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://127.0.0.1:6443
    insecure-skip-tls-verify: true
  name: test-cluster
contexts:
- context:
    cluster: test-cluster
    user: test-user
  name: test-context
current-context: test-context
users:
- name: test-user
  user:
    token: fake-token
`

func ConfigDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMap[brine.Empty, ResolvedConfig](
			"a worker configured for namespace {string}",
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder) (ResolvedConfig, error) {
				ns, ok := p.GetString(0)
				if !ok {
					return ResolvedConfig{}, fmt.Errorf("expected a namespace parameter")
				}
				return ResolvedConfig{Config: jetbridge.NewConfig(ns, "")}, nil
			},
		),

		brine.DefineMap[brine.Empty, ResolvedConfig](
			"a worker configured for namespace {string} with kubeconfig {string}",
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder) (ResolvedConfig, error) {
				ns, _ := p.GetString(0)
				path, ok := p.GetString(1)
				if !ok {
					return ResolvedConfig{}, fmt.Errorf("expected a namespace and a kubeconfig path")
				}
				return ResolvedConfig{Config: jetbridge.NewConfig(ns, path)}, nil
			},
		),

		// A kubeconfig that exists on disk, so the "valid file" and "missing
		// file" cases are distinguishable without naming a real cluster.
		brine.DefineMapUsing[brine.Empty, ResolvedConfig](
			"a worker configured with a kubeconfig file that exists",
			[]string{"task-workspace"},
			func(_ brine.Empty, _ brine.Params, _ *brine.Recorder, res brine.Resources) (ResolvedConfig, error) {
				workspace, ok := res.Get("task-workspace").(TaskWorkspace)
				if !ok {
					return ResolvedConfig{}, fmt.Errorf("task-workspace resource is %T", res.Get("task-workspace"))
				}
				path := filepath.Join(workspace.Dir, "kubeconfig")
				if err := os.WriteFile(path, []byte(testKubeconfig), 0o600); err != nil {
					return ResolvedConfig{}, fmt.Errorf("write kubeconfig: %w", err)
				}
				return ResolvedConfig{Config: jetbridge.NewConfig("my-namespace", path)}, nil
			},
		),

		CheckString[ResolvedConfig]("the namespace is {string}",
			"the namespace",
			func(in ResolvedConfig) (string, error) {
				return in.Config.Namespace, nil
			}),

		// Keeps its own body: the sentence counts minutes but the field is a
		// Duration, and an integer comparison would let 5m30s pass as 5.
		brine.DefineCheck[ResolvedConfig](
			"the pod startup timeout is {int} minutes",
			func(in ResolvedConfig, p brine.Params, _ *brine.Recorder) error {
				mins, ok := p.GetInt(0)
				if !ok {
					return fmt.Errorf("expected a minutes parameter")
				}
				want := time.Duration(mins) * time.Minute
				if in.Config.PodStartupTimeout != want {
					return fmt.Errorf("expected pod startup timeout %s, got %s", want, in.Config.PodStartupTimeout)
				}
				return nil
			},
		),

		CheckString[ResolvedConfig]("caches are stored under {string}",
			"the cache base path",
			func(_ ResolvedConfig) (string, error) {
				return jetbridge.CacheBasePath, nil
			}),

		brine.DefineMap[ResolvedConfig, ClientsetAttempt](
			"a clientset is built from it",
			func(in ResolvedConfig, _ brine.Params, _ *brine.Recorder) (ClientsetAttempt, error) {
				// KUBERNETES_SERVICE_HOST would make an empty kubeconfig
				// resolve in-cluster, which is a different case from the one
				// under test.
				os.Unsetenv("KUBERNETES_SERVICE_HOST")
				os.Unsetenv("KUBERNETES_SERVICE_PORT")

				clientset, err := jetbridge.NewClientset(in.Config)
				msg := ""
				if err != nil {
					msg = err.Error()
				}
				return ClientsetAttempt{Built: clientset != nil && err == nil, Err: err, Message: msg}, nil
			},
		),

		CheckThat[ClientsetAttempt](
			"a working clientset comes back",
			func(in ClientsetAttempt) error {
				if in.Err != nil {
					return fmt.Errorf("expected a clientset, got error: %v", in.Err)
				}
				if !in.Built {
					return fmt.Errorf("expected a clientset, got nil without an error")
				}
				return nil
			}),

		CheckThat[ClientsetAttempt](
			"it fails to build a clientset",
			func(in ClientsetAttempt) error {
				if in.Err == nil {
					return fmt.Errorf("expected building a clientset to fail, it succeeded")
				}
				return nil
			}),

		// --- Resource-type image overrides ---

		brine.DefineMap[brine.Empty, ResourceTypeImages](
			"the resource type images are merged with no overrides",
			func(_ brine.Empty, _ brine.Params, _ *brine.Recorder) (ResourceTypeImages, error) {
				return mergeImages(nil)
			},
		),

		brine.DefineMap[brine.Empty, ResourceTypeImages](
			"the resource type images are merged with the overrides {string}",
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder) (ResourceTypeImages, error) {
				spec, ok := p.GetString(0)
				if !ok {
					return ResourceTypeImages{}, fmt.Errorf("expected an overrides parameter")
				}
				var overrides []string
				for _, o := range strings.Split(spec, ",") {
					if o = strings.TrimSpace(o); o != "" {
						overrides = append(overrides, o)
					}
				}
				return mergeImages(overrides)
			},
		),

		CheckStringFor[ResourceTypeImages]("the resource type {string} resolves to image {string}",
			"the resource type image",
			func(in ResourceTypeImages, name string) (string, error) {
				got, found := in.Images[name]
				if !found {
					return "", fmt.Errorf("expected resource type %q to be present, it was not", name)
				}
				return got, nil
			}),

		// Keeps its own body: the parameter is the type being looked up, not a
		// value to compare against, and the assertion is its absence.
		brine.DefineCheck[ResourceTypeImages](
			"there is no resource type {string}",
			func(in ResourceTypeImages, p brine.Params, _ *brine.Recorder) error {
				name, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a type parameter")
				}
				if _, found := in.Images[name]; found {
					return fmt.Errorf("expected no resource type %q, but it is present as %q", name, in.Images[name])
				}
				return nil
			},
		),

		// The defaults are a package-level map; merging must not mutate it, or
		// the second worker in a process inherits the first one's overrides.
		CheckThat[ResourceTypeImages](
			"the built-in defaults were left untouched",
			func(in ResourceTypeImages) error {
				for k, v := range in.DefaultsBefore {
					now, found := jetbridge.DefaultResourceTypeImages[k]
					if !found {
						return fmt.Errorf("default %q disappeared from DefaultResourceTypeImages", k)
					}
					if now != v {
						return fmt.Errorf("default %q changed from %q to %q — merging mutated the shared map", k, v, now)
					}
				}
				if len(jetbridge.DefaultResourceTypeImages) != len(in.DefaultsBefore) {
					return fmt.Errorf("DefaultResourceTypeImages changed size from %d to %d",
						len(in.DefaultsBefore), len(jetbridge.DefaultResourceTypeImages))
				}
				return nil
			}),
	}
}

func mergeImages(overrides []string) (ResourceTypeImages, error) {
	before := make(map[string]string, len(jetbridge.DefaultResourceTypeImages))
	for k, v := range jetbridge.DefaultResourceTypeImages {
		before[k] = v
	}
	return ResourceTypeImages{
		Images:         jetbridge.MergeResourceTypeImages(overrides),
		DefaultsBefore: before,
	}, nil
}

// ConfigCompletenessDefinitions closes the gap a deletion probe found: brine
// asserted that three named resource types resolve, and nothing at all about
// the SIZE or COMPLETENESS of the merged map.
//
// The consequence, verified by mutation: dropping `s3` from the defaults copy
// passed a fully green 300-scenario suite. A built-in resource type can vanish
// from every pipeline on the cluster and no brine scenario notices. Naming
// three types is not the same as guarding the set.
func ConfigCompletenessDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		CheckThat[ResourceTypeImages](
			"every built-in resource type is still offered",
			func(in ResourceTypeImages) error {
				var missing []string
				for name := range jetbridge.DefaultResourceTypeImages {
					if _, ok := in.Images[name]; !ok {
						missing = append(missing, name)
					}
				}
				if len(missing) > 0 {
					sort.Strings(missing)
					return fmt.Errorf(
						"these built-in resource types vanished from the merge: %s — every pipeline using one "+
							"would fail to find its image", strings.Join(missing, ", "))
				}
				return nil
			}),

		// The other direction: a merge that INVENTS a type is just as wrong,
		// and is how a typo in an override silently becomes a resource type.
		CheckThat[ResourceTypeImages](
			"no resource type was invented that nobody configured",
			func(in ResourceTypeImages) error {
				expected := len(jetbridge.DefaultResourceTypeImages)
				if got := len(in.Images); got != expected {
					var extra []string
					for name := range in.Images {
						if _, ok := jetbridge.DefaultResourceTypeImages[name]; !ok {
							extra = append(extra, name)
						}
					}
					sort.Strings(extra)
					return fmt.Errorf(
						"expected exactly the %d built-in types with no overrides, got %d (unexpected: %s)",
						expected, got, strings.Join(extra, ", "))
				}
				return nil
			}),
	}
}
