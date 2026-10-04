package git_test

import (
	"context"
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

var _ = Describe("Store", func() {
	ctx := context.Background()
	var store *git.Store
	var now time.Time
	at := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	entry := func(id string) core.Entry {
		return core.Entry{ID: id, Commit: id + "-sha", Ref: "change/" + id, AdmittedAt: at}
	}

	BeforeEach(func() {
		remote := filepath.Join(GinkgoT().TempDir(), "remote.git")
		out, err := exec.Command("git", "init", "-q", "--bare", remote).CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), string(out))
		c := config.Defaults()
		c.Repository.URI = remote
		store = git.NewStore(c)
		now = at
		store.Now = func() time.Time { return now }
	})

	load := func() core.Snapshot {
		s, err := store.Load(ctx)
		Expect(err).NotTo(HaveOccurred())
		return s
	}
	acquire := func(owner string) core.Lease {
		l, err := store.Acquire(ctx, owner, time.Minute)
		Expect(err).NotTo(HaveOccurred())
		return l
	}
	save := func(token uint64, s core.Snapshot) string {
		v, err := store.Save(ctx, token, s)
		Expect(err).NotTo(HaveOccurred())
		return v
	}

	It("An empty repository loads as an empty queue", func() {
		Expect(load()).To(Equal(core.Snapshot{}))
	})

	It("State saved by one owner loads back identically", func() {
		l := acquire("one")
		want := core.Snapshot{
			Version:  load().Version,
			Queued:   []core.Entry{entry("a"), entry("b")},
			BuildsOn: map[string][]string{"b": {"a"}},
			Landed:   map[string]bool{"x": true},
			Ejected:  map[string]bool{"y": true},
			InFlight: []core.Flight{{Run: core.Run{ID: "t1.1-r", Entries: []core.Entry{entry("a")}}, Candidate: "c1"}},
			Paused:   true,
			Why:      "no verdict",
			Landing:  &core.Landing{Main: "main", Candidate: "c1", Entries: []core.Entry{entry("a")}, Fence: l.Token<<32 | 1},
			Fence:    l.Token<<32 | 1,
		}
		v := save(l.Token, want)
		want.Version = v
		Expect(load()).To(Equal(want))
	})

	It("A save from an old version is refused and the state is unchanged", func() {
		l := acquire("one")
		first := save(l.Token, core.Snapshot{Version: load().Version, Queued: []core.Entry{entry("a")}})
		second := core.Snapshot{Version: first, Queued: []core.Entry{entry("b")}}
		second.Version = save(l.Token, second)
		_, err := store.Save(ctx, l.Token, core.Snapshot{Version: first, Queued: []core.Entry{entry("c")}})
		Expect(err).To(MatchError(ContainSubstring("stale")))
		Expect(load()).To(Equal(second))
	})

	It("A save under an older lease is refused even when the version matches", func() {
		old := acquire("one")
		kept := core.Snapshot{Version: load().Version, Queued: []core.Entry{entry("a")}}
		kept.Version = save(old.Token, kept)
		now = now.Add(2 * time.Minute)
		Expect(acquire("two").Token).To(BeNumerically(">", old.Token))
		_, err := store.Save(ctx, old.Token, core.Snapshot{Version: load().Version})
		Expect(err).To(MatchError(ContainSubstring("lease token 2")))
		Expect(load()).To(Equal(kept))
	})

	It("A second owner cannot take a live lease but takes over an expired one with a higher token", func() {
		first := acquire("one")
		_, err := store.Acquire(ctx, "two", time.Minute)
		Expect(err).To(MatchError(And(ContainSubstring(`"one"`), ContainSubstring(first.Expires.Format(time.RFC3339)))))
		now = first.Expires.Add(time.Second)
		second := acquire("two")
		Expect(second.Owner).To(Equal("two"))
		Expect(second.Token).To(BeNumerically(">", first.Token))
	})

	It("The same owner renews its lease and keeps its token", func() {
		first := acquire("one")
		s := core.Snapshot{Version: load().Version, Queued: []core.Entry{entry("a")}}
		s.Version = save(first.Token, s)
		now = now.Add(30 * time.Second)
		renewed := acquire("one")
		Expect(renewed.Token).To(Equal(first.Token))
		Expect(renewed.Expires).To(BeTemporally(">", first.Expires))
		Expect(load().Version).To(Equal(s.Version))
		s.Queued = append(s.Queued, entry("b"))
		save(renewed.Token, s)
	})

	It("A save that cannot be encoded fails and leaves the stored state intact", func() {
		l := acquire("one")
		kept := core.Snapshot{Version: load().Version, Queued: []core.Entry{entry("keep")}}
		kept.Version = save(l.Token, kept)
		bad := core.Snapshot{Version: kept.Version, Queued: []core.Entry{entry("keep"), {ID: "bad", AdmittedAt: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}}}
		_, err := store.Save(ctx, l.Token, bad)
		Expect(err).To(HaveOccurred())
		Expect(load()).To(Equal(kept))
		Expect(acquire("one").Token).To(Equal(l.Token))
	})

	It("A takeover at the largest token is refused instead of wrapping", func() {
		git := func(stdin string, args ...string) string {
			cmd := exec.Command("git", append([]string{"-C", store.Remote, "-c", "user.name=t", "-c", "user.email=t@localhost"}, args...)...)
			cmd.Stdin = strings.NewReader(stdin)
			out, err := cmd.CombinedOutput()
			Expect(err).NotTo(HaveOccurred(), string(out))
			return strings.TrimSpace(string(out))
		}
		snap := git("{}", "hash-object", "-w", "--stdin")
		lease := git(`{"Owner":"one","Token":18446744073709551615,"Expires":"2026-10-03T12:00:00Z"}`, "hash-object", "-w", "--stdin")
		tree := git("100644 blob "+lease+"\tlease.json\n100644 blob "+snap+"\tsnapshot.json\n", "mktree")
		git("", "update-ref", store.Ref, git("", "commit-tree", tree, "-m", "seed"))
		now = at.Add(time.Hour)
		_, err := store.Acquire(ctx, "two", time.Minute)
		Expect(err).To(MatchError(ContainSubstring("overflow")))
	})
})
