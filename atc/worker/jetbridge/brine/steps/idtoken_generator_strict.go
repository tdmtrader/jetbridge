package steps

import (
	"encoding/base64"
	"fmt"
	"sync"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/creds"
	"github.com/concourse/concourse/atc/creds/idtoken"
	"github.com/concourse/concourse/atc/db"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

type IDTokenGeneratorStrictObservation struct{ Value string }

func IDTokenGeneratorStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, IDTokenGeneratorStrictObservation](
			"the strict real ID token generator evaluates profile {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (IDTokenGeneratorStrictObservation, error) {
				profile, ok := p.GetString(0)
				if !ok {
					return IDTokenGeneratorStrictObservation{}, fmt.Errorf("expected strict ID token generator profile")
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return IDTokenGeneratorStrictObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				value, err := observeIDTokenGeneratorStrict(database, profile)
				return IDTokenGeneratorStrictObservation{Value: value}, err
			},
		),
		CheckString[IDTokenGeneratorStrictObservation](
			"the strict ID token generator observation is {string}",
			"strict ID token generator observation",
			func(observation IDTokenGeneratorStrictObservation) (string, error) { return observation.Value, nil },
		),
	}
}

var (
	idTokenGeneratorKeysOnce sync.Once
	idTokenGeneratorRSA      *jose.JSONWebKey
	idTokenGeneratorEC       *jose.JSONWebKey
	idTokenGeneratorKeyErr   error
)

type idTokenGeneratorClaims struct {
	jwt.Claims
	Team         string           `json:"team"`
	Pipeline     string           `json:"pipeline"`
	InstanceVars atc.InstanceVars `json:"instance_vars"`
	Job          string           `json:"job"`
}

func observeIDTokenGeneratorStrict(database JetbridgeDB, profile string) (string, error) {
	rsaKey, ecKey, err := strictIDTokenGeneratorKeys()
	if err != nil {
		return "", err
	}
	factory := db.NewSigningKeyFactory(database.Conn)
	if err := factory.CreateKey(*rsaKey); err != nil {
		return "", fmt.Errorf("store RSA signing key: %w", err)
	}
	if err := factory.CreateKey(*ecKey); err != nil {
		return "", fmt.Errorf("store EC signing key: %w", err)
	}
	storedRSA, err := factory.GetNewestKey(db.SigningKeyTypeRSA)
	if err != nil {
		return "", fmt.Errorf("load stored RSA signing key: %w", err)
	}
	storedEC, err := factory.GetNewestKey(db.SigningKeyTypeEC)
	if err != nil {
		return "", fmt.Errorf("load stored EC signing key: %w", err)
	}
	storedRSAJWK := storedRSA.JWK()
	storedECJWK := storedEC.JWK()

	params := creds.SecretLookupParams{
		Team: "main", Pipeline: "idtoken", Job: "testjob",
		InstanceVars: atc.InstanceVars{"foo": "bar"},
	}
	generator := idtoken.TokenGenerator{
		Issuer: "https://concourse.test", SigningKeyFactory: factory,
		ExpiresIn: 15 * time.Minute,
	}
	algorithm := idtoken.DefaultAlgorithm
	verificationKey := storedRSAJWK.Public()
	switch profile {
	case "scope-team":
		generator.SubjectScope = idtoken.SubjectScopeTeam
	case "scope-instance":
		generator.SubjectScope = idtoken.SubjectScopeInstance
	case "scope-job":
		generator.SubjectScope = idtoken.SubjectScopeJob
	case "escaped-subject":
		generator.SubjectScope = idtoken.SubjectScopeJob
		params = creds.SecretLookupParams{
			Team: "fake/team", Pipeline: "fake/pipeline", Job: "fake/job",
			InstanceVars: atc.InstanceVars{"fake/foo": "fake/bar"},
		}
	case "audience":
		generator.Audience = []string{"testaud"}
	case "es256":
		generator.Algorithm = jose.ES256
		algorithm = jose.ES256
		verificationKey = storedECJWK.Public()
	case "valid-token", "claim-subject", "claim-issuer", "claim-expiry", "claim-team", "claim-pipeline", "claim-ivars", "claim-job", "no-audience":
	default:
		return "", fmt.Errorf("unknown strict ID token generator profile %q", profile)
	}

	token, validUntil, err := generator.GenerateToken(params)
	if err != nil {
		return "", err
	}
	parsed, err := jwt.ParseSigned(token, []jose.SignatureAlgorithm{algorithm})
	if err != nil {
		return "", fmt.Errorf("parse signed token: %w", err)
	}
	var claims idTokenGeneratorClaims
	if err := parsed.Claims(verificationKey, &claims); err != nil {
		return "", fmt.Errorf("verify signed token: %w", err)
	}

	switch profile {
	case "valid-token":
		return "signature=valid;subject=" + claims.Subject, nil
	case "scope-team", "claim-subject":
		return "subject=" + claims.Subject, nil
	case "scope-instance", "scope-job":
		return "subject64=" + base64.StdEncoding.EncodeToString([]byte(claims.Subject)), nil
	case "escaped-subject":
		return "escape=" + base64.StdEncoding.EncodeToString([]byte(claims.Subject)), nil
	case "audience":
		contains := false
		for _, audience := range claims.Audience {
			contains = contains || audience == "testaud"
		}
		return fmt.Sprintf("contains-testaud=%t", contains), nil
	case "es256":
		return "algorithm=" + parsed.Headers[0].Algorithm + ";signature=valid", nil
	case "claim-issuer":
		return "issuer=" + claims.Issuer, nil
	case "claim-expiry":
		duration := claims.Expiry.Time().Sub(claims.IssuedAt.Time())
		returnedMatch := validUntil.Sub(claims.Expiry.Time()) < time.Second && claims.Expiry.Time().Sub(validUntil) < time.Second
		return fmt.Sprintf("expiry=%s;returned-match=%t", duration, returnedMatch), nil
	case "claim-team":
		return "team=" + claims.Team, nil
	case "claim-pipeline":
		return "pipeline=" + claims.Pipeline, nil
	case "claim-ivars":
		return "foo=" + fmt.Sprint(claims.InstanceVars["foo"]), nil
	case "claim-job":
		return "job=" + claims.Job, nil
	case "no-audience":
		return fmt.Sprintf("audience-count=%d", len(claims.Audience)), nil
	default:
		return "", fmt.Errorf("unobserved strict ID token generator profile %q", profile)
	}
}

func strictIDTokenGeneratorKeys() (*jose.JSONWebKey, *jose.JSONWebKey, error) {
	idTokenGeneratorKeysOnce.Do(func() {
		idTokenGeneratorRSA, idTokenGeneratorKeyErr = idtoken.GenerateNewKey(db.SigningKeyTypeRSA)
		if idTokenGeneratorKeyErr != nil {
			return
		}
		idTokenGeneratorEC, idTokenGeneratorKeyErr = idtoken.GenerateNewKey(db.SigningKeyTypeEC)
	})
	return idTokenGeneratorRSA, idTokenGeneratorEC, idTokenGeneratorKeyErr
}
