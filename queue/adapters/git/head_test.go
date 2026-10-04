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
})
