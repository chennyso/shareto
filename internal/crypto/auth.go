package crypto

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

const (
	argonTime    = 1
	argonMemory  = 64 * 1024 // 64MB
	argonThreads = 2
	argonKeyLen  = 32
	argonSaltLen = 16
)

var (
	ErrInvalidHash   = errors.New("invalid argon2id hash format")
	ErrCSRFExpired   = errors.New("csrf token expired")
	ErrCSRFMismatch   = errors.New("csrf token mismatch")
	ErrCSRFMalformed = errors.New("csrf token malformed")
)

// GenerateToken generates a cryptographically secure 128-bit (16-byte) token
// encoded in 22-character raw URL-safe base64.
func GenerateToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// GenerateSecretKey generates 32 random bytes for internal symmetric operations (e.g. CSRF signing).
func GenerateSecretKey() ([]byte, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("failed to generate secret key: %w", err)
	}
	return key, nil
}

// HashPassword hashes a password using Argon2id with fixed parameters:
// time=1, memory=64MB, threads=2, keyLen=32, saltLen=16.
// Output format: $argon2id$v=19$m=65536,t=1,p=2$<base64_salt>$<base64_hash>
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("failed to generate salt: %w", err)
	}

	hash := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)

	b64Salt := base64.RawStdEncoding.EncodeToString(salt)
	b64Hash := base64.RawStdEncoding.EncodeToString(hash)

	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		argonMemory, argonTime, argonThreads, b64Salt, b64Hash), nil
}

// VerifyPassword checks whether a password matches the Argon2id hash string.
func VerifyPassword(password, encodedHash string) (bool, error) {
	parts := strings.Split(encodedHash, "$")
	if len(parts) != 6 {
		return false, ErrInvalidHash
	}
	if parts[1] != "argon2id" {
		return false, fmt.Errorf("unsupported algorithm: %s", parts[1])
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, fmt.Errorf("incompatible argon2 version: %s", parts[2])
	}

	var memory uint32
	var iterations uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &threads); err != nil {
		return false, fmt.Errorf("failed to parse argon2 parameters: %w", err)
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		// Fallback to std encoding with padding
		salt, err = base64.StdEncoding.DecodeString(parts[4])
		if err != nil {
			return false, fmt.Errorf("invalid salt encoding: %w", err)
		}
	}

	expectedHash, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		// Fallback to std encoding with padding
		expectedHash, err = base64.StdEncoding.DecodeString(parts[5])
		if err != nil {
			return false, fmt.Errorf("invalid hash encoding: %w", err)
		}
	}

	computedHash := argon2.IDKey([]byte(password), salt, iterations, memory, threads, uint32(len(expectedHash)))

	if subtle.ConstantTimeCompare(computedHash, expectedHash) == 1 {
		return true, nil
	}
	return false, nil
}

// GenerateCSRFToken generates a 10-minute HMAC-SHA256 signed CSRF token.
// Payload format: token|clientSubnet|unixTimestamp
// Returned token: base64RawURL(payload) + "." + base64RawURL(signature)
func GenerateCSRFToken(secret []byte, token, clientSubnet string, validDuration time.Duration) string {
	expiresAt := time.Now().Add(validDuration).Unix()
	payload := fmt.Sprintf("%s|%s|%d", token, clientSubnet, expiresAt)

	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))
	sig := mac.Sum(nil)

	return fmt.Sprintf("%s.%s",
		base64.RawURLEncoding.EncodeToString([]byte(payload)),
		base64.RawURLEncoding.EncodeToString(sig),
	)
}

// VerifyCSRFToken verifies the CSRF token against secret, expected token, and clientSubnet.
func VerifyCSRFToken(secret []byte, rawCSRF, expectedToken, clientSubnet string) error {
	parts := strings.Split(rawCSRF, ".")
	if len(parts) != 2 {
		return ErrCSRFMalformed
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return ErrCSRFMalformed
	}

	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ErrCSRFMalformed
	}

	// Verify HMAC signature in constant time
	mac := hmac.New(sha256.New, secret)
	mac.Write(payloadBytes)
	expectedSig := mac.Sum(nil)

	if subtle.ConstantTimeCompare(sig, expectedSig) != 1 {
		return ErrCSRFMismatch
	}

	// Parse payload
	fields := strings.Split(string(payloadBytes), "|")
	if len(fields) != 3 {
		return ErrCSRFMalformed
	}

	payloadToken := fields[0]
	payloadSubnet := fields[1]
	expStr := fields[2]

	if payloadToken != expectedToken || payloadSubnet != clientSubnet {
		return ErrCSRFMismatch
	}

	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil {
		return ErrCSRFMalformed
	}

	if time.Now().Unix() >= exp {
		return ErrCSRFExpired
	}

	return nil
}
