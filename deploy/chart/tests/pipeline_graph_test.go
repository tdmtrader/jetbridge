package tests

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/yaml"
)

// A Concourse task declares the directories it wants as `inputs`, and the
// scheduler refuses to run it unless every one of them exists in the build
// plan. Outputs do NOT cross job boundaries -- a job sees only its own steps
// and whatever it `get`s -- so a task input satisfied three jobs earlier is not
// satisfied at all.
//
// 473829956f shipped exactly that: it added a `web-public` output to
// build-image's build-frontend task and pasted the consuming block into
// tag-rc's create-rc-tag as well, a job that runs *earlier* and produces
// nothing of the sort. The task would have failed on its missing input,
// breaking the chain one job before the one the commit set out to fix. Nothing
// caught it -- the YAML is well-formed, and `fly validate-pipeline` checks
// schema, not reachability.
//
// So walk the graph: for every task, every declared input must be produced by
// an earlier step in the same job.
func TestPipelineTaskInputsAreSatisfied(t *testing.T) {
	for _, path := range pipelineFiles(t) {
		pipeline := loadPipeline(t, path)
		name := filepath.Base(path)

		for _, job := range pipeline.Jobs {
			// Artifact names visible at the current point in the plan.
			available := map[string]bool{}

			for _, step := range flattenPlan(job.Plan) {
				if step.Get != "" {
					// `get: x` produces a directory named x, unless renamed.
					produced := step.Get
					if step.Resource != "" {
						produced = step.Get
					}
					available[produced] = true
				}

				if step.Put != "" {
					available[step.Put] = true
				}

				if step.Task == "" {
					continue
				}

				for _, in := range step.Config.Inputs {
					if in.Optional {
						continue
					}
					if !available[in.Name] {
						t.Errorf(
							"%s: job %q, task %q declares input %q, "+
								"which no earlier step in that job produces "+
								"(available: %s).\n"+
								"Task outputs do not cross jobs -- if it is built "+
								"elsewhere, that job must build it too, or pass it "+
								"through a resource.",
							name, job.Name, step.Task, in.Name,
							strings.Join(sortedKeys(available), ", "),
						)
					}
				}

				for _, out := range step.Config.Outputs {
					available[out.Name] = true
				}
			}
		}
	}
}

// A task that declares an output nothing writes hands the next task an empty
// directory. That is how a stale frontend ships: the consumer's guard checks
// the file is non-empty, but a guard nobody reaches proves nothing. Require
// that a declared output is at least mentioned by the script that claims it.
func TestPipelineDeclaredOutputsAreWritten(t *testing.T) {
	for _, path := range pipelineFiles(t) {
		pipeline := loadPipeline(t, path)
		name := filepath.Base(path)

		for _, job := range pipeline.Jobs {
			for _, step := range flattenPlan(job.Plan) {
				if step.Task == "" {
					continue
				}

				script := strings.Join(step.Config.Run.Args, "\n")

				for _, out := range step.Config.Outputs {
					if !strings.Contains(script, out.Name) {
						t.Errorf(
							"%s: job %q, task %q declares output %q but its "+
								"script never mentions it, so the next task "+
								"receives an empty directory.",
							name, job.Name, step.Task, out.Name,
						)
					}
				}
			}
		}
	}
}

