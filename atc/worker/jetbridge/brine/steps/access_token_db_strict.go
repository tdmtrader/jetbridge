package steps

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
	"github.com/go-jose/go-jose/v4/jwt"
)

type AccessTokenDBObservation struct {
	Profile string
	Failure string
}

func AccessTokenDBStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, AccessTokenDBObservation](
			"the production access-token database profile {string} is exercised",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (AccessTokenDBObservation, error) {
				profile, err := paramAt("the production access-token database profile {string} is exercised", p, 0)
				if err != nil {
					return AccessTokenDBObservation{}, err
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return AccessTokenDBObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				return AccessTokenDBObservation{Profile: profile, Failure: observeAccessTokenDB(database, profile)}, nil
			},
		),
		brine.DefineCheck[AccessTokenDBObservation](
			"the access-token database observation exactly matches {string}",
			func(in AccessTokenDBObservation, p brine.Params, _ *brine.Recorder) error {
				profile, err := paramAt("the access-token database observation exactly matches {string}", p, 0)
				if err != nil {
					return err
				}
				if in.Profile != profile {
					return fmt.Errorf("profile got %q, want %q", in.Profile, profile)
				}
				if in.Failure != "" {
					return fmt.Errorf("%s: %s", profile, in.Failure)
				}
				return nil
			},
		),
	}
}

func observeAccessTokenDB(database JetbridgeDB, profile string) string {
	fail := func(format string, args ...any) string { return fmt.Sprintf(format, args...) }
	factory := db.NewAccessTokenFactory(database.Conn)
	switch profile {
	case "create-and-fetch-claims":
		date := jwt.NumericDate(1234567890)
		raw := map[string]any{
			"iss": "issuer", "sub": "subject", "aud": []any{"audience"},
			"exp": float64(date), "nbf": float64(date), "iat": float64(date), "jti": "id",
			"federated_claims": map[string]any{"user_id": "userid", "connector_id": "github", "other": "blah"},
			"groups":           []any{"group1", "group2"},
		}
		payload, err := json.Marshal(raw)
		if err != nil {
			return err.Error()
		}
		var claims db.Claims
		if err := json.Unmarshal(payload, &claims); err != nil {
			return err.Error()
		}
		if err := factory.CreateAccessToken("my-awesome-token", claims); err != nil {
			return err.Error()
		}
		stored, found, err := factory.GetAccessToken("my-awesome-token")
		if err != nil {
			return err.Error()
		}
		if !found || stored.Token != "my-awesome-token" || stored.Claims.Issuer != "issuer" ||
			stored.Claims.Subject != "subject" || len(stored.Claims.Audience) != 1 || stored.Claims.Audience[0] != "audience" ||
			stored.Claims.Expiry == nil || *stored.Claims.Expiry != date || stored.Claims.NotBefore == nil || *stored.Claims.NotBefore != date ||
			stored.Claims.IssuedAt == nil || *stored.Claims.IssuedAt != date || stored.Claims.ID != "id" ||
			stored.Claims.UserID != "userid" || stored.Claims.Connector != "github" || fmt.Sprint(stored.Claims.RawClaims) != fmt.Sprint(raw) {
			return fail("stored token found=%t value=%+v", found, stored)
		}
		return ""

	case "delete-token":
		if err := factory.CreateAccessToken("my-delete-token", db.Claims{RawClaims: map[string]any{"sub": "subject"}}); err != nil {
			return err.Error()
		}
		if err := factory.DeleteAccessToken("my-delete-token"); err != nil {
			return err.Error()
		}
		_, found, err := factory.GetAccessToken("my-delete-token")
		if err != nil {
			return err.Error()
		}
		if found {
			return "deleted token still exists"
		}
		return ""

	case "remove-expired-keeps-active":
		yesterday := jwt.NewNumericDate(time.Now().Add(-24 * time.Hour))
		tomorrow := jwt.NewNumericDate(time.Now().Add(24 * time.Hour))
		for _, token := range []string{"expiredToken1", "expiredToken2"} {
			if err := factory.CreateAccessToken(token, db.Claims{Claims: jwt.Claims{Expiry: yesterday}}); err != nil {
				return err.Error()
			}
		}
		if err := factory.CreateAccessToken("activeToken", db.Claims{Claims: jwt.Claims{Expiry: tomorrow}}); err != nil {
			return err.Error()
		}
		n, err := db.NewAccessTokenLifecycle(database.Conn).RemoveExpiredAccessTokens(0)
		if err != nil {
			return err.Error()
		}
		_, active, err := factory.GetAccessToken("activeToken")
		if err != nil {
			return err.Error()
		}
		if n != 2 || !active {
			return fail("removed=%d active=%t, want 2/true", n, active)
		}
		return ""

	case "expiration-leeway":
		yesterday := jwt.NewNumericDate(time.Now().Add(-24 * time.Hour))
		if err := factory.CreateAccessToken("expiredToken", db.Claims{Claims: jwt.Claims{Expiry: yesterday}}); err != nil {
			return err.Error()
		}
		n, err := db.NewAccessTokenLifecycle(database.Conn).RemoveExpiredAccessTokens(25 * time.Hour)
		if err != nil {
			return err.Error()
		}
		if n != 0 {
			return fail("removed=%d, want 0", n)
		}
		return ""
	default:
		return fail("unknown access-token database profile %q", profile)
	}
}
