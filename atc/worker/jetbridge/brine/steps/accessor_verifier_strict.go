package steps

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/db"
)

type AccessorVerifierObservation struct {
	Profile string
	Failure string
}

func AccessorVerifierStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, AccessorVerifierObservation](
			"the production access-token profile {string} is exercised",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (AccessorVerifierObservation, error) {
				profile, err := paramAt("the production access-token profile {string} is exercised", p, 0)
				if err != nil {
					return AccessorVerifierObservation{}, err
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return AccessorVerifierObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				return AccessorVerifierObservation{Profile: profile, Failure: observeAccessorVerifier(database, profile)}, nil
			},
		),
		brine.DefineCheck[AccessorVerifierObservation](
			"the access-token observation exactly matches {string}",
			func(in AccessorVerifierObservation, p brine.Params, _ *brine.Recorder) error {
				profile, err := paramAt("the access-token observation exactly matches {string}", p, 0)
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

func observeAccessorVerifier(database JetbridgeDB, profile string) string {
	fail := func(format string, args ...any) string { return fmt.Sprintf(format, args...) }
	factory := db.NewAccessTokenFactory(database.Conn)
	persist := func(token string, raw map[string]any) error {
		payload, err := json.Marshal(raw)
		if err != nil {
			return err
		}
		var claims db.Claims
		if err := json.Unmarshal(payload, &claims); err != nil {
			return err
		}
		return factory.CreateAccessToken(token, claims)
	}

	if profile == "deleted-token-not-found" {
		if err := persist("doomed-token", map[string]any{"sub": "some-sub"}); err != nil {
			return err.Error()
		}
		if err := factory.DeleteAccessToken("doomed-token"); err != nil {
			return err.Error()
		}
		_, found, err := factory.GetAccessToken("doomed-token")
		if err != nil {
			return err.Error()
		}
		if found {
			return "deleted token was found"
		}
		return ""
	}

	audience := "some-aud"
	raw := map[string]any{
		"sub":   "some-sub",
		"aud":   []any{audience},
		"exp":   float64(time.Now().Add(time.Hour).Unix()),
		"email": "some-user@example.com",
	}
	token := "1234567890"
	authorization := "bearer " + token
	switch profile {
	case "no-token":
		authorization = ""
	case "invalid-header":
		authorization = "invalid"
	case "invalid-token-type":
		authorization = "not-bearer " + token
	case "token-not-found":
		authorization = "bearer never-issued"
	case "expired-token":
		token = "expired-token"
		authorization = "bearer " + token
		raw["exp"] = float64(time.Now().Add(-time.Hour).Unix())
	case "invalid-audience":
		token = "wrong-audience-token"
		authorization = "bearer " + token
		raw["aud"] = []any{"invalid"}
	case "valid-token-succeeds", "valid-token-claims":
	default:
		return fail("unknown access-token profile %q", profile)
	}
	if profile != "no-token" && profile != "invalid-header" && profile != "invalid-token-type" && profile != "token-not-found" {
		if err := persist(token, raw); err != nil {
			return err.Error()
		}
	} else if profile == "invalid-header" || profile == "invalid-token-type" {
		if err := persist(token, raw); err != nil {
			return err.Error()
		}
	}

	req, err := http.NewRequest(http.MethodGet, "http://localhost:8080", nil)
	if err != nil {
		return err.Error()
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	claims, verifyErr := accessor.NewVerifier(factory, []string{audience}).Verify(req)
	switch profile {
	case "no-token":
		if !errors.Is(verifyErr, accessor.ErrVerificationNoToken) {
			return fail("error=%v, want no-token", verifyErr)
		}
	case "invalid-header", "invalid-token-type", "token-not-found":
		if !errors.Is(verifyErr, accessor.ErrVerificationInvalidToken) {
			return fail("error=%v, want invalid-token", verifyErr)
		}
	case "expired-token":
		if !errors.Is(verifyErr, accessor.ErrVerificationTokenExpired) {
			return fail("error=%v, want expired-token", verifyErr)
		}
	case "invalid-audience":
		if !errors.Is(verifyErr, accessor.ErrVerificationInvalidAudience) {
			return fail("error=%v, want invalid-audience", verifyErr)
		}
	case "valid-token-succeeds":
		if verifyErr != nil {
			return fail("verify: %v", verifyErr)
		}
	case "valid-token-claims":
		if verifyErr != nil {
			return fail("verify: %v", verifyErr)
		}
		if fmt.Sprint(claims) != fmt.Sprint(raw) {
			return fail("claims=%v, want %v", claims, raw)
		}
	}
	return ""
}
