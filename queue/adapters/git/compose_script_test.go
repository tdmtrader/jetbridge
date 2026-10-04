package git_test

import (
	"context"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/queue/adapters/git"
	"github.com/concourse/concourse/queue/config"
	"github.com/concourse/concourse/queue/core"
)

// hookScript is a fake hook script: it logs each verb to verbs, owns the files
// in owned and runs body for "run".
func hookScript(verbs, owned, body string) string {
	return "#!/bin/sh\necho \"$1\" >> " + verbs + "\ncase \"$1\" in\nowned) echo " + owned + " ;;\nrun) " + body + " ;;\nesac\n"
}

// scriptRepo is a remote with main at one empty commit, and a clone to commit from.
type scriptRepo struct{ remote, work, verbs string }

func newScriptRepo() scriptRepo {
	dir := GinkgoT().TempDir()
	r := scriptRepo{filepath.Join(dir, "remote.git"), filepath.Join(dir, "work"), filepath.Join(dir, "verbs")}
	composeRun(dir, "init", "-q", "--bare", r.remote)
	composeRun(dir, "clone", "-q", r.remote, r.work)
	composeRun(r.work, "commit", "-q", "--allow-empty", "-m", "first")
	composeRun(r.work, "push", "-q", "origin", "HEAD:main")
	return r
}

// commit writes file=content on branch, made off from, and pushes it.
func (r scriptRepo) commit(branch, from, file, content string) core.Entry {
	composeRun(r.work, "checkout", "-q", "-B", branch, from)
	ExpectWithOffset(1, os.MkdirAll(filepath.Dir(filepath.Join(r.work, file)), 0o755)).To(Succeed())
	ExpectWithOffset(1, os.WriteFile(filepath.Join(r.work, file), []byte(content), 0o755)).To(Succeed())
	composeRun(r.work, "add", file)
	composeRun(r.work, "commit", "-q", "-m", "change "+branch)
	composeRun(r.work, "push", "-q", "-f", "origin", "HEAD:"+branch)
	return core.Entry{ID: branch, Commit: composeRun(r.work, "rev-parse", "HEAD"), Ref: branch}
}

func (r scriptRepo) config(extra string) config.Config {
	c, err := config.Parse([]byte("apiVersion: jetbridge.dev/queue/v2\nrepository: {uri: " + r.remote + ", main: main}\ncompose: {hook_script: ci/hook.sh" + extra + "}\n"))
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	return c
}

var _ = Describe("Composer with a hook script on main", func() {
	var r scriptRepo
	ctx := context.Background()
	BeforeEach(func() { r = newScriptRepo() })

	It("Changes that both touch a generated file compose, keeping main's copy for the hook to regenerate", func() {
		r.commit("main", "main", "ci/hook.sh", hookScript(r.verbs, "gen/map.txt", "true"))
		r.commit("main", "main", "gen/map.txt", "old\n")
		a, b := r.commit("a", "main", "gen/map.txt", "from a\n"), r.commit("b", "main", "gen/map.txt", "from b\n")
		sha, err := git.NewComposer(r.config("")).Compose(ctx, "main", []core.Entry{a, b})
		Expect(err).NotTo(HaveOccurred())
		Expect(composeRun(r.remote, "show", sha+":gen/map.txt")).To(Equal("from a"))
		v, _ := os.ReadFile(r.verbs)
		Expect(string(v)).To(Equal("owned\n"))
	})

	It("The files a hook owns are asked of main's script, never of one a queued change brings", func() {
		r.commit("main", "main", "ci/hook.sh", hookScript(r.verbs, "gen/map.txt", "true"))
		r.commit("main", "main", "shared.txt", "old\n")
		a := r.commit("a", "main", "shared.txt", "from a\n")
		composeRun(r.work, "checkout", "-q", "main")
		b := r.commit("b", "main", "ci/hook.sh", hookScript(r.verbs, "shared.txt", "true"))
		b = r.commit("b", "b", "shared.txt", "from b\n")
		_, err := git.NewComposer(r.config("")).Compose(ctx, "main", []core.Entry{a, b})
		Expect(err).To(MatchError(core.ConflictError{EntryID: "b"}))
	})

	It("A hook script that cannot say what it owns gives no verdict", func() {
		r.commit("main", "main", "ci/hook.sh", "#!/bin/sh\nexit 3\n")
		batch := []core.Entry{r.commit("a", "main", "a.txt", "a\n")}
		_, err := git.NewComposer(r.config(", hook_timeout: 2s")).Compose(ctx, "main", batch)
		Expect(err).To(MatchError("compose hook failed: exit 3"))
		Expect(core.ComposeVerdict(err, batch)).To(Equal(core.None))
	})

	It("With no hook script on main a land composes as it does without one", func() {
		r.commit("main", "main", "gen/map.txt", "old\n")
		a, b := r.commit("a", "main", "gen/map.txt", "from a\n"), r.commit("b", "main", "gen/map.txt", "from b\n")
		_, err := git.NewComposer(r.config("")).Compose(ctx, "main", []core.Entry{a, b})
		Expect(err).To(MatchError(core.ConflictError{EntryID: "b"}))
		Expect(r.verbs).NotTo(BeAnExistingFile())
	})

	It("A hook script is a path in the repository and excludes a hook command", func() {
		for _, bad := range []string{"hook_script: /bin/sh", "hook_script: ../x", "hook_script: x, hook: [make]", "hook_script: x, hook_owned: [gen/]"} {
			_, err := config.Parse([]byte("apiVersion: jetbridge.dev/queue/v2\nrepository: {uri: x}\ncompose: {" + bad + "}\n"))
			Expect(err).To(HaveOccurred(), bad)
		}
	})
})
