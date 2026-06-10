package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
)

// encPrefix tags app-level ciphertext so Decrypt can pass plaintext through
// unchanged (rows written before encryption was enabled, or when no key is set).
const encPrefix = "enc:v1:"

// Cipher provides authenticated field-level encryption (AES-256-GCM) for
// sensitive values stored at rest (tool args/diffs, message content, memories,
// the audit payload). It complements — not replaces — an encrypted volume.
type Cipher struct{ aead cipher.AEAD }

// DeriveKey turns a passphrase into a 32-byte key via SHA-256. For production,
// prefer a real KDF (scrypt/argon2) or a KMS-managed data key; this keeps the
// scaffold dependency-free.
func DeriveKey(passphrase string) []byte {
	sum := sha256.Sum256([]byte(passphrase))
	return sum[:]
}

// NewCipher builds a Cipher from a 32-byte key.
func NewCipher(key []byte) (*Cipher, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Cipher{aead: aead}, nil
}

// Encrypt returns a tagged, base64 ciphertext (nonce prepended).
func (c *Cipher) Encrypt(plaintext string) (string, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := c.aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return encPrefix + base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt reverses Encrypt. Input without the encPrefix is returned unchanged so
// pre-encryption (plaintext) rows keep working after a key is introduced.
func (c *Cipher) Decrypt(s string) (string, error) {
	if !strings.HasPrefix(s, encPrefix) {
		return s, nil
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(s, encPrefix))
	if err != nil {
		return "", err
	}
	ns := c.aead.NonceSize()
	if len(raw) < ns {
		return "", errors.New("ciphertext too short")
	}
	nonce, ct := raw[:ns], raw[ns:]
	pt, err := c.aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt: %w", err)
	}
	return string(pt), nil
}
