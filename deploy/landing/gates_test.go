package landing_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"encoding/json"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/configvalidate"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/yaml"
)

// The gate templates: the test gate's tasks are the pipeline's, verbatim;
// the stage, change and review scripts run here as the templates carry them,
// against real git repositories, a real jb and the real verdict command.

const (
	testsGate  = "../landing-gate-tests-template.yml"
	reviewGate = "../landing-gate-review-template.yml"
	pipeline   = "../concourse-pipeline.yml"
)

// stepNamed is the task step called name anywhere in the document's plans,
// as YAML maps, so it can be compared key by key.
func stepNamed(path, name string) map[string]any {
	GinkgoHelper()
	body, err := os.ReadFile(path)
	Expect(err).NotTo(HaveOccurred())
	var doc map[string]any
	Expect(yaml.Unmarshal(body, &doc)).To(Succeed())
	var found []map[string]any
	var walk func(steps []any)
	walk = func(steps []any) {
		for _, raw := range steps {
			step, _ := raw.(map[string]any)
			if step["task"] == name {
				found = append(found, step)
			}
			if nested, ok := step["in_parallel"].([]any); ok {
				walk(nested)
			}
			if nested, ok := step["in_parallel"].(map[string]any); ok {
				walk(nested["steps"].([]any))
			}
			if nested, ok := step["do"].([]any); ok {
				walk(nested)
			}
		}
	}
	for _, job := range doc["jobs"].([]any) {
		walk(job.(map[string]any)["plan"].([]any))
	}
	Expect(found).To(HaveLen(1), "one task %s in %s", name, path)
	return found[0]
}

// stepScript is a task's inline script and the arguments after it.
func stepScript(path, name string) script {
	GinkgoHelper()
	args := stepNamed(path, name)["config"].(map[string]any)["run"].(map[string]any)["args"].([]any)
	for i, arg := range args {
		if arg == "-c" && i+1 < len(args) {
			s := script{body: args[i+1].(string)}
			for _, rest := range args[i+2:] {
				s.args = append(s.args, rest.(string))
			}
			return s
		}
	}
	Fail("no inline script in " + name)
	return script{}
}

func keys(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// runIn runs a script in wd with env added, returning its exit code and output.
func runIn(wd string, s script, env []string, params ...string) (int, string) {
	GinkgoHelper()
	cmd := exec.Command("/bin/sh", append([]string{"-eu", "-c", s.body, s.args[0]}, params...)...)
	cmd.Dir = wd
	cmd.Env = append(append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"), env...)
	out, err := cmd.CombinedOutput()
	if exit, ok := err.(*exec.ExitError); ok {
		return exit.ExitCode(), string(out)
	}
	Expect(err).NotTo(HaveOccurred(), string(out))
	return 0, string(out)
}

// replaced is the script with each old text replaced, failing if any is gone,
// so a test that stands in for an image path breaks when the template moves.
func replaced(s script, pairs ...string) script {
	GinkgoHelper()
	for i := 0; i < len(pairs); i += 2 {
		Expect(s.body).To(ContainSubstring(pairs[i]))
		s.body = strings.ReplaceAll(s.body, pairs[i], pairs[i+1])
	}
	return s
}

func buildTool(pkg string) string {
	GinkgoHelper()
	out := filepath.Join(GinkgoT().TempDir(), filepath.Base(pkg))
	build := exec.Command("go", "build", "-o", out, pkg)
	build.Dir = "../.."
	combined, err := build.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), string(combined))
	return out
}

