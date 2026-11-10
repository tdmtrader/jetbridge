package landing_test

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/concourse/concourse/atc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/yaml"
)

// privilegedSteps lists every step of a pipeline config that would run a
// privileged step pod, as "<job>/<step>": a task that sets privileged:, a
// task whose inline image_resource is of a privileged resource type, or a get
// or put whose resource is of one. A resource type is privileged when it is
// declared privileged: or is built (its type:, transitively) on one that is,
// since fetching its image runs privileged. Privileged containers a task
// creates itself (a dind pod applied with kubectl) are not step pods and are
// not listed; neither is a task whose config comes from a file:, which this
// cannot see.
func privilegedSteps(config atc.Config) []string {
	GinkgoHelper()
	privilegedType := func(name string) bool {
		seen := map[string]bool{}
		for !seen[name] {
			seen[name] = true
			t, ok := config.ResourceTypes.Lookup(name)
			if !ok {
				return false
			}
			if t.Privileged {
				return true
			}
			name = t.Type
		}
		return false
	}
	privilegedResource := func(name string) bool {
		r, ok := config.Resources.Lookup(name)
		return ok && privilegedType(r.Type)
	}

	var found []string
	for _, job := range config.Jobs {
		job := job
		Expect(job.StepConfig().Visit(atc.StepRecursor{
			OnTask: func(step *atc.TaskStep) error {
				image := step.Config != nil && step.Config.ImageResource != nil &&
					privilegedType(step.Config.ImageResource.Type)
				if step.Privileged || image {
					found = append(found, job.Name+"/"+step.Name)
				}
				return nil
			},
			OnGet: func(step *atc.GetStep) error {
				if privilegedResource(step.ResourceName()) {
					found = append(found, job.Name+"/"+step.Name)
				}
				return nil
			},
			OnPut: func(step *atc.PutStep) error {
				if privilegedResource(step.ResourceName()) {
					found = append(found, job.Name+"/"+step.Name)
				}
				return nil
			},
		})).To(Succeed())
	}
	return found
}

// pipelineConfigs decodes every deploy/*.yml that is a pipeline config. The
// only other kind there is a task config (fly execute -c, hack/ci-check.sh):
// a file with a top-level platform: and no jobs:. It is skipped, after
// proving it decodes as one; any other file must decode strictly as a
// pipeline config, so nothing is skipped by a swallowed error.
func pipelineConfigs() map[string]atc.Config {
	GinkgoHelper()
	paths, err := filepath.Glob("../*.yml")
	Expect(err).NotTo(HaveOccurred())
	configs := map[string]atc.Config{}
	for _, path := range paths {
		body, err := os.ReadFile(path)
		Expect(err).NotTo(HaveOccurred())
		var top map[string]any
		Expect(yaml.Unmarshal(body, &top)).To(Succeed(), path)
		_, hasPlatform := top["platform"]
		_, hasJobs := top["jobs"]
		if hasPlatform && !hasJobs {
			var task atc.TaskConfig
			Expect(yaml.UnmarshalStrict(body, &task)).To(Succeed(), "%s has platform: but is not a task config", path)
			continue
		}
		var config atc.Config
		Expect(yaml.UnmarshalStrict(body, &config)).To(Succeed(), path)
		configs[filepath.Base(path)] = config
	}
	return configs
}

func configFrom(body string) atc.Config {
	GinkgoHelper()
	var config atc.Config
	Expect(yaml.UnmarshalStrict([]byte(body), &config)).To(Succeed())
	return config
}

const privilegedTypes = `
resource_types:
- name: dind
  type: registry-image
  privileged: true
  source: {repository: dind}
- name: on-dind
  type: dind
  source: {repository: on-dind}
- name: plain
  type: registry-image
  source: {repository: plain}
resources:
- name: priv
  type: dind
  source: {}
- name: built-on-priv
  type: on-dind
  source: {}
- name: unpriv
  type: plain
  source: {}
`

const plainTask = `
  config:
    platform: linux
    image_resource: {type: plain, source: {}}
    run: {path: "true"}
`

