package atccmd

import (
	"testing"

	"github.com/concourse/concourse/atc/api/accessor"
)

func TestAgentSnapshotIdentityUsesStableConnectorSubject(t *testing.T) {
	first, err := agentSnapshotIdentity(accessor.Claims{
		Connector: "github", Sub: "subject-42", PreferredUsername: "alice",
	})
	if err != nil {
		t.Fatal(err)
	}
	renamed, err := agentSnapshotIdentity(accessor.Claims{
		Connector: "github", Sub: "subject-42", PreferredUsername: "alice-renamed",
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Actor != renamed.Actor {
		t.Fatalf("rename changed actor key: %q != %q", first.Actor, renamed.Actor)
	}
	if first.DisplayName != "alice" || renamed.DisplayName != "alice-renamed" {
		t.Fatalf("display names = %q, %q", first.DisplayName, renamed.DisplayName)
	}
	otherConnector, err := agentSnapshotIdentity(accessor.Claims{
		Connector: "gitlab", Sub: "subject-42", PreferredUsername: "alice",
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Actor == otherConnector.Actor {
		t.Fatal("connector namespace was omitted from actor key")
	}
	if len(first.Actor) != len("subject:sha256:")+64 {
		t.Fatalf("actor key length = %d: %q", len(first.Actor), first.Actor)
	}
}

func TestAgentSnapshotIdentityHasExplicitLegacyFallback(t *testing.T) {
	identity, err := agentSnapshotIdentity(accessor.Claims{
		Connector: "local", UserName: "Legacy User",
	})
	if err != nil {
		t.Fatal(err)
	}
	if identity.DisplayName != "Legacy User" {
		t.Fatalf("display name = %q", identity.DisplayName)
	}
	if len(identity.Actor) != len("legacy:sha256:")+64 {
		t.Fatalf("legacy actor = %q", identity.Actor)
	}
	if _, err := agentSnapshotIdentity(accessor.Claims{}); err == nil {
		t.Fatal("missing authenticated identity was accepted")
	}
}

func TestAgentSnapshotIdentityUsesStableUserIDBeforeLegacyDisplay(t *testing.T) {
	first, err := agentSnapshotIdentity(accessor.Claims{
		Connector: "oidc", UserID: "stable-user-7", PreferredUsername: "alice",
	})
	if err != nil {
		t.Fatal(err)
	}
	renamed, err := agentSnapshotIdentity(accessor.Claims{
		Connector: "oidc", UserID: "stable-user-7", PreferredUsername: "alice-renamed",
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Actor != renamed.Actor {
		t.Fatalf("display rename changed stable user-id actor: %q != %q", first.Actor, renamed.Actor)
	}
	if first.DisplayName != "alice" || renamed.DisplayName != "alice-renamed" {
		t.Fatalf("display names = %q, %q", first.DisplayName, renamed.DisplayName)
	}
	if len(first.Actor) != len("user-id:sha256:")+64 {
		t.Fatalf("user-id actor = %q", first.Actor)
	}

	onlyUserID, err := agentSnapshotIdentity(accessor.Claims{Connector: "oidc", UserID: "stable-user-8"})
	if err != nil {
		t.Fatal(err)
	}
	if onlyUserID.DisplayName != "stable-user-8" {
		t.Fatalf("user-id-only display = %q", onlyUserID.DisplayName)
	}
	sameDisplayOtherUser, err := agentSnapshotIdentity(accessor.Claims{
		Connector: "oidc", UserID: "stable-user-9", PreferredUsername: "alice",
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Actor == sameDisplayOtherUser.Actor {
		t.Fatal("distinct stable user IDs sharing a display name collided")
	}
}
