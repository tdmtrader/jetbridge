package outcomewatcher

import (
	"strings"
	"sync"

	"github.com/concourse/concourse/agent/gitcheck"
)

// Auth aliases gitcheck.Auth so callers configure one type.
type Auth = gitcheck.Auth

// MirrorCache opens one bare --mirror per repo under a shared dir. The outcome
// watcher uses it for polling and detection; GetAgentTicketDiff uses it only
// as a compatibility fallback for deliveries predating stored diff evidence.
type MirrorCache struct {
	dir         string
	urlTemplate string
	auth        Auth
	scanLimit   int

	mu      sync.Mutex
	mirrors map[string]*gitcheck.Mirror
}

// NewMirrorCache takes positional args (dir, urlTemplate, auth, scanLimit).
// urlTemplate resolves clone URLs by replacing {repo} with the ticket's
// repo slug (production default: https://github.com/{repo}.git).
func NewMirrorCache(dir, urlTemplate string, auth Auth, scanLimit int) *MirrorCache {
	if scanLimit <= 0 {
		scanLimit = 200
	}
	return &MirrorCache{
		dir: dir, urlTemplate: urlTemplate, auth: auth, scanLimit: scanLimit,
		mirrors: map[string]*gitcheck.Mirror{},
	}
}

func (c *MirrorCache) urlFor(repo string) string {
	return strings.ReplaceAll(c.urlTemplate, "{repo}", repo)
}

// mirror returns the (lazily-cloned) mirror for repo.
func (c *MirrorCache) mirror(repo string) (*gitcheck.Mirror, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if m, ok := c.mirrors[repo]; ok {
		return m, nil
	}
	m, err := gitcheck.OpenMirror(c.dir, repo, c.urlFor(repo), c.auth)
	if err != nil {
		return nil, err
	}
	c.mirrors[repo] = m
	return m, nil
}

// Sync refreshes repo's mirror — one `git fetch --prune` (cloning lazily on
// first use). The watcher dedups this to at most once per repo per tick.
func (c *MirrorCache) Sync(repo string) error {
	m, err := c.mirror(repo)
	if err != nil {
		return err
	}
	return m.Fetch()
}

// Detect runs the frozen §1.11.1 heuristics against the already-Synced mirror.
func (c *MirrorCache) Detect(repo, base, pushed, branch, target string) (*gitcheck.Result, error) {
	m, err := c.mirror(repo)
	if err != nil {
		return nil, err
	}
	return m.Detect(base, pushed, branch, target, c.scanLimit)
}

// BranchHead returns the remote head of branch ("" when the ref is absent)
// — the seedRows backstop's fallback pushed_sha source.
func (c *MirrorCache) BranchHead(repo, branch string) (string, error) {
	m, err := c.mirror(repo)
	if err != nil {
		return "", err
	}
	return m.BranchHead(branch)
}

// Diff implements outcomes.MirrorProvider for historical compatibility
// (§1.11.1 windowed diff bounds are enforced by gitcheck).
func (c *MirrorCache) Diff(repo, base, pushed string, offset, limit int) (gitcheck.DiffPage, error) {
	m, err := c.mirror(repo)
	if err != nil {
		return gitcheck.DiffPage{}, err
	}
	return m.FileDiff(base, pushed, offset, limit)
}
