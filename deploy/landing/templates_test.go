package landing_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/configvalidate"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/yaml"
)

func TestLandingTemplates(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Landing Templates Suite")
}

// The step seam: the two templates' inline scripts, exactly as the YAML
// carries them, run against real bare repositories with the git this machine
// has. Nothing here needs a cluster, coreutils, or a credential: the
// repository is a path, so the token branch of the land script is skipped.

type script struct {
	body string
	args []string
}

// taskScript extracts a task's inline script and the arguments after it.
func taskScript(templatePath, task string) (script, atc.Config) {
	GinkgoHelper()
	body, err := os.ReadFile(templatePath)
	Expect(err).NotTo(HaveOccurred())
	var config atc.Config
	Expect(yaml.UnmarshalStrict(body, &config)).To(Succeed())
	for _, job := range config.Jobs {
		for _, step := range job.PlanSequence {
			t, ok := step.Config.(*atc.TaskStep)
			if !ok || t.Name != task {
				continue
			}
			args := t.Config.Run.Args
			for i, arg := range args {
				if arg == "-c" && i+1 < len(args) {
					return script{body: args[i+1], args: args[i+2:]}, config
				}
			}
		}
	}
	Fail("no task " + task + " in " + templatePath)
	return script{}, config
}

func git(dir string, args ...string) string {
	GinkgoHelper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = gitEnv("t", "t@example.test")
	out, err := cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), string(out))
	return strings.TrimSpace(string(out))
}

// commitAs commits the given files in dir as one author at one date, and
// returns the new commit's sha.
func commitAs(dir, name, email, date, message string, files map[string]string) string {
	GinkgoHelper()
	for path, body := range files {
		Expect(os.WriteFile(filepath.Join(dir, path), []byte(body), 0o644)).To(Succeed())
		git(dir, "add", path)
	}
	cmd := exec.Command("git", "-C", dir, "commit", "-q", "--allow-empty", "-m", message)
	cmd.Env = append(gitEnv(name, email), "GIT_AUTHOR_DATE="+date)
	out, err := cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), string(out))
	return git(dir, "rev-parse", "HEAD")
}

func gitEnv(name, email string) []string {
	return append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME="+name, "GIT_AUTHOR_EMAIL="+email, "GIT_COMMITTER_NAME="+name, "GIT_COMMITTER_EMAIL="+email,
		"GIT_AUTHOR_DATE=2026-10-06T12:00:00+0000", "GIT_COMMITTER_DATE=2026-10-06T12:00:00+0000")
}

// run executes a task script in a fresh working directory with the output
// directories the task declares, and returns its exit code and combined output.
func run(s script, outputs []string, params ...string) (int, string, string) {
	return runWith(s, "", outputs, params...)
}

// runWith also copies the compose run's composition output in as the land
// task's input, the way the platform binds it.
func runWith(s script, inputsFrom string, outputs []string, params ...string) (int, string, string) {
	GinkgoHelper()
	wd := GinkgoT().TempDir()
	for _, out := range outputs {
		Expect(os.MkdirAll(filepath.Join(wd, out), 0o755)).To(Succeed())
	}
	if inputsFrom != "" {
		cp := exec.Command("cp", "-R", filepath.Join(inputsFrom, "composition"), filepath.Join(wd, "composition"))
		Expect(cp.Run()).To(Succeed())
	}
	args := append([]string{"-eu", "-c", s.body, s.args[0]}, params...)
	cmd := exec.Command("/bin/sh", args...)
	cmd.Dir = wd
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	code := 0
	var exit *exec.ExitError
	if err != nil {
		Expect(err).To(BeAssignableToTypeOf(exit), string(out))
		code = err.(*exec.ExitError).ExitCode()
	}
	return code, string(out), wd
}

