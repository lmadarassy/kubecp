package api

import (
	"testing"
)

func TestGenerateRandomString(t *testing.T) {
	s1, err := generateRandomString(32)
	if err != nil {
		t.Fatalf("generateRandomString failed: %v", err)
	}
	s2, err := generateRandomString(32)
	if err != nil {
		t.Fatalf("generateRandomString failed: %v", err)
	}

	if s1 == s2 {
		t.Error("two random strings should not be equal")
	}
	if len(s1) == 0 {
		t.Error("random string should not be empty")
	}
}

func TestGenerateCodeVerifier(t *testing.T) {
	v, err := generateCodeVerifier()
	if err != nil {
		t.Fatalf("generateCodeVerifier failed: %v", err)
	}
	// PKCE verifiers should be 43-128 chars
	if len(v) < 43 {
		t.Errorf("code verifier too short: %d chars", len(v))
	}
}

func TestCodeChallenge(t *testing.T) {
	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	challenge := codeChallenge(verifier)

	if challenge == "" {
		t.Error("code challenge should not be empty")
	}
	if challenge == verifier {
		t.Error("code challenge should differ from verifier")
	}

	// Same verifier should produce same challenge (deterministic)
	challenge2 := codeChallenge(verifier)
	if challenge != challenge2 {
		t.Error("code challenge should be deterministic")
	}
}

func TestGenerateSessionID(t *testing.T) {
	id1, err := generateSessionID()
	if err != nil {
		t.Fatalf("generateSessionID failed: %v", err)
	}
	id2, err := generateSessionID()
	if err != nil {
		t.Fatalf("generateSessionID failed: %v", err)
	}

	if id1 == id2 {
		t.Error("two session IDs should not be equal")
	}
	// Hex-encoded 32 bytes = 64 chars
	if len(id1) != 64 {
		t.Errorf("expected session ID length 64, got %d", len(id1))
	}
}

func TestNewAuthHandlerNilWhenNotConfigured(t *testing.T) {
	// With no env vars set, NewAuthHandler should return nil
	handler := NewAuthHandler()
	if handler != nil {
		t.Error("expected nil AuthHandler when OIDC is not configured")
	}
}
