package exec_test

import (
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/vars"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// credVars resolves variables from a fixed set and, like the Kubernetes
// credential manager, reports which secret each one was read from.
type credVars struct {
	vars.StaticVariables
	refs map[string]vars.SecretRef
}

func (c credVars) GetSecretRef(ref vars.Reference) (*vars.SecretRef, bool) {
	secretRef, found := c.refs[ref.Path]
	if !found {
		return nil, false
	}
	return &secretRef, true
}

var _ = Describe("BuildSecretEnv", func() {
	var (
		state  exec.RunState
		params atc.TaskEnv
	)

	BeforeEach(func() {
		state = exec.NewRunState(noopStepper, credVars{
			StaticVariables: vars.StaticVariables{
				"db-password": "s3cret",
				"api-key":     "k3y",
				"plain-var":   "not-a-secret",
				"db":          map[string]any{"password": "s3cret"},
			},
			refs: map[string]vars.SecretRef{
				"db-password": {Namespace: "concourse-main", Name: "db-password", Key: "value"},
				"api-key":     {Namespace: "concourse-main", Name: "api-key", Key: "value"},
				"db":          {Namespace: "concourse-main", Name: "db", Key: "password"},
			},
		})
	})

	resolve := func(ref vars.Reference) {
		GinkgoHelper()
		_, found, err := state.Get(ref)
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
	}

	It("returns nil when no secret refs are tracked", func() {
		params = atc.TaskEnv{"PLAIN": "not-a-secret"}

		resolve(vars.Reference{Path: "plain-var"})

		result := exec.BuildSecretEnv(params, state)
		Expect(result).To(BeNil())
	})

	It("maps param env vars to secret refs when values match", func() {
		params = atc.TaskEnv{"DB_PASS": "s3cret", "API_KEY": "k3y"}

		resolve(vars.Reference{Path: "db-password"})
		resolve(vars.Reference{Path: "api-key"})

		result := exec.BuildSecretEnv(params, state)
		Expect(result).To(HaveLen(2))
		Expect(result["DB_PASS"]).To(Equal(vars.SecretRef{Namespace: "concourse-main", Name: "db-password", Key: "value"}))
		Expect(result["API_KEY"]).To(Equal(vars.SecretRef{Namespace: "concourse-main", Name: "api-key", Key: "value"}))
	})

	It("only maps params whose values match tracked secrets", func() {
		params = atc.TaskEnv{"DB_PASS": "s3cret", "STATIC_VAR": "not-a-secret"}

		resolve(vars.Reference{Path: "db-password"})
		resolve(vars.Reference{Path: "plain-var"})

		result := exec.BuildSecretEnv(params, state)
		Expect(result).To(HaveLen(1))
		Expect(result["DB_PASS"]).To(Equal(vars.SecretRef{Namespace: "concourse-main", Name: "db-password", Key: "value"}))
	})

	It("returns nil when params is empty", func() {
		params = atc.TaskEnv{}

		resolve(vars.Reference{Path: "db-password"})

		result := exec.BuildSecretEnv(params, state)
		Expect(result).To(BeNil())
	})

	It("returns nil when no param values match tracked secrets", func() {
		params = atc.TaskEnv{"FOO": "bar"}

		resolve(vars.Reference{Path: "db-password"})

		result := exec.BuildSecretEnv(params, state)
		Expect(result).To(BeNil())
	})

	// This pins a known defect rather than the desired behaviour: Tracker keys
	// interpolated creds on path plus fields but secret refs on path alone, so
	// the lookup here misses and the literal secret is written into the pod env.
	// Fixing it needs two changes together -- the tracker keys, and a resolver
	// that reports the field as the secret's key. kubernetes.Secrets.GetSecretRef
	// takes only a path and hardcodes Key: "value", and VariableLookupFromSecrets
	// drops Fields before calling it, so the tracker change alone would produce a
	// ref to the wrong key, which is worse than the leak.
	It("returns nil for a param resolved from a field of a credential", func() {
		params = atc.TaskEnv{"DB_PASS": "s3cret"}

		resolve(vars.Reference{Path: "db", Fields: []string{"password"}})

		result := exec.BuildSecretEnv(params, state)
		Expect(result).To(BeNil())
	})
})
