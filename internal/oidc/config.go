// Package oidc implements the devws OpenID Connect client: configuration
// loading (from the devws-oidc Secret), provider discovery and the
// browser authorization-code flow with PKCE, state and nonce protection.
//
// There is deliberately no server-side flow state: everything a callback
// needs (state, nonce, PKCE verifier) is carried in a short-lived
// HMAC-signed cookie, so any apiserver replica can complete a flow
// started by another replica.
package oidc

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// SecretName is the Secret holding the OIDC client configuration.
const SecretName = "devws-oidc"

// DefaultUsernameClaim is the ID token claim matched against the devws
// username (User.metadata.name).
const DefaultUsernameClaim = "preferred_username"

// Config is the resolved OIDC client configuration.
type Config struct {
	// IssuerURL is the identity provider issuer identifier, e.g.
	// "https://dex.example.com/dex". Discovery happens at startup.
	IssuerURL string
	// ClientID and ClientSecret identify devws at the provider.
	ClientID     string
	ClientSecret string
	// RedirectURL is the absolute callback URL registered at the
	// provider, e.g. "https://devws.example.com/api/auth/oidc/callback".
	RedirectURL string
	// UsernameClaim is the ID token claim whose value must equal the
	// devws username of the account being logged in (lowercased before
	// comparison). Defaults to "preferred_username".
	UsernameClaim string
	// Scopes requested from the provider. "openid" is always included.
	Scopes []string
}

// Load reads the OIDC configuration from the devws-oidc Secret in the
// given namespace. A missing Secret means OIDC is disabled (nil config,
// nil error); an incomplete or invalid Secret is an error so
// misconfiguration fails loudly at startup.
func Load(ctx context.Context, c client.Reader, namespace string) (*Config, error) {
	sec := &corev1.Secret{}
	key := client.ObjectKey{Namespace: namespace, Name: SecretName}
	if err := c.Get(ctx, key, sec); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading secret %s/%s: %w", namespace, SecretName, err)
	}
	get := func(field string) string {
		return strings.TrimSpace(string(sec.Data[field]))
	}

	cfg := &Config{
		IssuerURL:     get("issuerUrl"),
		ClientID:      get("clientId"),
		ClientSecret:  get("clientSecret"),
		RedirectURL:   get("redirectUrl"),
		UsernameClaim: get("usernameClaim"),
	}
	if cfg.UsernameClaim == "" {
		cfg.UsernameClaim = DefaultUsernameClaim
	}
	if raw := get("scopes"); raw != "" {
		for _, s := range strings.Split(raw, ",") {
			if s = strings.TrimSpace(s); s != "" {
				cfg.Scopes = append(cfg.Scopes, s)
			}
		}
	}
	if len(cfg.Scopes) == 0 {
		cfg.Scopes = []string{"openid", "profile", "email"}
	}
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid oidc secret %s/%s: %w", namespace, SecretName, err)
	}
	return cfg, nil
}

func (c *Config) validate() error {
	switch {
	case c.IssuerURL == "":
		return errors.New("issuerUrl is required")
	case c.ClientID == "":
		return errors.New("clientId is required")
	case c.ClientSecret == "":
		return errors.New("clientSecret is required")
	case c.RedirectURL == "":
		return errors.New("redirectUrl is required")
	}
	u, err := url.Parse(c.IssuerURL)
	if err != nil || !u.IsAbs() || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("issuerUrl must be an absolute http(s) URL, got %q", c.IssuerURL)
	}
	if u, err := url.Parse(c.RedirectURL); err != nil || !u.IsAbs() || u.Host == "" {
		return fmt.Errorf("redirectUrl must be an absolute http(s) URL, got %q", c.RedirectURL)
	}
	hasOpenID := false
	for _, s := range c.Scopes {
		if s == "openid" {
			hasOpenID = true
			break
		}
	}
	if !hasOpenID {
		c.Scopes = append([]string{"openid"}, c.Scopes...)
	}
	return nil
}
