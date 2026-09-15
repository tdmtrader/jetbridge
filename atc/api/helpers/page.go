package helpers

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
)

// Bounded pages are additive API modes. Cursors bind the exact query, not an
// authority: every page still runs the normal role, visibility and policy checks.
type pageCursor struct {
	Version  int    `json:"v"`
	Query    string `json:"q"`
	Position int    `json:"p"`
}

func PagePosition(token, query string) (int, error) {
	if token == "" {
		return 0, nil
	}
	if len(token) > 2048 {
		return 0, errors.New("INVALID_CURSOR: oversized page cursor")
	}
	data, err := base64.RawURLEncoding.DecodeString(token)
	var cursor pageCursor
	if err != nil || json.Unmarshal(data, &cursor) != nil || cursor.Version != 1 || cursor.Query != pageQueryKey(query) || cursor.Position < 1 {
		return 0, errors.New("INVALID_CURSOR: cursor does not match the page query")
	}
	return cursor.Position, nil
}
func PageCursor(position int, query string) string {
	data, _ := json.Marshal(pageCursor{1, pageQueryKey(query), position})
	return base64.RawURLEncoding.EncodeToString(data)
}
func pageQueryKey(query string) string {
	sum := sha256.Sum256([]byte(query))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
func PageLimit(query url.Values) (int, error) {
	if query.Get("limit") == "" {
		return 20, nil
	}
	limit, err := strconv.Atoi(query.Get("limit"))
	if err != nil || limit < 1 || limit > 100 {
		return 0, errors.New("limit must be in 1..100")
	}
	return limit, nil
}
