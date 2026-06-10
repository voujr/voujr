package security

import (
	"strings"
	"testing"
)

func newTestCipher(t *testing.T) *Cipher {
	t.Helper()
	c, err := NewCipher(DeriveKey("correct horse battery staple"))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCipherRoundTrip(t *testing.T) {
	c := newTestCipher(t)
	plaintext := `{"namespace":"prod","token":"s3cr3t"}`

	ct, err := c.Encrypt(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ct, encPrefix) {
		t.Fatalf("ciphertext missing tag: %s", ct)
	}
	if strings.Contains(ct, "s3cr3t") {
		t.Fatal("plaintext secret leaked into ciphertext")
	}

	got, err := c.Decrypt(ct)
	if err != nil {
		t.Fatal(err)
	}
	if got != plaintext {
		t.Fatalf("round-trip mismatch: %q != %q", got, plaintext)
	}
}

func TestCipherPlaintextPassthrough(t *testing.T) {
	c := newTestCipher(t)
	// A value without the tag (e.g. a pre-encryption row) decrypts to itself.
	got, err := c.Decrypt("plain old value")
	if err != nil || got != "plain old value" {
		t.Fatalf("expected passthrough, got %q err=%v", got, err)
	}
}

func TestCipherTamperDetected(t *testing.T) {
	c := newTestCipher(t)
	ct, _ := c.Encrypt("hello")
	// Flip a character in the base64 body; GCM auth must reject it.
	tampered := ct[:len(ct)-1] + tweak(ct[len(ct)-1])
	if _, err := c.Decrypt(tampered); err == nil {
		t.Fatal("expected tamper to fail authentication")
	}
}

func tweak(b byte) string {
	if b == 'A' {
		return "B"
	}
	return "A"
}

func TestNewCipherRejectsShortKey(t *testing.T) {
	if _, err := NewCipher([]byte("too short")); err == nil {
		t.Fatal("expected error for non-32-byte key")
	}
}
