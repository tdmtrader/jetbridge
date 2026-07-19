package outcomewatcher_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/agent/api/outcomes"
	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/outcomewatcher"
)

func TestOutcomeWatcher(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Outcome Watcher Suite")
}

var botEnv = []string{
	"GIT_AUTHOR_NAME=concourse-agent[bot]", "GIT_AUTHOR_EMAIL=agent@concourse.local",
	"GIT_COMMITTER_NAME=concourse-agent[bot]", "GIT_COMMITTER_EMAIL=agent@concourse.local",
}

var humanEnv = []string{
	"GIT_AUTHOR_NAME=Reviewer", "GIT_AUTHOR_EMAIL=rev@example.com",
	"GIT_COMMITTER_NAME=Reviewer", "GIT_COMMITTER_EMAIL=rev@example.com",
}

func git(dir string, args ...string) string {
	return gitEnv(dir, botEnv, args...)
}

func gitEnv(dir string, env []string, args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

var _ = Describe("outcomewatcher", func() {
	const repo = "tdmtrader/concourse"
	const branch = "agent/ticket-1"

	var (
		tmp, seed, base, pushed string
		ticketStore             *tickets.MemoryStore
		outcomeStore            *outcomes.MemoryStore
		cache                   *outcomewatcher.MirrorCache
		watcher                 *outcomewatcher.Watcher
		ticketID                int
	)

	// seedHarvest simulates harvest's push-time outcome seeding
	// (exec.HarvestStep.seedOutcome → outcomes.Store.Ensure) — the
	// AUTHORITATIVE sha source per §1.11.1. The watcher itself is
	// metrics-independent by design (amendment C3-1): it never reads
	// agent_run_metrics for shas, whether or not harvest writes them.
	seedHarvest := func(pushedSha, baseSha string) {
		ExpectWithOffset(1, outcomeStore.Ensure(&outcomes.Outcome{
			TicketID: ticketID, Repo: repo, Branch: branch,
			PushedSha: pushedSha, BaseSha: baseSha,
		})).To(Succeed())
	}

	BeforeEach(func() {
		tmp = GinkgoT().TempDir()
		// bare origin laid out so the {repo} url template resolves to it
		bare := filepath.Join(tmp, "repos", repo+".git")
		Expect(os.MkdirAll(bare, 0o755)).To(Succeed())
		git(bare, "init", "--bare", "--initial-branch=main")
		seed = filepath.Join(tmp, "seed")
		git(tmp, "clone", bare, seed)
		Expect(os.WriteFile(filepath.Join(seed, "base.txt"), []byte("base\n"), 0o644)).To(Succeed())
		git(seed, "add", ".")
		git(seed, "commit", "-m", "base")
		git(seed, "push", "origin", "HEAD:main")
		base = git(seed, "rev-parse", "HEAD")

		// agent work on a branch
		Expect(os.WriteFile(filepath.Join(seed, "f.go"), []byte("package f\n"), 0o644)).To(Succeed())
		git(seed, "add", ".")
		git(seed, "commit", "-m", "agent work")
		pushed = git(seed, "rev-parse", "HEAD")
		git(seed, "push", "origin", "HEAD:refs/heads/"+branch)

		ticketStore = tickets.NewMemoryStore()
		outcomeStore = outcomes.NewMemoryStore()

		// ticket at needs_review with branch set
		id, err := ticketStore.Create(&tickets.Ticket{Title: "x", Repo: repo, TargetBranch: "main"})
		Expect(err).NotTo(HaveOccurred())
		ticketID = id
		Expect(ticketStore.Transition(id, tickets.StateDraft, tickets.StateQueued, tickets.TransitionMeta{})).To(Succeed())
		Expect(ticketStore.Transition(id, tickets.StateQueued, tickets.StateRunning, tickets.TransitionMeta{})).To(Succeed())
		Expect(ticketStore.Transition(id, tickets.StateRunning, tickets.StateNeedsReview, tickets.TransitionMeta{Branch: branch})).To(Succeed())

		cache = outcomewatcher.NewMirrorCache(
			filepath.Join(tmp, "cache"),
			filepath.Join(tmp, "repos")+"/{repo}.git",
			outcomewatcher.Auth{},
			200,
		)
		watcher = outcomewatcher.New(ticketStore, outcomeStore, cache)
	})

	It("keeps the harvest-seeded row (authoritative shas) and touches it on the first tick", func() {
		seedHarvest(pushed, base)
		Expect(watcher.Run(nil)).To(Succeed())
		o, found, _ := outcomeStore.Get(ticketID)
		Expect(found).To(BeTrue())
		Expect(o.PushedSha).To(Equal(pushed))
		Expect(o.BaseSha).To(Equal(base))
		Expect(o.MergeState).To(Equal(outcomes.MergeOpen))
		Expect(o.LastCheckedAt).To(BeNumerically(">", 0))
	})

	It("creates a fallback row when harvest did not seed one (backstop: branch-head pushed_sha, empty base_sha)", func() {
		Expect(watcher.Run(nil)).To(Succeed())
		o, found, _ := outcomeStore.Get(ticketID)
		Expect(found).To(BeTrue())
		Expect(o.PushedSha).To(Equal(pushed), "fallback pushed_sha is the remote branch head at first sync")
		Expect(o.BaseSha).To(BeEmpty(), "base is unknown until a re-push seeds it — the diff API 404s")
		Expect(o.MergeState).To(Equal(outcomes.MergeOpen))
	})

	It("does not clobber harvest-seeded shas with branch-head fallbacks", func() {
		// harvest seeded shas that deliberately differ from the branch head
		seedHarvest("aaa", "bbb")
		Expect(watcher.Run(nil)).To(Succeed())
		o, _, _ := outcomeStore.Get(ticketID)
		Expect(o.PushedSha).To(Equal("aaa"), "the backstop must never overwrite harvest-seeded shas")
		Expect(o.BaseSha).To(Equal("bbb"))
	})

	It("detects a merge and transitions the ticket through the store", func() {
		seedHarvest(pushed, base)
		// merge the branch to main (fast-forward) then run the watcher
		git(seed, "push", "origin", "HEAD:main")

		Expect(watcher.Run(nil)).To(Succeed()) // tick 1: seed/backstop
		Expect(watcher.Run(nil)).To(Succeed()) // tick 2: detect merge

		o, _, _ := outcomeStore.Get(ticketID)
		Expect(o.MergeState).To(Equal(outcomes.Merged))

		tk, _, _ := ticketStore.Get(ticketID)
		Expect(tk.State).To(Equal(tickets.StateMerged))
	})

	It("leaves an unmerged branch open", func() {
		seedHarvest(pushed, base)
		Expect(watcher.Run(nil)).To(Succeed())
		Expect(watcher.Run(nil)).To(Succeed())
		o, _, _ := outcomeStore.Get(ticketID)
		Expect(o.MergeState).To(Equal(outcomes.MergeOpen))
		tk, _, _ := ticketStore.Get(ticketID)
		Expect(tk.State).To(Equal(tickets.StateNeedsReview))
	})

	It("re-arms a sent-back row and detects the rework merge with the NEW shas (F6)", func() {
		// tick 1: the open row for the first push (harvest-seeded).
		seedHarvest(pushed, base)
		Expect(watcher.Run(nil)).To(Succeed())
		o, _, _ := outcomeStore.Get(ticketID)
		Expect(o.PushedSha).To(Equal(pushed))
		Expect(o.MergeState).To(Equal(outcomes.MergeOpen))

		// human sends the ticket back: disposition closes the row unmerged,
		// then the ticket walks sent_back → queued → running.
		Expect(outcomeStore.SetDisposition(ticketID, outcomes.DispositionInput{
			Disposition: outcomes.DispositionSentBack, Reason: "incomplete", By: "tdmtrader",
		})).To(Succeed())
		o, _, _ = outcomeStore.Get(ticketID)
		Expect(o.MergeState).To(Equal(outcomes.ClosedUnmerged))
		Expect(ticketStore.Transition(ticketID, tickets.StateNeedsReview, tickets.StateSentBack, tickets.TransitionMeta{})).To(Succeed())
		Expect(ticketStore.Transition(ticketID, tickets.StateSentBack, tickets.StateQueued, tickets.TransitionMeta{})).To(Succeed())
		Expect(ticketStore.Transition(ticketID, tickets.StateQueued, tickets.StateRunning, tickets.TransitionMeta{})).To(Succeed())

		// the re-worked branch: the re-dispatched agent pushes a NEW head (bot
		// commit) on top of the first push. Harvest re-seeds the row at push
		// time with the NEW shas — the primary path; its Ensure performs the
		// F6 re-arm. NOTE (F38): this rework commit becomes the row's new
		// pushed_sha, so it is NOT part of the human-touch delta — the delta
		// window is pushed_sha..tip-at-merge (§1.11.1). merged_with_fixes is
		// earned below by a separate human commit landing AFTER this push.
		newBase := pushed // the rework is based on the first push
		Expect(os.WriteFile(filepath.Join(seed, "g.go"), []byte("package g\n"), 0o644)).To(Succeed())
		git(seed, "add", ".")
		git(seed, "commit", "-m", "agent rework")
		reworked := git(seed, "rev-parse", "HEAD")
		Expect(reworked).NotTo(Equal(pushed))
		git(seed, "push", "origin", "HEAD:refs/heads/"+branch)
		seedHarvest(reworked, newBase) // harvest's push-time re-seed (authoritative)

		// ticket is back at needs_review. The branch is deliberately NOT merged
		// yet (F38): Run does seedRows then detectMerges in the SAME tick, so a
		// merge pushed before tick 2 would be detected on the re-arm tick and
		// the open-row assertions below would never observe the re-arm.
		Expect(ticketStore.Transition(ticketID, tickets.StateRunning, tickets.StateNeedsReview, tickets.TransitionMeta{Branch: branch})).To(Succeed())

		// tick 2: the row is re-armed to open with the NEW shas (harvest's
		// Ensure did the re-arm; the watcher's backstop leaves the open row
		// alone). detectMerges finds the branch still unmerged, so the row
		// stays open through the tick.
		Expect(watcher.Run(nil)).To(Succeed())
		o, _, _ = outcomeStore.Get(ticketID)
		Expect(o.MergeState).To(Equal(outcomes.MergeOpen), "row must be re-armed to open")
		Expect(o.PushedSha).To(Equal(reworked), "pushed_sha must be the reworked head")
		Expect(o.BaseSha).To(Equal(newBase))
		Expect(o.Disposition).To(BeEmpty(), "the send-back disposition must be cleared on re-arm")

		// NOW the human reviewer lands a fix commit on top of the reworked
		// push and merges to main. Only this commit sits inside pushed_sha..
		// tip-at-merge, so merged_with_fixes is genuinely earned
		// (human_commit_count > 0 with a non-empty line delta). Merging the
		// reworked head alone would be plain `merged` — an empty pushed_sha..
		// tip delta (§1.11.1). The fix is pushed to the agent branch before
		// the fast-forward merge (the landed C2 detector resolves a pure
		// ff's tip-at-merge to the agent branch's remote head); the row's
		// pushed_sha is NOT refreshed past the human commit because the
		// backstop never touches an open row.
		Expect(os.WriteFile(filepath.Join(seed, "g.go"), []byte("package g\n\nvar Fix = 1\n"), 0o644)).To(Succeed())
		git(seed, "add", ".")
		gitEnv(seed, humanEnv, "commit", "-m", "human fix before merge")
		git(seed, "push", "origin", "HEAD:refs/heads/"+branch)
		git(seed, "push", "origin", "HEAD:main")

		// tick 3: detect the merge on the re-armed row with a non-empty
		// pushed_sha..tip human-touch delta.
		Expect(watcher.Run(nil)).To(Succeed())
		o, _, _ = outcomeStore.Get(ticketID)
		Expect(o.MergeState).To(Equal(outcomes.MergedWithFixes))
		Expect(o.PushedSha).To(Equal(reworked))
		Expect(o.BaseSha).To(Equal(newBase))
		Expect(o.HumanCommitCount).To(Equal(1), "only the post-push human fix is in the delta window")
		Expect(o.HumanLinesAdded).To(BeNumerically(">", 0), "merged_with_fixes must carry a non-empty delta")
		tk, _, _ := ticketStore.Get(ticketID)
		Expect(tk.State).To(Equal(tickets.StateMergedWithFixes))
	})

	It("re-arms with fallback shas when harvest did not re-seed (F6 backstop)", func() {
		seedHarvest(pushed, base)
		Expect(watcher.Run(nil)).To(Succeed())

		// send-back closes the row; the ticket cycles back around.
		Expect(outcomeStore.SetDisposition(ticketID, outcomes.DispositionInput{
			Disposition: outcomes.DispositionSentBack, Reason: "incomplete", By: "tdmtrader",
		})).To(Succeed())
		Expect(ticketStore.Transition(ticketID, tickets.StateNeedsReview, tickets.StateSentBack, tickets.TransitionMeta{})).To(Succeed())
		Expect(ticketStore.Transition(ticketID, tickets.StateSentBack, tickets.StateQueued, tickets.TransitionMeta{})).To(Succeed())
		Expect(ticketStore.Transition(ticketID, tickets.StateQueued, tickets.StateRunning, tickets.TransitionMeta{})).To(Succeed())

		// the re-dispatched agent pushes a rework, but harvest does NOT
		// re-seed (crashed before seedOutcome, say).
		Expect(os.WriteFile(filepath.Join(seed, "g.go"), []byte("package g\n"), 0o644)).To(Succeed())
		git(seed, "add", ".")
		git(seed, "commit", "-m", "agent rework")
		reworked := git(seed, "rev-parse", "HEAD")
		git(seed, "push", "origin", "HEAD:refs/heads/"+branch)
		Expect(ticketStore.Transition(ticketID, tickets.StateRunning, tickets.StateNeedsReview, tickets.TransitionMeta{Branch: branch})).To(Succeed())

		// tick 2: the backstop re-arms the closed_unmerged/sent_back row with
		// FALLBACK shas — pushed = reworked branch head from the mirror,
		// base = '' (diff 404s until a re-push seeds it).
		Expect(watcher.Run(nil)).To(Succeed())
		o, _, _ := outcomeStore.Get(ticketID)
		Expect(o.MergeState).To(Equal(outcomes.MergeOpen), "row must be re-armed to open")
		Expect(o.PushedSha).To(Equal(reworked))
		Expect(o.BaseSha).To(BeEmpty())
		Expect(o.Disposition).To(BeEmpty())
	})

	It("skips merge-detection for a concluded ticket — the row closes as 'concluded' and never flips (flow-decoupling 2026-07-09)", func() {
		// tick 1: the open row for the pushed spike branch.
		seedHarvest(pushed, base)
		Expect(watcher.Run(nil)).To(Succeed())
		o, _, _ := outcomeStore.Get(ticketID)
		Expect(o.MergeState).To(Equal(outcomes.MergeOpen))

		// human concludes the spike: run finished, reviewed, no merge intended.
		// The disposition closes the row as 'concluded' (NOT closed_unmerged)
		// and the ticket takes the needs_review → concluded edge via the
		// single writer — exactly what the API handler does.
		Expect(outcomeStore.SetDisposition(ticketID, outcomes.DispositionInput{
			Disposition: outcomes.DispositionConcluded, Reason: "research_complete", By: "tdmtrader",
		})).To(Succeed())
		o, _, _ = outcomeStore.Get(ticketID)
		Expect(o.MergeState).To(Equal(outcomes.MergeConcluded))
		Expect(ticketStore.Transition(ticketID, tickets.StateNeedsReview, tickets.StateConcluded, tickets.TransitionMeta{})).To(Succeed())

		// someone merges the spike branch upstream anyway (it happens: a
		// reviewer cherry-picks the prototype). The outcome must NOT flip —
		// concluded never waits on, or reacts to, a branch merge.
		git(seed, "push", "origin", "HEAD:main")

		// tick 2: the concluded row is off the open work-list (ListOpen) and
		// the ticket is no longer needs_review (seedRows scans needs_review
		// only), so nothing is re-armed and nothing is detected.
		Expect(watcher.Run(nil)).To(Succeed())
		o, _, _ = outcomeStore.Get(ticketID)
		Expect(o.MergeState).To(Equal(outcomes.MergeConcluded), "concluded is terminal — merge-detection is skipped")
		Expect(o.Disposition).To(Equal(outcomes.DispositionConcluded), "the concluded disposition is never cleared")
		tk, _, _ := ticketStore.Get(ticketID)
		Expect(tk.State).To(Equal(tickets.StateConcluded), "the ticket never leaves concluded")
	})

	Describe("terminal sweep (writer reconciliation, amendment C3-3)", func() {
		It("closes an open row as closed_unmerged when the ticket was raw-transitioned to abandoned", func() {
			seedHarvest(pushed, base)
			Expect(watcher.Run(nil)).To(Succeed())
			// a bypassing writer abandons the ticket WITHOUT a disposition
			Expect(ticketStore.Transition(ticketID, tickets.StateNeedsReview, tickets.StateAbandoned, tickets.TransitionMeta{})).To(Succeed())

			Expect(watcher.Run(nil)).To(Succeed())
			o, _, _ := outcomeStore.Get(ticketID)
			Expect(o.MergeState).To(Equal(outcomes.ClosedUnmerged))
			// the sweep never invents disposition taxonomy
			Expect(o.Disposition).To(BeEmpty())
			Expect(o.DispositionReason).To(BeEmpty())
			Expect(o.DispositionNotes).To(BeEmpty())
			Expect(o.DisposedBy).To(BeEmpty())
		})

		It("closes an open row as concluded when the ticket was raw-transitioned to concluded", func() {
			seedHarvest(pushed, base)
			Expect(watcher.Run(nil)).To(Succeed())
			Expect(ticketStore.Transition(ticketID, tickets.StateNeedsReview, tickets.StateConcluded, tickets.TransitionMeta{})).To(Succeed())

			Expect(watcher.Run(nil)).To(Succeed())
			o, _, _ := outcomeStore.Get(ticketID)
			Expect(o.MergeState).To(Equal(outcomes.MergeConcluded))
			Expect(o.Disposition).To(BeEmpty())
		})

		It("does not sweep a merged ticket — organic detection later closes the row with a delta", func() {
			seedHarvest(pushed, base)
			Expect(watcher.Run(nil)).To(Succeed())
			// a human clicks the raw Merge button before the mirror sees it
			Expect(ticketStore.Transition(ticketID, tickets.StateNeedsReview, tickets.StateMerged, tickets.TransitionMeta{})).To(Succeed())

			Expect(watcher.Run(nil)).To(Succeed())
			o, _, _ := outcomeStore.Get(ticketID)
			Expect(o.MergeState).To(Equal(outcomes.MergeOpen), "merged tickets are left to organic detection")

			// the merge lands in the mirror; detection closes the row with
			// the full delta even though the ticket already left needs_review
			// (the state re-check only gates the TRANSITION).
			git(seed, "push", "origin", "HEAD:main")
			Expect(watcher.Run(nil)).To(Succeed())
			o, _, _ = outcomeStore.Get(ticketID)
			Expect(o.MergeState).To(Equal(outcomes.Merged))
			Expect(o.MergedSha).NotTo(BeEmpty())
			tk, _, _ := ticketStore.Get(ticketID)
			Expect(tk.State).To(Equal(tickets.StateMerged), "the ticket state is untouched")
		})

		It("leaves already-closed rows untouched", func() {
			seedHarvest(pushed, base)
			Expect(watcher.Run(nil)).To(Succeed())
			// disposition already closed the row before the ticket went terminal
			Expect(outcomeStore.SetDisposition(ticketID, outcomes.DispositionInput{
				Disposition: outcomes.DispositionAbandoned, Reason: "not_needed", By: "tdmtrader",
			})).To(Succeed())
			Expect(ticketStore.Transition(ticketID, tickets.StateNeedsReview, tickets.StateAbandoned, tickets.TransitionMeta{})).To(Succeed())

			Expect(watcher.Run(nil)).To(Succeed())
			o, _, _ := outcomeStore.Get(ticketID)
			Expect(o.MergeState).To(Equal(outcomes.ClosedUnmerged))
			Expect(o.Disposition).To(Equal(outcomes.DispositionAbandoned), "the human disposition survives the sweep")
			Expect(o.DispositionReason).To(Equal("not_needed"))
		})
	})

	It("serves the windowed diff via the MirrorProvider seam", func() {
		seedHarvest(pushed, base)
		Expect(watcher.Run(nil)).To(Succeed()) // fetches the mirror
		page, err := cache.Diff(repo, base, pushed, 0, 50)
		Expect(err).NotTo(HaveOccurred())
		Expect(page.TotalFiles).To(Equal(1))
		Expect(page.Files[0].Path).To(Equal("f.go"))
	})
})
