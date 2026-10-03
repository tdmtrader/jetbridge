package encryption

type noEncryption struct{}

func NewNoEncryption() *noEncryption {
	return &noEncryption{}
}

func (n noEncryption) Encrypt(plaintext []byte) (string, *string, error) {
	return string(plaintext), nil, nil
}

func (n noEncryption) Decrypt(text string, nonce *string) ([]byte, error) {
	if nonce != nil {
		return nil, ErrDataIsEncrypted
	}

	return []byte(text), nil
}
