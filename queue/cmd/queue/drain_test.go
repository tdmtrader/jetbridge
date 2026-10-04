package main

import (
	"bytes"
	"context"
	"encoding/json"
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

var _ = Describe("queue drain", func() {
	var file string
	var store *git.Store
	ctx := context.Background()
	sha := func(c string) string { return strings.Repeat(c, 40) }
	e := func(id, c string) core.Entry { return core.Entry{ID: id, Commit: sha(c)} }
	drain := func() (int, string) {
		var o, errw bytes.Buffer
		code := run(ctx, []string{"drain", "--config", file}, &o, &errw)
		return code, o.String()
	}
	save := func(token uint64, s core.Snapshot) {
		cur, err := store.Load(ctx)
		Expect(err).NotTo(HaveOccurred())
		s.Version = cur.Version
		_, err = store.Save(ctx, token, s)
		Expect(err).NotTo(HaveOccurred())
	}
	BeforeEach(func() {
		dir := GinkgoT().TempDir()
		remote := filepath.Join(dir, "remote.git")
		Expect(exec.Command("git", "init", "-q", "--bare", remote).Run()).To(Succeed())
		file = filepath.Join(dir, "queue.yaml")
		Expect(os.WriteFile(file, fmt.Appendf(nil, sample, remote, dir), 0o600)).To(Succeed())
		c, err := config.Parse(fmt.Appendf(nil, sample, remote, dir))
		Expect(err).NotTo(HaveOccurred())
		store = git.NewStore(c)
	})

	It("An empty queue drains to an idle push and no lease", func() {
		code, out := drain()
		Expect(code).To(Equal(0))
		Expect(out).To(Equal("PUSH idle\nLEASE none\n"))
	})

	It("Drain lists the rows in flight before the queued ones, in queue order", func() {
		save(0, core.Snapshot{
			InFlight: []core.Flight{{Run: core.Run{ID: "run1", Entries: []core.Entry{e("x", "1"), e("y", "2")}}}},
			Queued:   []core.Entry{e("b", "b"), e("a", "a")},
		})
		code, out := drain()
		Expect(code).To(Equal(0))
		Expect(out).To(Equal("ROW x " + sha("1") + " inflight\nROW y " + sha("2") + " inflight\nROW b " + sha("b") + " queued\nROW a " + sha("a") + " queued\nPUSH idle\nLEASE none\n"))
	})

	It("Drain says a push is mid-flight while a land is in progress", func() {
		save(0, core.Snapshot{Landing: &core.Landing{Main: "main", Candidate: sha("c"), Entries: []core.Entry{e("x", "1")}}})
		code, out := drain()
		Expect(code).To(Equal(0))
		Expect(out).To(Equal("ROW x " + sha("1") + " inflight\nPUSH mid-flight\nLEASE none\n"))
	})

	It("Drain names the lease holder and the run it drives, and no holder once it expired", func() {
		l, err := store.Acquire(ctx, "driver-1", time.Hour)
		Expect(err).NotTo(HaveOccurred())
		save(l.Token, core.Snapshot{InFlight: []core.Flight{{Run: core.Run{ID: "run7", Entries: []core.Entry{e("x", "1")}}}}})
		code, out := drain()
		Expect(code).To(Equal(0))
		Expect(out).To(HaveSuffix("PUSH idle\nLEASE driver-1 run7\n"))
		store.Now = func() time.Time { return time.Now().Add(-2 * time.Hour) }
		_, err = store.Acquire(ctx, "driver-1", time.Hour) // renewed to expire an hour ago
		Expect(err).NotTo(HaveOccurred())
		_, out = drain()
		Expect(out).To(HaveSuffix("PUSH idle\nLEASE none\n"))
	})

	It("Drain reads the resource's source, from a file or on stdin, as well as a config file", func() {
		save(0, core.Snapshot{Queued: []core.Entry{e("a", "a")}})
		cfg, err := os.ReadFile(file)
		Expect(err).NotTo(HaveOccurred())
		src, err := json.Marshal(map[string]any{"source": map[string]string{"mode": "candidate", "config": string(cfg)}})
		Expect(err).NotTo(HaveOccurred())
		srcFile := filepath.Join(GinkgoT().TempDir(), "source.json")
		Expect(os.WriteFile(srcFile, src, 0o600)).To(Succeed())
		want := "ROW a " + sha("a") + " queued\nPUSH idle\nLEASE none\n"
		var o, errw bytes.Buffer
		Expect(run(ctx, []string{"drain", "--source", srcFile}, &o, &errw)).To(Equal(0), errw.String())
		Expect(o.String()).To(Equal(want))
		stdin := os.Stdin
		DeferCleanup(func() { os.Stdin = stdin })
		os.Stdin, err = os.Open(srcFile)
		Expect(err).NotTo(HaveOccurred())
		o.Reset()
		Expect(run(ctx, []string{"drain"}, &o, &errw)).To(Equal(0), errw.String())
		Expect(o.String()).To(Equal(want))
		_, out := drain()
		Expect(out).To(Equal(want))
		o.Reset()
		Expect(run(ctx, []string{"drain", "--source", srcFile, "--config", file}, &o, &errw)).NotTo(Equal(0))
		Expect(o.String()).To(BeEmpty())
	})

	It("An unreadable store drains nothing and fails", func() {
		Expect(os.WriteFile(file, fmt.Appendf(nil, sample, filepath.Join(GinkgoT().TempDir(), "missing.git"), GinkgoT().TempDir()), 0o600)).To(Succeed())
		code, out := drain()
		Expect(code).NotTo(Equal(0))
		Expect(out).To(BeEmpty())
	})
})
