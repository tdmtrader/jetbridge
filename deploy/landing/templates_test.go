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

// runWith also copies the compose run's manifest and candidate outputs in as
// the land task's inputs, the way the platform binds them.
func runWith(s script, inputsFrom string, outputs []string, params ...string) (int, string, string) {
	GinkgoHelper()
	wd := GinkgoT().TempDir()
	for _, out := range outputs {
		Expect(os.MkdirAll(filepath.Join(wd, out), 0o755)).To(Succeed())
	}
	if inputsFrom != "" {
		for _, in := range []string{"manifest", "candidate"} {
			cp := exec.Command("cp", "-R", filepath.Join(inputsFrom, in), filepath.Join(wd, in))
			Expect(cp.Run()).To(Succeed())
		}
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
		for _, config := range []atc.Config{composeConfig, landConfig} {
			Expect(config.Template).To(BeTrue())
			warnings, errs := configvalidate.Validate(config)
			Expect(errs).To(BeEmpty())
			_ = warnings
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
		code, out, wd := run(compose, []string{"candidate", "manifest"}, remote, "core", "fix-1="+sha)
		Expect(code).To(Equal(0), out)
		manifest := readManifest(wd)
		Expect(manifest["base"]).To(Equal(git(remote, "rev-parse", "core")))
		head := manifest["head"]
		Expect(head).To(HaveLen(40))
		Expect(git(remote, "rev-parse", "core")).NotTo(Equal(head), "compose pushes nothing")
		Expect(filepath.Join(wd, "candidate", "fix.txt")).To(BeAnExistingFile())
		info, err := os.Stat(filepath.Join(wd, "candidate", "tool.sh"))
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Mode()&0o100).NotTo(BeZero(), "the exec bit survives git archive")

		code, _, again := run(compose, []string{"candidate", "manifest"}, remote, "core", "fix-1="+sha)
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
	})

	It("compose fails when the entry conflicts with the trunk", func() {
		git(work, "checkout", "-q", "core")
		Expect(os.WriteFile(filepath.Join(work, "fix.txt"), []byte("conflict\n"), 0o644)).To(Succeed())
		git(work, "add", "fix.txt")
		git(work, "commit", "-q", "-m", "trunk moves onto the same file")
		git(work, "push", "-q", "origin", "HEAD:core")
		code, out, _ := run(compose, []string{"candidate", "manifest"}, remote, "core", "fix-1="+sha)
		Expect(code).To(Equal(1))
		Expect(out).To(ContainSubstring("conflicts with the trunk"))
	})

	It("land rebuilds compose's commit, pushes it by fast-forward, and reports landed", func() {
		code, out, composed := run(compose, []string{"candidate", "manifest"}, remote, "core", "fix-1="+sha)
		Expect(code).To(Equal(0), out)
		head := readManifest(composed)["head"]
		code, out, landed := runWith(land, composed, []string{"verdict"}, remote, "core")
		Expect(code).To(Equal(0), out)
		verdict := readVerdict(landed)
		Expect(verdict["outcome"]).To(Equal("landed"))
		Expect(verdict["sha"]).To(Equal(head))
		Expect(git(remote, "rev-parse", "core")).To(Equal(head))
		Expect(git(remote, "log", "-1", "--format=%an", "core")).To(Equal("Ada"))
	})

	It("land reports moved, and pushes nothing, when the trunk is no longer at the base", func() {
		code, out, composed := run(compose, []string{"candidate", "manifest"}, remote, "core", "fix-1="+sha)
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

	It("land refuses a manifest whose head it cannot rebuild", func() {
		code, out, composed := run(compose, []string{"candidate", "manifest"}, remote, "core", "fix-1="+sha)
		Expect(code).To(Equal(0), out)
		env := filepath.Join(composed, "manifest", "manifest.env")
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
	body, err := os.ReadFile(filepath.Join(wd, "manifest", "manifest.env"))
	Expect(err).NotTo(HaveOccurred())
	manifest := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		key, value, _ := strings.Cut(line, "=")
		manifest[key] = value
	}
	return manifest
}

func readVerdict(wd string) map[string]string {
	GinkgoHelper()
	body, err := os.ReadFile(filepath.Join(wd, "verdict", "verdict.json"))
	Expect(err).NotTo(HaveOccurred())
	verdict := map[string]string{}
	Expect(json.Unmarshal(body, &verdict)).To(Succeed())
	return verdict
}
