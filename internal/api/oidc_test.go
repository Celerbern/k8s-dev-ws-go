package api

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"github.com/coreos/go-oidc/v3/oidc/oidctest"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	devwsv1alpha1 "github.com/celerbern/k8s-dev-ws-go/api/v1alpha1"
	"github.com/celerbern/k8s-dev-ws-go/internal/auth"
	"github.com/celerbern/k8s-dev-ws-go/internal/config"
	"github.com/celerbern/k8s-dev-ws-go/internal/oidc"
)

const oidcTestClientID = "devws-test"

// fakeIdP wraps oidctest's discovery/JWKS server with a token endpoint
// that returns a test-controlled ID token, mimicking the provider side
// of the authorization-code flow. The test itself plays the browser.
type fakeIdP struct {
	discovery *oidctest.Server
	priv      *rsa.PrivateKey

	mu        sync.Mutex
	idToken   string
	lastForm  url.Values
	tokenReqs int
	usedCodes map[string]bool
	// requireClientSecretPost rejects HTTP Basic client credentials,
	// like a provider whose client is registered for
	// client_secret_post only.
	requireClientSecretPost bool
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return &fakeIdP{
		discovery: &oidctest.Server{
			PublicKeys: []oidctest.PublicKey{
				{PublicKey: priv.Public(), KeyID: "test-key", Algorithm: gooidc.RS256},
			},
		},
		priv:      priv,
		usedCodes: map[string]bool{},
	}
}

func (f *fakeIdP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/token" {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// Model Keycloak's token endpoint: the authorization code is
		// consumed *before* the request is validated, and the PKCE
		// verifier must obey RFC 7636's 43-128 character limit. Serving
		// both here means a retried token request can only ever see
		// "Code not valid", exactly as in production.
		f.mu.Lock()
		f.lastForm = r.PostForm
		f.tokenReqs++
		token := f.idToken
		code := r.PostForm.Get("code")
		reused := f.usedCodes[code]
		f.usedCodes[code] = true
		f.mu.Unlock()
		fail := func(desc string) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error":             "invalid_grant",
				"error_description": desc,
			})
		}
		if reused {
			fail("Code not valid")
			return
		}
		if f.requireClientSecretPost {
			if _, _, ok := r.BasicAuth(); ok || r.PostForm.Get("client_secret") == "" {
				fail("Invalid client credentials")
				return
			}
		}
		if v := r.PostForm.Get("code_verifier"); len(v) < 43 || len(v) > 128 {
			fail("Invalid code verifier")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"access_token": "test-access-token",
			"token_type":   "Bearer",
			"id_token":     token,
		})
		return
	}
	f.discovery.ServeHTTP(w, r)
}

// setIDToken signs and installs the ID token the /token endpoint returns.
func (f *fakeIdP) setIDToken(issuer, nonce, username string) {
	claims := map[string]interface{}{
		"iss":                issuer,
		"aud":                oidcTestClientID,
		"sub":                "subject-" + username,
		"exp":                strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10),
		"nonce":              nonce,
		"preferred_username": username,
		"email":              username + "@example.com",
	}
	raw, _ := json.Marshal(claims)
	f.mu.Lock()
	f.idToken = oidctest.SignIDToken(f.priv, "test-key", gooidc.RS256, string(raw))
	f.mu.Unlock()
}

func (f *fakeIdP) tokenForm() url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastForm
}

func (f *fakeIdP) tokenRequests() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tokenReqs
}

func testOIDCUser(name, role string) *devwsv1alpha1.User {
	u := &devwsv1alpha1.User{}
	u.Name = name
	u.Spec.Role = role
	u.Spec.AuthMethod = "oidc"
	return u
}

// newOIDCTestServer starts a fake IdP and an api server with OIDC
// enabled against it. Returns the api server and the fake IdP.
func newOIDCTestServer(t *testing.T, objects ...runtime.Object) (*httptest.Server, *fakeIdP) {
	t.Helper()
	return newOIDCTestServerWith(t, nil, objects...)
}

