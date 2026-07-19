package gitcheck

// State values mirror the outcome merge-state vocabulary (§1.11) without
// importing agent/api/outcomes (gitcheck is a leaf package).
const (
	StateMerged          = "merged"
	StateMergedWithFixes = "merged_with_fixes"
)

// Result is a detected merge fact (nil from Detect means "still open").
type Result struct {
	State             string // StateMerged | StateMergedWithFixes
	MergedSha         string
	HumanCommitCount  int
	HumanLinesAdded   int
	HumanLinesDeleted int
}

// Detect runs the §1.11.1 heuristics against a freshly-fetched mirror:
// ancestor-primary, then patch-id squash fallback. Returns nil when the
// branch is neither reachable nor patch-id-matched (stays open) — an
// edited-squash/rebase is never guessed at.
func (m *Mirror) Detect(base, pushed, branch, target string, scanLimit int) (*Result, error) {
	// Primary: reachability.
	anc, err := m.IsAncestor(pushed, target)
	if err != nil {
		return nil, err
	}
	if anc {
		mp, err := m.MergePoint(pushed, target)
		if err != nil {
			return nil, err
		}
		// §1.11.1 fast-forward refinement: MergePoint only knows the target
		// branch, so it falls back to pushed for a fast-forward. Detect owns
		// the agent branch name, so it resolves the branch's remote head as
		// tip-at-merge (the delta window covers human commits that fast-
		// forwarded onto the branch before it merged); if the branch was
		// deleted, the documented fallback is pushed. True merges (a merge
		// commit exists) already carry the correct second-parent tip.
		tip := mp.TipAtMerge
		mergedSha := mp.MergedSha
		if mp.FastForwarded {
			if head, err := m.run(m.dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err == nil && head != "" {
				tip = head
				mergedSha = head // §1.11.1: merged_sha = tip-at-merge for a pure ff
			}
		}
		delta, err := m.HumanDelta(pushed, tip)
		if err != nil {
			return nil, err
		}
		return resultFrom(mergedSha, delta), nil
	}

	// Squash fallback (needs a known base).
	if base != "" {
		branchTip := branch
		if tip, err := m.run(m.dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err == nil && tip != "" {
			branchTip = tip
		}
		match, err := m.PatchIDMatch(base, branchTip, target, scanLimit)
		if err != nil {
			return nil, err
		}
		if match.Found {
			// Human delta still measured on the agent branch (pushed..tip).
			delta, err := m.HumanDelta(pushed, branchTip)
			if err != nil {
				return nil, err
			}
			return resultFrom(match.Sha, delta), nil
		}
	}

	return nil, nil // still open — the honest v1 answer
}

func resultFrom(mergedSha string, d Delta) *Result {
	r := &Result{
		State:             StateMerged,
		MergedSha:         mergedSha,
		HumanCommitCount:  d.CommitCount,
		HumanLinesAdded:   d.LinesAdded,
		HumanLinesDeleted: d.LinesDeleted,
	}
	if d.CommitCount > 0 {
		r.State = StateMergedWithFixes
	}
	return r
}

// DiffFile is one file's unified-diff patch in a windowed diff.
type DiffFile struct {
	Path      string `json:"path"`
	Patch     string `json:"patch"`
	Truncated bool   `json:"truncated,omitempty"`
}

// DiffPage is a file-windowed diff (§1.11.1 diff API contract).
type DiffPage struct {
	Files      []DiffFile `json:"files"`
	Offset     int        `json:"offset"`
	Limit      int        `json:"limit"`
	TotalFiles int        `json:"total_files"`
	HasMore    bool       `json:"has_more"`
}

// perFilePatchCap bounds any single file's patch (§1.11.1: 64 KiB).
const perFilePatchCap = 64 << 10

// FileDiff returns the base..pushed unified diff windowed to [offset, offset+limit)
// files, each capped at perFilePatchCap bytes.
func (m *Mirror) FileDiff(base, pushed string, offset, limit int) (DiffPage, error) {
	if limit <= 0 {
		limit = 50
	}
	names, err := m.run(m.dir, "diff", "--name-only", base+".."+pushed)
	if err != nil {
		return DiffPage{}, err
	}
	all := nonEmptyLines(names)
	page := DiffPage{Offset: offset, Limit: limit, TotalFiles: len(all)}
	if offset >= len(all) {
		page.Files = []DiffFile{}
		return page, nil
	}
	end := offset + limit
	if end > len(all) {
		end = len(all)
	}
	page.HasMore = end < len(all)
	for _, path := range all[offset:end] {
		patch, err := m.run(m.dir, "diff", base+".."+pushed, "--", path)
		if err != nil {
			return DiffPage{}, err
		}
		df := DiffFile{Path: path, Patch: patch}
		if len(df.Patch) > perFilePatchCap {
			df.Patch = df.Patch[:perFilePatchCap] + "\n... [diff truncated]\n"
			df.Truncated = true
		}
		page.Files = append(page.Files, df)
	}
	return page, nil
}
