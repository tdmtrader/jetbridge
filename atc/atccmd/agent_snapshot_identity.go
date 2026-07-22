package atccmd

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"

	snapshotsapi "github.com/concourse/concourse/agent/api/snapshots"
	"github.com/concourse/concourse/atc/api/accessor"
)

func agentSnapshotIdentity(claims accessor.Claims) (snapshotsapi.RequestIdentity, error) {
	displayName := firstNonBlank(
		claims.PreferredUsername,
		claims.UserName,
		claims.Email,
		claims.Sub,
		claims.UserID,
	)
	if displayName == "" {
		return snapshotsapi.RequestIdentity{}, fmt.Errorf("snapshot actor identity is unavailable")
	}

	if strings.TrimSpace(claims.Sub) != "" {
		return snapshotsapi.RequestIdentity{
			Actor:       hashedSnapshotActor("subject", claims.Connector, claims.Sub),
			DisplayName: displayName,
		}, nil
	}
	if strings.TrimSpace(claims.UserID) != "" {
		return snapshotsapi.RequestIdentity{
			Actor:       hashedSnapshotActor("user-id", claims.Connector, claims.UserID),
			DisplayName: displayName,
		}, nil
	}
	return snapshotsapi.RequestIdentity{
		Actor:       hashedSnapshotActor("legacy", claims.Connector, displayName),
		DisplayName: displayName,
	}, nil
}

func hashedSnapshotActor(kind string, namespace string, identity string) string {
	hash := sha256.New()
	var size [8]byte
	for _, value := range []string{namespace, identity} {
		binary.BigEndian.PutUint64(size[:], uint64(len(value)))
		_, _ = hash.Write(size[:])
		_, _ = hash.Write([]byte(value))
	}
	return kind + ":sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
