package accessor

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// jwksVerifier validates JWTs against a JWKS endpoint (standard OIDC flow).
// It replaces the DB-backed opaque token verifier with stateless cryptographic
// verification against Dex's published signing keys.
type jwksVerifier struct {
	jwksURL  string
	audience []string

	mu       sync.RWMutex
	keys     *jose.JSONWebKeySet
	fetchedAt time.Time
	cacheTTL  time.Duration
}

// NewJWKSVerifier creates a verifier that validates JWTs by fetching public
// keys from the given JWKS URL (typically Dex's /keys endpoint).
func NewJWKSVerifier(jwksURL string, audience []string) TokenVerifier {
	return &jwksVerifier{
		jwksURL:  jwksURL,
		audience: audience,
		cacheTTL: 5 * time.Minute,
	}
}

func (v *jwksVerifier) Verify(r *http.Request) (map[string]any, error) {
	rawToken, err := extractBearerToken(r)
	if err != nil {
		return nil, err
	}

	token, err := jwt.ParseSigned(rawToken, []jose.SignatureAlgorithm{jose.RS256})
	if err != nil {
		return nil, ErrVerificationInvalidToken
	}

	// Get the key ID from the token header
	if len(token.Headers) == 0 {
		return nil, ErrVerificationInvalidToken
	}
	kid := token.Headers[0].KeyID

	// Try cached keys first
	key, err := v.getKey(kid)
	if err != nil {
		return nil, ErrVerificationInvalidToken
	}

	// Verify signature and extract claims
	var rawClaims map[string]any
	if err := token.Claims(key, &rawClaims); err != nil {
		return nil, ErrVerificationInvalidToken
	}

	// Validate standard claims (exp, aud)
	var stdClaims jwt.Claims
	if err := token.Claims(key, &stdClaims); err != nil {
		return nil, ErrVerificationInvalidToken
	}

	if err := stdClaims.Validate(jwt.Expected{Time: time.Now()}); err != nil {
		if errors.Is(err, jwt.ErrExpired) {
			return nil, ErrVerificationTokenExpired
		}
		return nil, ErrVerificationInvalidToken
	}

	// Check audience
	for _, aud := range v.audience {
		if stdClaims.Audience.Contains(aud) {
			return rawClaims, nil
		}
	}

	return nil, ErrVerificationInvalidAudience
}

func (v *jwksVerifier) getKey(kid string) (any, error) {
	// Try cached keys
	v.mu.RLock()
	key := v.findKey(kid)
	cacheValid := v.keys != nil && time.Since(v.fetchedAt) < v.cacheTTL
	v.mu.RUnlock()

	if key != nil {
		return key, nil
	}

	// If cache is still valid and key not found, it's genuinely unknown
	if cacheValid {
		return nil, errors.New("unknown key ID")
	}

	// Fetch fresh keys
	if err := v.fetchKeys(); err != nil {
		return nil, err
	}

	v.mu.RLock()
	key = v.findKey(kid)
	v.mu.RUnlock()

	if key == nil {
		return nil, errors.New("unknown key ID after refresh")
	}
	return key, nil
}

func (v *jwksVerifier) findKey(kid string) any {
	if v.keys == nil {
		return nil
	}
	keys := v.keys.Key(kid)
	if len(keys) == 0 {
		return nil
	}
	return keys[0].Key
}

func (v *jwksVerifier) fetchKeys() error {
	resp, err := http.Get(v.jwksURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return errors.New("JWKS endpoint returned non-200")
	}

	var jwks jose.JSONWebKeySet
	if err := json.NewDecoder(resp.Body).Decode(&jwks); err != nil {
		return err
	}

	v.mu.Lock()
	v.keys = &jwks
	v.fetchedAt = time.Now()
	v.mu.Unlock()

	return nil
}

func extractBearerToken(r *http.Request) (string, error) {
	header := r.Header.Get("Authorization")
	if header == "" {
		return "", ErrVerificationNoToken
	}

	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return "", ErrVerificationInvalidToken
	}

	return parts[1], nil
}
