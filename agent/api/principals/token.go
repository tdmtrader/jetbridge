package principals

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

// TokenVersionPrefix distinguishes agent principal tokens from Concourse
// JWTs at the auth seam. Wire format (00-shared-contracts.md §1.2):
// cap1.<id>.<43-char base64url secret> — cap = concourse agent principal,
// 1 = format version.
const TokenVersionPrefix = "cap1."

// tokenPrefixLen is how much of the token is stored for display
// (agent_principals.token_prefix).
const tokenPrefixLen = 12

// MintToken generates the wire token for principal id. It returns the
// full token (surfaced exactly once, never stored), the 12-char display
// prefix, and the hex sha256 hash stored at rest.
func MintToken(id int) (token, prefix, hash string, err error) {
	secret := make([]byte, 32)
	if _, err = rand.Read(secret); err != nil {
		return "", "", "", fmt.Errorf("generate principal secret: %w", err)
	}
	token = fmt.Sprintf("cap1.%d.%s", id, base64.RawURLEncoding.EncodeToString(secret))
	return token, DisplayPrefix(token), HashToken(token), nil
}

// DisplayPrefix is the stored/displayed first 12 characters of a token.
func DisplayPrefix(token string) string {
	if len(token) < tokenPrefixLen {
		return token
	}
	return token[:tokenPrefixLen]
}

// HashToken is hex(sha256(full token)) — the at-rest form.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// ParseTokenID extracts the principal id from a wire token. ok=false for
// anything that is not a well-formed cap1 token.
func ParseTokenID(token string) (int, bool) {
	if !strings.HasPrefix(token, TokenVersionPrefix) {
		return 0, false
	}
	parts := strings.SplitN(token, ".", 3)
	if len(parts) != 3 || parts[2] == "" {
		return 0, false
	}
	id, err := strconv.Atoi(parts[1])
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}
