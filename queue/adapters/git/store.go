package git

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/concourse/concourse/queue/config"
	"github.com/concourse/concourse/queue/core"
)

// Store keeps the Snapshot and lease in one ref on Remote: every change is one
// push with --force-with-lease on the commit read. The commit has no parent
// and holds snapshot.json and lease.json; Version is snapshot.json's blob id,
// so a lease renewal never makes a loaded Snapshot stale.
type Store struct {
	Remote, Ref string
	Now         func() time.Time
}

func NewStore(c config.Config) *Store { return &Store{c.Repository.URI, c.Store.Ref, time.Now} }

type storeState struct {
	commit, version string
	snap            core.Snapshot
	lease           core.Lease
}

func (s *Store) Load(ctx context.Context) (snap core.Snapshot, err error) {
	err = s.storeDo(ctx, func(_ string, st storeState) error { snap = st.snap; return nil })
	return snap, err
}

func (s *Store) Save(ctx context.Context, token uint64, snap core.Snapshot) (version string, err error) {
	err = s.storeDo(ctx, func(dir string, st storeState) (err error) {
		if snap.Version != st.version {
			return fmt.Errorf("stale save: the stored version is %q, not %q", st.version, snap.Version)
		}
		if token < st.lease.Token {
			return fmt.Errorf("fenced: token %d is below the lease token %d", token, st.lease.Token)
		}
		st.snap = snap
		version, err = s.storeWrite(ctx, dir, st)
		return err
	})
	return version, err
}

// Acquire gives a new owner the next token; a renewal keeps its own.
func (s *Store) Acquire(ctx context.Context, owner string, ttl time.Duration) (l core.Lease, err error) {
	err = s.storeDo(ctx, func(dir string, st storeState) error {
		now := s.Now()
		if st.lease.Owner != owner && now.Before(st.lease.Expires) {
			return fmt.Errorf("lease held by %q until %s", st.lease.Owner, st.lease.Expires.Format(time.RFC3339))
		}
		if st.lease.Owner != owner {
			if st.lease.Token == math.MaxUint64 {
				return errors.New("lease token overflow")
			}
			st.lease.Token++
		}
		st.lease.Owner, st.lease.Expires = owner, now.Add(ttl)
		l = st.lease
		_, err := s.storeWrite(ctx, dir, st)
		return err
	})
	return l, err
}

func (s *Store) storeDo(ctx context.Context, f func(dir string, st storeState) error) error {
	dir, err := os.MkdirTemp("", "queue-store-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if _, err := storeGit(ctx, dir, "", "init", "-q", "--bare"); err != nil {
		return err
	}
	st, err := s.storeRead(ctx, dir)
	if err != nil {
		return err
	}
	return f(dir, st)
}

func (s *Store) storeRead(ctx context.Context, dir string) (st storeState, err error) {
	out, err := storeGit(ctx, dir, "", "ls-remote", s.Remote, s.Ref)
	if err != nil || !slices.Contains(strings.Fields(out), s.Ref) {
		return st, err // no ref: an empty queue, version "", token 0
	}
	if _, err = storeGit(ctx, dir, "", "fetch", "-q", s.Remote, "+"+s.Ref+":refs/read"); err != nil {
		return st, err
	}
	ids, err0 := storeGit(ctx, dir, "", "rev-parse", "refs/read", "refs/read:snapshot.json")
	st.commit, st.version, _ = strings.Cut(ids, "\n")
	snap, err1 := storeGit(ctx, dir, "", "cat-file", "blob", "refs/read:snapshot.json")
	lease, err2 := storeGit(ctx, dir, "", "cat-file", "blob", "refs/read:lease.json")
	if err = errors.Join(err0, err1, err2, json.Unmarshal([]byte(snap), &st.snap), json.Unmarshal([]byte(lease), &st.lease)); err != nil {
		return st, fmt.Errorf("read %s: %w", s.Ref, err)
	}
	st.snap.Version = st.version
	return st, nil
}

// storeWrite pushes st as a new commit only if the ref is still st.commit ("" =
// absent) and returns the new version. A JSON error precedes any write or push.
func (s *Store) storeWrite(ctx context.Context, dir string, st storeState) (string, error) {
	st.snap.Version = ""
	snap, err0 := json.Marshal(core.RedactSnapshot(st.snap)) // its free text redacted, whenever it was saved
	lease, err00 := json.Marshal(st.lease)
	if err := errors.Join(err0, err00); err != nil {
		return "", fmt.Errorf("encode %s: %w", s.Ref, err)
	}
	version, err1 := storeGit(ctx, dir, string(snap), "hash-object", "-w", "--stdin")
	lid, err2 := storeGit(ctx, dir, string(lease), "hash-object", "-w", "--stdin")
	tree, err3 := storeGit(ctx, dir, "100644 blob "+lid+"\tlease.json\n100644 blob "+version+"\tsnapshot.json\n", "mktree")
	if err := errors.Join(err1, err2, err3); err != nil {
		return "", err
	}
	commit, err := storeGit(ctx, dir, "", "commit-tree", "--no-gpg-sign", tree, "-m", "queue state")
	if err == nil {
		_, err = storeGit(ctx, dir, "", "push", "-q", "--force-with-lease="+s.Ref+":"+st.commit, s.Remote, commit+":"+s.Ref)
	}
	return version, err
}

func storeGit(ctx context.Context, dir, stdin string, args ...string) (string, error) {
	return runGit(ctx, stdin, []string{"-C", dir, "-c", "user.name=queue", "-c", "user.email=queue@localhost"}, args...)
}
