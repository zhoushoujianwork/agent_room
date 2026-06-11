package relay

import (
	"errors"
	"strings"
	"testing"
)

func TestEncryptDecryptRoundTrip(t *testing.T) {
	key := deriveSecretKey("a-secret-passphrase")
	if len(key) != 32 {
		t.Fatalf("derived key len = %d, want 32", len(key))
	}
	plain := "sk-ant-abcd1234567890"
	cipher, err := encryptSecret(key, plain)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if strings.Contains(cipher, plain) {
		t.Fatalf("ciphertext leaks plaintext: %q", cipher)
	}
	got, err := decryptSecret(key, cipher)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if got != plain {
		t.Fatalf("roundtrip = %q, want %q", got, plain)
	}
}

func TestEncryptNonDeterministic(t *testing.T) {
	key := deriveSecretKey("k")
	a, _ := encryptSecret(key, "same")
	b, _ := encryptSecret(key, "same")
	if a == b {
		t.Fatalf("nonce reuse: two encryptions produced identical ciphertext")
	}
}

func TestEncryptRequiresKey(t *testing.T) {
	if _, err := encryptSecret(nil, "x"); !errors.Is(err, errSecretKeyUnset) {
		t.Fatalf("encrypt without key err = %v, want errSecretKeyUnset", err)
	}
	// Empty plaintext decrypts to "" even without a key (nothing stored).
	if got, err := decryptSecret(nil, ""); err != nil || got != "" {
		t.Fatalf("decrypt empty = %q,%v want \"\",nil", got, err)
	}
}

func TestDecryptWrongKeyFails(t *testing.T) {
	cipher, _ := encryptSecret(deriveSecretKey("right"), "secret")
	if _, err := decryptSecret(deriveSecretKey("wrong"), cipher); err == nil {
		t.Fatalf("decrypt with wrong key unexpectedly succeeded")
	}
}

func TestMaskAPIKey(t *testing.T) {
	cases := map[string]string{
		"":                      "",
		"short":                 "***",
		"sk-ant-1234567890abcd": "sk-***abcd",
	}
	for in, want := range cases {
		if got := maskAPIKey(in); got != want {
			t.Errorf("maskAPIKey(%q) = %q, want %q", in, got, want)
		}
	}
}