// `attempts: N` reruns the whole task, which is right for a flaky download and
// wrong for an operation that destroys history.
//
// The bar here is deliberately narrow. Most mutations in this pipeline are
// idempotent -- `docker push` of identical content, `kubectl apply`, a rollout
// restart -- and re-running them costs time, not correctness. What is not
// recoverable is a force-push or a remote-ref delete: the second attempt cannot
// tell whether the first one already moved the ref, and whatever was there
// before is gone either way.
//
// Both used to be here. The RC tag was force-pushed under `attempts: 2` -- a
// retried force-push, which is not a record of anything -- and the release task
// force-pushed HEAD to main, silently discarding any commit on main that was
// not an ancestor. Both are gone; this keeps them gone.
func TestPipelineDoesNotRetryDestructiveGit(t *testing.T) {
	destructive := []string{
		"git push --force",
		"git push -f",
		"git push --delete",
		"git push origin :",
	}

	for _, path := range pipelineFiles(t) {
		pipeline := loadPipeline(t, path)
		name := filepath.Base(path)

		for _, job := range pipeline.Jobs {
			for _, step := range flattenPlan(job.Plan) {
				if step.Task == "" {
					continue
				}

				// Comments in these scripts quote the very commands they
				// explain the removal of -- the release task documents the
				// `git push --force origin HEAD:refs/heads/main` it no longer
				// runs. Matching prose as if it were code makes this test fire
				// on its own changelog.
				script := stripShellComments(strings.Join(step.Config.Run.Args, "\n"))
				for _, d := range destructive {
					if !strings.Contains(script, d) {
						continue
					}

					// A force-push is worth flagging on its own; under
					// `attempts:` it is worse. Report either way, and say which.
					if step.Attempts > 1 {
						t.Errorf(
							"%s: job %q, task %q runs %q under attempts: %d. "+
								"A retried force-push cannot tell whether the "+
								"first attempt already moved the ref.",
							name, job.Name, step.Task, d, step.Attempts,
						)
					} else {
						t.Errorf(
							"%s: job %q, task %q runs %q. CI does not rewrite "+
								"remote history; whatever it overwrites is "+
								"unrecoverable and unlogged.",
							name, job.Name, step.Task, d,
						)
					}
				}
			}
		}
	}
}

// Cutting a release is a decision. Every other job in the chain runs on every
// green push -- that is what keeps the branch deployable -- but `release` tags
// the commit, publishes `:VERSION` and `:latest` to a public registry, and
// redeploys the cluster. With `trigger: true` a green push WAS a release: eight
// jobs would go green and the ninth would ship, without anyone choosing to.
//
// The failure mode is quiet in exactly the wrong way. Nothing anywhere required
// that VERSION had been bumped, so the steady state after the first release is
// a ninth job that republishes mutable tags from an unreleased commit and only
// then goes red on the existing git tag.
//
// So: release is manual, and stays manual.
func TestReleaseJobIsManuallyTriggered(t *testing.T) {
	const releaseJob = "release"

	for _, path := range pipelineFiles(t) {
		pipeline := loadPipeline(t, path)
		name := filepath.Base(path)

		for _, job := range pipeline.Jobs {
			if job.Name != releaseJob {
				continue
			}

			var gets int
			for _, step := range flattenPlan(job.Plan) {
				if step.Get == "" {
					continue
				}
				gets++

				if step.Trigger == nil {
					// Concourse defaults trigger to false, so an absent key is
					// correct behaviour -- but it is indistinguishable from an
					// oversight, and this is the one job where the difference
					// matters. Require it stated.
					t.Errorf(
						"%s: job %q gets %q without an explicit `trigger:`. "+
							"The default is false, which is right, but say so: "+
							"an unstated default is one edit away from becoming "+
							"an automatic release.",
						name, job.Name, step.Get,
					)
					continue
				}

				if *step.Trigger {
					t.Errorf(
						"%s: job %q gets %q with `trigger: true`, which makes "+
							"every green push a release. Releases are cut by a "+
							"person.",
						name, job.Name, step.Get,
					)
				}
			}

			if gets == 0 {
				t.Errorf("%s: job %q has no `get` steps; this test would pass vacuously", name, job.Name)
			}
		}
	}
}

