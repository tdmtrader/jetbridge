package git_test

import (
	"context"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/queue/adapters/git"
	"github.com/concourse/concourse/queue/config"
	"github.com/concourse/concourse/queue/core"
)

func ids(es []core.Entry) []string {
	out := []string{}
	for _, e := range es {
		out = append(out, e.ID)
	}
	return out
}

const prefix = "refs/queue/admit/"

var _ = Describe("Admissions", func() {
	ctx := context.Background()
	var (
		r   *remote
		adm *git.Admissions
		c   config.Config
	)
	BeforeEach(func() {
		r = newRemote()
		c = r.config()
		c.Admission.Prefix = prefix
		adm = &git.Admissions{Lander: r.lander(), Prefix: prefix}
	})
	admit := func(id, sha string) { Expect(git.Admit(ctx, c, r.work, id, sha)).To(Succeed()) }
	pending := func(queued ...core.Entry) []core.Pending {
		ps, err := adm.Pending(ctx, queued)
		Expect(err).NotTo(HaveOccurred())
		return ps
	}

	It("A stacked change pushed after its parent builds on it and is batched with it", func() {
		p := r.commit("p", r.base)
		child, other := r.commit("child", p), r.commit("other", r.base)
		admit("child", child)
		admit("other", other)
		queued := []core.Entry{{ID: "p", Commit: p}}
		ps := pending(queued...)
		Expect(ps).To(Equal([]core.Pending{{ID: "child", Commit: child, BuildsOn: []string{"p"}}, {ID: "other", Commit: other}}))
		b := core.FormBatch(append(queued, core.Entry{ID: "child"}), map[string][]string{"child": ps[0].BuildsOn}, nil)
		Expect(ids(b.Entries())).To(Equal([]string{"p", "child"}))
		Expect(b.Ancestors("child")).To(Equal([]string{"p"}))

		By("a parent pending in the same drain comes first, whatever its ID")
		mid := r.commit("mid", p)
		top := r.commit("top", mid)
		admit("aa-top", top)
		admit("zz-mid", mid)
		Expect(pending()).To(Equal([]core.Pending{{ID: "child", Commit: child}, {ID: "other", Commit: other},
			{ID: "zz-mid", Commit: mid}, {ID: "aa-top", Commit: top, BuildsOn: []string{"zz-mid"}}}))
	})

	It("derives every queued ancestor of a merge, so a divergent merge can be refused", func() {
		a, b := r.commit("a", r.base), r.commit("b", r.base)
		m := run(r.work, "commit-tree", run(r.work, "mktree"), "-p", a, "-p", b, "-m", "m")
		admit("x", m)
		Expect(pending(core.Entry{ID: "a", Commit: a}, core.Entry{ID: "b", Commit: b})).To(Equal(
			[]core.Pending{{ID: "x", Commit: m, BuildsOn: []string{"a", "b"}}}))
	})

	It("a pending replacement of a queued id is no ancestor candidate for a child", func() {
		a := r.commit("a", r.base)
		b := r.commit("b", r.base)
		cc := r.commit("c", b)
		admit("a", b)
		admit("c", cc)
		ps := pending(core.Entry{ID: "a", Commit: a})
		Expect(ps).To(ConsistOf(core.Pending{ID: "a", Commit: b}, core.Pending{ID: "c", Commit: cc}))
	})

	It("a git call returns soon after its context ends even when a child holds its stderr", func() {
		script := filepath.Join(GinkgoT().TempDir(), "ssh.sh")
		Expect(os.WriteFile(script, []byte("#!/bin/sh\nsleep 20\n"), 0o700)).To(Succeed())
		GinkgoT().Setenv("GIT_SSH_COMMAND", script)
		GinkgoT().Setenv("GIT_SSH_VARIANT", "ssh") // no probe: the sleeper runs as the transport and holds git's stderr
		c.Repository.URI = "ssh://host.invalid/repo.git"
		cctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		defer cancel()
		start := time.Now()
		Expect(git.Admit(cctx, c, r.work, "a", r.base)).NotTo(Succeed())
		Expect(time.Since(start)).To(BeNumerically("<", 9*time.Second))
	})

	It("An unsafe change id is refused", func() {
		sha := r.commit("c1", r.base)
		for _, id := range []string{"", "a/b", "../x", "-x", "a..b", "x.lock", "x.", "a b", "a:b"} {
			Expect(git.Admit(ctx, c, r.work, id, sha)).To(MatchError(ContainSubstring("unsafe")), id)
		}
		Expect(git.Admit(ctx, c, r.work, "ok", "--force")).NotTo(Succeed(), "a sha is never read as an option")
		Expect(run(r.bare, "for-each-ref", prefix)).To(BeEmpty())

		By("one pushed under the prefix anyway is refused at drain")
		run(r.work, "push", "-q", r.bare, sha+":"+prefix+"team/x")
		ps := pending()
		Expect(ps).To(HaveLen(1))
		Expect(ps[0].ID).To(Equal("team/x"))
		Expect(ps[0].Why).To(ContainSubstring("unsafe"))
	})

	It("refuses an admitted object that is not a commit", func() {
		run(r.work, "push", "-q", r.bare, run(r.work, "mktree")+":"+prefix+"t")
		ps := pending()
		Expect(ps).To(HaveLen(1))
		Expect(ps[0].Why).To(ContainSubstring("not a commit"))
	})

	It("deletes an admit ref only at the sha it was seen at", func() {
		c1, c2 := r.commit("c1", r.base), r.commit("c2", r.base)
		admit("a", c1)
		Expect(adm.Done(ctx, "a", c2)).NotTo(Succeed())
		Expect(run(r.bare, "rev-parse", prefix+"a")).To(Equal(c1))
		Expect(adm.Done(ctx, "a", c1)).To(Succeed())
		Expect(run(r.bare, "for-each-ref", prefix)).To(BeEmpty())
	})
})