var _ = Describe("Privileged steps", func() {
	DescribeTable("are found",
		func(jobs string, want []string) {
			found := privilegedSteps(configFrom(privilegedTypes + "jobs:\n- name: j\n  plan:\n" + jobs))
			if want == nil {
				Expect(found).To(BeEmpty())
			} else {
				Expect(found).To(ConsistOf(want))
			}
		},
		Entry("not in an unprivileged task, get or put", `
  - task: t`+indent(plainTask, 2)+`
  - get: unpriv
  - put: unpriv
`, nil),
		Entry("in a task that sets privileged", `
  - task: t
    privileged: true`+indent(plainTask, 2), []string{"j/t"}),
		Entry("in a get and a put on a privileged resource type", `
  - get: g
    resource: priv
  - put: priv
`, []string{"j/g", "j/priv"}),
		Entry("in a task whose image_resource is of a privileged type", `
  - task: t
    config:
      platform: linux
      image_resource: {type: dind, source: {}}
      run: {path: "true"}
`, []string{"j/t"}),
		Entry("in a get on a type built on a privileged type", `
  - get: built-on-priv
`, []string{"j/built-on-priv"}),
		Entry("nested in in_parallel, do, try and across", `
  - in_parallel:
    - get: priv
  - do:
    - task: in-do
      privileged: true`+indent(plainTask, 4)+`
  - try:
      task: in-try
      privileged: true`+indent(plainTask, 4)+`
  - across:
    - var: v
      values: [a]
    task: in-across
    privileged: true`+indent(plainTask, 2), []string{"j/priv", "j/in-do", "j/in-try", "j/in-across"}),
		Entry("in a step hook and a job hook", `
  - get: unpriv
    on_success:
      task: step-hook
      privileged: true`+indent(plainTask, 4)+`
  ensure:
    task: job-hook
    privileged: true`+indent(plainTask, 2), []string{"j/step-hook", "j/job-hook"}),
	)

	It("appear in no Run pipeline template", func() {
		templates := map[string]atc.Config{}
		for name, config := range pipelineConfigs() {
			if config.Template {
				templates[name] = config
			}
		}
		Expect(templates).To(HaveKey("implement-template.yml"))
		Expect(templates).To(HaveKey("review-template.yml"))
		Expect(templates).To(HaveKey("landing-compose-template.yml"))
		Expect(templates).To(HaveKey("landing-land-template.yml"))
		Expect(templates).To(HaveKey("landing-parent-template.yml"))
		Expect(templates).To(HaveKey("landing-gate-tests-template.yml"))
		Expect(templates).To(HaveKey("landing-gate-review-template.yml"))
		for name, config := range templates {
			Expect(config.Jobs).NotTo(BeEmpty(), name)
			Expect(privilegedSteps(config)).To(BeEmpty(),
				"%s is a Run pipeline template; a Run pipeline's builds have no owner and can never hold a step pod grant, "+
					"so a privileged step in it would be refused once the privilege gate enforces", name)
		}
	})

	// The inventory a privilege gate's step pod grants must cover, as
	// "<job>/<step>". Every deployment that runs one of these pipelines needs a
	// privileged=true step pod grant for each job listed.
	DescribeTable("in the CI pipelines are exactly the checked-in inventory",
		func(name string, inventory []string) {
			config, ok := pipelineConfigs()[name]
			Expect(ok).To(BeTrue(), name)
			Expect(config.Jobs).NotTo(BeEmpty(), name)
			Expect(privilegedSteps(config)).To(ConsistOf(inventory),
				"%s's privileged steps changed. A new privileged job needs a privileged=true step pod grant "+
					"(--kubernetes-step-pod-grant) on every deployment that runs this pipeline, or the privilege gate "+
					"will refuse its step once it enforces; then update this inventory. A privileged pod a task creates "+
					"with kubectl (a dind pod) is not a step pod and does not belong here. Privileged one-off builds "+
					"(hack/ci-check.sh's extracted tasks, deploy/run-kubelet-task.yml via fly execute --privileged) are "+
					"outside this inventory and need a one-off step pod grant for team main (owner=main/one-off).", name)
		},
		Entry("concourse-pipeline.yml", "concourse-pipeline.yml", []string{
			"brine/brine-suite",
			"worker-image/worker-image",
			"build-image/build-and-push-local",
			"release/tag-push-release",
		}),
		Entry("k8s-e2e-pipeline.yml", "k8s-e2e-pipeline.yml", []string{
			"build-kind-runner/build-kind-runner-image",
			"hangar-generated-pod-contract/hangar-generated-pod-contract",
			"hangar-disk-store-contract/hangar-disk-store-contract",
			"hangar-cluster-contract/hangar-cluster-contract",
			"run-cancellation-kubelet/run-cancellation-kubelet",
			"k8s-behavioral-tests/k8s-behavioral",
		}),
	)
})

// indent pads every line after the first with n spaces, to nest a snippet.
func indent(s string, n int) string {
	return strings.ReplaceAll(s, "\n", "\n"+strings.Repeat(" ", n))
}
