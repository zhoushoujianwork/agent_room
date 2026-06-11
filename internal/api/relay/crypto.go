package relay

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

// errSecretKeyUnset is returned by encryptSecret when the relay was started
// without AGENT_ROOM_SECRET_KEY. model/api_base_url are unaffected; only api
// key persistence requires the key (acceptance criterion 6).
var errSecretKeyUnset = errors.New("AGENT_ROOM_SECRET_KEY not configured; cannot store agent api_key")

// deriveSecretKey turns the operator-provided AGENT_ROOM_SECRET_KEY into a
// fixed 32-byte AES-256 key. We SHA-256 the raw value so any non-empty secret
// works (no exact-length requirement), and return nil when unset so callers can
// detect "no key configured" via len()==0.
func deriveSecretKey(raw string) []byte {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}

// encryptSecret seals plaintext with AES-256-GCM and returns base64(nonce ||
// ciphertext). Returns errSecretKeyUnset when no key is configured.
func encryptSecret(key []byte, plaintext string) (string, error) {
	if len(key) == 0 {
		return "", errSecretKeyUnset
	}
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// decryptSecret reverses encryptSecret. An empty input decrypts to "" (no key
// stored). Returns an error on a configured-but-wrong key or corrupt data.
func decryptSecret(key []byte, b64 string) (string, error) {
	if strings.TrimSpace(b64) == "" {
		return "", nil
	}
	if len(key) == 0 {
		return "", errSecretKeyUnset
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", fmt.Errorf("decode cipher: %w", err)
	}
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("cipher too short")
	}
	nonce, body := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, body, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt: %w", err)
	}
	return string(plain), nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("new cipher: %w", err)
	}
	return cipher.NewGCM(block)
}

// maskAPIKey produces a display-safe rendering of an api key: a short prefix,
// "***", then the last 4 chars (e.g. "sk-***abcd"). Short keys collapse to
// "***" so we never reveal a meaningful fraction of a tiny secret.
func maskAPIKey(key string) string {
	if key == "" {
		return ""
	}
	if len(key) < 8 {
		return "***"
	}
	return key[:3] + "***" + key[len(key)-4:]
}