// The pipeline writes fly archives into the image; the ATC serves them from
// --cli-artifacts-dir. Neither half knows about the other, and the naming is a
// contract with exactly one enforcement point: whether the file the handler
// opens happens to exist.
//
// It did not. atc/api/cliserver/download.go picks the extension by platform --
// zip for windows, tgz otherwise -- and the pipeline wrote
// fly-windows-amd64.tgz for every platform, so the windows branch called
// zip.OpenReader on a file that was never created. `fly sync` on Windows 500s,
// which is precisely the upgrade path that a 0.2 -> 0.3 minor bump forces every
// existing client through.
//
// Rather than restate the naming here, read the extensions the handler picks
// straight out of its source. A test that hardcodes both sides proves the two
// hardcodings agree with each other, not with the code.
func TestPipelinePackagesFlyArchivesTheATCCanServe(t *testing.T) {
	root := repoRoot(t)

	handler, err := os.ReadFile(filepath.Join(root, "atc", "api", "cliserver", "download.go"))
	if err != nil {
		t.Fatalf("reading the CLI download handler: %v", err)
	}

	extFor := map[string]string{}
	for _, platform := range []string{"windows", "other"} {
		// archiveExtension = "zip"  /  archiveExtension = "tgz"
		re := regexp.MustCompile(`archiveExtension\s*=\s*"([a-z]+)"`)
		all := re.FindAllStringSubmatch(string(handler), -1)
		if len(all) != 2 {
			t.Fatalf("expected 2 archiveExtension assignments in download.go, found %d -- "+
				"the handler changed shape and this test needs revisiting", len(all))
		}
		// The windows branch is first in the if/else.
		if platform == "windows" {
			extFor[platform] = all[0][1]
		} else {
			extFor[platform] = all[1][1]
		}
	}

	if extFor["windows"] == extFor["other"] {
		t.Fatalf("both branches of download.go use %q; this test can no longer "+
			"distinguish them", extFor["windows"])
	}

	pipeline, err := os.ReadFile(filepath.Join(root, "deploy", "concourse-pipeline.yml"))
	if err != nil {
		t.Fatalf("reading the pipeline: %v", err)
	}
	// Comments only, again: the packaging loop carries a comment naming the
	// fly-windows-amd64.tgz it no longer writes, and a plain substring search
	// over the raw file would accept that as the archive existing. `#` opens a
	// comment in both YAML and the embedded shell, so one pass covers both.
	script := stripShellComments(string(pipeline))

	for _, want := range []struct{ platform, arch string }{
		{"linux", "amd64"},
		{"linux", "arm64"},
		{"darwin", "amd64"},
		{"darwin", "arm64"},
		{"windows", "amd64"},
	} {
		key := "other"
		if want.platform == "windows" {
			key = "windows"
		}
		archive := fmt.Sprintf("fly-%s-%s.%s", want.platform, want.arch, extFor[key])

		if !strings.Contains(script, archive) {
			t.Errorf(
				"deploy/concourse-pipeline.yml never mentions %q, but "+
					"atc/api/cliserver/download.go serves %s/%s from that exact "+
					"name. A missing archive is a 500 from `fly sync`, not a "+
					"build failure.",
				archive, want.platform, want.arch,
			)
		}
	}
}

// The chart resolves its image tag as `default .Chart.AppVersion
// .Values.image.tag`, so with no override a GitOps render asks for
// <repository>:<appVersion> -- the bare version, e.g. 0.3.0.
//
// Only the `release` job ever published that tag. Since appVersion is bumped
// when work on a version STARTS, not when it ships, every commit between the
// bump and the release left the chart naming an image that did not exist, and
// an ArgoCD sync in that window produced ImagePullBackOff on a cluster that was
// otherwise healthy.
//
// build-image now publishes the bare tag too, while the version is unreleased.
// This asserts it keeps doing so: the candidate job, not just the release job,
// has to satisfy what the chart asks for.
func TestCandidateBuildPublishesTheTagTheChartResolves(t *testing.T) {
	root := repoRoot(t)

	values, err := os.ReadFile(filepath.Join(root, "deploy", "chart", "values.yaml"))
	if err != nil {
		t.Fatalf("reading chart values: %v", err)
	}

	// An explicit image.tag would pin the deploy and make this moot; the
	// default is empty, which is what triggers the appVersion fallback.
	var vals struct {
		Image struct {
			Tag string `json:"tag"`
		} `json:"image"`
	}
	if err := yaml.Unmarshal(values, &vals); err != nil {
		t.Fatalf("parsing chart values: %v", err)
	}
	if vals.Image.Tag != "" {
		t.Skipf("image.tag is pinned to %q, so the appVersion fallback does not apply", vals.Image.Tag)
	}

	pipeline := loadPipeline(t, filepath.Join(root, "deploy", "concourse-pipeline.yml"))

	var checked bool
	for _, job := range pipeline.Jobs {
		if job.Name != "build-image" {
			continue
		}
		checked = true

		var script string
		for _, step := range flattenPlan(job.Plan) {
			script += stripShellComments(strings.Join(step.Config.Run.Args, "\n"))
		}

		// A push of the bare ${VERSION} tag -- not ${VERSION}-rc, not
		// ${RC_TAG}. The `[^-]` guards against matching the rc forms.
		bareTag := regexp.MustCompile(`docker push [^\s]*jetbridge:\$\{VERSION\}(\s|$)`)
		if !bareTag.MatchString(script) {
			t.Errorf(
				"job %q never pushes jetbridge:${VERSION}. The chart resolves "+
					"its tag from Chart.yaml appVersion, so between bumping the "+
					"version and cutting the release that tag would name an "+
					"image nobody has published, and an ArgoCD sync would "+
					"ImagePullBackOff.",
				job.Name,
			)
		}
	}

	if !checked {
		t.Fatal("no build-image job found; this test would pass vacuously")
	}
}

