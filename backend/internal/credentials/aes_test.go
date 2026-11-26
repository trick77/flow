package credentials

import (
	"bytes"
	"testing"
)

func TestAESSealer(t *testing.T) {
	s, err := NewAESSealer(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := s.Seal([]byte("token"))
	if err != nil || bytes.Contains(sealed, []byte("token")) {
		t.Fatalf("Seal = %x, %v", sealed, err)
	}
	if plain, err := s.Open(sealed); err != nil || string(plain) != "token" {
		t.Errorf("Open = %q, %v", plain, err)
	}
	sealed[len(sealed)-1] ^= 1
	if _, err := s.Open(sealed); err == nil {
		t.Error("a tampered value opened")
	}
	if _, err := s.Open([]byte{1}); err == nil {
		t.Error("a short value opened")
	}
	if _, err := NewAESSealer([]byte("short")); err == nil {
		t.Error("a short key accepted")
	}
}
