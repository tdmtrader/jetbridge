package credentials_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/credentials"
)

func TestValidKind(t *testing.T) {
	if !credentials.ValidKind("anthropic_oauth") || !credentials.ValidKind("anthropic_api_key") {
		t.Fatal("contract kinds must validate")
	}
	if credentials.ValidKind("openai") || credentials.ValidKind("") {
		t.Fatal("unknown kinds must not validate")
	}
}

func TestCredentialJSONNeverCarriesToken(t *testing.T) {
	c := credentials.Credential{UserID: 1, UserName: "alice", Kind: "anthropic_oauth", Token: "sk-secret"}
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "sk-secret") {
		t.Fatalf("token leaked into JSON: %s", data)
	}
}

func TestPutRequestValidate(t *testing.T) {
	ok := credentials.PutRequest{Kind: "anthropic_oauth", Token: "tok"}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	platform := credentials.PutRequest{Kind: "anthropic_oauth", Token: "tok", User: credentials.PlatformUserName}
	if err := platform.Validate(); err != nil {
		t.Fatalf("platform-user request rejected: %v", err)
	}
	for _, bad := range []credentials.PutRequest{
		{Kind: "", Token: "tok"},
		{Kind: "openai", Token: "tok"},
		{Kind: "anthropic_oauth", Token: ""},
		{Kind: "anthropic_oauth", Token: "tok", User: "someone-else"},
	} {
		if err := bad.Validate(); err == nil {
			t.Fatalf("invalid request accepted: %+v", bad)
		}
	}
}

func TestMemoryBackendRoundTrip(t *testing.T) {
	m := credentials.NewMemoryBackend()
	m.AddUser("sub-1", 7, "alice")

	id, name, found, err := m.UserBySub("sub-1")
	if err != nil || !found || id != 7 || name != "alice" {
		t.Fatalf("UserBySub: %d %q %v %v", id, name, found, err)
	}

	exp := time.Now().Add(time.Hour)
	if err := m.Put(7, "alice", "anthropic_oauth", "sk-tok", exp); err != nil {
		t.Fatal(err)
	}

	status, err := m.Status(7)
	if err != nil || len(status) != 1 {
		t.Fatalf("Status: %v %v", status, err)
	}
	if status[0].Token != "" {
		t.Fatal("Status must not carry tokens")
	}
	if status[0].ExpiresAt != exp.Unix() {
		t.Fatalf("ExpiresAt: got %d want %d", status[0].ExpiresAt, exp.Unix())
	}

	cred, found, err := m.Resolve(7, "anthropic_oauth")
	if err != nil || !found || cred.Token != "sk-tok" {
		t.Fatalf("Resolve: %+v %v %v", cred, found, err)
	}

	expiring, err := m.ExpiringWithin(2 * time.Hour)
	if err != nil || len(expiring) != 1 {
		t.Fatalf("ExpiringWithin(2h): %v %v", expiring, err)
	}
	expiring, err = m.ExpiringWithin(time.Minute)
	if err != nil || len(expiring) != 0 {
		t.Fatalf("ExpiringWithin(1m): %v %v", expiring, err)
	}

	if err := m.SetJiraAccountID(7, "jira-123"); err != nil {
		t.Fatal(err)
	}
	status, _ = m.Status(7)
	if status[0].JiraAccountID != "jira-123" {
		t.Fatal("jira seam not persisted")
	}

	if err := m.Delete(7, "anthropic_oauth"); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := m.Resolve(7, "anthropic_oauth"); found {
		t.Fatal("credential survived Delete")
	}
}
