package git_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/queue/adapters/git"
	"github.com/concourse/concourse/queue/core"
)

var _ = Describe("The test job's hook step", func() {
	var r scriptRepo
	var a core.Entry
	var wd string
	var prep func() // after the inputs are checked out, before the step runs
	step, _ := filepath.Abs("../../example/hook-step.sh")
	BeforeEach(func() {
		r = newScriptRepo()
		wd, prep = GinkgoT().TempDir(), func() {}
		Expect(os.WriteFile(filepath.Join(wd, "input"), []byte("v1\n"), 0o644)).To(Succeed())
	})
	// run makes main's hook script do body (none if empty), checks out the
	// candidate a and main as the job's inputs, and runs the step: its exit status.
	run := func(body string) int {
		if body != "" {
			r.commit("main", "main", "ci/hook.sh", hookScript(r.verbs, "gen/map.txt", body))
		}
		a = r.commit("a", "main", "a.txt", "a\n")
		composeRun(wd, "clone", "-q", "-b", "a", r.remote, "candidate")
		composeRun(wd, "clone", "-q", "-b", "main", r.remote, "main")
		prep()
		cmd := exec.Command("bash", step)
		cmd.Dir, cmd.Env = wd, append(os.Environ(), "HOOK_SCRIPT=ci/hook.sh", "HOOK_INPUTS="+wd, "HOOK_TIMEOUT=1")
		out, err := cmd.CombinedOutput()
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode()
		}
		Expect(err).NotTo(HaveOccurred(), string(out))
		return 0
	}
	bundle := func() string { return filepath.Join(wd, "hook", "hook.bundle") }

	It("The test job's hook step publishes the regenerated files as one commit on the candidate, and that commit lands", func() {
		Expect(run(`mkdir -p gen; { cat a.txt; cat "$JBQ_HOOK_INPUTS/input"; } > gen/map.txt; git reset -q --soft "$JBQ_HOOK_BASE"`)).To(Equal(0))
		v, _ := os.ReadFile(r.verbs)
		Expect(string(v)).To(Equal("owned\nrun\n"))
		h := composeRun(wd, "-C", "candidate", "rev-parse", "HEAD")
		Expect(composeRun(wd, "-C", "candidate", "rev-parse", "HEAD~1")).To(Equal(a.Commit))
		composeRun(r.remote, "fetch", "-q", bundle(), "refs/heads/hooked:"+git.HookedRef(a.Commit))
		l, err := git.New(r.config(""))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(l.Close)
		Expect(l.Land(context.Background(), "main", a.Commit, 1)).To(Succeed())
		Expect(composeRun(r.remote, "rev-parse", "main")).To(Equal(h))
		Expect(composeRun(r.remote, "show", "main:gen/map.txt")).To(Equal("a\nv1"))
	})

	It("The test job's hook step leaves the get's .mq dir out of the hook's commit", func() {
		prep = func() {
			Expect(os.MkdirAll(filepath.Join(wd, "candidate", ".mq"), 0o755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(wd, "candidate", ".mq", "run"), []byte("r\n"), 0o644)).To(Succeed())
		}
		Expect(run(`mkdir -p gen; cat a.txt > gen/map.txt`)).To(Equal(0))
		Expect(composeRun(wd, "-C", "candidate", "diff", "--name-only", a.Commit, "HEAD")).To(Equal("gen/map.txt"))
	})

	It("A hook that refuses, or changes a file it does not own, fails the test job's hook step", func() {
		Expect(run("exit 3")).To(Equal(1))
		Expect(bundle()).NotTo(BeAnExistingFile())
		wd = GinkgoT().TempDir()
		Expect(run("echo x > stray.txt")).To(Equal(1))
		Expect(bundle()).NotTo(BeAnExistingFile())
	})

	It("A hook that cannot finish or runs out of time leaves the test job's hook step without a verdict", func() {
		Expect(run("exit 75")).To(Equal(75))
		wd = GinkgoT().TempDir()
		Expect(run("sleep 30")).To(Equal(75))
		Expect(bundle()).NotTo(BeAnExistingFile())
	})

	It("With no hook script on main the test job's hook step does nothing", func() {
		Expect(run("")).To(Equal(0))
		Expect(bundle()).NotTo(BeAnExistingFile())
		Expect(composeRun(wd, "-C", "candidate", "rev-parse", "HEAD")).To(Equal(a.Commit))
	})
})