// newOIDCTestServerWith is newOIDCTestServer with a hook to adjust the
// fake IdP before the api server discovers it.
func newOIDCTestServerWith(t *testing.T, tweak func(*fakeIdP), objects ...runtime.Object) (*httptest.Server, *fakeIdP) {
	t.Helper()
	idp := newFakeIdP(t)
	if tweak != nil {
		tweak(idp)
	}
	idpSrv := httptest.NewServer(idp)
	t.Cleanup(idpSrv.Close)
	idp.discovery.SetIssuer(idpSrv.URL)

	c := fake.NewClientBuilder().
		WithScheme(testScheme).
		WithRuntimeObjects(objects...).
		WithStatusSubresource(&devwsv1alpha1.User{}, &devwsv1alpha1.Workspace{}).
		Build()
	sessions := auth.NewSessionManager([]byte("test-key-test-key-test-key-32!"))
	provider, err := oidc.NewProvider(context.Background(), &oidc.Config{
		IssuerURL:     idpSrv.URL,
		ClientID:      oidcTestClientID,
		ClientSecret:  "test-secret",
		RedirectURL:   "http://apiserver.test/api/auth/oidc/callback",
		UsernameClaim: "preferred_username",
		Scopes:        []string{"openid", "profile", "email"},
	}, sessions)
	if err != nil {
		t.Fatal(err)
	}
	s := New("127.0.0.1:0", c, testScheme, config.Default(), sessions, nil, provider)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, idp
}

