package token

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

// EnsureUser wraps a handler (typically Dex) and creates/updates the user
// record in Concourse's database when a token is issued. Unlike the old
// StoreAccessToken, it does NOT replace the access token — Dex's tokens
// pass through unmodified.
func EnsureUser(
	logger lager.Logger,
	handler http.Handler,
	claimsParser ClaimsParser,
	userFactory db.UserFactory,
	displayUserIdGenerator atc.DisplayUserIdGenerator,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sky/issuer/token" {
			handler.ServeHTTP(w, r)
			return
		}
		logger := logger.Session("token-request")

		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, r)

		// Copy the original response to the client regardless of what happens
		defer func() {
			copyHeaders(w, rec.Result())
			io.Copy(w, rec.Body)
		}()

		if rec.Code < 200 || rec.Code > 299 {
			return
		}

		// Parse the response to extract id_token for user creation
		var resp struct {
			IDToken string `json:"id_token"`
		}
		// Read and re-buffer the body since we need it twice
		body := rec.Body.Bytes()
		if err := json.Unmarshal(body, &resp); err != nil {
			logger.Error("unmarshal-response", err)
			return
		}
		rec.Body = bytes.NewBuffer(body)

		if resp.IDToken == "" {
			return
		}

		claims, err := claimsParser.ParseClaims(resp.IDToken)
		if err != nil {
			logger.Error("parse-id-token", err)
			return
		}

		username := displayUserIdGenerator.DisplayUserId(
			claims.Connector,
			claims.UserID,
			claims.Username,
			claims.PreferredUsername,
			claims.Email,
		)

		if err := userFactory.CreateOrUpdateUser(username, claims.Connector, claims.Subject); err != nil {
			logger.Error("create-or-update-user", err)
		}
	})
}

func copyHeaders(w http.ResponseWriter, res *http.Response) {
	for k, v := range res.Header {
		k = http.CanonicalHeaderKey(k)
		if k != "Content-Length" {
			w.Header()[k] = v
		}
	}
	w.WriteHeader(res.StatusCode)
}