var _ = Describe("The landing templates", func() {
	var remote, work, sha string
	compose, composeConfig := taskScript("../landing-compose-template.yml", "compose")
	land, landConfig := taskScript("../landing-land-template.yml", "land")

	BeforeEach(func() {
		root := GinkgoT().TempDir()
		remote = filepath.Join(root, "remote.git")
		work = filepath.Join(root, "work")
		git(root, "init", "-q", "--bare", "-b", "core", remote)
		git(root, "clone", "-q", remote, work)
		Expect(os.WriteFile(filepath.Join(work, "README"), []byte("trunk\n"), 0o644)).To(Succeed())
		git(work, "add", "README")
		git(work, "commit", "-q", "-m", "trunk")
		git(work, "push", "-q", "origin", "HEAD:core")
		git(work, "checkout", "-q", "-b", "fix")
		Expect(os.WriteFile(filepath.Join(work, "fix.txt"), []byte("fix\n"), 0o644)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(work, "tool.sh"), []byte("#!/bin/sh\n"), 0o755)).To(Succeed())
		git(work, "add", "fix.txt", "tool.sh")
		cmd := exec.Command("git", "-C", work, "commit", "-q", "-m", "fix: the thing\n\nA body line.\n")
		cmd.Env = gitEnv("Ada", "ada@example.test")
		Expect(cmd.Run()).To(Succeed())
		sha = git(work, "rev-parse", "HEAD")
		git(work, "push", "-q", "origin", sha+":refs/queue/submit/fix-1")
	})

	It("The templates are valid Run templates with canonical task ids and one result per task", func() {
		for name, config := range map[string]atc.Config{"landing-compose": composeConfig, "landing-land": landConfig} {
			Expect(config.Template).To(BeTrue())
			_, errs := configvalidate.Validate(config)
			Expect(errs).To(BeEmpty(), name)
			Expect(configvalidate.ValidateTemplateDeclaration(atc.PipelineRef{Name: name}, config)).To(Succeed(), "set-pipeline refuses %s", name)
			for _, job := range config.Jobs {
				for _, step := range job.PlanSequence {
					task := step.Config.(*atc.TaskStep)
					Expect(uuid.Validate(task.TaskID)).To(Succeed(), task.Name)
					Expect(task.RunResult).NotTo(BeNil(), task.Name)
				}
			}
		}
	})

	It("compose rebuilds the entry on the trunk's head with its author, subject, body and an original: trailer, reproducibly", func() {
		code, out, wd := run(compose, []string{"composition"}, remote, "core", "fix-1="+sha)
		Expect(code).To(Equal(0), out)
		manifest := readManifest(wd)
		Expect(manifest["base"]).To(Equal(git(remote, "rev-parse", "core")))
		head := manifest["head"]
		Expect(head).To(HaveLen(40))
		Expect(git(remote, "rev-parse", "core")).NotTo(Equal(head), "compose pushes nothing")
		Expect(filepath.Join(wd, "composition", "candidate", "fix.txt")).To(BeAnExistingFile())
		info, err := os.Stat(filepath.Join(wd, "composition", "candidate", "tool.sh"))
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Mode()&0o100).NotTo(BeZero(), "the exec bit survives git archive")

		code, _, again := run(compose, []string{"composition"}, remote, "core", "fix-1="+sha)
		Expect(code).To(Equal(0))
		Expect(readManifest(again)["head"]).To(Equal(head), "the same input composes the same sha")

		// The rebuilt commit's facts, read from the work repo compose left behind.
		repo := filepath.Join(wd, "work")
		Expect(git(repo, "log", "-1", "--format=%an <%ae>", head)).To(Equal("Ada <ada@example.test>"))
		Expect(git(repo, "log", "-1", "--format=%cn <%ce>", head)).To(Equal("landing-queue <landing-queue@jetbridge>"))
		Expect(git(repo, "log", "-1", "--format=%s", head)).To(Equal("fix: the thing"))
		Expect(git(repo, "log", "-1", "--format=%B", head)).To(ContainSubstring("A body line."))
		Expect(git(repo, "log", "-1", "--format=%B", head)).To(ContainSubstring("original: " + sha))
		Expect(git(repo, "rev-parse", head+"^{tree}")).To(Equal(git(work, "rev-parse", sha+"^{tree}")))
		Expect(git(repo, "rev-list", manifest["base"]+".."+head)).To(Equal(head), "a one-commit entry lands as one commit")
	})

	It("The rebuild recipe is byte-identical in both templates, so land's sha equality can hold", func() {
		recipe := func(body string) string {
			start := strings.Index(body, "# recipe-begin")
			end := strings.Index(body, "# recipe-end")
			Expect(start).To(BeNumerically(">", 0))
			Expect(end).To(BeNumerically(">", start))
			return body[start:end]
		}
		Expect(recipe(land.body)).To(Equal(recipe(compose.body)))
	})

	It("compose records each commit's sha, author, email, date and message in replay order in the manifest, which land rebuilds from", func() {
		code, out, wd := run(compose, []string{"composition"}, remote, "core", "fix-1="+sha)
		Expect(code).To(Equal(0), out)
		facts := filepath.Join(wd, "composition", "manifest", "entries", "fix-1")
		Expect(readFile(filepath.Join(facts, "order"))).To(Equal("0001\n"))
		commit := filepath.Join(facts, "commits", "0001")
		Expect(readFile(filepath.Join(commit, "sha"))).To(Equal(sha + "\n"))
		Expect(readFile(filepath.Join(commit, "author"))).To(Equal("Ada\n"))
		Expect(readFile(filepath.Join(commit, "email"))).To(Equal("ada@example.test\n"))
		Expect(readFile(filepath.Join(commit, "date"))).To(Equal("Tue, 6 Oct 2026 12:00:00 +0000\n"))
		body := readFile(filepath.Join(commit, "message"))
		Expect(body).To(HavePrefix("fix: the thing\n"))
		Expect(body).To(HaveSuffix("original: " + sha + "\n"))
		Expect(readManifest(wd)).NotTo(HaveKey("trunk"), "the trunk comes from the land Run's params, never from the manifest")
	})

	Describe("an entry of several commits", func() {
		var base string
		var series []string

		// Three commits by three authors at three dates, branched from the trunk.
		BeforeEach(func() {
			base = git(remote, "rev-parse", "core")
			git(work, "checkout", "-q", "-b", "series", base)
			series = []string{
				commitAs(work, "Ada", "ada@example.test", "2026-10-01T09:00:00+0200", "feat: one\n\nWhy one.\n", map[string]string{"one.txt": "1\n"}),
				commitAs(work, "Grace", "grace@example.test", "2026-10-02T10:30:00-0500", "feat: two", map[string]string{"two.txt": "2\n"}),
				commitAs(work, "Linus", "linus@example.test", "2026-10-03T11:45:00+0000", "feat: three\n\nWhy three.\n", map[string]string{"one.txt": "1\n3\n"}),
			}
			git(work, "push", "-q", "origin", series[2]+":refs/queue/submit/series-1")
		})

		It("lands as three commits, each with its own author, dates and message and an original: trailer", func() {
			code, out, wd := run(compose, []string{"composition"}, remote, "core", "series-1="+series[2])
			Expect(code).To(Equal(0), out)
			head := readManifest(wd)["head"]
			repo := filepath.Join(wd, "work")
			rebuilt := strings.Fields(git(repo, "rev-list", "--reverse", base+".."+head))
			Expect(rebuilt).To(HaveLen(3), "the queue never squashes")
			for i, c := range rebuilt {
				original := series[i]
				Expect(git(repo, "log", "-1", "--format=%an <%ae> %aD", c)).To(Equal(git(work, "log", "-1", "--format=%an <%ae> %aD", original)))
				Expect(git(repo, "log", "-1", "--format=%cn <%ce> %cD", c)).To(Equal("landing-queue <landing-queue@jetbridge> " + git(work, "log", "-1", "--format=%aD", original)))
				Expect(git(repo, "log", "-1", "--format=%B", c)).To(Equal(git(work, "log", "-1", "--format=%B", original) + "\n\noriginal: " + original))
				Expect(git(repo, "rev-parse", c+"^{tree}")).To(Equal(git(work, "rev-parse", original+"^{tree}")))
			}
			facts := filepath.Join(wd, "composition", "manifest", "entries", "series-1")
			Expect(readFile(filepath.Join(facts, "order"))).To(Equal("0001\n0002\n0003\n"))
			for i, n := range []string{"0001", "0002", "0003"} {
				Expect(readFile(filepath.Join(facts, "commits", n, "sha"))).To(Equal(series[i] + "\n"))
			}

			code, _, again := run(compose, []string{"composition"}, remote, "core", "series-1="+series[2])
			Expect(code).To(Equal(0))
			Expect(readManifest(again)["head"]).To(Equal(head), "the same input composes the same shas")
		})

		It("land rebuilds and pushes exactly compose's commits", func() {
			code, out, composed := run(compose, []string{"composition"}, remote, "core", "series-1="+series[2])
			Expect(code).To(Equal(0), out)
			head := readManifest(composed)["head"]
			composedCommits := git(filepath.Join(composed, "work"), "rev-list", "--reverse", base+".."+head)
			code, out, landed := runWith(land, composed, []string{"verdict"}, remote, "core")
			Expect(code).To(Equal(0), out)
			Expect(readVerdict(landed)["outcome"]).To(Equal("landed"))
			Expect(readVerdict(landed)["sha"]).To(Equal(head))
			Expect(git(remote, "rev-list", "--reverse", base+"..core")).To(Equal(composedCommits))
			Expect(git(remote, "log", "--format=%an", base+"..core")).To(Equal("Linus\nGrace\nAda"))
		})

		It("two entries in a batch keep their order and their commits", func() {
			git(work, "checkout", "-q", "-b", "other", base)
			other := commitAs(work, "Barbara", "barbara@example.test", "2026-10-04T08:00:00+0000", "fix: other", map[string]string{"other.txt": "o\n"})
			git(work, "push", "-q", "origin", other+":refs/queue/submit/other-1")
			code, out, composed := run(compose, []string{"composition"}, remote, "core", "series-1="+series[2]+",other-1="+other)
			Expect(code).To(Equal(0), out)
			head := readManifest(composed)["head"]
			rebuilt := strings.Fields(git(filepath.Join(composed, "work"), "rev-list", "--reverse", base+".."+head))
			Expect(rebuilt).To(HaveLen(4))
			for i, original := range append(append([]string{}, series...), other) {
				Expect(git(filepath.Join(composed, "work"), "log", "-1", "--format=%B", rebuilt[i])).To(HaveSuffix("original: " + original))
			}
			code, out, landed := runWith(land, composed, []string{"verdict"}, remote, "core")
			Expect(code).To(Equal(0), out)
			Expect(readVerdict(landed)["outcome"]).To(Equal("landed"))
			Expect(strings.Fields(git(remote, "rev-list", "--reverse", base+"..core"))).To(Equal(rebuilt))
		})

		It("an entry stacked on an earlier entry of the batch replays only its own commits", func() {
			stacked := commitAs(work, "Barbara", "barbara@example.test", "2026-10-04T08:00:00+0000", "feat: four", map[string]string{"four.txt": "4\n"})
			git(work, "push", "-q", "origin", stacked+":refs/queue/submit/stacked-1")
			code, out, composed := run(compose, []string{"composition"}, remote, "core", "series-1="+series[2]+",stacked-1="+stacked)
			Expect(code).To(Equal(0), out)
			Expect(readFile(filepath.Join(composed, "composition", "manifest", "entries", "stacked-1", "order"))).To(Equal("0001\n"))
			head := readManifest(composed)["head"]
			Expect(strings.Fields(git(filepath.Join(composed, "work"), "rev-list", base+".."+head))).To(HaveLen(4))
			code, out, landed := runWith(land, composed, []string{"verdict"}, remote, "core")
			Expect(code).To(Equal(0), out)
			Expect(readVerdict(landed)["sha"]).To(Equal(head))
		})

		It("compose refuses the entry when a middle commit conflicts with the trunk, naming that commit", func() {
			git(work, "checkout", "-q", "core")
			commitAs(work, "t", "t@example.test", "2026-10-05T00:00:00+0000", "trunk takes two.txt", map[string]string{"two.txt": "trunk\n"})
			git(work, "push", "-q", "origin", "HEAD:core")
			code, out, _ := run(compose, []string{"composition"}, remote, "core", "series-1="+series[2])
			Expect(code).To(Equal(1))
			Expect(out).To(ContainSubstring("rebuild: series-1 commit " + series[1] + " conflicts with the trunk"))
		})

		It("land refuses a manifest whose commits are not the entry's commits", func() {
			code, out, composed := run(compose, []string{"composition"}, remote, "core", "series-1="+series[2])
			Expect(code).To(Equal(0), out)
			order := filepath.Join(composed, "composition", "manifest", "entries", "series-1", "order")
			Expect(os.WriteFile(order, []byte("0001\n0003\n"), 0o644)).To(Succeed())
			code, out, landed := runWith(land, composed, []string{"verdict"}, remote, "core")
			Expect(code).To(Equal(1), out)
			Expect(readVerdict(landed)["outcome"]).To(Equal("failed"))
			Expect(readVerdict(landed)["reason"]).To(ContainSubstring("are not the entry's commits"))
			Expect(git(remote, "rev-parse", "core")).To(Equal(base))
		})
	})

	It("compose refuses an entry that contains a merge commit", func() {
		base := git(remote, "rev-parse", "core")
		git(work, "checkout", "-q", "-b", "side", base)
		commitAs(work, "Ada", "ada@example.test", "2026-10-01T09:00:00+0000", "side", map[string]string{"side.txt": "s\n"})
		git(work, "checkout", "-q", "-b", "merged", base)
		commitAs(work, "Ada", "ada@example.test", "2026-10-01T09:00:00+0000", "main line", map[string]string{"main.txt": "m\n"})
		git(work, "merge", "-q", "--no-ff", "--no-edit", "side")
		merge := git(work, "rev-parse", "HEAD")
		git(work, "push", "-q", "origin", merge+":refs/queue/submit/merged-1")
		code, out, _ := run(compose, []string{"composition"}, remote, "core", "merged-1="+merge)
		Expect(code).To(Equal(1))
		Expect(out).To(ContainSubstring("compose: entry merged-1 contains merge commit " + merge + "; rebase it onto the trunk before submitting"))
	})

	It("compose refuses an entry with no commits off the trunk", func() {
		base := git(remote, "rev-parse", "core")
		git(work, "push", "-q", "origin", base+":refs/queue/submit/empty-1")
		code, out, _ := run(compose, []string{"composition"}, remote, "core", "empty-1="+base)
		Expect(code).To(Equal(1))
		Expect(out).To(ContainSubstring("compose: empty-1 is already on the trunk"))
	})

	It("land sends back to compose a candidate composed without per-commit facts, pushing nothing", func() {
		code, out, composed := run(compose, []string{"composition"}, remote, "core", "fix-1="+sha)
		Expect(code).To(Equal(0), out)
		facts := filepath.Join(composed, "composition", "manifest", "entries", "fix-1")
		Expect(os.RemoveAll(filepath.Join(facts, "commits"))).To(Succeed())
		Expect(os.Remove(filepath.Join(facts, "order"))).To(Succeed())
		base := git(remote, "rev-parse", "core")
		code, out, landed := runWith(land, composed, []string{"verdict"}, remote, "core")
		Expect(code).To(Equal(0), out)
		Expect(readVerdict(landed)["outcome"]).To(Equal("moved"))
		Expect(readVerdict(landed)["reason"]).To(ContainSubstring("without per-commit facts"))
		Expect(git(remote, "rev-parse", "core")).To(Equal(base))
	})

	It("compose fails when the entry conflicts with the trunk", func() {
		git(work, "checkout", "-q", "core")
		Expect(os.WriteFile(filepath.Join(work, "fix.txt"), []byte("conflict\n"), 0o644)).To(Succeed())
		git(work, "add", "fix.txt")
		git(work, "commit", "-q", "-m", "trunk moves onto the same file")
		git(work, "push", "-q", "origin", "HEAD:core")
		code, out, _ := run(compose, []string{"composition"}, remote, "core", "fix-1="+sha)
		Expect(code).To(Equal(1))
		Expect(out).To(ContainSubstring("conflicts with the trunk"))
	})

	It("land rebuilds compose's commit, pushes it by fast-forward, and reports landed", func() {
		code, out, composed := run(compose, []string{"composition"}, remote, "core", "fix-1="+sha)
		Expect(code).To(Equal(0), out)
		head := readManifest(composed)["head"]
		code, out, landed := runWith(land, composed, []string{"verdict"}, remote, "core")
		Expect(code).To(Equal(0), out)
		verdict := readVerdict(landed)
		Expect(verdict["outcome"]).To(Equal("landed"))
		Expect(verdict["sha"]).To(Equal(head))
		Expect(verdict["reason"]).To(ContainSubstring("fast-forwarded core"))
		Expect(git(remote, "rev-parse", "core")).To(Equal(head))
		Expect(git(remote, "log", "-1", "--format=%an", "core")).To(Equal("Ada"))
	})

	It("land reports moved, and pushes nothing, when the trunk is no longer at the base", func() {
		code, out, composed := run(compose, []string{"composition"}, remote, "core", "fix-1="+sha)
		Expect(code).To(Equal(0), out)
		git(work, "checkout", "-q", "core")
		Expect(os.WriteFile(filepath.Join(work, "other.txt"), []byte("x\n"), 0o644)).To(Succeed())
		git(work, "add", "other.txt")
		git(work, "commit", "-q", "-m", "trunk moves")
		git(work, "push", "-q", "origin", "HEAD:core")
		moved := git(remote, "rev-parse", "core")
		code, out, landed := runWith(land, composed, []string{"verdict"}, remote, "core")
		Expect(code).To(Equal(0), out)
		Expect(readVerdict(landed)["outcome"]).To(Equal("moved"))
		Expect(git(remote, "rev-parse", "core")).To(Equal(moved))
	})

	It("land refuses a manifest with a key missing rather than reading it as moved", func() {
		code, out, composed := run(compose, []string{"composition"}, remote, "core", "fix-1="+sha)
		Expect(code).To(Equal(0), out)
		Expect(os.WriteFile(filepath.Join(composed, "composition", "manifest", "manifest.env"), []byte("entries=fix-1="+sha+"\n"), 0o644)).To(Succeed())
		code, out, landed := runWith(land, composed, []string{"verdict"}, remote, "core")
		Expect(code).To(Equal(1), out)
		Expect(readVerdict(landed)["outcome"]).To(Equal("failed"))
		Expect(readVerdict(landed)["reason"]).To(ContainSubstring("manifest.env has no base"))
	})

	It("land refuses a manifest whose head it cannot rebuild", func() {
		code, out, composed := run(compose, []string{"composition"}, remote, "core", "fix-1="+sha)
		Expect(code).To(Equal(0), out)
		env := filepath.Join(composed, "composition", "manifest", "manifest.env")
		body, err := os.ReadFile(env)
		Expect(err).NotTo(HaveOccurred())
		forged := strings.Replace(string(body), "head="+readManifest(composed)["head"], "head="+strings.Repeat("0", 40), 1)
		Expect(os.WriteFile(env, []byte(forged), 0o644)).To(Succeed())
		code, out, landed := runWith(land, composed, []string{"verdict"}, remote, "core")
		Expect(code).To(Equal(1), out)
		Expect(readVerdict(landed)["outcome"]).To(Equal("failed"))
		Expect(git(remote, "rev-parse", "core")).NotTo(Equal(sha))
	})
})

func readManifest(wd string) map[string]string {
	GinkgoHelper()
	body, err := os.ReadFile(filepath.Join(wd, "composition", "manifest", "manifest.env"))
	Expect(err).NotTo(HaveOccurred())
	manifest := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		key, value, _ := strings.Cut(line, "=")
		manifest[key] = value
	}
	return manifest
}

func readFile(path string) string {
	GinkgoHelper()
	body, err := os.ReadFile(path)
	Expect(err).NotTo(HaveOccurred())
	return string(body)
}

func readVerdict(wd string) map[string]string {
	GinkgoHelper()
	body, err := os.ReadFile(filepath.Join(wd, "verdict", "verdict.json"))
	Expect(err).NotTo(HaveOccurred())
	verdict := map[string]string{}
	Expect(json.Unmarshal(body, &verdict)).To(Succeed())
	return verdict
}
