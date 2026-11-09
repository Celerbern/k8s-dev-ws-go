package auth

import (
	"testing"
	"time"
)

func TestSessionSignVerify(t *testing.T) {
	m := NewSessionManager([]byte("0123456789abcdef0123456789abcdef"))
	token := m.Sign("alice")

	user, err := m.Verify(token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if user != "alice" {
		t.Fatalf("got user %q, want alice", user)
	}
}

func TestSessionVerifyRejectsTampering(t *testing.T) {
	m1 := NewSessionManager([]byte("0123456789abcdef0123456789abcdef"))
	m2 := NewSessionManager([]byte("fedcba9876543210fedcba9876543210"))
	token := m1.Sign("alice")

	if _, err := m2.Verify(token); err == nil {
		t.Fatal("token signed with a different key must not verify")
	}
	if _, err := m1.Verify(token + "x"); err == nil {
		t.Fatal("tampered token must not verify")
	}
	if _, err := m1.Verify("v1.alice.99999999999.deadbeef"); err == nil {
		t.Fatal("forged signature must not verify")
	}
}

func TestSessionVerifyRejectsExpired(t *testing.T) {
	m := NewSessionManager([]byte("0123456789abcdef0123456789abcdef"))
	token := m.SignWithTTL("alice", -time.Minute)
	if _, err := m.Verify(token); err == nil {
		t.Fatal("expired token must not verify")
	}
}

func TestPasswordHash(t *testing.T) {
	hash, err := HashPassword("s3cret-password")
	if err != nil {
		t.Fatal(err)
	}
	if !CheckPassword(hash, "s3cret-password") {
		t.Fatal("correct password must check out")
	}
	if CheckPassword(hash, "wrong") {
		t.Fatal("wrong password must not check out")
	}
}

func TestTokenRoundTrip(t *testing.T) {
	token, prefix, hash, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if !TokenLooksValid(token) {
		t.Fatal("issued token must look valid")
	}
	if len(prefix) >= len(token) {
		t.Fatal("prefix must be shorter than the token")
	}
	if !CheckToken(hash, token) {
		t.Fatal("token must match its hash")
	}
	if CheckToken(hash, token+"x") {
		t.Fatal("modified token must not match")
	}
}
