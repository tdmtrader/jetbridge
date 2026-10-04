package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/queue/adapters/git"
	"github.com/concourse/concourse/queue/config"
	"github.com/concourse/concourse/queue/core"
)

func gitIn(dir string, args ...string) string {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_GLOBAL="+os.DevNull)
	out, err := cmd.CombinedOutput()
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), string(out))
	return strings.TrimSpace(string(out))
}

var _ = Describe("queue resource", func() {
	var dir, remote, base, change string
	var source map[string]any

	// call runs the resource in a fresh process, as the CI system does.
	call := func(as string, req map[string]any, args ...string) (code int, out []byte, errw string) {
		body, err := json.Marshal(req)
		Expect(err).NotTo(HaveOccurred())
		cmd := exec.Command(os.Args[0], args...)
		cmd.Env = append(os.Environ(), "QUEUE_RESOURCE_AS="+as, "GIT_CONFIG_GLOBAL="+os.DevNull)
		var o, e bytes.Buffer
		cmd.Stdin, cmd.Stdout, cmd.Stderr = bytes.NewReader(body), &o, &e
		err = cmd.Run()
		if x, ok := err.(*exec.ExitError); ok {
			return x.ExitCode(), o.Bytes(), e.String()
		}
		Expect(err).NotTo(HaveOccurred())
		return 0, o.Bytes(), e.String()
	}
	check := func() []map[string]string {
		code, out, errw := call("check", map[string]any{"source": source})
		Expect(code).To(Equal(0), errw)
		var vs []map[string]string
		Expect(json.Unmarshal(out, &vs)).To(Succeed(), string(out))
		return vs
	}
	sources := func() string { return filepath.Join(dir, "sources") }
	get := func(v map[string]string) string {
		dest := filepath.Join(sources(), "run")
		code, out, errw := call("in", map[string]any{"source": source, "version": v}, dest)
		Expect(code).To(Equal(0), errw)
		Expect(string(out)).To(ContainSubstring(v["run"]))
		return dest
	}
	put := func(verdict string, hook ...string) (int, string) {
		params := map[string]any{"verdict": verdict, "run_dir": "run"}
		if len(hook) > 0 {
			params["hook_dir"] = hook[0]
		}
		vs := map[string]any{"mode": "verdict"}
		for k, v := range source {
			if k != "mode" {
				vs[k] = v
			}
		}
		code, _, errw := call("out", map[string]any{"source": vs, "params": params}, sources())
		return code, errw
	}
	snapshot := func() core.Snapshot {
		c, err := config.Parse(fmt.Appendf(nil, "apiVersion: %s\nrepository: {uri: %s, main: trunk}\n", config.APIVersion, remote))
		Expect(err).NotTo(HaveOccurred())
		s, err := git.NewStore(c).Load(context.Background())
		Expect(err).NotTo(HaveOccurred())
		return s
	}
	admit := func() { gitIn(remote, "update-ref", "refs/queue/admit/0000000000000000001.a", change) }

	BeforeEach(func() {
		dir = GinkgoT().TempDir()
		remote = filepath.Join(dir, "remote.git")
		gitIn(dir, "init", "-q", "--bare", remote)
		work := filepath.Join(dir, "work")
		gitIn(dir, "init", "-q", work)
		Expect(os.WriteFile(filepath.Join(work, "a"), []byte("a\n"), 0o600)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(work, "run"), []byte("the repo's own\n"), 0o600)).To(Succeed())
		gitIn(work, "add", "a", "run")
		gitIn(work, "commit", "-q", "-m", "base")
		base = gitIn(work, "rev-parse", "HEAD")
		Expect(os.WriteFile(filepath.Join(work, "b"), []byte("b\n"), 0o600)).To(Succeed())
		gitIn(work, "add", "b")
		gitIn(work, "commit", "-q", "-m", "add b")
		change = gitIn(work, "rev-parse", "HEAD")
		gitIn(work, "push", "-q", remote, base+":refs/heads/trunk", change+":refs/heads/topic")
		Expect(os.MkdirAll(sources(), 0o700)).To(Succeed())
		source = map[string]any{
			"mode": "candidate", "uri": remote, "main": "trunk", "owner": "ci", "wait_cap": "1h",
			"config": fmt.Sprintf("apiVersion: %s\nlander: {scratch: %s}\n", config.APIVersion, dir),
		}
	})

	It("A check of an empty queue finds no version", func() {
		Expect(check()).To(BeEmpty())
	})

	It("The source's batch_max caps a batch, and one below 1 is refused", func() {
		source["batch_max"] = 1
		Expect(check()).To(BeEmpty())
		source["batch_max"] = -1
		code, _, errw := call("check", map[string]any{"source": source})
		Expect(code).To(Equal(1))
		Expect(errw).To(ContainSubstring("batch.max must be at least 1"))
	})

	It("A check composes an admitted change and starts its test run", func() {
		admit()
		vs := check()
		Expect(vs).To(HaveLen(1))
		Expect(vs[0]).To(HaveKey("fence"))
		Expect(vs[0]["candidate"]).To(MatchRegexp("^[0-9a-f]{40}$"))
		Expect(gitIn(remote, "rev-parse", "refs/mq/runs/"+vs[0]["run"]+"^{commit}")).To(Equal(vs[0]["candidate"]))
		Expect(snapshot().InFlight).To(HaveLen(1))
	})

	It("The queue lands a change after its test job records a pass", func() {
		admit()
		vs := check()
		dest := get(vs[0])
		for f, want := range map[string]string{"run": vs[0]["run"], "candidate": vs[0]["candidate"], "fence": vs[0]["fence"]} {
			b, err := os.ReadFile(filepath.Join(dest, ".mq", f))
			Expect(err).NotTo(HaveOccurred())
			Expect(strings.TrimSpace(string(b))).To(Equal(want))
		}
		Expect(gitIn(dest, "rev-parse", "HEAD")).To(Equal(vs[0]["candidate"]))
		Expect(filepath.Join(dest, "b")).To(BeAnExistingFile())
		Expect(os.ReadFile(filepath.Join(dest, "run"))).To(Equal([]byte("the repo's own\n")), "the candidate's own files are left alone")
		code, errw := put("pass")
		Expect(code).To(Equal(0), errw)
		Expect(gitIn(remote, "rev-parse", "refs/heads/trunk")).To(Equal(base), "a put never lands")
		Expect(check()).To(BeEmpty())
		Expect(gitIn(remote, "rev-parse", "refs/heads/trunk")).To(Equal(vs[0]["candidate"]))
		msg := gitIn(remote, "log", "-1", "--format=%B", "refs/heads/trunk")
		Expect(msg).To(HavePrefix("land(a): "))
		Expect(msg).To(ContainSubstring("original: " + change))
		Expect(snapshot().Landed).To(HaveKey("a"))
	})

	It("A change whose test job records a fail is ejected", func() {
		admit()
		get(check()[0])
		code, errw := put("fail")
		Expect(code).To(Equal(0), errw)
		Expect(check()).To(BeEmpty())
		Expect(snapshot().Ejected).To(HaveKey("a"))
		Expect(gitIn(remote, "rev-parse", "refs/heads/trunk")).To(Equal(base))
	})

	It("Two checks with no test result give the same version and start no second run", func() {
		admit()
		first := check()
		Expect(check()).To(Equal(first))
		Expect(strings.Fields(gitIn(remote, "for-each-ref", "--format=%(refname)", "refs/mq/runs/"))).To(HaveLen(1))
	})

	It("A test job that errors never counts as a failure", func() {
		source["wait_cap"] = "1ms"
		admit()
		first := check()
		time.Sleep(1500 * time.Millisecond)
		second := check()
		Expect(second).To(HaveLen(1), "the run that gave no verdict is retried")
		Expect(second[0]["run"]).NotTo(Equal(first[0]["run"]))
		s := snapshot()
		Expect(s.Ejected).To(BeEmpty())
	})

	It("A get of a candidate that holds its own .mq dir is refused", func() {
		work := filepath.Join(dir, "work")
		Expect(os.MkdirAll(filepath.Join(work, ".mq"), 0o700)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(work, ".mq", "x"), []byte("x\n"), 0o600)).To(Succeed())
		gitIn(work, "add", ".mq")
		gitIn(work, "commit", "-q", "-m", "add .mq")
		change = gitIn(work, "rev-parse", "HEAD")
		gitIn(work, "push", "-q", remote, change+":refs/heads/topic2")
		admit()
		v := check()[0]
		code, _, errw := call("in", map[string]any{"source": source, "version": v}, filepath.Join(sources(), "run"))
		Expect(code).To(Equal(1))
		Expect(errw).To(ContainSubstring(".mq"))
	})

	It("A test job that errored gives no verdict at once, never ejects, and a new run on the same candidate lands on a pass", func() {
		admit()
		v1 := check()[0]
		tree, parent := gitIn(get(v1), "rev-parse", "HEAD^{tree}"), gitIn(filepath.Join(sources(), "run"), "rev-parse", "HEAD~1")
		code, errw := put("errored")
		Expect(code).To(Equal(0), errw)
		code, errw = put("errored")
		Expect(code).To(Equal(0), "a repeat is accepted: "+errw)
		vs := check()
		Expect(vs).To(HaveLen(1), "retried at once, not after the wait cap")
		Expect(vs[0]["run"]).NotTo(Equal(v1["run"]))
		s := snapshot()
		Expect(s.Ejected).To(BeEmpty())
		Expect(s.Paused).To(BeFalse())
		Expect(os.RemoveAll(filepath.Join(sources(), "run"))).To(Succeed())
		dest := get(vs[0])
		Expect(gitIn(dest, "rev-parse", "HEAD^{tree}", "HEAD~1")).To(Equal(tree+"\n"+parent), "the same change composed again on the same main")
		code, errw = put("pass")
		Expect(code).To(Equal(0), errw)
		Expect(check()).To(BeEmpty())
		Expect(gitIn(remote, "rev-parse", "refs/heads/trunk")).To(Equal(vs[0]["candidate"]))
		Expect(snapshot().Landed).To(HaveKey("a"))
	})

	It("A test job that keeps erroring pauses the queue after its retries, across checks, and health goes red", func() {
		source["config"] = source["config"].(string) + "batch: {retry_none: 2}\npause: {cooldown: 0s}\nhealth: {pause_after: 1ms}\n"
		admit()
		runs := map[string]bool{}
		for range 3 {
			vs := check()
			Expect(vs).To(HaveLen(1), "retried, not yet paused")
			runs[vs[0]["run"]] = true
			Expect(os.RemoveAll(filepath.Join(sources(), "run"))).To(Succeed())
			get(vs[0])
			code, errw := put("errored")
			Expect(code).To(Equal(0), errw)
		}
		Expect(runs).To(HaveLen(3))
		Expect(check()).To(BeEmpty())
		s := snapshot()
		Expect(s.Paused).To(BeTrue(), "the third no verdict, past retry_none 2, pauses")
		Expect(s.Why).To(ContainSubstring("2 retries"))
		Expect(s.Ejected).To(BeEmpty())
		bin := filepath.Join(dir, "queue")
		build := exec.Command("go", "build", "-o", bin, "../queue")
		out, err := build.CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), string(out))
		body, err := json.Marshal(map[string]any{"source": source})
		Expect(err).NotTo(HaveOccurred())
		health := exec.Command(bin, "health")
		health.Stdin, health.Env = bytes.NewReader(body), append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull)
		out, err = health.Output()
		var exit *exec.ExitError
		Expect(errors.As(err, &exit)).To(BeTrue(), string(out))
		Expect(exit.ExitCode()).To(Equal(3))
		Expect(string(out)).To(HavePrefix("unhealthy: paused"))
	})

	It("A put with a verdict other than pass, fail or errored is refused", func() {
		admit()
		get(check()[0])
		code, errw := put("flaky")
		Expect(code).To(Equal(1))
		Expect(errw).To(ContainSubstring("verdict"))
		Expect(strings.TrimSpace(gitIn(remote, "for-each-ref", "refs/mq/verdicts/"))).To(BeEmpty())
	})

	// hookedIn commits n commits changing file on top of from in repo and bundles
	// them, beyond from, as refs/heads/hooked into sources/hook/hook.bundle, as the hook step does.
	hookedIn := func(repo, from, file string, n int) string {
		gitIn(repo, "checkout", "-q", "--detach", from)
		for i := range n {
			Expect(os.WriteFile(filepath.Join(repo, file), []byte(fmt.Sprint(i)), 0o600)).To(Succeed())
			gitIn(repo, "add", file)
			gitIn(repo, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "generated")
		}
		gitIn(repo, "update-ref", "refs/heads/hooked", "HEAD")
		Expect(os.MkdirAll(filepath.Join(sources(), "hook"), 0o700)).To(Succeed())
		gitIn(repo, "bundle", "create", "-q", filepath.Join(sources(), "hook", "hook.bundle"), "refs/heads/hooked", "^"+from)
		return gitIn(repo, "rev-parse", "HEAD")
	}
	hooked := func(repo, from string, n int) string { return hookedIn(repo, from, "gen", n) }
	refs := func() string {
		return gitIn(remote, "for-each-ref", "--format=%(refname)", "refs/mq/verdicts/", "refs/mq/hooked/")
	}

	It("A pass with the hook's commit records both in one push", func() {
		admit()
		v := check()[0]
		commit := hooked(get(v), v["candidate"], 1)
		code, errw := put("pass", "hook")
		Expect(code).To(Equal(0), errw)
		Expect(gitIn(remote, "rev-parse", git.HookedRef(v["candidate"]))).To(Equal(commit), "keyed by the candidate, as the lander reads it")
		Expect(gitIn(remote, "rev-parse", "refs/mq/verdicts/"+v["run"])).NotTo(BeEmpty())
		code, errw = put("pass", "hook")
		Expect(code).To(Equal(0), "a repeat of the same put is accepted: %s", errw)
		hookedIn(filepath.Join(sources(), "run"), v["candidate"], "gen2", 1)
		code, errw = put("pass", "hook")
		Expect(code).To(Equal(1), "another hook commit for a recorded candidate is refused")
		Expect(errw).To(ContainSubstring("already recorded"))
		code, _ = put("fail")
		Expect(code).To(Equal(1), "another verdict for a recorded run is refused")
		Expect(gitIn(remote, "rev-parse", git.HookedRef(v["candidate"]))).To(Equal(commit))
	})

	It("The test job's hook step runs on what the get fetched and commits only the hook's files", func() {
		work := filepath.Join(dir, "work")
		gitIn(work, "checkout", "-q", "--detach", base)
		Expect(os.MkdirAll(filepath.Join(work, "ci"), 0o700)).To(Succeed())
		hook := "#!/bin/bash\ncase \"$1\" in owned) echo gen ;; run) echo x > gen ;; esac\n"
		Expect(os.WriteFile(filepath.Join(work, "ci", "hook.sh"), []byte(hook), 0o755)).To(Succeed())
		gitIn(work, "add", "ci/hook.sh")
		gitIn(work, "commit", "-q", "-m", "hook")
		gitIn(work, "commit", "-q", "--allow-empty", "-m", "a second, so main is more than one commit deep")
		gitIn(work, "push", "-q", "-f", remote, "HEAD:refs/heads/trunk")
		admit()
		v := check()[0]
		cand := filepath.Join(sources(), "candidate")
		code, _, errw := call("in", map[string]any{"source": source, "version": v}, cand)
		Expect(code).To(Equal(0), errw)
		Expect(gitIn(cand, "status", "--porcelain")).To(BeEmpty(), "the get's .mq is no change of the checkout's")
		gitIn(sources(), "clone", "-q", "-b", "trunk", remote, "main")
		step, err := filepath.Abs("../../example/hook-step.sh")
		Expect(err).NotTo(HaveOccurred())
		cmd := exec.Command("bash", step)
		cmd.Dir, cmd.Env = sources(), append(os.Environ(), "HOOK_SCRIPT=ci/hook.sh", "HOOK_TIMEOUT=5", "GIT_CONFIG_GLOBAL="+os.DevNull)
		out, err := cmd.CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), string(out))
		Expect(gitIn(cand, "rev-parse", "HEAD~1")).To(Equal(v["candidate"]))
		Expect(gitIn(cand, "diff", "--name-only", v["candidate"], "HEAD")).To(Equal("gen"))
	})

	It("A pass with a hook dir holding no bundle records the verdict only", func() {
		admit()
		v := check()[0]
		get(v)
		Expect(os.MkdirAll(filepath.Join(sources(), "hook"), 0o700)).To(Succeed())
		code, errw := put("pass", "hook")
		Expect(code).To(Equal(0), errw)
		Expect(errw).To(ContainSubstring("warning: the hook dir holds no bundle"))
		Expect(refs()).To(Equal("refs/mq/verdicts/" + v["run"]))
	})

	It("A hook dir holding more than one bundle is refused, and nothing is recorded", func() {
		admit()
		v := check()[0]
		hooked(get(v), v["candidate"], 1)
		gitIn(filepath.Join(sources(), "run"), "bundle", "create", "-q", filepath.Join(sources(), "hook", "h2.bundle"), "HEAD", "^"+v["candidate"])
		code, errw := put("pass", "hook")
		Expect(code).To(Equal(1))
		Expect(errw).To(ContainSubstring("more than one"))
		Expect(refs()).To(BeEmpty())
	})

	It("A hook bundle with more than one ref is refused, and nothing is recorded", func() {
		admit()
		v := check()[0]
		dest := get(v)
		hooked(dest, v["candidate"], 1)
		gitIn(dest, "update-ref", "refs/heads/other", "HEAD")
		gitIn(dest, "bundle", "create", "-q", filepath.Join(sources(), "hook", "hook.bundle"), "refs/heads/hooked", "refs/heads/other", "^"+v["candidate"])
		code, errw := put("pass", "hook")
		Expect(code).To(Equal(1))
		Expect(errw).To(ContainSubstring("one ref"))
		Expect(refs()).To(BeEmpty())
	})

	Context("when main has a hook script", func() {
		BeforeEach(func() {
			work := filepath.Join(dir, "work")
			gitIn(work, "checkout", "-q", "--detach", base)
			Expect(os.WriteFile(filepath.Join(work, "hook.sh"), []byte("#!/bin/sh\n[ \"$1\" = owned ] && echo gen\n"), 0o755)).To(Succeed())
			gitIn(work, "add", "hook.sh")
			gitIn(work, "commit", "-q", "-m", "hook script")
			base = gitIn(work, "rev-parse", "HEAD")
			gitIn(work, "push", "-q", "-f", remote, base+":refs/heads/trunk")
			source["config"] = source["config"].(string) + "compose: {hook_script: hook.sh}\n"
		})

		It("The queue lands the hook's commit after its test job records a pass with it", func() {
			admit()
			v := check()[0]
			commit := hooked(get(v), v["candidate"], 1)
			code, errw := put("pass", "hook")
			Expect(code).To(Equal(0), errw)
			Expect(check()).To(BeEmpty())
			Expect(gitIn(remote, "rev-parse", "refs/heads/trunk")).To(Equal(commit))
			Expect(snapshot().Landed).To(HaveKey("a"))
		})

		It("A hook commit that changes a file the hook does not own never lands and never ejects", func() {
			admit()
			v := check()[0]
			hookedIn(get(v), v["candidate"], "b", 1)
			code, errw := put("pass", "hook")
			Expect(code).To(Equal(0), errw)
			code, _, errw = call("check", map[string]any{"source": source})
			Expect(errw).To(ContainSubstring("does not own"))
			Expect(code).To(Equal(1), "a land error")
			Expect(gitIn(remote, "rev-parse", "refs/heads/trunk")).To(Equal(base))
			s := snapshot()
			Expect(s.Landed).To(BeEmpty())
			Expect(s.Ejected).To(BeEmpty())
		})
	})

	It("A hook commit that is not one commit on the candidate is refused, and nothing is recorded", func() {
		admit()
		v := check()[0]
		dest := get(v)
		hooked(dest, v["candidate"], 2)
		code, errw := put("pass", "hook")
		Expect(code).To(Equal(1))
		Expect(errw).To(ContainSubstring("only parent is the candidate"))
		hooked(filepath.Join(dir, "work"), base, 1)
		code, _ = put("pass", "hook")
		Expect(code).To(Equal(1))
		Expect(refs()).To(BeEmpty())
	})

	It("refuses a private key without known hosts, and never prints the key", func() {
		source["private_key"] = "-----BEGIN KEY-----\nS3CRET\n-----END KEY-----"
		code, out, errw := call("check", map[string]any{"source": source})
		Expect(code).To(Equal(1))
		Expect(errw).To(ContainSubstring("known_hosts"))
		Expect(string(out) + errw).NotTo(ContainSubstring("S3CRET"))
	})

	It("A source with no known mode is refused and never runs the queue", func() {
		admit()
		for _, mode := range []any{nil, "queue"} {
			source["mode"] = mode
			code, _, errw := call("check", map[string]any{"source": source})
			Expect(code).To(Equal(1))
			Expect(errw).To(ContainSubstring("source.mode"))
		}
		Expect(gitIn(remote, "for-each-ref", "refs/queue/state", "refs/mq/")).To(BeEmpty())
	})

	It("checks the verdict source without running the queue, and refuses a put to the candidate source", func() {
		admit()
		source["mode"] = "verdict"
		Expect(check()).To(BeEmpty())
		Expect(gitIn(remote, "for-each-ref", "refs/queue/state", "refs/mq/")).To(BeEmpty())
		source["mode"] = "candidate"
		code, _, errw := call("out", map[string]any{"source": source, "params": map[string]any{"verdict": "pass", "run_dir": "run"}}, sources())
		Expect(code).To(Equal(1))
		Expect(errw).To(ContainSubstring("verdict"))
	})

	It("refuses an unknown command name", func() {
		code, _, errw := call("bogus", map[string]any{"source": source})
		Expect(code).To(Equal(1))
		Expect(errw).To(ContainSubstring("check, in or out"))
	})
})
