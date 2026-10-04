package git

import (
	"context"
	"fmt"
	"strings"

	"github.com/concourse/concourse/queue/core"
)

// RecordPassHooked records a pass for the run's candidate together with the
// commit the git bundle at bundle holds, at HookedRef(candidate), in one atomic
// push; both refs are create-only. The bundle must name one ref, of any name,
// at one commit whose only parent is the candidate.
func (r *Runner) RecordPassHooked(ctx context.Context, id, candidate, bundle string) error {
	if !fullSHA.MatchString(candidate) {
		return fmt.Errorf("candidate %q is not a full commit sha", candidate)
	}
	return runnerDo(ctx, func(dir string) error {
		if _, err := storeGit(ctx, dir, "", "fetch", "-q", "--no-tags", "--depth=1", r.Remote, candidate); err != nil {
			return err
		}
		heads, err := storeGit(ctx, dir, "", "bundle", "unbundle", bundle)
		if err != nil {
			return fmt.Errorf("the hook bundle cannot be read: %w", err)
		}
		var refs []string
		for line := range strings.SplitSeq(heads, "\n") {
			if f := strings.Fields(line); len(f) > 0 {
				refs = append(refs, f[0])
			}
		}
		if len(refs) != 1 || !fullSHA.MatchString(refs[0]) {
			return fmt.Errorf("the hook bundle must name one ref, not %d", len(refs))
		}
		hooked := refs[0]
		if same, err := r.recorded(ctx, dir, id, candidate, core.Pass); err != nil {
			return err
		} else if same { // a retried put: accepted only with the same hook commit
			out, err := storeGit(ctx, dir, "", "ls-remote", r.Remote, HookedRef(candidate))
			if err != nil {
				return err
			}
			if f := strings.Fields(out); len(f) != 2 || f[0] != hooked {
				return fmt.Errorf("another hook commit for candidate %s is already recorded", candidate)
			}
			return nil
		}
		parents, err := storeGit(ctx, dir, "", "rev-list", "--parents", "-n", "1", hooked)
		if err != nil || parents != hooked+" "+candidate {
			return fmt.Errorf("the hook commit must be one commit whose only parent is the candidate %s", candidate)
		}
		tree, err := storeGit(ctx, dir, "", "mktree")
		if err != nil {
			return err
		}
		obj, err := storeGit(ctx, dir, "", "commit-tree", "--no-gpg-sign", tree, "-m", "candidate "+candidate+"\nverdict "+string(core.Pass))
		if err != nil {
			return err
		}
		if _, err = storeGit(ctx, dir, "", "push", "-q", "--atomic", "--force-with-lease="+verdictRef(id)+":", "--force-with-lease="+HookedRef(candidate)+":",
			r.Remote, obj+":"+verdictRef(id), hooked+":"+HookedRef(candidate)); err != nil {
			return fmt.Errorf("a verdict or hook commit for run %q is already recorded, or the push failed: %w", id, err)
		}
		return nil
	})
}
