// Package pagination defines the opaque keyset cursor shared by durable
// agent-history APIs. A cursor identifies the last value returned from an
// exact (created_at DESC, id DESC) ordering; stores apply it exclusively.
package pagination

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"time"
)

const (
	cursorVersion = byte(1)
	cursorBytes   = 1 + 8 + 4 + 8
)

// Cursor is the exclusive lower key for a descending durable-history page.
// ID is the positive signed 64-bit database identity of the returned value.
type Cursor struct {
	CreatedAt time.Time
	ID        int64
}

// Validate rejects values that cannot be represented canonically or safely
// compared with PostgreSQL timestamptz and signed bigint values.
func (cursor Cursor) Validate() error {
	year := cursor.CreatedAt.Year()
	if cursor.CreatedAt.IsZero() || year < 1 || year > 9999 {
		return fmt.Errorf("pagination: cursor creation time is invalid")
	}
	if cursor.CreatedAt.Nanosecond()%1_000 != 0 {
		return fmt.Errorf("pagination: cursor creation time exceeds database precision")
	}
	if cursor.ID <= 0 {
		return fmt.Errorf("pagination: cursor ID must be positive")
	}
	return nil
}

// Encode returns a versioned, unpadded URL-safe binary token. Encoding the
// same instant and ID always produces exactly the same token.
func Encode(cursor Cursor) (string, error) {
	if err := cursor.Validate(); err != nil {
		return "", err
	}
	instant := cursor.CreatedAt.UTC()
	raw := make([]byte, cursorBytes)
	raw[0] = cursorVersion
	binary.BigEndian.PutUint64(raw[1:9], uint64(instant.Unix()))
	binary.BigEndian.PutUint32(raw[9:13], uint32(instant.Nanosecond()))
	binary.BigEndian.PutUint64(raw[13:21], uint64(cursor.ID))
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// Decode accepts only the canonical current cursor representation.
func Decode(raw string) (Cursor, error) {
	if raw == "" {
		return Cursor{}, fmt.Errorf("pagination: cursor is empty")
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(raw)
	if err != nil || len(decoded) != cursorBytes || decoded[0] != cursorVersion {
		return Cursor{}, fmt.Errorf("pagination: cursor is malformed")
	}
	nanosecond := binary.BigEndian.Uint32(decoded[9:13])
	if nanosecond >= uint32(time.Second) {
		return Cursor{}, fmt.Errorf("pagination: cursor nanosecond is invalid")
	}
	cursor := Cursor{
		CreatedAt: time.Unix(int64(binary.BigEndian.Uint64(decoded[1:9])), int64(nanosecond)).UTC(),
		ID:        int64(binary.BigEndian.Uint64(decoded[13:21])),
	}
	if err := cursor.Validate(); err != nil {
		return Cursor{}, err
	}
	canonical, err := Encode(cursor)
	if err != nil || canonical != raw {
		return Cursor{}, fmt.Errorf("pagination: cursor is not canonical")
	}
	return cursor, nil
}
