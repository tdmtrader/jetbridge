package commands

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/concourse/concourse/atc"
	"github.com/jessevdk/go-flags"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// recordingLandingClient records what the commands asked of the team.
type recordingLandingClient struct {
	configs     [][]byte
	submissions []atc.LandingSubmission
	created     bool
	status      atc.LandingQueueStatus
	found       bool
	err         error
}

func (c *recordingLandingClient) SetLandingQueue(_ string, config []byte) (bool, error) {
	c.configs = append(c.configs, config)
	return c.created, c.err
}

func (c *recordingLandingClient) SubmitLanding(_ string, submission atc.LandingSubmission) (bool, error) {
	c.submissions = append(c.submissions, submission)
	return c.created, c.err
}

func (c *recordingLandingClient) LandingQueue(string) (atc.LandingQueueStatus, bool, error) {
	return c.status, c.found, c.err
}

func git(dir string, args ...string) string {
	GinkgoHelper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.test", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.test")
	out, err := cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), string(out))
	return strings.TrimSpace(string(out))
}

var _ = Describe("The landing queue commands", func() {
	It("register set-landing-queue, land and landing-queue", func() {
		for _, name := range []string{"set-landing-queue", "land", "landing-queue"} {
			parser := flags.NewParser(&FlyCommand{}, flags.HelpFlag)
			_, err := parser.ParseArgs([]string{name, "--help"})
			Expect(err).To(MatchError(ContainSubstring("[" + name + " command options]")))
		}
	})

	It("set-landing-queue refuses a config with an unknown key before sending it, and reports created or updated", func() {
		dir := GinkgoT().TempDir()
		path := filepath.Join(dir, "queue.yml")
		Expect(os.WriteFile(path, []byte("repository: r\ntrunk: core\ncompose: c\nland: l\nbranch: x\n"), 0o644)).To(Succeed())
		client := &recordingLandingClient{created: true}
		command := &SetLandingQueueCommand{Queue: "trunk", Config: atc.PathFlag(path)}
		err := command.run(client, nil, &bytes.Buffer{})
		Expect(err).To(MatchError(ContainSubstring("branch")))
		Expect(client.configs).To(BeEmpty())

		Expect(os.WriteFile(path, []byte("repository: r\ntrunk: core\ncompose: c\nland: l\n"), 0o644)).To(Succeed())
		out := &bytes.Buffer{}
		Expect(command.run(client, nil, out)).To(Succeed())
		Expect(client.configs).To(HaveLen(1))
		Expect(out.String()).To(ContainSubstring(`landing queue "trunk" created`))
	})

	It("land pushes the commit to the queue's submit ref with the user's git, then submits the entry", func() {
		root := GinkgoT().TempDir()
		remote := filepath.Join(root, "remote.git")
		work := filepath.Join(root, "work")
		git(root, "init", "-q", "--bare", "-b", "core", remote)
		git(root, "clone", "-q", remote, work)
		Expect(os.WriteFile(filepath.Join(work, "a.txt"), []byte("a\n"), 0o644)).To(Succeed())
		git(work, "add", "a.txt")
		git(work, "commit", "-q", "-m", "a")
		sha := git(work, "rev-parse", "HEAD")

		client := &recordingLandingClient{created: true}
		command := &LandCommand{Queue: "trunk", Repo: work, Remote: "origin"}
		out := &bytes.Buffer{}
		Expect(command.run(client, out)).To(Succeed())
		Expect(client.submissions).To(Equal([]atc.LandingSubmission{{ID: sha[:12], Commit: sha}}))
		Expect(git(remote, "rev-parse", "refs/queue/submit/"+sha[:12])).To(Equal(sha))
		Expect(out.String()).To(ContainSubstring("submitted " + sha))

		named := &LandCommand{Queue: "trunk", Repo: work, Remote: "origin", ID: "fix-1"}
		named.Args.Commit = "HEAD"
		Expect(named.run(client, out)).To(Succeed())
		Expect(git(remote, "rev-parse", "refs/queue/submit/fix-1")).To(Equal(sha))

		bad := &LandCommand{Queue: "trunk", Repo: work, ID: "a/b"}
		Expect(bad.run(client, out)).To(MatchError(ContainSubstring("safe ref component")))
		Expect(client.submissions).To(HaveLen(2))
	})

	It("landing-queue prints the entries with their Runs and the failed landings", func() {
		client := &recordingLandingClient{found: true, status: atc.LandingQueueStatus{
			Name: "trunk", Config: atc.LandingQueueConfig{Repository: "r", Trunk: "core"},
			Entries: []atc.LandingEntry{
				{ID: "fix-1", Commit: strings.Repeat("a", 40), State: atc.LandingEntryLanded, ComposeRun: 3, LandRun: 2, SettleReason: "landed by Run 2"},
				{ID: "fix-2", Commit: strings.Repeat("b", 40), State: atc.LandingEntryQueued},
			},
			FailedLands: 2, LastError: "push refused",
		}}
		out := &bytes.Buffer{}
		Expect((&LandingQueueCommand{Queue: "trunk"}).run(client, out)).To(Succeed())
		Expect(out.String()).To(ContainSubstring("failed landings in a row: 2 (last: push refused)"))
		Expect(out.String()).To(MatchRegexp(`fix-1\s+aaaaaaaaaaaa\s+landed\s+#3\s+#2\s+landed by Run 2`))
		Expect(out.String()).To(MatchRegexp(`fix-2\s+bbbbbbbbbbbb\s+queued\s+-\s+-`))

		missing := &recordingLandingClient{}
		Expect((&LandingQueueCommand{Queue: "trunk"}).run(missing, out)).To(MatchError(`landing queue "trunk" not found`))
	})
})
