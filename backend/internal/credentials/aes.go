package credentials

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
)

// AESSealer is the Sealer: AES-256-GCM with a random nonce stored in front of the ciphertext.
type AESSealer struct{ aead cipher.AEAD }

// NewAESSealer returns a sealer for a 32-byte key.
func NewAESSealer(key []byte) (*AESSealer, error) {
	if len(key) != 32 {
		return nil, errors.New("credentials: the seal key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &AESSealer{aead: aead}, nil
}

// Seal implements Sealer.
func (s *AESSealer) Seal(plain []byte) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return s.aead.Seal(nonce, nonce, plain, nil), nil
}

// Open implements Sealer.
func (s *AESSealer) Open(sealed []byte) ([]byte, error) {
	n := s.aead.NonceSize()
	if len(sealed) < n {
		return nil, errors.New("credentials: sealed value too short")
	}
	return s.aead.Open(nil, sealed[:n], sealed[n:], nil)
}
