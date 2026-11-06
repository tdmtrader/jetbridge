package atc_test

import (
	"encoding/json"

	"github.com/concourse/concourse/atc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// An in-Run result input reads a result another task of the same job
// produces. It is bound to this Run's own capture, never by admission.
var _ = Describe("in-Run result inputs", func() {
	const authorID = "4c1e0f7a-5b6d-4e2f-8a9b-0c1d2e3f4a5b"
	const validateID = "7d2f1a8b-6c5e-4f3a-9b0c-1d2e3f4a5b6c"

	var author, validate *atc.TaskStep

	BeforeEach(func() {
		author = &atc.TaskStep{
			Name: "author", TaskID: authorID,
			RunInputs: []atc.RunInput{{Name: "snapshot", Input: "source"}},
			RunResult: &atc.RunResult{Name: "change", Output: "change"},
			Config: &atc.TaskConfig{Platform: "linux", Run: atc.TaskRunConfig{Path: "true"},
				Inputs: []atc.TaskInputConfig{{Name: "source"}}, Outputs: []atc.TaskOutputConfig{{Name: "change"}}},
		}
		validate = &atc.TaskStep{
			Name: "validate", TaskID: validateID,
			RunInputs: []atc.RunInput{{Name: "snapshot", Input: "source"}, {Name: "change", Input: "change", Result: "change"}},
			Config: &atc.TaskConfig{Platform: "linux", Run: atc.TaskRunConfig{Path: "true"},
				Inputs: []atc.TaskInputConfig{{Name: "source"}, {Name: "change"}}},
		}
	})

	config := func(jobs ...atc.JobConfig) atc.Config {
		if len(jobs) == 0 {
			jobs = []atc.JobConfig{{Name: "implement", PlanSequence: []atc.Step{{Config: author}, {Config: validate}}}}
		}
		return atc.Config{Template: true, Jobs: jobs}
	}

	It("is declared on the consuming task", func() {
		declarations, err := atc.RunTaskDeclarations(config())
		Expect(err).NotTo(HaveOccurred())
		Expect(declarations).To(HaveLen(2))
		Expect(declarations[1].Inputs).To(ContainElement(atc.RunInput{Name: "change", Input: "change", Result: "change"}))
	})

	It("keeps the closed wire shape", func() {
		var route atc.RunInput
		Expect(json.Unmarshal([]byte(`{"name":"change","input":"change","result":"change"}`), &route)).To(Succeed())
		Expect(route).To(Equal(atc.RunInput{Name: "change", Input: "change", Result: "change"}))
		Expect(json.Unmarshal([]byte(`{"name":"change","input":"change","run":1}`), &route)).NotTo(Succeed())
		encoded, err := json.Marshal(atc.RunInput{Name: "snapshot", Input: "source"})
		Expect(err).NotTo(HaveOccurred())
		Expect(string(encoded)).To(Equal(`{"name":"snapshot","input":"source"}`))
	})

	It("refuses a result no task declares", func() {
		validate.RunInputs[1].Result = "other"
		_, err := atc.RunTaskDeclarations(config())
		Expect(err).To(MatchError(ContainSubstring("must name a result of job implement")))
	})

	It("refuses an invalid result name", func() {
		validate.RunInputs[1].Result = "../change"
		_, err := atc.RunTaskDeclarations(config())
		Expect(err).To(MatchError(ContainSubstring("input result")))
	})

	It("refuses a result another job produces", func() {
		_, err := atc.RunTaskDeclarations(config(
			atc.JobConfig{Name: "author", PlanSequence: []atc.Step{{Config: author}}},
			atc.JobConfig{Name: "validate", PlanSequence: []atc.Step{{Config: validate}}},
		))
		Expect(err).To(MatchError(ContainSubstring("must name a result of job validate")))
	})

	It("refuses a task consuming its own result", func() {
		validate.RunResult = &atc.RunResult{Name: "validation", Output: "validation"}
		validate.Config.Outputs = []atc.TaskOutputConfig{{Name: "validation"}}
		validate.RunInputs[1].Result = "validation"
		_, err := atc.RunTaskDeclarations(config())
		Expect(err).To(MatchError(ContainSubstring("cannot consume its own result")))
	})

	// The order rule judges a template being set or a new Run's template;
	// RunTaskDeclarations, which also reads retained definitions, does not.
	Describe("the producer's place in the plan", func() {
		job := func(steps ...atc.StepConfig) atc.Config {
			var plan []atc.Step
			for _, step := range steps {
				plan = append(plan, atc.Step{Config: step})
			}
			return config(atc.JobConfig{Name: "implement", PlanSequence: plan})
		}
		do := func(steps ...atc.StepConfig) atc.StepConfig {
			var plan []atc.Step
			for _, step := range steps {
				plan = append(plan, atc.Step{Config: step})
			}
			return &atc.DoStep{Steps: plan}
		}
		parallel := func(steps ...atc.StepConfig) atc.StepConfig {
			return &atc.InParallelStep{Config: atc.InParallelConfig{Steps: do(steps...).(*atc.DoStep).Steps}}
		}
		order := func(steps ...atc.StepConfig) error {
			GinkgoHelper()
			declarations, err := atc.RunTaskDeclarations(job(steps...))
			Expect(err).NotTo(HaveOccurred())
			Expect(declarations).To(HaveLen(2))
			return atc.ValidateRunResultOrder(job(steps...))
		}

		It("refuses a consumer that comes before its producer", func() {
			Expect(order(validate, author)).To(MatchError(`Run input "change" of task "validate" reads result "change" of task "author", which comes after it in job implement`))
		})

		It("accepts a producer that comes first across a do", func() {
			Expect(order(do(author), validate)).To(Succeed())
			Expect(order(author, do(validate))).To(Succeed())
		})

		It("accepts a consumer in a hook of the producer's step", func() {
			Expect(order(&atc.OnSuccessStep{Step: author, Hook: atc.Step{Config: validate}})).To(Succeed())
			Expect(order(&atc.EnsureStep{Step: &atc.OnSuccessStep{Step: author, Hook: atc.Step{Config: &atc.TaskStep{Name: "notify"}}}, Hook: atc.Step{Config: validate}})).To(Succeed())
		})

		It("refuses a producer in a hook of the consumer's step", func() {
			Expect(order(&atc.EnsureStep{Step: validate, Hook: atc.Step{Config: author}})).To(MatchError(ContainSubstring("which comes after it")))
		})

		Describe("in_parallel", func() {
			const inParallel = `Run input "change" of task "validate" reads result "change" of task "author", which runs in parallel with it in job implement`

			It("refuses producer and consumer in different branches, in either listed order", func() {
				Expect(order(parallel(author, validate))).To(MatchError(inParallel))
				Expect(order(parallel(validate, author))).To(MatchError(inParallel))
				Expect(order(parallel(do(&atc.TaskStep{Name: "slow"}, validate), author))).To(MatchError(inParallel))
			})

			It("refuses them in different branches of an in_parallel nested in a branch", func() {
				Expect(order(parallel(&atc.TaskStep{Name: "lint"}, do(parallel(author, do(validate)))))).To(MatchError(inParallel))
			})

			It("judges producer and consumer in one branch by their order in it", func() {
				Expect(order(parallel(&atc.TaskStep{Name: "lint"}, do(author, validate)))).To(Succeed())
				Expect(order(parallel(&atc.TaskStep{Name: "lint"}, do(validate, author)))).To(MatchError(ContainSubstring("which comes after it")))
				Expect(order(parallel(do(author, parallel(&atc.TaskStep{Name: "lint"}, validate))))).To(Succeed())
			})

			It("judges the branches of an in_parallel limited to one by their listed order", func() {
				serial := func(steps ...atc.StepConfig) atc.StepConfig {
					step := parallel(steps...).(*atc.InParallelStep)
					step.Config.Limit = 1
					return step
				}
				Expect(order(serial(author, validate))).To(Succeed())
				Expect(order(serial(validate, author))).To(MatchError(ContainSubstring("which comes after it")))
			})

			It("accepts a producer before the in_parallel whose branch holds the consumer", func() {
				Expect(order(author, parallel(&atc.TaskStep{Name: "lint"}, validate))).To(Succeed())
				Expect(order(author, parallel(&atc.TaskStep{Name: "lint"}, do(parallel(validate))))).To(Succeed())
			})
		})
	})

	It("refuses a name an admission input also uses", func() {
		validate.RunInputs[1].Name = "snapshot"
		validate.RunInputs = validate.RunInputs[1:]
		validate.Config.Inputs = validate.Config.Inputs[1:]
		_, err := atc.RunTaskDeclarations(config())
		Expect(err).To(MatchError(ContainSubstring("both an in-Run result input and an admission input")))
	})

	It("refuses one name for two results", func() {
		author.RunResult = &atc.RunResult{Name: "change", Output: "change"}
		third := &atc.TaskStep{
			Name: "summarize", TaskID: "9e3a2b1c-7d6f-4a5b-8c0d-2e3f4a5b6c7d",
			RunResult: &atc.RunResult{Name: "summary", Output: "summary"},
			RunInputs: []atc.RunInput{{Name: "change", Input: "change", Result: "summary"}},
			Config: &atc.TaskConfig{Platform: "linux", Run: atc.TaskRunConfig{Path: "true"},
				Inputs: []atc.TaskInputConfig{{Name: "change"}}, Outputs: []atc.TaskOutputConfig{{Name: "summary"}}},
		}
		_, err := atc.RunTaskDeclarations(config(atc.JobConfig{Name: "implement", PlanSequence: []atc.Step{{Config: author}, {Config: validate}, {Config: third}}}))
		Expect(err).To(MatchError(ContainSubstring("names more than one result")))
	})
})