// A task script is a string in a YAML file. Nothing parses it until the job
// runs, which on this pipeline means after up to eight upstream jobs and a
// deploy -- so a stray quote costs a full chain to discover. `sh -n` parses
// without executing.
func TestPipelineTaskScriptsAreValidShell(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skipf("no sh on PATH: %v", err)
	}

	var checked int

	for _, path := range pipelineFiles(t) {
		pipeline := loadPipeline(t, path)
		name := filepath.Base(path)

		for _, job := range pipeline.Jobs {
			for _, step := range flattenPlan(job.Plan) {
				if step.Task == "" {
					continue
				}
				if !strings.HasSuffix(step.Config.Run.Path, "sh") {
					continue
				}

				// args is ["-exc", "<script>"]; the script is the last element.
				args := step.Config.Run.Args
				if len(args) == 0 {
					continue
				}
				script := args[len(args)-1]
				if len(script) < 20 {
					t.Errorf("%s: job %q, task %q has a suspiciously short script (%d bytes); "+
						"this check would be vacuous", name, job.Name, step.Task, len(script))
					continue
				}
				checked++

				cmd := exec.Command("sh", "-n")
				cmd.Stdin = strings.NewReader(script)
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Errorf(
						"%s: job %q, task %q is not valid shell:\n%s",
						name, job.Name, step.Task, strings.TrimSpace(string(out)),
					)
				}
			}
		}
	}

	if checked == 0 {
		t.Fatal("no shell task scripts found; this test would pass vacuously")
	}
	t.Logf("parsed %d task scripts", checked)
}

// A Concourse task runs until it exits or a human aborts it. There is no
// implicit ceiling, and concourse-pipeline.yml puts every job in one
// `serial_groups: [pipeline]`, so a task that hangs does not fail a build -- it
// holds the group and nothing else ever starts.
//
// Most of the work in that pipeline is `kubectl exec` into a DinD pod, and none
// of those calls is individually bounded. A wedged dockerd, a pod that never
// goes Ready, a registry that accepts a push and stops responding: each of them
// presents as a task that is simply still running, indefinitely.
func TestEveryPipelineTaskHasATimeout(t *testing.T) {
	var checked int

	for _, path := range pipelineFiles(t) {
		pipeline := loadPipeline(t, path)
		name := filepath.Base(path)

		for _, job := range pipeline.Jobs {
			for _, step := range flattenPlan(job.Plan) {
				if step.Task == "" {
					continue
				}
				checked++

				if step.Timeout == "" {
					t.Errorf(
						"%s: job %q, task %q has no `timeout:`. Concourse will "+
							"run it forever, and with one serial group that "+
							"blocks every other job until someone aborts by hand.",
						name, job.Name, step.Task,
					)
					continue
				}

				d, err := time.ParseDuration(step.Timeout)
				if err != nil {
					t.Errorf("%s: job %q, task %q has an unparseable timeout %q: %v",
						name, job.Name, step.Task, step.Timeout, err)
					continue
				}

				// A ceiling, so nobody satisfies this test with `timeout: 168h`.
				//
				// It has to clear the longest real workload here: the K8s
				// behavioral suite runs 2-3 hours (see CLAUDE.md), and its 4h
				// budget is legitimate headroom rather than a formality. 6h
				// leaves room above that and still catches a number chosen to
				// mean "never".
				const ceiling = 6 * time.Hour
				if d > ceiling {
					t.Errorf(
						"%s: job %q, task %q has timeout %s, above the %s ceiling. "+
							"A timeout that long is not a backstop -- a person "+
							"would notice the wedge first, which is the thing it "+
							"exists to avoid.",
						name, job.Name, step.Task, step.Timeout, ceiling,
					)
				}
			}
		}
	}

	if checked == 0 {
		t.Fatal("no tasks found; this test would pass vacuously")
	}
	t.Logf("checked %d tasks", checked)
}

