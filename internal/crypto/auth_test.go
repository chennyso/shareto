package crypto_test

import (
	"strings"
	"testing"
	"time"

	"shareto/internal/crypto"
)

func TestTokenGeneration(t *testing.T) {
	token, err := crypto.GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken failed: %v", err)
	}
	if len(token) != 22 {
		t.Fatalf("Expected 22 characters, got %d (%s)", len(token), token)
	}
}

func TestArgon2idHashAndVerify(t *testing.T) {
	pass := "StrongP@ssw0rd!"
	hash, err := crypto.HashPassword(pass)
	if err != nil {
		t.Fatalf("HashPassword failed: %v", err)
	}

	if !strings.HasPrefix(hash, "$argon2id$v=19$m=65536,t=1,p=2$") {
		t.Fatalf("Unexpected hash format: %s", hash)
	}

	match, err := crypto.VerifyPassword(pass, hash)
	if err != nil || !match {
		t.Fatalf("VerifyPassword failed for correct password: match=%v, err=%v", match, err)
	}

	match, _ = crypto.VerifyPassword("WrongPass", hash)
	if match {
		t.Fatalf("VerifyPassword succeeded for incorrect password!")
	}
}

func TestCSRFTokenVerification(t *testing.T) {
	secret, err := crypto.GenerateSecretKey()
	if err != nil {
		t.Fatalf("GenerateSecretKey failed: %v", err)
	}

	token := "sampleToken1234567890"
	subnet := "192.168.1.0/24"

	csrf := crypto.GenerateCSRFToken(secret, token, subnet, 1*time.Second)
	if err := crypto.VerifyCSRFToken(secret, csrf, token, subnet); err != nil {
		t.Fatalf("VerifyCSRFToken failed for valid token: %v", err)
	}

	// Mismatched token
	if err := crypto.VerifyCSRFToken(secret, csrf, "differentToken", subnet); err == nil {
		t.Fatalf("Expected error for mismatched token")
	}

	// Mismatched subnet
	if err := crypto.VerifyCSRFToken(secret, csrf, token, "10.0.0.0/24"); err == nil {
		t.Fatalf("Expected error for mismatched subnet")
	}

	// Expired token
	expiredCSRF := crypto.GenerateCSRFToken(secret, token, subnet, -1*time.Second)
	if err := crypto.VerifyCSRFToken(secret, expiredCSRF, token, subnet); err == nil {
		t.Fatalf("Expected error for expired token")
	}
}
