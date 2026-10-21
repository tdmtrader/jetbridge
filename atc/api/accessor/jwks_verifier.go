package accessor

import (
	"context"
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

	mu        sync.RWMutex
	keys      *jose.JSONWebKeySet
	fetchedAt time.Time
	cacheTTL  time.Duration

	// A failed fetch is remembered for retryAfter so that requests during an
	// outage are refused from memory instead of each re-fetching.
	failedAt   time.Time
	failErr    error
	retryAfter time.Duration

	// refresh is the fetch in flight, shared by every request that misses
	// while it runs.
	refresh      *keyRefresh
	fetchTimeout time.Duration
}

type keyRefresh struct {
	done chan struct{}
	err  error
}

// NewJWKSVerifier creates a verifier that validates JWTs by fetching public
// keys from the given JWKS URL (typically Dex's /keys endpoint).
func NewJWKSVerifier(jwksURL string, audience []string) TokenVerifier {
	return &jwksVerifier{
		jwksURL:  jwksURL,
		audience: audience,
		cacheTTL: 5 * time.Minute,

		retryAfter:   5 * time.Second,
		fetchTimeout: 10 * time.Second,
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
	key, err := v.getKey(r.Context(), kid)
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

func (v *jwksVerifier) getKey(ctx context.Context, kid string) (any, error) {
	v.mu.RLock()
	key := v.findKey(kid)
	v.mu.RUnlock()
	if key != nil {
		return key, nil
	}

	v.mu.Lock()
	if key := v.findKey(kid); key != nil {
		v.mu.Unlock()
		return key, nil
	}
	// If cache is still valid and key not found, it's genuinely unknown
	if v.keys != nil && time.Since(v.fetchedAt) < v.cacheTTL {
		v.mu.Unlock()
		return nil, errors.New("unknown key ID")
	}
	if v.failErr != nil && time.Since(v.failedAt) < v.retryAfter {
		err := v.failErr
		v.mu.Unlock()
		return nil, err
	}
	refresh := v.refresh
	if refresh == nil {
		refresh = &keyRefresh{done: make(chan struct{})}
		v.refresh = refresh
		// The fetch is shared, so it must outlive the request that started it.
		go v.runRefresh(context.WithoutCancel(ctx), refresh)
	}
	v.mu.Unlock()

	select {
	case <-refresh.done:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if refresh.err != nil {
		return nil, refresh.err
	}

	v.mu.RLock()
	key = v.findKey(kid)
	v.mu.RUnlock()

	if key == nil {
		return nil, errors.New("unknown key ID after refresh")
	}
	return key, nil
}

func (v *jwksVerifier) runRefresh(ctx context.Context, refresh *keyRefresh) {
	ctx, cancel := context.WithTimeout(ctx, v.fetchTimeout)
	defer cancel()

	keys, err := v.fetchKeys(ctx)

	v.mu.Lock()
	if err != nil {
		v.failErr = err
		v.failedAt = time.Now()
	} else {
		v.keys = keys
		v.fetchedAt = time.Now()
		v.failErr = nil
	}
	refresh.err = err
	v.refresh = nil
	v.mu.Unlock()

	close(refresh.done)
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

func (v *jwksVerifier) fetchKeys(ctx context.Context) (*jose.JSONWebKeySet, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.jwksURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("JWKS endpoint returned non-200")
	}

	var jwks jose.JSONWebKeySet
	if err := json.NewDecoder(resp.Body).Decode(&jwks); err != nil {
		return nil, err
	}

	return &jwks, nil
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
