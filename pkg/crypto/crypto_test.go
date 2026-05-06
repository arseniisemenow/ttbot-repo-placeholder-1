package crypto

import (
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
)

func freshKey(t *testing.T) string {
	t.Helper()
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(key)
}

func TestRoundTrip(t *testing.T) {
	c, err := New(freshKey(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range []string{"", "x", "secret123", strings.Repeat("a", 1024)} {
		ct, err := c.Encrypt(in)
		if err != nil {
			t.Fatal(err)
		}
		pt, err := c.Decrypt(ct)
		if err != nil {
			t.Fatal(err)
		}
		if pt != in {
			t.Fatalf("got %q want %q", pt, in)
		}
	}
}

func TestBadKeySize(t *testing.T) {
	if _, err := NewFromKey(make([]byte, 16)); err != ErrKeySize {
		t.Fatalf("want ErrKeySize, got %v", err)
	}
}

func TestTamperedCiphertextFails(t *testing.T) {
	c, _ := New(freshKey(t))
	ct, _ := c.Encrypt("secret")
	raw, _ := base64.StdEncoding.DecodeString(ct)
	raw[len(raw)-1] ^= 1 // flip last bit
	tampered := base64.StdEncoding.EncodeToString(raw)
	if _, err := c.Decrypt(tampered); err == nil {
		t.Fatal("expected decrypt error on tampered ciphertext")
	}
}

func TestEncryptedDiffersAcrossCalls(t *testing.T) {
	c, _ := New(freshKey(t))
	a, _ := c.Encrypt("same")
	b, _ := c.Encrypt("same")
	if a == b {
		t.Fatal("encryption should produce a different nonce each time")
	}
}
