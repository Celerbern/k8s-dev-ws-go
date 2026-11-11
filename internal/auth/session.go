package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// SessionCookieName is the browser session cookie.
const SessionCookieName = "devws_session"

// SessionKeySecretName is the Secret holding the session signing key.
const SessionKeySecretName = "devws-session-key"

// DefaultSessionTTL is how long browser sessions last.
const DefaultSessionTTL = 7 * 24 * time.Hour

// ErrInvalidSession is returned for forged or malformed session cookies.
var ErrInvalidSession = errors.New("invalid session")

// SessionManager issues and verifies HMAC-signed session tokens. Tokens are
// of the form "v1.<username>.<expiry-unix>.<hex-signature>".
type SessionManager struct {
	key []byte
	ttl time.Duration
}

// NewSessionManager returns a SessionManager signing with the given 32-byte
// key.
func NewSessionManager(key []byte) *SessionManager {
	return &SessionManager{key: key, ttl: DefaultSessionTTL}
}

// NewSessionManagerFromSecret loads (or creates) the session key Secret in
// the given namespace using c.
func NewSessionManagerFromSecret(ctx context.Context, c client.Client, namespace string) (*SessionManager, error) {
	key, err := loadOrCreateSecretKey(ctx, c, namespace, SessionKeySecretName, "session-key")
	if err != nil {
		return nil, err
	}
	return NewSessionManager(key), nil
}

// Sign issues a session token for username valid for the default TTL.
func (m *SessionManager) Sign(username string) string {
	return m.SignWithTTL(username, m.ttl)
}

// SignWithTTL issues a session token with a custom validity.
func (m *SessionManager) SignWithTTL(username string, ttl time.Duration) string {
	exp := time.Now().Add(ttl).Unix()
	payload := fmt.Sprintf("v1.%s.%d", username, exp)
	mac := hmac.New(sha256.New, m.key)
	mac.Write([]byte(payload))
	sig := hex.EncodeToString(mac.Sum(nil))
	return payload + "." + sig
}

// Verify checks the signature and expiry of a session token and returns the
// username it was issued to.
func (m *SessionManager) Verify(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 4 || parts[0] != "v1" {
		return "", ErrInvalidSession
	}
	username, expStr, sig := parts[1], parts[2], parts[3]
	payload := fmt.Sprintf("v1.%s.%s", username, expStr)
	mac := hmac.New(sha256.New, m.key)
	mac.Write([]byte(payload))
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(sig), []byte(expected)) {
		return "", ErrInvalidSession
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return "", ErrInvalidSession
	}
	if username == "" || strings.ContainsAny(username, ".") {
		return "", ErrInvalidSession
	}
	return username, nil
}

// SignPayload returns an HMAC-signed token carrying arbitrary data for a
// given purpose, valid for ttl. Tokens for different purposes cannot be
// confused with each other or with session tokens (different format and
// purpose field). Used for short-lived browser flow state such as the
// OIDC login transaction cookie.
func (m *SessionManager) SignPayload(purpose string, data []byte, ttl time.Duration) string {
	if strings.Contains(purpose, ".") {
		panic("auth: payload purpose must not contain '.'")
	}
	exp := time.Now().Add(ttl).Unix()
	payload := fmt.Sprintf("p1.%s.%d.%s", purpose, exp, base64.RawURLEncoding.EncodeToString(data))
	mac := hmac.New(sha256.New, m.key)
	mac.Write([]byte(payload))
	sig := hex.EncodeToString(mac.Sum(nil))
	return payload + "." + sig
}

// VerifyPayload checks the signature, purpose and expiry of a SignPayload
// token and returns the original data.
func (m *SessionManager) VerifyPayload(purpose, token string) ([]byte, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 5 || parts[0] != "p1" || parts[1] != purpose {
		return nil, ErrInvalidSession
	}
	expStr, b64, sig := parts[2], parts[3], parts[4]
	payload := fmt.Sprintf("p1.%s.%s.%s", parts[1], expStr, b64)
	mac := hmac.New(sha256.New, m.key)
	mac.Write([]byte(payload))
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(sig), []byte(expected)) {
		return nil, ErrInvalidSession
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return nil, ErrInvalidSession
	}
	data, err := base64.RawURLEncoding.DecodeString(b64)
	if err != nil {
		return nil, ErrInvalidSession
	}
	return data, nil
}

// loadOrCreateSecretKey loads a 32-byte key from a Secret, creating the
// Secret with a fresh random key if it does not exist.
func loadOrCreateSecretKey(ctx context.Context, c client.Client, namespace, name, dataKey string) ([]byte, error) {
	sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}}
	err := c.Get(ctx, client.ObjectKeyFromObject(sec), sec)
	if err == nil {
		if key, ok := sec.Data[dataKey]; ok && len(key) == 32 {
			return key, nil
		}
		return nil, fmt.Errorf("secret %s/%s exists but key %q is missing or not 32 bytes", namespace, name, dataKey)
	}
	if !apierrors.IsNotFound(err) {
		return nil, err
	}

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	sec.Data = map[string][]byte{dataKey: key}
	if err := c.Create(ctx, sec); err != nil {
		// Lost a create race; re-read.
		if !apierrors.IsAlreadyExists(err) {
			return nil, err
		}
		if err2 := c.Get(ctx, client.ObjectKeyFromObject(sec), sec); err2 == nil {
			if key, ok := sec.Data[dataKey]; ok && len(key) == 32 {
				return key, nil
			}
		}
		return nil, err
	}
	return key, nil
}
