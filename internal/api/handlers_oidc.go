package api

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	devwsv1alpha1 "github.com/celerbern/k8s-dev-ws-go/api/v1alpha1"
	"github.com/celerbern/k8s-dev-ws-go/internal/oidc"
)

// Login page error codes surfaced by the OIDC callback via the
// /login?error=... query parameter. Raw provider errors are logged but
// never shown.
const (
	oidcErrUnauthorized = "oidc_unauthorized"
	oidcErrDisabled     = "oidc_disabled"
	oidcErrFailed       = "oidc_error"
)

// handleAuthProviders reports which login methods the platform offers so
// the login page can render the SSO button before signing in.
func (s *Server) handleAuthProviders(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"local": true,
		"oidc":  map[string]bool{"enabled": s.oidc != nil},
	})
}

// handleOIDCStart redirects the browser to the identity provider's
// authorization endpoint, carrying state, nonce and a PKCE challenge
// bound to a short-lived signed transaction cookie.
func (s *Server) handleOIDCStart(w http.ResponseWriter, r *http.Request) {
	if s.oidc == nil {
		writeErr(w, http.StatusNotFound, "oidc is not enabled")
		return
	}
	tx, value, err := s.oidc.NewTx()
	if err != nil {
		slog.Error("oidc: starting login", "error", err)
		writeErr(w, http.StatusInternalServerError, "failed to start login")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     oidc.TxCookieName,
		Value:    value,
		Path:     "/api/auth/oidc/callback",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   600,
	})
	http.Redirect(w, r, s.oidc.AuthCodeURL(tx), http.StatusFound)
}

// handleOIDCCallback completes the browser login: it checks the state,
// exchanges the code (PKCE), verifies the ID token (signature, issuer,
// audience, expiry, nonce) and maps the username claim to a pre-created
// devws user. Users are never auto-provisioned: a matching user with
// authMethod "oidc" must already exist, otherwise the login fails.
func (s *Server) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	if s.oidc == nil {
		writeErr(w, http.StatusNotFound, "oidc is not enabled")
		return
	}
	fail := func(code string) {
		http.SetCookie(w, clearTxCookie(r.TLS != nil))
		http.Redirect(w, r, "/login?error="+code, http.StatusFound)
	}

	q := r.URL.Query()
	if q.Get("error") != "" {
		slog.Warn("oidc: provider returned an error", "error", q.Get("error"), "description", q.Get("error_description"))
		fail(oidcErrFailed)
		return
	}
	code, state := q.Get("code"), q.Get("state")
	if code == "" || state == "" {
		fail(oidcErrFailed)
		return
	}
	cookie, err := r.Cookie(oidc.TxCookieName)
	if err != nil || cookie.Value == "" {
		fail(oidcErrFailed)
		return
	}
	tx, err := s.oidc.ParseTx(cookie.Value)
	if err != nil || subtle.ConstantTimeCompare([]byte(tx.State), []byte(state)) != 1 {
		fail(oidcErrFailed)
		return
	}

	username, err := s.oidc.VerifyCallback(r.Context(), tx, code)
	if err != nil {
		slog.Error("oidc: verifying callback", "error", err)
		fail(oidcErrFailed)
		return
	}
	username = strings.ToLower(strings.TrimSpace(username))
	if !usernameRE.MatchString(username) {
		// The claim value cannot name a devws user (e.g. an email-like
		// value while usernames are DNS labels).
		slog.Warn("oidc: username claim is not a valid devws username", "claim", s.oidc.UsernameClaim())
		fail(oidcErrUnauthorized)
		return
	}

	var user devwsv1alpha1.User
	if err := s.client.Get(r.Context(), client.ObjectKey{Name: username}, &user); err != nil {
		if apierrors.IsNotFound(err) {
			slog.Warn("oidc: login for user without a devws account", "username", username)
		} else {
			slog.Error("oidc: looking up user", "username", username, "error", err)
		}
		fail(oidcErrUnauthorized)
		return
	}
	if user.Spec.AuthMethod != "oidc" {
		slog.Warn("oidc: login for user with local auth method", "username", username)
		fail(oidcErrUnauthorized)
		return
	}
	if user.Spec.Disabled {
		fail(oidcErrDisabled)
		return
	}

	http.SetCookie(w, sessionCookie(s.sessions.Sign(user.Name), r.TLS != nil))
	http.SetCookie(w, clearTxCookie(r.TLS != nil))
	http.Redirect(w, r, "/", http.StatusFound)
}

func clearTxCookie(secure bool) *http.Cookie {
	return &http.Cookie{
		Name:     oidc.TxCookieName,
		Value:    "",
		Path:     "/api/auth/oidc/callback",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	}
}