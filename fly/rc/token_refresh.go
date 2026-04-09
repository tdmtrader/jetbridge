package rc

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

// refreshingTokenSource wraps a static token and transparently refreshes
// it via /sky/token/refresh when the JWT is near expiry.
type refreshingTokenSource struct {
	token      *TargetToken
	targetName TargetName
	targetAPI  string
	base       http.RoundTripper
}

func newRefreshingTokenSource(token *TargetToken, targetName TargetName, targetAPI string, base http.RoundTripper) oauth2.TokenSource {
	return &refreshingTokenSource{
		token:      token,
		targetName: targetName,
		targetAPI:  targetAPI,
		base:       base,
	}
}

func (s *refreshingTokenSource) Token() (*oauth2.Token, error) {
	if s.token == nil {
		return nil, fmt.Errorf("no token available")
	}

	oauthToken := &oauth2.Token{
		TokenType:   s.token.Type,
		AccessToken: s.token.Value,
	}

	// If no refresh token, return as-is (legacy token)
	if s.token.RefreshToken == "" {
		return oauthToken, nil
	}

	// Check if JWT is near expiry (within 60 seconds)
	expiry, err := parseJWTExpiry(s.token.Value)
	if err != nil || time.Until(expiry) > 60*time.Second {
		return oauthToken, nil
	}

	// Attempt refresh
	newToken, err := s.refresh()
	if err != nil {
		// Refresh failed — return old token and let the 401 flow handle it
		return oauthToken, nil
	}

	// Update stored token
	s.token.Value = newToken.AccessToken
	if newToken.RefreshToken != "" {
		s.token.RefreshToken = newToken.RefreshToken
	}

	// Persist to ~/.flyrc
	_ = SaveTargetToken(s.targetName, s.token)

	return &oauth2.Token{
		TokenType:   s.token.Type,
		AccessToken: newToken.AccessToken,
	}, nil
}

func (s *refreshingTokenSource) refresh() (*refreshResponse, error) {
	refreshURL := strings.TrimRight(s.targetAPI, "/") + "/sky/token/refresh"

	form := url.Values{}
	form.Set("refresh_token", s.token.RefreshToken)

	req, err := http.NewRequest("POST", refreshURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Transport: s.base}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("refresh failed: %d %s", resp.StatusCode, string(body))
	}

	var result refreshResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	return &result, nil
}

type refreshResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

// SaveTargetToken updates only the token for a target in ~/.flyrc
func SaveTargetToken(targetName TargetName, token *TargetToken) error {
	targets, err := LoadTargets()
	if err != nil {
		return err
	}
	if t, ok := targets[targetName]; ok {
		t.Token = token
		targets[targetName] = t
		return writeTargets(flyrcPath(), targets)
	}
	return nil
}

func parseJWTExpiry(rawToken string) (time.Time, error) {
	parts := strings.Split(rawToken, ".")
	if len(parts) != 3 {
		return time.Time{}, fmt.Errorf("not a JWT")
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, err
	}

	var claims struct {
		Exp float64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return time.Time{}, err
	}

	if claims.Exp == 0 {
		return time.Time{}, fmt.Errorf("no exp claim")
	}

	return time.Unix(int64(claims.Exp), 0), nil
}
