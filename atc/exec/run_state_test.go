package exec_test

import (
	"context"
	"errors"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/vars"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type runStateContextKey struct{}

var _ = Describe("RunState", func() {
	var (
		stepper exec.Stepper
		runStep stepFunc

		credVars vars.Variables

		state exec.RunState
	)

	BeforeEach(func() {
		runStep = stepFunc(func(context.Context, exec.RunState) (bool, error) {
			return true, nil
		})
		stepper = func(atc.Plan) exec.Step {
			return runStep
		}

		credVars = vars.StaticVariables{"k1": "v1", "k2": "v2", "k3": "v3"}

		state = exec.NewRunState(stepper, credVars)
	})

	Describe("Run", func() {
		var ctx context.Context
		var plan atc.Plan

		var runOk bool
		var runErr error

		BeforeEach(func() {
			ctx = context.WithValue(context.Background(), runStateContextKey{}, "forwarded-context")
			plan = atc.Plan{
				ID: "some-plan",
				LoadVar: &atc.LoadVarPlan{
					Name: "foo",
					File: "bar",
				},
			}

			runStep = stepFunc(func(ctx context.Context, state exec.RunState) (bool, error) {
				if ctx.Value(runStateContextKey{}) != "forwarded-context" {
					return false, errors.New("run context was not forwarded")
				}
				value, found, err := state.Get(vars.Reference{Path: "k1"})
				if err != nil {
					return false, err
				}
				if !found || value != "v1" {
					return false, errors.New("run state was not forwarded")
				}
				state.StoreResult(plan.ID, "context-and-state-forwarded")
				return true, nil
			})

			stepper = func(candidate atc.Plan) exec.Step {
				if candidate.ID != plan.ID || candidate.LoadVar == nil || candidate.LoadVar.Name != "foo" || candidate.LoadVar.File != "bar" {
					return stepFunc(func(context.Context, exec.RunState) (bool, error) {
						return false, errors.New("run plan was not forwarded")
					})
				}
				return runStep
			}
			state = exec.NewRunState(stepper, credVars)
		})

		JustBeforeEach(func() {
			runOk, runErr = state.Run(ctx, plan)
		})

		It("forwards the plan, context, and state through the step contract", func() {
			Expect(runErr).ToNot(HaveOccurred())
			var forwarded string
			Expect(state.Result(plan.ID, &forwarded)).To(BeTrue())
			Expect(forwarded).To(Equal("context-and-state-forwarded"))
		})

		Context("when the step succeeds", func() {
			BeforeEach(func() {
				runStep = stepFunc(func(context.Context, exec.RunState) (bool, error) {
					return true, nil
				})
			})

			It("succeeds", func() {
				Expect(runOk).To(BeTrue())
			})
		})

		Context("when the step fails", func() {
			BeforeEach(func() {
				runStep = stepFunc(func(context.Context, exec.RunState) (bool, error) {
					return false, nil
				})
			})

			It("fails", func() {
				Expect(runOk).To(BeFalse())
			})
		})

		Context("when the step errors", func() {
			disaster := errors.New("nope")

			BeforeEach(func() {
				runStep = stepFunc(func(context.Context, exec.RunState) (bool, error) {
					return false, disaster
				})
			})

			It("returns the error", func() {
				Expect(runErr).To(Equal(disaster))
			})
		})
	})

	Describe("Result", func() {
		var to int

		BeforeEach(func() {
			to = 42
		})

		Context("when no result is present", func() {
			It("returns false", func() {
				Expect(state.Result("some-id", &to)).To(BeFalse())
			})

			It("does not mutate the var", func() {
				state.Result("some-id", &to)
				Expect(to).To(Equal(42))
			})
		})

		Context("when a result under a different id is present", func() {
			BeforeEach(func() {
				state.StoreResult("other", 43)
			})

			It("returns false", func() {
				Expect(state.Result("some-id", &to)).To(BeFalse())
			})

			It("does not mutate the var", func() {
				state.Result("some-id", &to)
				Expect(to).To(Equal(42))
			})
		})

		Context("when a result under the given id is present", func() {
			BeforeEach(func() {
				state.StoreResult("some-id", 123)
			})

			It("returns true", func() {
				Expect(state.Result("some-id", &to)).To(BeTrue())
			})

			It("mutates the var", func() {
				state.Result("some-id", &to)
				Expect(to).To(Equal(123))
			})

			Context("but with a different type", func() {
				BeforeEach(func() {
					state.StoreResult("some-id", "one hundred and twenty-three")
				})

				It("returns false", func() {
					Expect(state.Result("some-id", &to)).To(BeFalse())
				})

				It("does not mutate the var", func() {
					state.Result("some-id", &to)
					Expect(to).To(Equal(42))
				})
			})
		})
	})

	Describe("Get", func() {
		BeforeEach(func() {
			state = exec.NewRunState(stepper, credVars)
		})

		It("fetches from cred vars", func() {
			val, found, err := state.Get(vars.Reference{Path: "k1"})
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(val).To(Equal("v1"))
		})

		Context("when local var subfield does not exist", func() {
			It("errors", func() {
				state.AddLocalVar("foo", map[string]any{"bar": "baz"}, false)
				_, _, err := state.Get(vars.Reference{Source: ".", Path: "foo", Fields: []string{"missing"}})
				Expect(err).To(HaveOccurred())
			})
		})

		Context("when redaction is enabled", func() {
			BeforeEach(func() {
				state = exec.NewRunState(stepper, credVars)
			})

			It("fetched variables are tracked", func() {
				state.Get(vars.Reference{Path: "k1"})
				state.Get(vars.Reference{Path: "k2"})
				mapit := vars.TrackedVarsMap{}
				state.IterateInterpolatedCreds(mapit)
				Expect(mapit["k1"]).To(Equal("v1"))
				Expect(mapit["k2"]).To(Equal("v2"))
				// "k3" has not been Get, thus should not be tracked.
				Expect(mapit).ToNot(HaveKey("k3"))
			})
		})
	})

	Describe("List", func() {
		It("returns list of names from multiple vars with duplicates", func() {
			defs, err := state.List()
			Expect(defs).To(ConsistOf([]vars.Reference{
				{Path: "k1"},
				{Path: "k2"},
				{Path: "k3"},
			}))
			Expect(err).ToNot(HaveOccurred())
		})

		It("includes all local vars", func() {
			state.AddLocalVar("l1", 1, false)
			state.AddLocalVar("l2", 2, false)

			defs, err := state.List()
			Expect(defs).To(ConsistOf([]vars.Reference{
				{Source: ".", Path: "l1"},
				{Source: ".", Path: "l2"},

				{Path: "k1"},
				{Path: "k2"},
				{Path: "k3"},
			}))
			Expect(err).ToNot(HaveOccurred())
		})
	})

	Describe("AddLocalVar", func() {
		Describe("redact", func() {
			BeforeEach(func() {
				state = exec.NewRunState(stepper, credVars)
				state.AddLocalVar("foo", "bar", true)
			})

			It("should get local value", func() {
				val, found, err := state.Get(vars.Reference{Source: ".", Path: "foo"})
				Expect(err).To(BeNil())
				Expect(found).To(BeTrue())
				Expect(val).To(Equal("bar"))
			})

			It("fetched variables are tracked when added", func() {
				mapit := vars.TrackedVarsMap{}
				state.IterateInterpolatedCreds(mapit)
				Expect(mapit["foo"]).To(Equal("bar"))
			})
		})
	})

	Describe("NewLocalScope", func() {
		It("maintains a reference to the parent", func() {
			Expect(state.NewLocalScope().Parent()).To(Equal(state))
		})

		It("can access local vars from parent scope", func() {
			state.AddLocalVar("hello", "world", false)
			scope := state.NewLocalScope()
			val, _, _ := scope.Get(vars.Reference{Source: ".", Path: "hello"})
			Expect(val).To(Equal("world"))
		})

		It("adding local vars does not affect the original tracker", func() {
			scope := state.NewLocalScope()
			scope.AddLocalVar("hello", "world", false)
			_, found, _ := state.Get(vars.Reference{Source: ".", Path: "hello"})
			Expect(found).To(BeFalse())
		})

		It("shares the underlying non-local variables", func() {
			scope := state.NewLocalScope()
			val, _, _ := scope.Get(vars.Reference{Path: "k1"})
			Expect(val).To(Equal("v1"))
		})

		It("local vars added after creating the subscope are accessible", func() {
			scope := state.NewLocalScope()
			state.AddLocalVar("hello", "world", false)
			val, _, _ := scope.Get(vars.Reference{Source: ".", Path: "hello"})
			Expect(val).To(Equal("world"))
		})

		It("current scope is preferred over parent scope", func() {
			state.AddLocalVar("a", 1, false)
			scope := state.NewLocalScope()
			scope.AddLocalVar("a", 2, false)

			val, _, _ := scope.Get(vars.Reference{Source: ".", Path: "a"})
			Expect(val).To(Equal(2))
		})

		It("results set in parent scope are accessible in child", func() {
			parent := state
			child := parent.NewLocalScope()

			parent.StoreResult("id", "hello")

			var dst string
			child.Result("id", &dst)
			Expect(dst).To(Equal("hello"))
		})

		It("results set in child scope are accessible in parent", func() {
			parent := state
			child := parent.NewLocalScope()

			child.StoreResult("id", "hello")

			var dst string
			state.Result("id", &dst)
			Expect(dst).To(Equal("hello"))
		})

		It("has a local artifact scope inheriting from the outer scope", func() {
			Expect(state.NewLocalScope().ArtifactRepository().Parent()).To(Equal(state.ArtifactRepository()))
		})

		Describe("TrackedVarsMap", func() {
			BeforeEach(func() {
				state = exec.NewRunState(stepper, credVars)
			})

			It("prefers the value set in the current scope over the parent scope", func() {
				state.AddLocalVar("a", "from parent", true)
				scope := state.NewLocalScope()
				scope.AddLocalVar("a", "from child", true)

				mapit := vars.TrackedVarsMap{}
				scope.IterateInterpolatedCreds(mapit)

				Expect(mapit["a"]).To(Equal("from child"))
			})
		})
	})
})
