package steps

import (
	"fmt"
	"os"
	"path/filepath"
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

		brine.DefineCheck[ResolvedConfig](
			"the namespace is {string}",
			func(in ResolvedConfig, p brine.Params, _ *brine.Recorder) error {
				want, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a namespace parameter")
				}
				if in.Config.Namespace != want {
					return fmt.Errorf("expected namespace %q, got %q", want, in.Config.Namespace)
				}
				return nil
			},
		),

		brine.DefineCheck[ResolvedConfig](
			"the kubeconfig path is {string}",
			func(in ResolvedConfig, p brine.Params, _ *brine.Recorder) error {
				want, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a path parameter")
				}
				if in.Config.KubeconfigPath != want {
					return fmt.Errorf("expected kubeconfig path %q, got %q", want, in.Config.KubeconfigPath)
				}
				return nil
			},
		),

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

		brine.DefineCheck[ResolvedConfig](
			"caches are stored under {string}",
			func(_ ResolvedConfig, p brine.Params, _ *brine.Recorder) error {
				want, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a path parameter")
				}
				if jetbridge.CacheBasePath != want {
					return fmt.Errorf("expected cache base path %q, got %q", want, jetbridge.CacheBasePath)
				}
				return nil
			},
		),

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

		brine.DefineCheck[ClientsetAttempt](
			"a working clientset comes back",
			func(in ClientsetAttempt, _ brine.Params, _ *brine.Recorder) error {
				if in.Err != nil {
					return fmt.Errorf("expected a clientset, got error: %v", in.Err)
				}
				if !in.Built {
					return fmt.Errorf("expected a clientset, got nil without an error")
				}
				return nil
			},
		),

		brine.DefineCheck[ClientsetAttempt](
			"it fails to build a clientset",
			func(in ClientsetAttempt, _ brine.Params, _ *brine.Recorder) error {
				if in.Err == nil {
					return fmt.Errorf("expected building a clientset to fail, it succeeded")
				}
				return nil
			},
		),

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

		brine.DefineCheck[ResourceTypeImages](
			"the resource type {string} resolves to image {string}",
			func(in ResourceTypeImages, p brine.Params, _ *brine.Recorder) error {
				name, _ := p.GetString(0)
				want, ok := p.GetString(1)
				if !ok {
					return fmt.Errorf("expected a type and an image")
				}
				got, found := in.Images[name]
				if !found {
					return fmt.Errorf("expected resource type %q to be present, it was not", name)
				}
				if got != want {
					return fmt.Errorf("expected %q to resolve to %q, got %q", name, want, got)
				}
				return nil
			},
		),

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
		brine.DefineCheck[ResourceTypeImages](
			"the built-in defaults were left untouched",
			func(in ResourceTypeImages, _ brine.Params, _ *brine.Recorder) error {
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
			},
		),
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
