package landing_test

import (
	"os"
	"path/filepath"

	"github.com/concourse/concourse/atc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/yaml"
)

// privilegedSteps lists every step of a pipeline file that would run in a
// privileged step pod, as "<job>/<step>": a task that sets privileged:, or a
// get or put whose resource's type is declared privileged:. Privileged
// containers a task creates itself (a dind pod applied with kubectl) are not
// step pods and are not listed.
func privilegedSteps(path string) []string {
	GinkgoHelper()
	body, err := os.ReadFile(path)
	Expect(err).NotTo(HaveOccurred())
	var config atc.Config
	Expect(yaml.UnmarshalStrict(body, &config)).To(Succeed(), path)

	privilegedTypes := map[string]bool{}
	for _, t := range config.ResourceTypes {
		if t.Privileged {
			privilegedTypes[t.Name] = true
		}
	}
	privilegedResource := func(name string) bool {
		r, ok := config.Resources.Lookup(name)
		return ok && privilegedTypes[r.Type]
	}

	var found []string
	for _, job := range config.Jobs {
		job := job
		Expect(job.StepConfig().Visit(atc.StepRecursor{
			OnTask: func(step *atc.TaskStep) error {
				if step.Privileged {
					found = append(found, job.Name+"/"+step.Name)
				}
				return nil
			},
			OnGet: func(step *atc.GetStep) error {
				resource := step.Resource
				if resource == "" {
					resource = step.Name
				}
				if privilegedResource(resource) {
					found = append(found, job.Name+"/"+step.Name)
				}
				return nil
			},
			OnPut: func(step *atc.PutStep) error {
				resource := step.Resource
				if resource == "" {
					resource = step.Name
				}
				if privilegedResource(resource) {
					found = append(found, job.Name+"/"+step.Name)
				}
				return nil
			},
		})).To(Succeed(), path)
	}
	return found
}

var _ = Describe("Privileged steps", func() {
	It("appear in no landing, implement or review template", func() {
		templates, err := filepath.Glob("../*-template.yml")
		Expect(err).NotTo(HaveOccurred())
		// The set this guard covers: if a template is added or renamed, the
		// guard still reads it, and this pins that it reads the ones known today.
		Expect(templates).To(ContainElements(
			"../implement-template.yml",
			"../review-template.yml",
			"../landing-compose-template.yml",
			"../landing-land-template.yml",
			"../landing-parent-template.yml",
			"../landing-gate-tests-template.yml",
			"../landing-gate-review-template.yml",
		))
		for _, path := range templates {
			Expect(privilegedSteps(path)).To(BeEmpty(),
				"%s is a Run pipeline template; a Run pipeline's builds have no owner and can never hold a step pod grant, "+
					"so a privileged step in it would be refused once the privilege gate enforces", path)
		}
	})
})
