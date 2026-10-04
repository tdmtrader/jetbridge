package git

import (
	"context"
	"fmt"
)

// operators checks a commit against a git allowed-signers file (ssh keys); the
// zero file accepts everything. Admits and resume, promote, withdraw and resolve requests share it.
type operators struct {
	l    *Lander
	file string
}

// verify is nil if sha, already fetched into l's repo, is signed by a listed key. The error
// is "<sha7> is not signed by an operator" and never git's own words, which may quote key material.
func (o operators) verify(ctx context.Context, sha string) error {
	if o.file == "" {
		return nil
	}
	if _, err := o.l.git(ctx, "-c", "gpg.format=ssh", "-c", "gpg.ssh.allowedSignersFile="+o.file, "verify-commit", sha); err != nil {
		return fmt.Errorf("%.7s is not signed by an operator", sha)
	}
	return nil
}
