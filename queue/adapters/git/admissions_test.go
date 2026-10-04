package git_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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
		Expect(ps).To(Equal([]core.Pending{{ID: "child", Commit: child, Owner: "t", BuildsOn: []string{"p"}}, {ID: "other", Commit: other, Owner: "t"}}))
		b := core.FormBatch(append(queued, core.Entry{ID: "child"}), map[string][]string{"child": ps[0].BuildsOn}, nil)
		Expect(ids(b.Entries())).To(Equal([]string{"p", "child"}))
		Expect(b.Ancestors("child")).To(Equal([]string{"p"}))

		By("a parent pending in the same drain comes first, whatever its ID")
		mid := r.commit("mid", p)
		top := r.commit("top", mid)
		admit("aa-top", top)
		admit("zz-mid", mid)
		Expect(pending()).To(Equal([]core.Pending{{ID: "child", Commit: child, Owner: "t"}, {ID: "other", Commit: other, Owner: "t"},
			{ID: "zz-mid", Commit: mid, Owner: "t"}, {ID: "aa-top", Commit: top, Owner: "t", BuildsOn: []string{"zz-mid"}, Ancestors: []string{mid}}}))
	})

	It("A new commit built on the queued one does not make the change build on itself", func() {
		a := r.commit("a", r.base)
		a2 := r.commit("a2", a)
		admit("a", a2)
		Expect(pending(core.Entry{ID: "a", Commit: a})).To(Equal([]core.Pending{{ID: "a", Commit: a2, Owner: "t"}}))
	})

	It("derives every queued ancestor of a merge, so a divergent merge can be refused", func() {
		a, b := r.commit("a", r.base), r.commit("b", r.base)
		m := run(r.work, "commit-tree", run(r.work, "mktree"), "-p", a, "-p", b, "-m", "m")
		admit("x", m)
		Expect(pending(core.Entry{ID: "a", Commit: a}, core.Entry{ID: "b", Commit: b})).To(Equal(
			[]core.Pending{{ID: "x", Commit: m, BuildsOn: []string{"a", "b"}, Owner: "t"}}))
	})

	It("A child of a pending replacement of a queued id builds on that id and comes after it", func() {
		a := r.commit("a", r.base)
		a2 := r.commit("a2", r.base) // not built on a
		cc := r.commit("c", a2)
		admit("c", cc)
		admit("a", a2)
		ps := pending(core.Entry{ID: "a", Commit: a})
		Expect(ps).To(Equal([]core.Pending{{ID: "a", Commit: a2, Owner: "t"}, {ID: "c", Commit: cc, Owner: "t", BuildsOn: []string{"a"}, Ancestors: []string{a2}}}))
	})

	It("A change lists every pending commit in its history, a refused one too, and comes after them", func() {
		dir := GinkgoT().TempDir()
		op := sshKey(dir, "op")
		pub, err := os.ReadFile(op + ".pub")
		Expect(err).NotTo(HaveOccurred())
		adm.Operators = filepath.Join(dir, "operators")
		Expect(os.WriteFile(adm.Operators, append([]byte("t@example.com "), pub...), 0o600)).To(Succeed())
		a1 := r.commit("a1", r.base) // unsigned
		b1 := run(r.work, "-c", "gpg.format=ssh", "-c", "user.signingkey="+op, "commit-tree", "-S", run(r.work, "mktree"), "-p", a1, "-m", "b1")
		admit("b", b1)
		admit("a", a1)
		ps := pending()
		Expect(ps).To(HaveLen(2))
		Expect(ps[0].ID).To(Equal("a"))
		Expect(ps[0].Why).To(ContainSubstring("is not signed by an operator"))
		Expect(ps[1].ID).To(Equal("b"))
		Expect(ps[1].Why).To(BeEmpty())
		Expect(ps[1].Ancestors).To(Equal([]string{a1}))
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

	key := sshKey

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

var _ = Describe("Signed promotes", func() {
	It("With operators configured, a promote signed by an operator is honoured and any other is refused", func() {
		ctx, dir := context.Background(), GinkgoT().TempDir()
		op, other := sshKey(dir, "op"), sshKey(dir, "other")
		pub, err := os.ReadFile(op + ".pub")
		Expect(err).NotTo(HaveOccurred())
		operators := filepath.Join(dir, "operators")
		Expect(os.WriteFile(operators, append([]byte("t@example.com "), pub...), 0o600)).To(Succeed())
		r := newRemote()
		c := config.Defaults()
		c.Repository.URI, c.Repository.Main, c.Admission.OperatorsFile = r.bare, "main", operators
		prefix := c.Admission.ControlPrefix + "promote/"
		for i, kv := range [][2]string{{"gpg.format", "ssh"}, {"user.signingkey", op}} {
			GinkgoT().Setenv("GIT_CONFIG_KEY_"+strconv.Itoa(i), kv[0])
			GinkgoT().Setenv("GIT_CONFIG_VALUE_"+strconv.Itoa(i), kv[1])
		}
		GinkgoT().Setenv("GIT_CONFIG_COUNT", "2")
		Expect(git.Promote(ctx, c, r.work, "signed")).To(Succeed())
		run(r.bare, "update-ref", prefix+"unsigned", r.main())
		strange := run(r.work, "-c", "gpg.format=ssh", "-c", "user.signingkey="+other, "commit-tree", "-S", run(r.work, "mktree"), "-p", r.base, "-m", "x")
		run(r.work, "push", "-q", r.bare, strange+":"+prefix+"stranger")
		reqs, err := (&git.Promotes{Lander: r.lander(), Prefix: prefix, Operators: operators}).Pending(ctx)
		Expect(err).NotTo(HaveOccurred())
		why := map[string]string{}
		for _, q := range reqs {
			why[q.ID] = q.Why
		}
		Expect(why).To(HaveLen(3))
		Expect(why["signed"]).To(BeEmpty())
		Expect(why["unsigned"]).To(Equal(fmt.Sprintf("promote request %.7s is not signed by an operator", r.main())))
		Expect(why["stranger"]).To(Equal(fmt.Sprintf("promote request %.7s is not signed by an operator", strange)))
	})
})

var _ = Describe("Signed withdraw and resolve", func() {
	It("With operators configured, a request signed by an operator is honoured and any other is refused", func() {
		ctx, dir := context.Background(), GinkgoT().TempDir()
		op, other := sshKey(dir, "op"), sshKey(dir, "other")
		pub, err := os.ReadFile(op + ".pub")
		Expect(err).NotTo(HaveOccurred())
		operators := filepath.Join(dir, "operators")
		Expect(os.WriteFile(operators, append([]byte("t@example.com "), pub...), 0o600)).To(Succeed())
		r := newRemote()
		c := config.Defaults()
		c.Repository.URI, c.Repository.Main, c.Admission.OperatorsFile = r.bare, "main", operators
		for i, kv := range [][2]string{{"gpg.format", "ssh"}, {"user.signingkey", op}} {
			GinkgoT().Setenv("GIT_CONFIG_KEY_"+strconv.Itoa(i), kv[0])
			GinkgoT().Setenv("GIT_CONFIG_VALUE_"+strconv.Itoa(i), kv[1])
		}
		GinkgoT().Setenv("GIT_CONFIG_COUNT", "2")
		Expect(git.Request(ctx, c, r.work, "withdraw-", "signed", "c1")).To(Succeed())
		Expect(run(r.bare, "log", "-1", "--format=%s", c.Admission.ControlPrefix+"withdraw-signed.c1")).To(Equal("queue withdraw signed c1"))
		Expect(git.Request(ctx, c, r.work, "withdraw-", "other", "c4")).To(Succeed())
		run(r.bare, "update-ref", c.Admission.ControlPrefix+"withdraw-renamed.c4", run(r.bare, "rev-parse", c.Admission.ControlPrefix+"withdraw-other.c4"))
		run(r.bare, "update-ref", "-d", c.Admission.ControlPrefix+"withdraw-other.c4")
		Expect(git.Promote(ctx, c, r.work, "p1")).To(Succeed())
		promote := run(r.bare, "rev-parse", c.Admission.ControlPrefix+"promote/p1")
		run(r.bare, "update-ref", c.Admission.ControlPrefix+"withdraw-replayed.c5", promote)
		run(r.bare, "update-ref", c.Admission.ControlPrefix+"resolve-unsigned.c2", r.main())
		strange := run(r.work, "-c", "gpg.format=ssh", "-c", "user.signingkey="+other, "commit-tree", "-S", run(r.work, "mktree"), "-p", r.base, "-m", "x")
		run(r.work, "push", "-q", r.bare, strange+":"+c.Admission.ControlPrefix+"withdraw-stranger.c3")
		reqs, err := (&git.Lifecycle{Lander: r.lander(), Prefix: c.Admission.ControlPrefix, Operators: operators}).Pending(ctx)
		Expect(err).NotTo(HaveOccurred())
		why := map[string]string{}
		for _, q := range reqs {
			why[q.ID] = q.Why
		}
		renamed := run(r.bare, "rev-parse", c.Admission.ControlPrefix+"withdraw-renamed.c4")
		Expect(why).To(Equal(map[string]string{"signed": "",
			"unsigned": fmt.Sprintf("resolve request %.7s is not signed by an operator", r.main()),
			"stranger": fmt.Sprintf("withdraw request %.7s is not signed by an operator", strange),
			"renamed":  fmt.Sprintf("withdraw request %.7s is not signed for queue withdraw renamed c4", renamed),
			"replayed": fmt.Sprintf("withdraw request %.7s is not signed for queue withdraw replayed c5", promote)}))
	})
})

var _ = Describe("Admission owner", func() {
	authored := func(name string) string {
		ctx, r := context.Background(), newRemote()
		c := r.config()
		c.Admission.Prefix = prefix
		cmd := exec.Command("git", "-C", r.work, "commit-tree", run(r.work, "mktree"), "-m", "x", "-p", r.base)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME="+name, "GIT_AUTHOR_EMAIL=a@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		out, err := cmd.Output()
		Expect(err).NotTo(HaveOccurred())
		sha := strings.TrimSpace(string(out))
		Expect(git.Admit(ctx, c, r.work, "x", sha)).To(Succeed())
		ps, err := (&git.Admissions{Lander: r.lander(), Prefix: prefix}).Pending(ctx, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(ps).To(HaveLen(1))
		return ps[0].Owner
	}
	It("A change is owned by the author of its commit", func() {
		Expect(authored("alice")).To(Equal("alice"))
	})
	DescribeTable("an author name that looks like an address is not kept",
		func(name, want string) { Expect(authored(name)).To(Equal(want)) },
		Entry("a plain name is kept", "Alice Smith", "Alice Smith"),
		Entry("an email-like name", "alice@example.test", "unknown"),
		Entry("userinfo with a comma in the password", "https://alice:p,ass@example.test", "unknown"),
		Entry("a url without userinfo", "https://example.test/x", "unknown"),
		Entry("a path", "a/b", "unknown"),
		Entry("a backslash", `a\b`, "unknown"),
		Entry("a colon then text", "user:secret", "unknown"),
		Entry("a colon then space is kept", "Team: Alice", "Team: Alice"),
	)
})

// sshKey makes an ed25519 key in dir and returns its path; the key is never read or printed here.
func sshKey(dir, name string) string {
	f := filepath.Join(dir, name)
	out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", f).CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), string(out))
	return f
}

var _ = Describe("Signed resumes", func() {
	ctx := context.Background()
	setup := func(operators string) (*remote, config.Config, string) {
		r := newRemote()
		c := config.Defaults()
		c.Repository.URI, c.Repository.Main, c.Admission.OperatorsFile = r.bare, "main", operators
		store := git.NewStore(c)
		l, err := store.Acquire(ctx, "runner", time.Minute)
		Expect(err).NotTo(HaveOccurred())
		snap, err := store.Load(ctx)
		Expect(err).NotTo(HaveOccurred())
		snap.Paused, snap.PauseSeq = true, 3
		_, err = store.Save(ctx, l.Token, snap)
		Expect(err).NotTo(HaveOccurred())
		return r, c, c.Admission.ControlPrefix + "resume-"
	}
	signWith := func(k string) {
		for i, kv := range [][2]string{{"gpg.format", "ssh"}, {"user.signingkey", k}} {
			n := strconv.Itoa(i)
			GinkgoT().Setenv("GIT_CONFIG_KEY_"+n, kv[0])
			GinkgoT().Setenv("GIT_CONFIG_VALUE_"+n, kv[1])
		}
		GinkgoT().Setenv("GIT_CONFIG_COUNT", "2")
	}

	It("With operators configured, a resume signed by an operator is honoured and any other is refused", func() {
		dir := GinkgoT().TempDir()
		op, other := sshKey(dir, "op"), sshKey(dir, "other")
		pub, err := os.ReadFile(op + ".pub")
		Expect(err).NotTo(HaveOccurred())
		operators := filepath.Join(dir, "operators")
		Expect(os.WriteFile(operators, append([]byte("t@example.com "), pub...), 0o600)).To(Succeed())
		r, c, prefix := setup(operators)
		res := &git.Resumes{Lander: r.lander(), Prefix: prefix, Operators: operators}
		why := func() map[uint64]string {
			reqs, err := res.Pending(ctx)
			Expect(err).NotTo(HaveOccurred())
			out := map[uint64]string{}
			for _, q := range reqs {
				out[q.Seq] = q.Why
			}
			return out
		}

		signWith(op)
		Expect(git.Resume(ctx, c, r.work)).To(Succeed())
		Expect(why()).To(Equal(map[uint64]string{3: ""}), "signed by an operator")
		Expect(run(r.bare, "log", "-1", "--format=%s", prefix+"3")).To(Equal("queue resume 3"))
		old := run(r.bare, "rev-parse", prefix+"3")
		run(r.bare, "update-ref", prefix+"6", old) // replayed for a later pause
		Expect(why()[6]).To(Equal(fmt.Sprintf("resume request %.7s is not signed for queue resume 6", old)))
		run(r.bare, "update-ref", "-d", prefix+"6")

		run(r.bare, "update-ref", prefix+"4", r.main())
		strange := run(r.work, "-c", "gpg.format=ssh", "-c", "user.signingkey="+other, "commit-tree", "-S", run(r.work, "mktree"), "-p", r.base, "-m", "x")
		run(r.work, "push", "-q", r.bare, strange+":"+prefix+"5")
		got := why()
		Expect(got[3]).To(BeEmpty())
		Expect(got[4]).To(Equal(fmt.Sprintf("resume request %.7s is not signed by an operator", r.main())), "unsigned")
		Expect(got[5]).To(Equal(fmt.Sprintf("resume request %.7s is not signed by an operator", strange)), "unknown signer")
		for _, w := range got {
			Expect(w).NotTo(ContainSubstring("ssh-ed25519"))
		}
	})

	It("With no operators configured, a resume request is honoured unsigned", func() {
		r, c, prefix := setup("")
		Expect(git.Resume(ctx, c, r.work)).To(Succeed())
		Expect(run(r.bare, "rev-parse", prefix+"3")).To(Equal(r.main()), "pushed as is, not re-signed")
		reqs, err := (&git.Resumes{Lander: r.lander(), Prefix: prefix}).Pending(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(reqs).To(Equal([]core.ResumeRequest{{Seq: 3, SHA: r.main()}}))
	})
})
