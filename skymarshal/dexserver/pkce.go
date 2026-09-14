package dexserver

import (
	"encoding/base64"
	"net/http"
	"strings"
)

// RequireDesktopPKCE closes the pinned issuer's optional-PKCE behavior for the
// public fly browser client. Other existing client contracts remain unchanged.
func RequireDesktopPKCE(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/auth") && r.URL.Query().Get("client_id") == "fly-browser" {
			query := r.URL.Query()
			digest, err := base64.RawURLEncoding.DecodeString(query.Get("code_challenge"))
			if query.Get("code_challenge_method") != "S256" || err != nil || len(digest) != 32 {
				http.Error(w, "fly browser login requires S256 PKCE", http.StatusBadRequest)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
