package oidc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/celerbern/k8s-dev-ws-go/internal/auth"
)

// TxCookieName is the browser cookie carrying an in-flight OIDC login.
const TxCookieName = "devws_oidc_tx"

// txPurpose namespaces the transaction cookie payload signature.
const txPurpose = "oidc-tx"

// txTTL bounds how long a started login may take to complete.
const txTTL = 10 * time.Minute

// Provider is the platform's OIDC client. A nil Provider means OIDC is
// disabled. It is safe for concurrent use.
type Provider struct {
	cfg      *Config
	oauth2   *oauth2.Config
	verifier *gooidc.IDTokenVerifier
	sessions *auth.SessionManager
}

// NewProvider performs discovery against the configured issuer and
// returns a ready Provider. sessions is used to sign the short-lived
// login transaction cookie (state + nonce + PKCE verifier), so any
// replica can complete a browser login started by another replica.
func NewProvider(ctx context.Context, cfg *Config, sessions *auth.SessionManager) (*Provider, error) {
	op, err := gooidc.NewProvider(ctx, cfg.IssuerURL)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery for %q: %w", cfg.IssuerURL, err)
	}
	return &Provider{
		cfg: cfg,
		oauth2: &oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			RedirectURL:  cfg.RedirectURL,
			Endpoint:     op.Endpoint(),
			Scopes:       cfg.Scopes,
		},
		verifier: op.Verifier(&gooidc.Config{ClientID: cfg.ClientID}),
		sessions: sessions,
	}, nil
}

// IssuerURL returns the configured issuer (for logs and status).
func (p *Provider) IssuerURL() string { return p.cfg.IssuerURL }

// LoadAndNew loads the OIDC configuration (devws-oidc Secret) and builds
// a Provider. A missing Secret (OIDC disabled) returns (nil, nil).
func LoadAndNew(ctx context.Context, c client.Reader, namespace string, sessions *auth.SessionManager) (*Provider, error) {
	cfg, err := Load(ctx, c, namespace)
	if err != nil || cfg == nil {
		return nil, err
	}
	return NewProvider(ctx, cfg, sessions)
}

// UsernameClaim returns the claim matched against the devws username.
func (p *Provider) UsernameClaim() string { return p.cfg.UsernameClaim }

// Tx is one in-flight browser login attempt.
type Tx struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
}

// NewTx starts a login attempt and returns the transaction plus the
// signed cookie value to hand to the browser.
func (p *Provider) NewTx() (Tx, string, error) {
	tx := Tx{}
	for _, dst := range []*string{&tx.State, &tx.Nonce, &tx.Verifier} {
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			return Tx{}, "", fmt.Errorf("generating oidc state: %w", err)
		}
		*dst = hex.EncodeToString(b)
	}
	raw, err := json.Marshal(tx)
	if err != nil {
		return Tx{}, "", err
	}
	return tx, p.sessions.SignPayload(txPurpose, raw, txTTL), nil
}

// ParseTx verifies and decodes a transaction cookie value.
func (p *Provider) ParseTx(value string) (Tx, error) {
	var tx Tx
	raw, err := p.sessions.VerifyPayload(txPurpose, value)
	if err != nil {
		return tx, err
	}
	if err := json.Unmarshal(raw, &tx); err != nil {
		return tx, fmt.Errorf("decoding oidc tx: %w", err)
	}
	if tx.State == "" || tx.Nonce == "" || tx.Verifier == "" {
		return tx, errors.New("incomplete oidc tx")
	}
	return tx, nil
}

// AuthCodeURL builds the provider authorization URL for a transaction
// (PKCE S256 challenge + nonce included).
func (p *Provider) AuthCodeURL(tx Tx) string {
	return p.oauth2.AuthCodeURL(
		tx.State,
		gooidc.Nonce(tx.Nonce),
		oauth2.S256ChallengeOption(tx.Verifier),
	)
}

// VerifyCallback exchanges the authorization code (with the PKCE
// verifier from the transaction), verifies the returned ID token
// (signature, issuer, audience, expiry) and the nonce, and returns the
// raw value of the configured username claim.
func (p *Provider) VerifyCallback(ctx context.Context, tx Tx, code string) (string, error) {
	token, err := p.oauth2.Exchange(ctx, code, oauth2.VerifierOption(tx.Verifier))
	if err != nil {
		return "", fmt.Errorf("code exchange: %w", err)
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok {
		return "", errors.New("token response has no id_token")
	}
	idToken, err := p.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return "", fmt.Errorf("verifying id_token: %w", err)
	}
	// The verifier does not check the nonce; it must match the value
	// bound to this transaction cookie.
	if idToken.Nonce != tx.Nonce {
		return "", errors.New("id_token nonce mismatch")
	}
	var claims map[string]interface{}
	if err := idToken.Claims(&claims); err != nil {
		return "", fmt.Errorf("decoding id_token claims: %w", err)
	}
	v, ok := claims[p.cfg.UsernameClaim].(string)
	if !ok || v == "" {
		return "", fmt.Errorf("id_token has no usable %q claim", p.cfg.UsernameClaim)
	}
	return v, nil
}
