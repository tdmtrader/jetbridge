package git_test

import (
	"context"
	"os"
	"os/exec"
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
		pending()
		Expect(adm.Done(ctx, "a", c2)).NotTo(Succeed())
		Expect(run(r.bare, "for-each-ref", "--format=%(objectname)", prefix)).To(Equal(c1))
		Expect(adm.Done(ctx, "a", c1)).To(Succeed())
		Expect(run(r.bare, "for-each-ref", prefix)).To(BeEmpty())
	})
})

var _ = Describe("Admission order and repeats", func() {
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

	It("Two changes admitted in the same second keep their order", func() {
		cb, ca := r.commit("b", r.base), r.commit("a", r.base)
		Expect(git.Admit(ctx, c, r.work, "b", cb)).To(Succeed())
		Expect(git.Admit(ctx, c, r.work, "a", ca)).To(Succeed())
		ps, err := adm.Pending(ctx, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect([]string{ps[0].ID, ps[1].ID}).To(Equal([]string{"b", "a"}))
		Expect(adm.Done(ctx, "b", cb)).To(Succeed())
		Expect(adm.Done(ctx, "a", ca)).To(Succeed())
		Expect(run(r.bare, "for-each-ref", prefix)).To(BeEmpty())
	})

	It("A change updated after its test is not ejected", func() {
		c1, c2 := r.commit("c1", r.base), r.commit("c2", r.base)
		Expect(git.Admit(ctx, c, r.work, "a", c1)).To(Succeed())
		_, err := adm.Pending(ctx, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(git.Admit(ctx, c, r.work, "a", c2)).To(Succeed())
		Expect(adm.Done(ctx, "a", c1)).To(Succeed())
		Expect(run(r.bare, "for-each-ref", "--format=%(objectname)", prefix)).To(Equal(c2))
	})

	It("Settling an already settled change again changes nothing", func() {
		c1 := r.commit("c1", r.base)
		Expect(git.Admit(ctx, c, r.work, "a", c1)).To(Succeed())
		_, err := adm.Pending(ctx, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(adm.Done(ctx, "a", c1)).To(Succeed())
		Expect(adm.Done(ctx, "a", c1)).To(Succeed())
		Expect(run(r.bare, "for-each-ref", prefix)).To(BeEmpty())
	})
})

var _ = Describe("Resumes", func() {
	ctx := context.Background()
	It("Resume pushes the current main sha to the control ref of the current pause, read and deleted only at that sha", func() {
		r := newRemote()
		c := config.Defaults()
		c.Repository.URI, c.Repository.Main = r.bare, "main"
		store := git.NewStore(c)
		l, err := store.Acquire(ctx, "runner", time.Minute)
		Expect(err).NotTo(HaveOccurred())
		snap, err := store.Load(ctx)
		Expect(err).NotTo(HaveOccurred())
		snap.Paused, snap.PauseSeq = true, 3
		_, err = store.Save(ctx, l.Token, snap)
		Expect(err).NotTo(HaveOccurred())
		prefix := c.Admission.ControlPrefix + "resume-"
		res := &git.Resumes{Lander: r.lander(), Prefix: prefix}
		reqs, err := res.Pending(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(reqs).To(BeEmpty())
		Expect(git.Resume(ctx, c, r.work)).To(Succeed())
		main := r.main()
		Expect(run(r.bare, "rev-parse", prefix+"3")).To(Equal(main))
		Expect(git.Resume(ctx, c, r.work)).To(Succeed(), "asking twice is fine")
		run(r.bare, "update-ref", prefix+"x1", main)
		reqs, err = res.Pending(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(reqs).To(Equal([]core.ResumeRequest{{Seq: 3, SHA: main}}), "a ref not named by a number is no request")
		Expect(res.Done(ctx, core.ResumeRequest{Seq: 3, SHA: r.commit("other", r.base)})).NotTo(Succeed())
		Expect(run(r.bare, "rev-parse", prefix+"3")).To(Equal(main))
		Expect(res.Done(ctx, reqs[0])).To(Succeed())
		Expect(run(r.bare, "for-each-ref", c.Admission.ControlPrefix)).NotTo(ContainSubstring(prefix + "3"))
	})
})

var _ = Describe("Signed admits", func() {
	ctx := context.Background()

	// key makes an ed25519 key in dir and returns its path; the key is never read or printed here.
	key := func(dir, name string) string {
		f := filepath.Join(dir, name)
		out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", f).CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), string(out))
		return f
	}

	It("With operators configured, a change signed by an operator is admitted and any other is refused", func() {
		r, dir := newRemote(), GinkgoT().TempDir()
		op, other := key(dir, "op"), key(dir, "other")
		pub, err := os.ReadFile(op + ".pub")
		Expect(err).NotTo(HaveOccurred())
		operators := filepath.Join(dir, "operators")
		Expect(os.WriteFile(operators, append([]byte("t@example.com "), pub...), 0o600)).To(Succeed())
		sign := func(name, k string) string {
			sha := run(r.work, "-c", "gpg.format=ssh", "-c", "user.signingkey="+k, "commit-tree", "-S", run(r.work, "mktree"), "-p", r.base, "-m", name)
			run(r.work, "push", "-q", r.bare, sha+":refs/heads/"+name)
			return sha
		}
		c := r.config()
		c.Admission.Prefix = prefix
		good, stranger, unsigned := sign("good", op), sign("stranger", other), r.commit("unsigned", r.base)
		for id, sha := range map[string]string{"good": good, "stranger": stranger, "unsigned": unsigned} {
			Expect(git.Admit(ctx, c, r.work, id, sha)).To(Succeed())
		}
		why := map[string]string{}
		adm := &git.Admissions{Lander: r.lander(), Prefix: prefix, Operators: operators}
		ps, err := adm.Pending(ctx, nil)
		Expect(err).NotTo(HaveOccurred())
		for _, p := range ps {
			why[p.ID] = p.Why
		}
		Expect(why["good"]).To(BeEmpty())
		for _, id := range []string{"stranger", "unsigned"} {
			Expect(why[id]).To(ContainSubstring("not signed by an operator"), id)
			Expect(why[id]).NotTo(ContainSubstring("ssh-ed25519"), id)
		}

		By("with no operators configured, signing is not asked for")
		ps, err = (&git.Admissions{Lander: r.lander(), Prefix: prefix}).Pending(ctx, nil)
		Expect(err).NotTo(HaveOccurred())
		for _, p := range ps {
			Expect(p.Why).To(BeEmpty(), p.ID)
		}
	})
})

var _ = Describe("Promotes", func() {
	It("Promote pushes main's sha to the promote ref of the id, read and deleted only at that sha", func() {
		r := newRemote()
		c := config.Defaults()
		c.Repository.URI, c.Repository.Main = r.bare, "main"
		prefix := c.Admission.ControlPrefix + "promote/"
		pro := &git.Promotes{Lander: r.lander(), Prefix: prefix}
		Expect(git.Promote(context.Background(), c, r.work, "../x")).NotTo(Succeed())
		Expect(git.Promote(context.Background(), c, r.work, "fix-1")).To(Succeed())
		reqs, err := pro.Pending(context.Background())
		Expect(err).NotTo(HaveOccurred())
		Expect(reqs).To(Equal([]core.PromoteRequest{{ID: "fix-1", SHA: r.main()}}))
		Expect(pro.Done(context.Background(), core.PromoteRequest{ID: "fix-1", SHA: r.commit("other", r.base)})).NotTo(Succeed())
		Expect(pro.Done(context.Background(), reqs[0])).To(Succeed())
		Expect(run(r.bare, "for-each-ref", prefix)).To(BeEmpty())
	})
})
