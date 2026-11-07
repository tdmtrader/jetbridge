package db

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"code.cloudfoundry.org/lager/v3"
	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/atc/db/migration"
	"github.com/go-jose/go-jose/v4"
)

type SigningKeyType string

const (
	SigningKeyTypeRSA SigningKeyType = "RSA"
	SigningKeyTypeEC  SigningKeyType = "EC"
)

type SigningKeyFactory interface {
	CreateKey(jwk jose.JSONWebKey) error
	GetAllKeys() ([]SigningKey, error)
	GetNewestKey(keyType SigningKeyType) (SigningKey, error)
}

type signingKeyFactory struct {
	logger         lager.Logger
	conn           DbConn
	encryptOnWrite bool
}

// NewSigningKeyFactory stores keys encrypted only once
// migration.SigningKeysEncryptedAtRest is on; it reads both forms always.
func NewSigningKeyFactory(logger lager.Logger, conn DbConn) SigningKeyFactory {
	return &signingKeyFactory{
		logger:         logger.Session("signing-key-factory"),
		conn:           conn,
		encryptOnWrite: migration.SigningKeysEncryptedAtRest,
	}
}

// CreateKey stores the key as its JSON, or, when writes are encrypted, as its
// ciphertext in a JSON string, which the json column accepts.
func (f *signingKeyFactory) CreateKey(jwk jose.JSONWebKey) error {
	encoded, err := json.Marshal(jwk)
	if err != nil {
		return err
	}

	stored, nonce := string(encoded), (*string)(nil)
	if f.encryptOnWrite {
		var ciphertext string
		ciphertext, nonce, err = f.conn.EncryptionStrategy().Encrypt(encoded)
		if err != nil {
			return err
		}
		if nonce != nil {
			quoted, err := json.Marshal(ciphertext)
			if err != nil {
				return err
			}
			stored = string(quoted)
		}
	}

	_, err = psql.Insert("signing_keys").
		Columns("kid", "kty", "jwk", "nonce").
		Values(jwk.KeyID, signingKeyTypeFromAlg(jwk.Algorithm), stored, nonce).
		RunWith(f.conn).
		Exec()
	return err
}

func (f *signingKeyFactory) GetAllKeys() ([]SigningKey, error) {
	return f.keys(psql.Select("kid", "jwk", "nonce", "created_at").
		From("signing_keys").
		OrderBy("created_at ASC"))
}

func (f *signingKeyFactory) GetNewestKey(keyType SigningKeyType) (SigningKey, error) {
	keys, err := f.keys(psql.Select("kid", "jwk", "nonce", "created_at").
		From("signing_keys").
		Where(sq.Eq{"kty": keyType}).
		OrderBy("created_at DESC").
		Limit(1))
	if err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return nil, errors.New("no signing key found with specified type")
	}
	return keys[0], nil
}

type storedSigningKey struct {
	kid       string
	jwk       []byte
	nonce     *string
	createdAt time.Time
}

// keys reads the selected rows and decodes each one. A row this web cannot
// decrypt or decode is logged and left out, so one bad row does not take
// down the JWKS for every key.
func (f *signingKeyFactory) keys(query sq.SelectBuilder) ([]SigningKey, error) {
	stored, err := f.storedKeys(query)
	if err != nil {
		return nil, err
	}

	var signingKeys []SigningKey
	for _, row := range stored {
		jwk, err := f.decode(row)
		if err != nil {
			f.logger.Error("skipped-unreadable-signing-key", err, lager.Data{"kid": row.kid})
			continue
		}
		signingKeys = append(signingKeys, signingKey{jwk: jwk, createdAt: row.createdAt, conn: f.conn})
	}
	return signingKeys, nil
}

func (f *signingKeyFactory) storedKeys(query sq.SelectBuilder) ([]storedSigningKey, error) {
	rows, err := query.RunWith(f.conn).Query()
	if err != nil {
		return nil, err
	}
	defer Close(rows)

	var stored []storedSigningKey
	for rows.Next() {
		var row storedSigningKey
		if err := rows.Scan(&row.kid, &row.jwk, &row.nonce, &row.createdAt); err != nil {
			return nil, err
		}
		stored = append(stored, row)
	}
	return stored, rows.Err()
}

func (f *signingKeyFactory) decode(row storedSigningKey) (jose.JSONWebKey, error) {
	var jwk jose.JSONWebKey
	plaintext := row.jwk
	if row.nonce != nil {
		var ciphertext string
		if err := json.Unmarshal(row.jwk, &ciphertext); err != nil {
			return jwk, err
		}
		var err error
		plaintext, err = f.conn.EncryptionStrategy().Decrypt(ciphertext, row.nonce)
		if err != nil {
			return jwk, err
		}
	}
	err := json.Unmarshal(plaintext, &jwk)
	return jwk, err
}

// the jwk type does not expose the kty header to us, so we have to infer it from the alg
// Key Types: https://datatracker.ietf.org/doc/html/rfc7518#section-6.1
func signingKeyTypeFromAlg(alg string) SigningKeyType {
	keyType := SigningKeyTypeRSA
	if strings.HasPrefix(alg, "ES") {
		keyType = SigningKeyTypeEC
	}
	return keyType
}
