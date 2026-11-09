package auth

import (
	"crypto/rand"
	"encoding/base64"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// TokenPrefix is the prefix of every issued API token.
const TokenPrefix = "devws_"

// NewToken generates a new API token. It returns the full token (shown to
// the user exactly once), the lookup prefix (safe to store) and the bcrypt
// hash of the full token.
func NewToken() (token, prefix, hash string, err error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", "", "", err
	}
	token = TokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
	prefix = token[:len(TokenPrefix)+8]
	h, err := bcrypt.GenerateFromPassword([]byte(token), bcrypt.DefaultCost)
	if err != nil {
		return "", "", "", err
	}
	return token, prefix, string(h), nil
}

// HashToken returns the bcrypt hash of a token.
func HashToken(token string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(token), bcrypt.DefaultCost)
	return string(h), err
}

// CheckToken reports whether token matches the stored bcrypt hash.
func CheckToken(hash, token string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(token)) == nil
}

// TokenLooksValid performs a cheap format check before any hashing work.
func TokenLooksValid(token string) bool {
	return strings.HasPrefix(token, TokenPrefix) && len(token) > len(TokenPrefix)+8
}
