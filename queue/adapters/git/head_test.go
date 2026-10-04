package git_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/queue/adapters/git"
	"github.com/concourse/concourse/queue/config"
	"github.com/concourse/concourse/queue/core"
)

var _ core.Heads = (*git.Lander)(nil)

var _ = Describe("Main's head", func() {
	ctx := context.Background()

	It("The lander reads where main points now", func() {
		r := newRemote()
		l := r.lander()
		Expect(l.Head(ctx, "main")).To(Equal(r.base))
		c1 := r.commit("c1", r.base)
		r.setMain(c1)
		Expect(l.Head(ctx, "refs/heads/main")).To(Equal(c1))
		_, err := l.Head(ctx, "absent")
		Expect(err).To(MatchError(ContainSubstring("refs/heads/absent")))
	})

	It("A batch composes on main's sha, not on wherever the branch points later", func() {
		dir := GinkgoT().TempDir()
		remote, work := filepath.Join(dir, "remote.git"), filepath.Join(dir, "work")
		composeRun(dir, "init", "-q", "--bare", remote)
		composeRun(dir, "clone", "-q", remote, work)
		write := func(name string) string {
			Expect(os.WriteFile(filepath.Join(work, name), []byte(name+"\n"), 0o644)).To(Succeed())
			composeRun(work, "add", name)
			composeRun(work, "commit", "-q", "-m", name)
			return composeRun(work, "rev-parse", "HEAD")
		}
		tested := write("first")
		composeRun(work, "push", "-q", "origin", "HEAD:main")
		composeRun(work, "checkout", "-q", "-b", "a")
		a := core.Entry{ID: "a", Commit: write("a.txt"), Ref: "a"}
		composeRun(work, "push", "-q", "origin", "a")
		composeRun(work, "checkout", "-q", "--detach", tested)
		composeRun(work, "push", "-q", "origin", write("moved")+":main")
		c, err := config.Parse([]byte("apiVersion: jetbridge.dev/queue/v2\nrepository: {uri: " + remote + ", main: main}\n"))
		Expect(err).NotTo(HaveOccurred())
		sha, err := git.NewComposer(c).Compose(ctx, tested, []core.Entry{a})
		Expect(err).NotTo(HaveOccurred())
		Expect(composeRun(remote, "rev-parse", sha+"~1")).To(Equal(tested))
	})

	It("A push lost to a main that moved after the check is named as main having moved", func() {
		r := newRemote()
		l, c1, other := r.lander(), r.commit("c1", r.base), r.commit("other", r.base)
		git.SetBeforePush(l, func() { r.setMain(other) })
		err := l.Land(ctx, "main", c1, 5)
		var moved *core.MainMovedError
		Expect(errors.As(err, &moved)).To(BeTrue(), "the driver recomposes, not counting it toward a pause: %v", err)
		Expect(moved.Head).To(Equal(other))
		Expect(r.main()).To(Equal(other))
	})

	It("A push refused while main stayed put is an error, not a main that moved", func() {
		r := newRemote()
		l, c1, x := r.lander(), r.commit("c1", r.base), r.commit("x", r.base)
		git.SetBeforePush(l, func() { run(r.bare, "update-ref", lease, x) }) // the lease moved; main did not
		err := l.Land(ctx, "main", c1, 5)
		Expect(err).To(HaveOccurred())
		var moved *core.MainMovedError
		Expect(errors.As(err, &moved)).To(BeFalse())
	})
})
