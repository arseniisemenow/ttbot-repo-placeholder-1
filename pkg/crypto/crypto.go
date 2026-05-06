// Package crypto wraps AES-256-GCM with a base64 envelope. Used for
// admins.s21_credentials_encrypted.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

// KeySize is the required key length: 32 bytes for AES-256.
const KeySize = 32

// ErrKeySize is returned when the provided key is not 32 bytes.
var ErrKeySize = errors.New("encryption key must be 32 bytes (AES-256)")

// Cipher is a thread-safe AES-256-GCM encrypter/decrypter.
type Cipher struct {
	aead cipher.AEAD
}

// New creates a Cipher from a base64-encoded 32-byte key.
func New(base64Key string) (*Cipher, error) {
	key, err := base64.StdEncoding.DecodeString(base64Key)
	if err != nil {
		return nil, fmt.Errorf("decode key: %w", err)
	}
	return NewFromKey(key)
}

// NewFromKey creates a Cipher from a raw 32-byte key.
func NewFromKey(key []byte) (*Cipher, error) {
	if len(key) != KeySize {
		return nil, ErrKeySize
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("gcm: %w", err)
	}
	return &Cipher{aead: aead}, nil
}

// Encrypt returns base64(nonce || ciphertext || tag).
func (c *Cipher) Encrypt(plaintext string) (string, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("nonce: %w", err)
	}
	ct := c.aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ct), nil
}

// Decrypt reverses Encrypt.
func (c *Cipher) Decrypt(b64 string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}
	ns := c.aead.NonceSize()
	if len(raw) < ns {
		return "", errors.New("ciphertext too short")
	}
	nonce, body := raw[:ns], raw[ns:]
	pt, err := c.aead.Open(nil, nonce, body, nil)
	if err != nil {
		return "", fmt.Errorf("open: %w", err)
	}
	return string(pt), nil
}