var _ = Describe("The landing gate templates", func() {
	var remote, work, sha string

	BeforeEach(func() {
		root := GinkgoT().TempDir()
		remote = filepath.Join(root, "remote.git")
		work = filepath.Join(root, "work")
		git(root, "init", "-q", "--bare", "-b", "core", remote)
		git(root, "clone", "-q", remote, work)
		Expect(os.WriteFile(filepath.Join(work, "parser.go"), []byte("package parser\n\nfunc First(s string) byte { return s[0] }\n"), 0o644)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(work, ".gitignore"), []byte("*.log\n"), 0o644)).To(Succeed())
		git(work, "add", ".")
		git(work, "commit", "-q", "-m", "trunk")
		git(work, "push", "-q", "origin", "HEAD:core")
		git(work, "checkout", "-q", "-b", "fix")
		Expect(os.WriteFile(filepath.Join(work, "parser.go"), []byte("package parser\n\nfunc First(s string) byte { return s[1] }\n"), 0o644)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(work, "tool.sh"), []byte("#!/bin/sh\n"), 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(work, "kept.log"), []byte("tracked although ignored\n"), 0o644)).To(Succeed())
		git(work, "add", "parser.go", "tool.sh")
		git(work, "add", "-f", "kept.log")
		git(work, "commit", "-q", "-m", "fix: the first byte")
		sha = git(work, "rev-parse", "HEAD")
		git(work, "push", "-q", "origin", sha+":refs/queue/submit/fix-1")
	})

	// composed runs the real compose script and returns the directory holding
	// its composition, the way the gate Runs receive it.
	composed := func() string {
		GinkgoHelper()
		compose, _ := taskScript("../landing-compose-template.yml", "compose")
		code, out, wd := run(compose, []string{"composition"}, remote, "core", "fix-1="+sha)
		Expect(code).To(Equal(0), out)
		return wd
	}

	It("The gate templates are valid Run templates taking the queue's params and its composition, with canonical, distinct task ids", func() {
		ids := map[string]bool{}
		for _, path := range []string{testsGate, reviewGate} {
			body, err := os.ReadFile(path)
			Expect(err).NotTo(HaveOccurred())
			var config atc.Config
			Expect(yaml.UnmarshalStrict(body, &config)).To(Succeed(), path)
			Expect(config.Template).To(BeTrue())
			_, errs := configvalidate.Validate(config)
			Expect(errs).To(BeEmpty(), path)
			Expect(configvalidate.ValidateTemplateDeclaration(atc.PipelineRef{Name: filepath.Base(path)}, config)).To(Succeed(), "set-pipeline refuses %s", path)
			var params []string
			for _, p := range config.Params {
				params = append(params, p.Name)
			}
			Expect(params).To(ConsistOf("repository", "trunk", "entries"), "the engine passes every gate the queue's three params")
			Expect(config.Jobs).To(HaveLen(1))
			first := config.Jobs[0].PlanSequence[0].Config.(*atc.TaskStep)
			Expect(first.RunInputs).To(ContainElement(atc.RunInput{Name: "composition", Input: "composition"}), "the engine binds the compose Run's composition as `composition`")
			Expect(config.Jobs[0].PlanSequence[0].Config.(*atc.TaskStep).TaskID).NotTo(BeEmpty())
		}
		for _, step := range []struct{ path, name string }{{testsGate, "stage"}, {testsGate, "build-and-vet"}, {testsGate, "unit-tests"}, {reviewGate, "change"}, {reviewGate, "review"}} {
			id, _ := stepNamed(step.path, step.name)["task_id"].(string)
			Expect(uuid.Validate(id)).To(Succeed(), step.name)
			Expect(ids).NotTo(HaveKey(id), step.name)
			ids[id] = true
		}
	})

	It("The test gate runs exactly the pipeline's build-and-vet and unit-tests tasks: config, image, resources, params and timeout", func() {
		for _, name := range []string{"build-and-vet", "unit-tests"} {
			job := stepNamed(pipeline, name)
			gate := stepNamed(testsGate, name)
			Expect(gate["config"]).To(Equal(job["config"]), "%s's config, script included, drifted from deploy/concourse-pipeline.yml", name)
			Expect(gate["timeout"]).To(Equal(job["timeout"]), name)
			Expect(gate).NotTo(HaveKey("attempts"), "%s takes a run input, which cannot sit in a repeated step; the queue retries a no-verdict gate instead", name)
			Expect(gate["privileged"] == job["privileged"]).To(BeTrue(), "%s runs privileged exactly when the job does", name)
			var want []string
			for _, key := range keys(job) {
				if key != "attempts" {
					want = append(want, key)
				}
			}
			want = append(want, "run_inputs", "task_id")
			sort.Strings(want)
			Expect(keys(gate)).To(Equal(want), "%s adds only its task id and the repo route", name)
			Expect(gate["run_inputs"]).To(Equal([]any{map[string]any{"name": "repo", "input": "repo", "result": "repo"}}))
		}
	})

	It("stage gives the tasks compose's candidate as repo, every file and mode, ignored files too", func() {
		wd := composed()
		Expect(os.MkdirAll(filepath.Join(wd, "repo"), 0o755)).To(Succeed())
		code, out := runIn(wd, stepScript(testsGate, "stage"), nil)
		Expect(code).To(Equal(0), out)
		Expect(out).To(ContainSubstring("stage: repo is candidate"))
		Expect(readFile(filepath.Join(wd, "repo", "parser.go"))).To(ContainSubstring("s[1]"))
		Expect(readFile(filepath.Join(wd, "repo", "kept.log"))).To(ContainSubstring("tracked"))
		info, err := os.Stat(filepath.Join(wd, "repo", "tool.sh"))
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Mode() & 0o100).NotTo(BeZero())
		_, err = os.Stat(filepath.Join(wd, "repo", ".git"))
		Expect(os.IsNotExist(err)).To(BeTrue(), "repo is a tree, as hack/ci-check.sh gives these tasks")
	})

	It("stage refuses a candidate that is not compose's tree, by content or by mode", func() {
		for _, damage := range []func(candidate string){
			func(candidate string) {
				Expect(os.WriteFile(filepath.Join(candidate, "parser.go"), []byte("tampered\n"), 0o644)).To(Succeed())
			},
			func(candidate string) { Expect(os.Chmod(filepath.Join(candidate, "tool.sh"), 0o644)).To(Succeed()) },
		} {
			wd := composed()
			Expect(os.MkdirAll(filepath.Join(wd, "repo"), 0o755)).To(Succeed())
			damage(filepath.Join(wd, "composition", "candidate"))
			code, out := runIn(wd, stepScript(testsGate, "stage"), nil)
			Expect(code).NotTo(BeZero())
			Expect(out).To(ContainSubstring("but compose composed"))
			Expect(filepath.Join(wd, "repo", "parser.go")).NotTo(BeAnExistingFile())
		}
	})

	Describe("the review gate", func() {
		var jb, reviewWorker string

		BeforeEach(func() {
			jb = buildTool("./cmd/jb")
			reviewWorker = buildTool("./cmd/jb-review-worker")
		})

		// change builds jb from the base's source; here there is no such source,
		// so the jb built from this tree stands in for that one build line.
		changeScript := func() script {
			return replaced(stepScript(reviewGate, "change"),
				`(cd "$top/tools" && go build -o "$top/jb" ./cmd/jb)`, `cp `+jb+` "$top/jb"`)
		}

		It("change builds a review bundle of the base to the candidate, the whole change the entry introduces", func() {
			wd := composed()
			base := readManifest(wd)["base"]
			Expect(os.MkdirAll(filepath.Join(wd, "change"), 0o755)).To(Succeed())
			code, out := runIn(wd, changeScript(), nil, remote, "core")
			Expect(code).To(Equal(0), out)
			// jb review capture verifies the bundle before it publishes it; this
			// suite is core's and reads only the manifest's wire shape.
			var manifest struct {
				Version    string `json:"version"`
				BaseCommit string `json:"base_commit"`
				HeadCommit string `json:"head_commit"`
				Files      []struct{ Side, Path string }
			}
			Expect(json.Unmarshal([]byte(readFile(filepath.Join(wd, "change", "bundle", "manifest.json"))), &manifest)).To(Succeed())
			Expect(manifest.Version).To(Equal("review-input/v1"))
			Expect(manifest.BaseCommit).To(Equal(base))
			changed := map[string]bool{}
			for _, f := range manifest.Files {
				changed[f.Side+"/"+f.Path] = true
			}
			Expect(changed).To(HaveKey("head/tool.sh"))
			Expect(changed).To(HaveKey("head/kept.log"))
			Expect(changed).To(HaveKey("base/parser.go"))
			diff := readFile(filepath.Join(wd, "change", "bundle", "change.diff"))
			Expect(diff).To(ContainSubstring("-func First(s string) byte { return s[0] }"))
			Expect(diff).To(ContainSubstring("+func First(s string) byte { return s[1] }"))
			Expect(diff).To(ContainSubstring("new file mode 100755"))
			Expect(git(filepath.Join(wd, "work"), "rev-parse", manifest.HeadCommit+"^{tree}")).To(Equal(readManifest(wd)["tree"]), "the reviewed head is compose's tree")

			published := readFile(filepath.Join(wd, "change", "bundle", "manifest.json"))
			code, out = runIn(wd, changeScript(), nil, remote, "core")
			Expect(code).To(Equal(0), "a bundle that cannot be built is no verdict, not a failed review: %s", out)
			Expect(out).To(ContainSubstring("change: no verdict"))
			Expect(readFile(filepath.Join(wd, "change", "bundle", "manifest.json"))).To(Equal(published), "a published bundle is never replaced")
		})

		It("change refuses a candidate that is not compose's tree, as no verdict", func() {
			wd := composed()
			Expect(os.MkdirAll(filepath.Join(wd, "change"), 0o755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(wd, "composition", "candidate", "parser.go"), []byte("tampered\n"), 0o644)).To(Succeed())
			code, out := runIn(wd, changeScript(), nil, remote, "core")
			Expect(code).To(Equal(0), out)
			Expect(out).To(ContainSubstring("not reviewing it"))
			Expect(out).To(ContainSubstring("change: no verdict"))
			Expect(filepath.Join(wd, "change", "bundle")).NotTo(BeADirectory())
		})

		It("change builds jb from the base's source, never the candidate's", func() {
			body := stepScript(reviewGate, "change").body
			Expect(body).To(ContainSubstring(`git -C "$top/work" archive "$base" | tar -x -C "$top/tools"`))
			Expect(body).To(ContainSubstring(`(cd "$top/tools" && go build -o "$top/jb" ./cmd/jb)`))
		})

		// review runs here with a stand-in for the worker's review session,
		// which needs Linux and Codex (cmd/jb-review-worker tests that), and
		// the real worker's verdict.
		Describe("review", func() {
			const secret = `{"tokens":{"access_token":"synthetic-secret-token"}}`
			var wd, shm string

			reviewScript := func() script {
				stub := filepath.Join(GinkgoT().TempDir(), "jb-review-worker")
				Expect(os.WriteFile(stub, []byte(`#!/bin/sh
if [ "$1" = verdict ]; then exec `+reviewWorker+` "$@"; fi
out= auth=
while [ $# -gt 0 ]; do
  case $1 in --output) out=$2; shift ;; --auth-file) auth=$2; shift ;; esac
  shift
done
ls -l "$auth" | cut -c1-10 > "$RECORD/mode"
cat "$auth" > "$RECORD/auth"
rm -f "$auth"
mkdir -p "$out"
cp "$FIXTURE" "$out/review.json"
printf '# Code review\n' > "$out/review.md"
`), 0o755)).To(Succeed())
				return replaced(stepScript(reviewGate, "review"), "/usr/local/bin/jb-review-worker", stub, "/dev/shm", shm)
			}

			BeforeEach(func() {
				wd = GinkgoT().TempDir()
				shm = GinkgoT().TempDir()
				Expect(os.MkdirAll(filepath.Join(wd, "result"), 0o755)).To(Succeed())
				Expect(os.MkdirAll(filepath.Join(wd, "gate"), 0o755)).To(Succeed())
				Expect(os.MkdirAll(filepath.Join(wd, "change", "bundle"), 0o755)).To(Succeed())
			})

			passed := func() string { return filepath.Join(wd, "gate", "passed") }

			review := func(fixture string, auth string) (int, string, string) {
				GinkgoHelper()
				record := GinkgoT().TempDir()
				fixturePath, err := filepath.Abs(filepath.Join("../../cmd/jb-review-worker/testdata", fixture))
				Expect(err).NotTo(HaveOccurred())
				code, out := runIn(wd, reviewScript(), []string{"CODEX_AUTH=" + auth, "RECORD=" + record, "FIXTURE=" + fixturePath}, "gpt-5.5", "--run-id=7")
				return code, out, record
			}

			DescribeTable("fails only on a finding of severity high or blocker, naming each; passes medium and low; an incomplete review is no verdict",
				func(fixture string, exit int, pass bool, says ...string) {
					code, out, _ := review(fixture, secret)
					Expect(code).To(Equal(exit), out)
					for _, s := range says {
						Expect(out).To(ContainSubstring(s))
					}
					if pass {
						Expect(passed()).To(BeAnExistingFile(), "the verdict step loads gate/passed")
					} else {
						Expect(passed()).NotTo(BeAnExistingFile(), "without gate/passed the verdict step errors the Run")
					}
					Expect(filepath.Join(wd, "gate", "failed")).NotTo(BeAnExistingFile(), "gate publishes only the pass")
				},
				Entry("a blocker", "review-blocker.json", 1, false, "f-002 [blocker] Gate verdicts replay after a restart", "review gate failed"),
				Entry("a high", "review-high.json", 1, false, "f-001 [high] Overdue Run is never cancelled", "review gate failed"),
				Entry("medium and low", "review-medium-low.json", 0, true, "review gate passed"),
				Entry("no findings", "review-no-findings.json", 0, true, "review gate passed: verdict no_findings"),
				Entry("incomplete", "review-incomplete.json", 0, false, "the review is incomplete", "review: no verdict"),
			)

			It("is no verdict when change built no bundle", func() {
				Expect(os.Remove(filepath.Join(wd, "change", "bundle"))).To(Succeed())
				code, out, _ := review("review-medium-low.json", secret)
				Expect(code).To(Equal(0), out)
				Expect(out).To(ContainSubstring("change built no review bundle"))
				Expect(passed()).NotTo(BeAnExistingFile())
			})

			It("hands the worker the credential in an owner-only file under /dev/shm that is gone afterwards, and never prints it", func() {
				code, out, record := review("review-medium-low.json", secret)
				Expect(code).To(Equal(0), out)
				Expect(readFile(filepath.Join(record, "auth"))).To(Equal(secret))
				Expect(readFile(filepath.Join(record, "mode"))).To(Equal("-rw-------\n"))
				Expect(out).NotTo(ContainSubstring("synthetic-secret-token"))
				Expect(filepath.Join(shm, "jb-review-auth.json")).NotTo(BeAnExistingFile())
				entries, err := os.ReadDir(filepath.Join(wd, "result"))
				Expect(err).NotTo(HaveOccurred())
				var names []string
				for _, e := range entries {
					names = append(names, e.Name())
				}
				Expect(names).To(ConsistOf("review.json", "review.md"), "the report is the result; nothing of the credential is")
				Expect(out).To(ContainSubstring("# Code review"), "the full report is in the build log")
			})

			It("removes the credential file even when the worker never ran, and an empty credential or a review that cannot run is no verdict", func() {
				code, out, _ := review("review-medium-low.json", "")
				Expect(code).To(Equal(0), out)
				Expect(out).To(ContainSubstring("((codex-auth)) is empty"))
				Expect(passed()).NotTo(BeAnExistingFile())
				failing := replaced(reviewScript(), "mkdir -p "+shm+"/jb-review\n", "mkdir -p "+shm+"/jb-review\nfalse\n")
				code, out = runIn(wd, failing, []string{"CODEX_AUTH=" + secret}, "gpt-5.5", "--run-id=7")
				Expect(code).To(Equal(0), out)
				Expect(out).To(ContainSubstring("review: no verdict"))
				Expect(passed()).NotTo(BeAnExistingFile())
				Expect(out).NotTo(ContainSubstring("synthetic-secret-token"))
				Expect(filepath.Join(shm, "jb-review-auth.json")).NotTo(BeAnExistingFile())
			})

			It("ends with the verdict step, which errors the Run unless the review passed", func() {
				body, err := os.ReadFile(reviewGate)
				Expect(err).NotTo(HaveOccurred())
				var config atc.Config
				Expect(yaml.UnmarshalStrict(body, &config)).To(Succeed())
				plan := config.Jobs[0].PlanSequence
				last, ok := plan[len(plan)-1].Config.(*atc.LoadVarStep)
				Expect(ok).To(BeTrue(), "the last step is a load_var")
				Expect(last.File).To(Equal("gate/passed"))
				outputs := stepNamed(reviewGate, "review")["config"].(map[string]any)["outputs"]
				Expect(outputs).To(ContainElement(map[string]any{"name": "gate"}))
			})

			It("never traces its commands, which would print the credential", func() {
				step := stepNamed(reviewGate, "review")
				args := step["config"].(map[string]any)["run"].(map[string]any)["args"].([]any)
				Expect(args[0]).To(Equal("-eu"))
				Expect(args[2]).NotTo(ContainSubstring("set -x"))
				Expect(step["params"]).To(Equal(map[string]any{"CODEX_AUTH": "((codex-auth))"}))
				Expect(stepNamed(reviewGate, "change")["params"]).To(BeNil(), "the credential reaches only the review task")
			})
		})
	})
})
