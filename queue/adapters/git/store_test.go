package git_test

import (
	"context"
	"errors"
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

	It("Free text in a snapshot is redacted as it is written, even text saved before its secret was known", func() {
		const token = "Tk5mQw2zRb9x"
		l := acquire("one")
		snap := load()
		snap.Paused, snap.Why, snap.Commits = true, "denied Bearer "+token, map[string]string{"a": "c"}
		snap.Settled = []core.SettleRecord{{ID: "a", Kind: core.EjectedEvent, Why: "red: " + token, Cause: token}}
		snap.Refused = []core.Refusal{{ID: "b", Commit: "d", Why: "refused " + token}}
		save(l.Token, snap)
		core.Secrets.Add(token)
		acquire("one") // a renewal rewrites the loaded snapshot
		out, err := exec.Command("git", "-C", store.Remote, "show", "refs/queue/state:snapshot.json").CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), string(out))
		Expect(string(out)).To(ContainSubstring("denied"))
		Expect(string(out)).NotTo(ContainSubstring(token))
	})
	It("A secret equal to a JSON key or token never corrupts the saved state", func() {
		for _, secret := range []string{"Queued", "null", "true", "12345"} {
			old := core.Secrets
			core.Secrets = &core.SecretSet{}
			l := acquire("one")
			snap := load()
			snap.Queued, snap.Landed = []core.Entry{entry("a"), entry("b")}, map[string]bool{"c": true}
			snap.Paused, snap.Why, snap.Fence = true, "said "+secret, 123456
			save(l.Token, snap)
			core.Secrets.Add(secret)
			_, err := store.Acquire(ctx, "one", time.Minute) // a renewal rewrites the loaded snapshot
			got, lerr := store.Load(ctx)
			core.Secrets = old
			Expect(errors.Join(err, lerr)).NotTo(HaveOccurred(), secret)
			Expect(got.Queued).To(Equal(snap.Queued), secret)
			Expect(got.Landed).To(Equal(snap.Landed), secret)
			Expect(got.Fence).To(Equal(snap.Fence), secret)
			Expect(got.Why).To(Equal("said ***"), secret)
		}
	})

	It("A change id equal to a configured secret survives a reload", func() {
		old := core.Secrets
		core.Secrets = &core.SecretSet{}
		DeferCleanup(func() { core.Secrets = old })
		core.Secrets.Add("fake-id")
		l := acquire("one")
		snap := load()
		snap.Queued = []core.Entry{entry("fake-id")}
		snap.BuildsOn = map[string][]string{"fake-id": {"parent"}}
		snap.Ejected = map[string]bool{"parent": true}
		snap.Settled = []core.SettleRecord{{ID: "fake-id", Kind: core.EjectedEvent, Why: "fake-id failed", Cause: "fake-id"}}
		snap.Refused = []core.Refusal{{ID: "fake-id", Commit: "fake-id", Why: "refused fake-id"}}
		save(l.Token, snap)
		_, err := store.Acquire(ctx, "one", time.Minute) // a renewal rewrites the loaded snapshot
		Expect(err).NotTo(HaveOccurred())
		got := load()
		Expect(got.Queued).To(Equal(snap.Queued))
		Expect(got.BuildsOn).To(Equal(snap.BuildsOn))
		Expect(got.Refused).To(Equal([]core.Refusal{{ID: "fake-id", Commit: "fake-id", Why: "refused ***"}}))
		Expect(got.Settled[0].ID).To(Equal("fake-id"))
		Expect([]string{got.Settled[0].Why, got.Settled[0].Cause}).To(Equal([]string{"*** failed", "***"}))
		_, settle := (&core.Serial{Max: 3}).Plan(core.View{Queued: got.Queued, BuildsOn: got.BuildsOn, Ejected: got.Ejected, Landed: got.Landed, Slots: 1})
		Expect(settle).To(HaveLen(1), "the child is ejected, not run")
		Expect(settle[0].Cause).To(Equal(core.ParentEjected))
		Expect(settle[0].Entries).To(Equal(snap.Queued))
	})
})
