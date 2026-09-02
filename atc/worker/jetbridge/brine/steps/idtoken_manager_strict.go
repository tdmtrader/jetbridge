package steps

import (
	"fmt"
	"reflect"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/creds/idtoken"
	"github.com/concourse/concourse/atc/db"
	"github.com/go-jose/go-jose/v4"
)

type IDTokenManagerObservation struct {
	Profile string
	Failure string
}

func IDTokenManagerStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, IDTokenManagerObservation](
			"the production ID token manager profile {string} is exercised",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (IDTokenManagerObservation, error) {
				profile, err := paramAt("the production ID token manager profile {string} is exercised", p, 0)
				if err != nil {
					return IDTokenManagerObservation{}, err
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return IDTokenManagerObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				return IDTokenManagerObservation{Profile: profile, Failure: observeIDTokenManager(database, profile)}, nil
			},
		),
		brine.DefineCheck[IDTokenManagerObservation](
			"the ID token manager observation exactly matches {string}",
			func(in IDTokenManagerObservation, p brine.Params, _ *brine.Recorder) error {
				profile, err := paramAt("the ID token manager observation exactly matches {string}", p, 0)
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

func observeIDTokenManager(database JetbridgeDB, profile string) string {
	const issuer = "https://issuer.example"
	factory := db.NewSigningKeyFactory(database.Conn)
	config := map[string]any{
		"audience":      []any{"testaud"},
		"subject_scope": string(idtoken.SubjectScopeTeam),
		"expires_in":    "15m",
		"algorithm":     "ES256",
	}

	wantNewError := func() string {
		if _, err := idtoken.NewManager(issuer, factory, config); err == nil {
			return "NewManager unexpectedly accepted invalid configuration"
		}
		return ""
	}
	wantValidationError := func() string {
		manager, err := idtoken.NewManager(issuer, factory, config)
		if err != nil {
			return fmt.Sprintf("NewManager returned %v", err)
		}
		if err := manager.Validate(); err == nil {
			return "Validate unexpectedly accepted invalid configuration"
		}
		return ""
	}

	switch profile {
	case "valid":
		manager, err := idtoken.NewManager(issuer, factory, config)
		if err != nil {
			return err.Error()
		}
		if err := manager.Init(nil); err != nil {
			return err.Error()
		}
		if err := manager.Validate(); err != nil {
			return err.Error()
		}
		generator := manager.GetTokenGenerator()
		if generator.Issuer != issuer || generator.SubjectScope != idtoken.SubjectScopeTeam ||
			!reflect.DeepEqual(generator.Audience, []string{"testaud"}) || generator.ExpiresIn != 15*time.Minute || generator.Algorithm != jose.ES256 {
			return fmt.Sprintf("generator got issuer=%q scope=%q audience=%v expiry=%s algorithm=%q", generator.Issuer, generator.SubjectScope, generator.Audience, generator.ExpiresIn, generator.Algorithm)
		}
		return ""
	case "malformed-audience":
		config["audience"] = "invalid"
		return wantNewError()
	case "malformed-subject-scope":
		config["subject_scope"] = 123
		return wantNewError()
	case "malformed-expires-in":
		config["expires_in"] = 123
		if failure := wantNewError(); failure != "" {
			return "numeric expires_in: " + failure
		}
		config["expires_in"] = "15abc"
		if failure := wantNewError(); failure != "" {
			return "unparseable expires_in: " + failure
		}
		return ""
	case "malformed-algorithm":
		config["algorithm"] = 123
		return wantNewError()
	case "unknown-setting":
		config["unknown"] = "abc"
		return wantNewError()
	case "unknown-subject-scope":
		config["subject_scope"] = "something"
		return wantValidationError()
	case "excessive-expires-in":
		config["expires_in"] = "48h"
		return wantValidationError()
	case "unknown-algorithm":
		config["algorithm"] = "something"
		return wantValidationError()
	default:
		return fmt.Sprintf("unknown ID token manager profile %q", profile)
	}
}
