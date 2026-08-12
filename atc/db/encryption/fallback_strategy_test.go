package encryption_test

import (
	"crypto/aes"
	"crypto/cipher"

	"github.com/concourse/concourse/atc/db/encryption"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func newEncryptionKey(k string) *encryption.Key {
	block, err := aes.NewCipher([]byte(k))
	Expect(err).ToNot(HaveOccurred())

	aesgcm, err := cipher.NewGCM(block)
	Expect(err).ToNot(HaveOccurred())

	return encryption.NewKey(aesgcm)
}

var _ = Describe("Encryption Key with Fallback", func() {
	var (
		key      *encryption.Key
		strategy *encryption.FallbackStrategy
	)

	BeforeEach(func() {
		key = newEncryptionKey("AES256Key-32Characters1234567890")

		strategy = encryption.NewFallbackStrategy(key, encryption.NewNoEncryption())
	})

	It("encrypts with the main key", func() {
		encryptedText, nonce, err := strategy.Encrypt([]byte("plaintext"))
		Expect(err).ToNot(HaveOccurred())
		Expect(nonce).ToNot(BeNil())
		Expect(encryptedText).ToNot(Equal("plaintext"))

		decryptedText, err := key.Decrypt(encryptedText, nonce)
		Expect(err).ToNot(HaveOccurred())
		Expect(decryptedText).To(Equal([]byte("plaintext")))
	})

	Context("when the data is encrypted with the main key", func() {
		It("decrypts it with the main key", func() {
			encryptedText, nonce, err := key.Encrypt([]byte("plaintext"))
			Expect(err).ToNot(HaveOccurred())

			decryptedText, err := strategy.Decrypt(encryptedText, nonce)
			Expect(err).ToNot(HaveOccurred())
			Expect(decryptedText).To(Equal([]byte("plaintext")))
		})
	})

	Context("when the data is not encrypted", func() {
		It("falls back to reading it as plaintext", func() {
			decryptedText, err := strategy.Decrypt("plaintext", nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(decryptedText).To(Equal([]byte("plaintext")))
		})
	})

	Context("when the data is encrypted with a different key", func() {
		It("errors instead of returning the ciphertext", func() {
			otherKey := newEncryptionKey("AES256Key-32Characters9564567123")

			encryptedText, nonce, err := otherKey.Encrypt([]byte("plaintext"))
			Expect(err).ToNot(HaveOccurred())

			decryptedText, err := strategy.Decrypt(encryptedText, nonce)
			Expect(err).To(MatchError(encryption.ErrDataIsEncrypted))
			Expect(decryptedText).To(BeNil())
		})
	})
})