type pipelineDoc struct {
	Jobs []struct {
		Name string `json:"name"`
		Plan []step `json:"plan"`
	} `json:"jobs"`
}

type step struct {
	Get      string `json:"get"`
	Put      string `json:"put"`
	Task     string `json:"task"`
	Resource string `json:"resource"`
	Attempts int    `json:"attempts"`
	Timeout  string `json:"timeout"`

	// Pointer so an absent `trigger:` is distinguishable from `trigger: false`.
	// They mean the same thing to Concourse and different things to a reader.
	Trigger *bool `json:"trigger"`

	// Nesting. These pipelines are nearly flat, but test-pipeline.yml uses
	// in_parallel, and a step hidden inside one is still a step.
	InParallel *inParallel `json:"in_parallel"`
	Do         []step      `json:"do"`
	Try        *step       `json:"try"`

	Config struct {
		Inputs []struct {
			Name     string `json:"name"`
			Optional bool   `json:"optional"`
		} `json:"inputs"`
		Outputs []struct {
			Name string `json:"name"`
		} `json:"outputs"`
		Run struct {
			Path string   `json:"path"`
			Args []string `json:"args"`
		} `json:"run"`
	} `json:"config"`
}

// in_parallel accepts either a bare list of steps or a mapping with a `steps`
// key; both appear in the wild.
type inParallel struct {
	Steps []step `json:"steps"`
}

func (p *inParallel) UnmarshalJSON(b []byte) error {
	var asList []step
	if err := yaml.Unmarshal(b, &asList); err == nil {
		p.Steps = asList
		return nil
	}

	var asMap struct {
		Steps []step `json:"steps"`
	}
	if err := yaml.Unmarshal(b, &asMap); err != nil {
		return err
	}
	p.Steps = asMap.Steps

	return nil
}

// flattenPlan returns the steps in execution order. Steps inside in_parallel
// run concurrently, so treating them as sequential is a deliberate
// simplification: it can only make this check more permissive about ordering,
// never less, and every real finding here is a missing artifact rather than a
// racy one.
func flattenPlan(plan []step) []step {
	var out []step

	for _, s := range plan {
		switch {
		case s.InParallel != nil:
			out = append(out, flattenPlan(s.InParallel.Steps)...)
		case len(s.Do) > 0:
			out = append(out, flattenPlan(s.Do)...)
		case s.Try != nil:
			out = append(out, flattenPlan([]step{*s.Try})...)
		default:
			out = append(out, s)
		}
	}

	return out
}

// stripShellComments removes whole-line and trailing `#` comments so a matcher
// sees only what the shell would run. It is deliberately simple: a `#` inside a
// quoted string is treated as a comment too. That direction of error only makes
// the matcher blinder, never noisier, which is the right way for it to be
// wrong.
func stripShellComments(script string) string {
	var kept []string

	for _, line := range strings.Split(script, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if i := strings.Index(line, " #"); i >= 0 {
			line = line[:i]
		}
		kept = append(kept, line)
	}

	return strings.Join(kept, "\n")
}

func pipelineFiles(t *testing.T) []string {
	t.Helper()

	root := repoRoot(t)
	matches, err := filepath.Glob(filepath.Join(root, "deploy", "*.yml"))
	if err != nil {
		t.Fatalf("globbing deploy/*.yml: %v", err)
	}

	var pipelines []string
	for _, m := range matches {
		b, err := os.ReadFile(m)
		if err != nil {
			t.Fatalf("reading %s: %v", m, err)
		}
		// Chart values and similar live here too; a pipeline is the thing with
		// a jobs: list.
		var doc pipelineDoc
		if err := yaml.Unmarshal(b, &doc); err != nil {
			continue
		}
		if len(doc.Jobs) > 0 {
			pipelines = append(pipelines, m)
		}
	}

	if len(pipelines) == 0 {
		t.Fatal("found no pipeline files under deploy/ -- this test would pass vacuously")
	}

	sort.Strings(pipelines)

	return pipelines
}

func loadPipeline(t *testing.T, path string) pipelineDoc {
	t.Helper()

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	var doc pipelineDoc
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}

	return doc
}

func repoRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	for i := 0; i < 10; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	t.Fatal(fmt.Sprint("could not find repo root (no go.mod above ", dir, ")"))

	return ""
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	return keys
}
