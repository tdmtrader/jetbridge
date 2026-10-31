// Package encryption holds the one key the web encrypts sensitive database
// columns with. Every web and every migration run is started with the same
// key (--encryption-key); there is no plaintext mode and no rotation. A
// cluster that needs a different key restores the database from a backup
// taken under that key, as it would for any other forward-only change.
//
// An encrypted column carries a nonce column beside it, and a row whose nonce
// is NULL holds plaintext: that is the schema's convention, and the migrator
// encrypts every such row at the end of each run. Decrypt honours it, so a Go
// migration can read a row a keyless deployment once wrote.
package encryption

import (
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
)

// ErrNoKey is returned wherever a key is required and none was given: opening
// a database connection, running migrations, starting the web.
var ErrNoKey = errors.New("an encryption key is required")

type Key struct {
	aesgcm cipher.AEAD
}

func NewKey(a cipher.AEAD) *Key {
	return &Key{
		aesgcm: a,
	}
}

func (e Key) Encrypt(plaintext []byte) (string, *string, error) {
	nonce := make([]byte, 12)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", nil, err
	}

	ciphertext := e.aesgcm.Seal(nil, nonce, plaintext, nil)

	noncense := hex.EncodeToString(nonce)

	return hex.EncodeToString(ciphertext), &noncense, nil
}

// Decrypt returns the plaintext of a row. A row without a nonce is plaintext
// already and is returned as is.
func (e Key) Decrypt(text string, n *string) ([]byte, error) {
	if n == nil {
		return []byte(text), nil
	}

	ciphertext, err := hex.DecodeString(text)
	if err != nil {
		return nil, err
	}

	nonce, err := hex.DecodeString(*n)
	if err != nil {
		return nil, err
	}

	plaintext, err := e.aesgcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, err
	}

	return plaintext, nil
}
