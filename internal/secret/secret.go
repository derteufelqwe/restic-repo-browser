// Package secret encrypts small values (repository passwords) with AES-256-GCM.
// The key lives in a file next to the database, so a copied database alone does
// not reveal the passwords.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
)

const keySize = 32

// Box seals and opens values with a single symmetric key.
type Box struct {
	aead cipher.AEAD
}

// NewBox builds a Box from a 32-byte key.
func NewBox(key []byte) (*Box, error) {
	if len(key) != keySize {
		return nil, fmt.Errorf("secret: key must be %d bytes, got %d", keySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secret: create cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secret: create gcm: %w", err)
	}
	return &Box{aead: aead}, nil
}

// LoadOrCreate reads the key file at path, creating a random key (mode 0600)
// when it does not exist yet.
func LoadOrCreate(path string) (*Box, error) {
	key, err := os.ReadFile(path)
	switch {
	case err == nil:
	case errors.Is(err, os.ErrNotExist):
		key = make([]byte, keySize)
		if _, err := rand.Read(key); err != nil {
			return nil, fmt.Errorf("secret: generate key: %w", err)
		}
		if err := os.WriteFile(path, key, 0o600); err != nil {
			return nil, fmt.Errorf("secret: write key file: %w", err)
		}
	default:
		return nil, fmt.Errorf("secret: read key file: %w", err)
	}
	return NewBox(key)
}

// Seal encrypts plaintext. The random nonce is prepended to the result.
func (b *Box) Seal(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("secret: nonce: %w", err)
	}
	return b.aead.Seal(nonce, nonce, plaintext, nil), nil
}

// Open decrypts a value produced by Seal.
func (b *Box) Open(sealed []byte) ([]byte, error) {
	n := b.aead.NonceSize()
	if len(sealed) < n {
		return nil, errors.New("secret: sealed value too short")
	}
	plain, err := b.aead.Open(nil, sealed[:n], sealed[n:], nil)
	if err != nil {
		return nil, fmt.Errorf("secret: decrypt (wrong key file?): %w", err)
	}
	return plain, nil
}
