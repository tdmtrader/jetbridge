package git_test

import (
	"context"
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/queue/adapters/git"
	"github.com/concourse/concourse/queue/core"
)

var _ = Describe("Lander with a hook script on main", func() {
	var r scriptRepo
	var a core.Entry
	ctx := context.Background()
	BeforeEach(func() {
		r = newScriptRepo()
		r.commit("main", "main", "ci/hook.sh", hookScript(r.verbs, "gen/map.txt", "true"))
		a = r.commit("a", "main", "a.txt", "a\n")
	})
	// hooked commits file=content on from and publishes it as the hooked commit of a.
	hooked := func(from, file, content string) string {
		h := r.commit("h", from, file, content)
		composeRun(r.work, "push", "-q", "-f", "origin", h.Commit+":"+git.HookedRef(a.Commit))
		return h.Commit
	}
	land := func() error {
		l, err := git.New(r.config(""))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(l.Close)
		return l.Land(ctx, "main", a.Commit, 1)
	}
	refusedWith := func(err error, why string) {
		before := composeRun(r.remote, "rev-parse", "main")
		ExpectWithOffset(1, err).To(MatchError(ContainSubstring(why)))
		var moved *core.MainMovedError
		ExpectWithOffset(1, errors.As(err, &moved)).To(BeFalse())
		ExpectWithOffset(1, composeRun(r.remote, "rev-parse", "main")).To(Equal(before))
	}

	It("A land refreshes the generated boundaries before it reaches main", func() {
		h := hooked(a.Commit, "gen/map.txt", "regenerated\n")
		Expect(land()).To(Succeed())
		Expect(composeRun(r.remote, "rev-parse", "main")).To(Equal(h))
		Expect(composeRun(r.remote, "show", "main:gen/map.txt")).To(Equal("regenerated"))
	})

	It("A candidate whose hook has not run does not land while main has a hook script", func() {
		refusedWith(land(), "has no hooked commit at "+git.HookedRef(a.Commit))
	})

	It("A hooked commit that is not one commit on the candidate does not land", func() {
		hooked("main", "gen/map.txt", "regenerated\n")
		refusedWith(land(), "is not one commit on the candidate")
	})

	It("A hooked commit that changes a file the hook does not own does not land", func() {
		hooked(a.Commit, "stray.txt", "x\n")
		refusedWith(land(), "changes 1 file(s) the hook does not own")
	})

	It("With no hook script on main the candidate lands as it is", func() {
		composeRun(r.work, "checkout", "-q", "main")
		composeRun(r.work, "rm", "-q", "ci/hook.sh")
		composeRun(r.work, "commit", "-q", "-m", "no hook")
		composeRun(r.work, "push", "-q", "origin", "HEAD:main")
		a = r.commit("a", "main", "a.txt", "a\n")
		Expect(land()).To(Succeed())
		Expect(composeRun(r.remote, "rev-parse", "main")).To(Equal(a.Commit))
	})
})