// oidcClient is a browser-like client: cookie jar, no redirect follow.
func oidcClient(t *testing.T) *http.Client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	return &http.Client{
		Jar: jar,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// startOIDCLogin hits /api/auth/oidc/start and returns the authorize URL.
func startOIDCLogin(t *testing.T, client *http.Client, srv string) *url.URL {
	t.Helper()
	resp, err := client.Get(srv + "/api/auth/oidc/start")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("start must redirect, got %d", resp.StatusCode)
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

// finishOIDCLogin completes the flow as the browser/IdP would: it takes
// state and nonce from the authorize URL, arms the fake IdP with an ID
// token for username, and hits the callback.
func finishOIDCLogin(t *testing.T, client *http.Client, srv string, idp *fakeIdP, authURL *url.URL, username string) *http.Response {
	t.Helper()
	q := authURL.Query()
	state, nonce := q.Get("state"), q.Get("nonce")
	if state == "" || nonce == "" {
		t.Fatal("authorize URL must carry state and nonce")
	}
	if q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256" {
		t.Fatal("authorize URL must carry an S256 PKCE challenge")
	}
	idp.setIDToken(authURL.Scheme+"://"+authURL.Host, nonce, username)
	callback := srv + "/api/auth/oidc/callback?code=test-code&state=" + url.QueryEscape(state)
	resp, err := client.Get(callback)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func sessionCookieSet(resp *http.Response) bool {
	for _, c := range resp.Cookies() {
		if c.Name == auth.SessionCookieName && c.Value != "" {
			return true
		}
	}
	return false
}

func TestProvidersEndpoint(t *testing.T) {
	srv, _ := newOIDCTestServer(t)
	resp, err := srv.Client().Get(srv.URL + "/api/auth/providers")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var p struct {
		Local bool `json:"local"`
		OIDC  struct {
			Enabled bool `json:"enabled"`
		} `json:"oidc"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&p)
	if !p.Local || !p.OIDC.Enabled {
		t.Fatalf("providers must report local+oidc, got %+v", p)
	}

	// And with oidc disabled.
	c := fake.NewClientBuilder().WithScheme(testScheme).Build()
	s := New("127.0.0.1:0", c, testScheme, config.Default(),
		auth.NewSessionManager([]byte("test-key-test-key-test-key-32!")), nil, nil)
	srv2 := httptest.NewServer(s.Handler())
	t.Cleanup(srv2.Close)
	resp2, err := srv2.Client().Get(srv2.URL + "/api/auth/providers")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	_ = json.NewDecoder(resp2.Body).Decode(&p)
	if !p.Local || p.OIDC.Enabled {
		t.Fatalf("providers must report oidc disabled, got %+v", p)
	}
	resp3, _ := srv2.Client().Get(srv2.URL + "/api/auth/oidc/start")
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusNotFound {
		t.Fatalf("oidc start must 404 when disabled, got %d", resp3.StatusCode)
	}
}

func TestOIDCLoginSuccess(t *testing.T) {
	srv, idp := newOIDCTestServer(t, testOIDCUser("e2e-oidc", "user"))
	client := oidcClient(t)

	authURL := startOIDCLogin(t, client, srv.URL)
	// The IdP receives client id, scopes and the registered redirect.
	q := authURL.Query()
	if q.Get("client_id") != oidcTestClientID {
		t.Fatalf("authorize URL client_id: %q", q.Get("client_id"))
	}
	if q.Get("redirect_uri") != "http://apiserver.test/api/auth/oidc/callback" {
		t.Fatalf("authorize URL redirect_uri: %q", q.Get("redirect_uri"))
	}
	if !strings.Contains(q.Get("scope"), "openid") {
		t.Fatalf("authorize URL must request openid scope: %q", q.Get("scope"))
	}

	resp := finishOIDCLogin(t, client, srv.URL, idp, authURL, "e2e-oidc")
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("callback must redirect on success, got %d", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/" {
		t.Fatalf("callback must redirect to /, got %q", loc)
	}
	if !sessionCookieSet(resp) {
		t.Fatal("callback must set the session cookie")
	}
	// The code exchange carried the PKCE verifier.
	if v := idp.tokenForm().Get("code_verifier"); v == "" {
		t.Fatal("token exchange must include a code_verifier")
	} else if len(v) < 43 || len(v) > 128 {
		t.Fatalf("code_verifier must be 43-128 characters per RFC 7636, got %d: %q", len(v), v)
	}
	// The token request must not be retried: providers that consume the
	// authorization code before validating it (Keycloak) answer the
	// second attempt with a misleading "Code not valid".
	if n := idp.tokenRequests(); n != 1 {
		t.Fatalf("token endpoint must be called exactly once, got %d", n)
	}

	// The session authenticates like any other.
	me, err := client.Get(srv.URL + "/api/auth/me")
	if err != nil {
		t.Fatal(err)
	}
	defer me.Body.Close()
	var u userView
	_ = json.NewDecoder(me.Body).Decode(&u)
	if u.Username != "e2e-oidc" || u.AuthMethod != "oidc" {
		t.Fatalf("me must return the oidc user, got %+v", u)
	}
}

func TestOIDCLoginAdminUser(t *testing.T) {
	srv, idp := newOIDCTestServer(t, testOIDCUser("oidc-admin", "admin"))
	client := oidcClient(t)

	authURL := startOIDCLogin(t, client, srv.URL)
	resp := finishOIDCLogin(t, client, srv.URL, idp, authURL, "oidc-admin")
	if resp.StatusCode != http.StatusFound || !sessionCookieSet(resp) {
		t.Fatalf("oidc admin login failed: %d", resp.StatusCode)
	}
	// Admin endpoints work for oidc admins.
	list, err := client.Get(srv.URL + "/api/users")
	if err != nil {
		t.Fatal(err)
	}
	defer list.Body.Close()
	if list.StatusCode != http.StatusOK {
		t.Fatalf("oidc admin must reach admin endpoints, got %d", list.StatusCode)
	}
}

func TestOIDCLoginWithoutUserIsUnauthorized(t *testing.T) {
	srv, idp := newOIDCTestServer(t, testOIDCUser("someone-else", "user"))
	client := oidcClient(t)

	authURL := startOIDCLogin(t, client, srv.URL)
	resp := finishOIDCLogin(t, client, srv.URL, idp, authURL, "nouser")
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("callback must redirect, got %d", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); !strings.Contains(loc, "error=oidc_unauthorized") {
		t.Fatalf("missing user must be unauthorized, got %q", loc)
	}
	if sessionCookieSet(resp) {
		t.Fatal("no session cookie may be set for an unknown oidc user")
	}
}

func TestOIDCLoginLocalUserIsUnauthorized(t *testing.T) {
	srv, idp := newOIDCTestServer(t, testUser("alice", "password123", "user"))
	client := oidcClient(t)

	authURL := startOIDCLogin(t, client, srv.URL)
	resp := finishOIDCLogin(t, client, srv.URL, idp, authURL, "alice")
	if loc := resp.Header.Get("Location"); !strings.Contains(loc, "error=oidc_unauthorized") {
		t.Fatalf("local user via oidc must be unauthorized, got %q", loc)
	}
	if sessionCookieSet(resp) {
		t.Fatal("no session cookie may be set for a local user via oidc")
	}
}

func TestOIDCLoginDisabledUser(t *testing.T) {
	u := testOIDCUser("gone", "user")
	u.Spec.Disabled = true
	srv, idp := newOIDCTestServer(t, u)
	client := oidcClient(t)

	authURL := startOIDCLogin(t, client, srv.URL)
	resp := finishOIDCLogin(t, client, srv.URL, idp, authURL, "gone")
	if loc := resp.Header.Get("Location"); !strings.Contains(loc, "error=oidc_disabled") {
		t.Fatalf("disabled user must be rejected, got %q", loc)
	}
}

func TestOIDCCallbackRejectsBadState(t *testing.T) {
	srv, idp := newOIDCTestServer(t, testOIDCUser("e2e-oidc", "user"))
	client := oidcClient(t)

	startOIDCLogin(t, client, srv.URL)
	// A callback with a state that does not match the transaction cookie.
	idp.setIDToken("http://irrelevant", "nonce", "e2e-oidc")
	resp, err := client.Get(srv.URL + "/api/auth/oidc/callback?code=test-code&state=forged-state")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if loc := resp.Header.Get("Location"); !strings.Contains(loc, "error=oidc_error") {
		t.Fatalf("forged state must fail, got %q", loc)
	}
	if sessionCookieSet(resp) {
		t.Fatal("no session cookie may be set on a forged state")
	}

	// A callback without any transaction cookie.
	resp2, err := client.Get(srv.URL + "/api/auth/oidc/callback?code=test-code&state=anything")
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if loc := resp2.Header.Get("Location"); !strings.Contains(loc, "error=oidc_error") {
		t.Fatalf("missing tx cookie must fail, got %q", loc)
	}
}

func TestOIDCLoginRejectsInvalidUsernameClaim(t *testing.T) {
	srv, idp := newOIDCTestServer(t, testOIDCUser("e2e-oidc", "user"))
	client := oidcClient(t)

	authURL := startOIDCLogin(t, client, srv.URL)
	// An email-like claim cannot name a devws user.
	resp := finishOIDCLogin(t, client, srv.URL, idp, authURL, "not.a.valid@username")
	if loc := resp.Header.Get("Location"); !strings.Contains(loc, "error=oidc_unauthorized") {
		t.Fatalf("invalid username claim must be unauthorized, got %q", loc)
	}
}

func TestOIDCUserCannotUsePasswordLogin(t *testing.T) {
	srv, _ := newOIDCTestServer(t, testOIDCUser("e2e-oidc", "user"))
	resp := doJSON(t, srv.Client(), "POST", srv.URL+"/api/auth/login",
		map[string]string{"username": "e2e-oidc", "password": "whatever123"}, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("oidc user password login must be 401, got %d", resp.StatusCode)
	}
	resp2 := doJSON(t, srv.Client(), "POST", srv.URL+"/api/auth/token",
		map[string]string{"username": "e2e-oidc", "password": "whatever123"}, nil)
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("oidc user token issue must be 401, got %d", resp2.StatusCode)
	}
}

func TestAdminCreatesOIDCUser(t *testing.T) {
	srv, _ := newOIDCTestServer(t,
		testUser("bob", "password123", "admin"),
		testUser("alice", "password123", "user"))
	admin := loginClient(t, srv, "bob", "password123")

	var created userView
	resp := doJSON(t, admin, "POST", srv.URL+"/api/users", map[string]interface{}{
		"username":   "carol",
		"authMethod": "oidc",
		"email":      "carol@example.com",
	}, &created)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create oidc user: %d", resp.StatusCode)
	}
	if created.AuthMethod != "oidc" {
		t.Fatalf("created user must be oidc, got %q", created.AuthMethod)
	}

	// Password must be rejected for oidc users.
	resp = doJSON(t, admin, "POST", srv.URL+"/api/users", map[string]interface{}{
		"username": "dave", "authMethod": "oidc", "password": "password123",
	}, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("password for oidc user must 400, got %d", resp.StatusCode)
	}
	// Local users still need a password.
	resp = doJSON(t, admin, "POST", srv.URL+"/api/users", map[string]interface{}{
		"username": "erin", "authMethod": "local",
	}, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("local user without password must 400, got %d", resp.StatusCode)
	}

	// Setting a password on an oidc user is rejected.
	resp = doJSON(t, admin, "POST", srv.URL+"/api/users/carol/password",
		map[string]string{"newPassword": "password123"}, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("set password for oidc user must 400, got %d", resp.StatusCode)
	}
	// Non-admins cannot use the admin token issue path for oidc users.
	// (covered by auth handler tests; here verify self-service for the
	// oidc user's own password also fails the same way.)
	resp = doJSON(t, admin, "PATCH", srv.URL+"/api/users/carol", map[string]interface{}{
		"password": "password123",
	}, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("patch password onto oidc user must 400, got %d", resp.StatusCode)
	}
}

func TestAdminSwitchesAuthMethod(t *testing.T) {
	srv, _ := newOIDCTestServer(t,
		testUser("bob", "password123", "admin"),
		testUser("alice", "password123", "user"))
	admin := loginClient(t, srv, "bob", "password123")

	// local -> oidc drops the password.
	resp := doJSON(t, admin, "PATCH", srv.URL+"/api/users/alice", map[string]interface{}{
		"authMethod": "oidc",
	}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("switch to oidc: %d", resp.StatusCode)
	}
	resp = doJSON(t, srv.Client(), "POST", srv.URL+"/api/auth/login",
		map[string]string{"username": "alice", "password": "password123"}, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old password must stop working after switch to oidc, got %d", resp.StatusCode)
	}

	// oidc -> local requires a new password.
	resp = doJSON(t, admin, "PATCH", srv.URL+"/api/users/alice", map[string]interface{}{
		"authMethod": "local",
	}, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("switch to local without password must 400, got %d", resp.StatusCode)
	}
	resp = doJSON(t, admin, "PATCH", srv.URL+"/api/users/alice", map[string]interface{}{
		"authMethod": "local", "password": "newpassword123",
	}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("switch to local with password: %d", resp.StatusCode)
	}
	resp = doJSON(t, srv.Client(), "POST", srv.URL+"/api/auth/login",
		map[string]string{"username": "alice", "password": "newpassword123"}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("new password must work after switch to local, got %d", resp.StatusCode)
	}

	// Admins cannot flip their own auth method.
	resp = doJSON(t, admin, "PATCH", srv.URL+"/api/users/bob", map[string]interface{}{
		"authMethod": "oidc",
	}, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("own auth method change must 400, got %d", resp.StatusCode)
	}
}

func TestOIDCTxCookieIsHttpOnly(t *testing.T) {
	srv, _ := newOIDCTestServer(t)
	client := oidcClient(t)
	resp, err := client.Get(srv.URL + "/api/auth/oidc/start")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	found := false
	for _, c := range resp.Cookies() {
		if c.Name == "devws_oidc_tx" {
			found = true
			if !c.HttpOnly {
				t.Fatal("tx cookie must be HttpOnly")
			}
			if c.Path != "/api/auth/oidc/callback" {
				t.Fatalf("tx cookie must be scoped to the callback path, got %q", c.Path)
			}
		}
	}
	if !found {
		t.Fatal("start must set the tx cookie")
	}
}

func TestOidcLogPrefix(t *testing.T) {
	cases := []struct{ in, want string }{
		// Keycloak codes are "<uuid>.<userSessionId>.<clientUUID>" —
		// only the leading segment is logged.
		{"1a2b3c4d-5678-90ab-cdef-1234567890ab.f00dcafe.c0ffee", "1a2b3c4d"},
		{"short.abc.def", "short"},
		{"no-dots-here", "no-dots-"}, // capped at 8 chars
		{"a.b.c", "a"},
		{"", ""},
		{".leading.dot", ".leading"}, // segment before the dot is empty: keep as-is, capped
	}
	for _, c := range cases {
		if got := oidcLogPrefix(c.in); got != c.want {
			t.Errorf("oidcLogPrefix(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestOIDCTokenRequestIsNotRetried covers the failure mode seen against
// Keycloak: x/oauth2 auto-detects the client auth style by sending the
// token request twice. Keycloak consumes the authorization code before
// validating the request, so the retry reports a misleading
// `invalid_grant "Code not valid"` and hides the real error.
func TestOIDCTokenRequestIsNotRetried(t *testing.T) {
	srv, idp := newOIDCTestServerWith(t, func(f *fakeIdP) {
		f.requireClientSecretPost = true
	}, testOIDCUser("e2e-oidc", "user"))
	client := oidcClient(t)

	authURL := startOIDCLogin(t, client, srv.URL)
	resp := finishOIDCLogin(t, client, srv.URL, idp, authURL, "e2e-oidc")
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("callback must redirect, got %d", resp.StatusCode)
	}
	// The default style is basic, which this IdP rejects. One attempt
	// only: the operator must pick clientAuthStyle=post instead of
	// relying on a retry that burns the code.
	if n := idp.tokenRequests(); n != 1 {
		t.Fatalf("token endpoint must be called exactly once, got %d", n)
	}
	if loc := resp.Header.Get("Location"); !strings.Contains(loc, "error=oidc_error") {
		t.Fatalf("wrong client auth style must fail the login, got %q", loc)
	}
}

// TestOIDCClientAuthStylePost covers providers registered for
// client_secret_post.
func TestOIDCClientAuthStylePost(t *testing.T) {
	idp := newFakeIdP(t)
	idp.requireClientSecretPost = true
	idpSrv := httptest.NewServer(idp)
	t.Cleanup(idpSrv.Close)
	idp.discovery.SetIssuer(idpSrv.URL)

	c := fake.NewClientBuilder().
		WithScheme(testScheme).
		WithRuntimeObjects(testOIDCUser("e2e-oidc", "user")).
		WithStatusSubresource(&devwsv1alpha1.User{}, &devwsv1alpha1.Workspace{}).
		Build()
	sessions := auth.NewSessionManager([]byte("test-key-test-key-test-key-32!"))
	provider, err := oidc.NewProvider(context.Background(), &oidc.Config{
		IssuerURL:       idpSrv.URL,
		ClientID:        oidcTestClientID,
		ClientSecret:    "test-secret",
		RedirectURL:     "http://apiserver.test/api/auth/oidc/callback",
		UsernameClaim:   "preferred_username",
		Scopes:          []string{"openid", "profile", "email"},
		ClientAuthStyle: "post",
	}, sessions)
	if err != nil {
		t.Fatal(err)
	}
	s := New("127.0.0.1:0", c, testScheme, config.Default(), sessions, nil, provider)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	client := oidcClient(t)
	authURL := startOIDCLogin(t, client, srv.URL)
	resp := finishOIDCLogin(t, client, srv.URL, idp, authURL, "e2e-oidc")
	if !sessionCookieSet(resp) {
		t.Fatalf("clientAuthStyle=post must log in, got %q", resp.Header.Get("Location"))
	}
	if n := idp.tokenRequests(); n != 1 {
		t.Fatalf("token endpoint must be called exactly once, got %d", n)
	}
}
