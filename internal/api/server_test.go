package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	devwsv1alpha1 "github.com/celerbern/k8s-dev-ws-go/api/v1alpha1"
	"github.com/celerbern/k8s-dev-ws-go/internal/auth"
	"github.com/celerbern/k8s-dev-ws-go/internal/config"
	devwsmetrics "github.com/celerbern/k8s-dev-ws-go/internal/metrics"
)

var testScheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(testScheme))
	utilruntime.Must(devwsv1alpha1.AddToScheme(testScheme))
}

func testUser(name, password, role string) *devwsv1alpha1.User {
	hash, _ := auth.HashPassword(password)
	u := &devwsv1alpha1.User{}
	u.Name = name
	u.Spec.Role = role
	u.Spec.PasswordHash = hash
	return u
}

func newTestServer(t *testing.T, objects ...runtime.Object) *httptest.Server {
	t.Helper()
	c := fake.NewClientBuilder().
		WithScheme(testScheme).
		WithRuntimeObjects(objects...).
		WithStatusSubresource(&devwsv1alpha1.User{}, &devwsv1alpha1.Workspace{}).
		Build()
	s := New("127.0.0.1:0", c, testScheme, config.Default(), auth.NewSessionManager([]byte("test-key-test-key-test-key-32!")), nil, nil)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv
}

func doJSON(t *testing.T, client *http.Client, method, url string, body any, out any) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequest(method, url, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp
}

func TestLoginSetsSession(t *testing.T) {
	srv := newTestServer(t, testUser("alice", "password123", "user"))
	client := &http.Client{}
	resp := doJSON(t, client, "POST", srv.URL+"/api/auth/login",
		map[string]string{"username": "alice", "password": "password123"}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login status %d", resp.StatusCode)
	}
	found := false
	for _, c := range resp.Cookies() {
		if c.Name == auth.SessionCookieName && c.Value != "" {
			found = true
		}
	}
	if !found {
		t.Fatal("login must set a session cookie")
	}
}

func TestLoginRejectsBadPassword(t *testing.T) {
	srv := newTestServer(t, testUser("alice", "password123", "user"))
	resp := doJSON(t, srv.Client(), "POST", srv.URL+"/api/auth/login",
		map[string]string{"username": "alice", "password": "wrong"}, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad password must be 401, got %d", resp.StatusCode)
	}
}

func loginClient(t *testing.T, srv *httptest.Server, username, password string) *http.Client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	resp := doJSON(t, client, "POST", srv.URL+"/api/auth/login",
		map[string]string{"username": username, "password": password}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login failed: %d", resp.StatusCode)
	}
	return client
}

func TestUserCannotListUsers(t *testing.T) {
	srv := newTestServer(t,
		testUser("alice", "password123", "user"),
		testUser("bob", "password123", "admin"))
	client := loginClient(t, srv, "alice", "password123")
	resp := doJSON(t, client, "GET", srv.URL+"/api/users", nil, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-admin listing users must be 403, got %d", resp.StatusCode)
	}
}

func TestAdminCanListUsers(t *testing.T) {
	srv := newTestServer(t,
		testUser("alice", "password123", "user"),
		testUser("bob", "password123", "admin"))
	client := loginClient(t, srv, "bob", "password123")
	var users []*userView
	resp := doJSON(t, client, "GET", srv.URL+"/api/users", nil, &users)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin listing users: %d", resp.StatusCode)
	}
	if len(users) != 2 {
		t.Fatalf("expected 2 users, got %d", len(users))
	}
	for _, u := range users {
		if u.Quotas.MaxWorkspaces == 0 {
			t.Fatal("default quotas must be applied in the user view")
		}
	}
}

func TestCreateWorkspaceEnforcesQuota(t *testing.T) {
	user := testUser("alice", "password123", "user")
	user.Spec.Quotas.MaxWorkspaces = 1
	existing := &devwsv1alpha1.Workspace{}
	existing.Name = "one"
	existing.Namespace = "devws-alice"
	existing.Spec.UserRef = "alice"
	srv := newTestServer(t, user, existing)

	client := loginClient(t, srv, "alice", "password123")
	resp := doJSON(t, client, "POST", srv.URL+"/api/workspaces",
		map[string]string{"name": "two"}, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("quota must be enforced with 409, got %d", resp.StatusCode)
	}
}

func TestCreateAndStopStartWorkspace(t *testing.T) {
	user := testUser("alice", "password123", "user")
	srv := newTestServer(t, user)
	client := loginClient(t, srv, "alice", "password123")

	var ws workspaceView
	resp := doJSON(t, client, "POST", srv.URL+"/api/workspaces",
		map[string]string{"name": "main", "class": "small"}, &ws)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create workspace: %d", resp.StatusCode)
	}
	if ws.Namespace != "devws-alice" || ws.User != "alice" {
		t.Fatalf("workspace created in wrong place: %+v", ws)
	}

	resp = doJSON(t, client, "POST", srv.URL+"/api/workspaces/main/stop", nil, &ws)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stop: %d", resp.StatusCode)
	}
	if !ws.Paused {
		t.Fatal("workspace must be paused after stop")
	}

	resp = doJSON(t, client, "POST", srv.URL+"/api/workspaces/main/start", nil, &ws)
	if resp.StatusCode != http.StatusOK || ws.Paused {
		t.Fatal("workspace must be unpaused after start")
	}
}

func TestTokenIssueAndUse(t *testing.T) {
	srv := newTestServer(t, testUser("alice", "password123", "user"))
	var tokResp struct {
		Token string `json:"token"`
	}
	resp := doJSON(t, srv.Client(), "POST", srv.URL+"/api/auth/token",
		map[string]string{"username": "alice", "password": "password123", "name": "test"}, &tokResp)
	if resp.StatusCode != http.StatusOK || tokResp.Token == "" {
		t.Fatalf("token issue failed: %d", resp.StatusCode)
	}

	req, _ := http.NewRequest("GET", srv.URL+"/api/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+tokResp.Token)
	meResp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer meResp.Body.Close()
	if meResp.StatusCode != http.StatusOK {
		t.Fatalf("bearer token must authenticate, got %d", meResp.StatusCode)
	}
}

func TestCannotDeleteSelf(t *testing.T) {
	srv := newTestServer(t, testUser("bob", "password123", "admin"))
	client := loginClient(t, srv, "bob", "password123")
	resp := doJSON(t, client, "DELETE", srv.URL+"/api/users/bob", nil, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("self deletion must be rejected, got %d", resp.StatusCode)
	}
}

func TestAPIRequestMetrics(t *testing.T) {
	devwsmetrics.APIRequestsTotal.Reset()
	srv := newTestServer(t, testUser("alice", "password123", "user"))
	client := loginClient(t, srv, "alice", "password123")

	resp, _ := doGet(t, client, srv.URL+"/api/auth/me")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("me: %d", resp.StatusCode)
	}
	// A request that matches no route pattern is counted as unmatched
	// (unknown GET paths fall through to the SPA handler; a DELETE hits
	// the method mismatch and is served 405 with an empty pattern).
	resp = doJSON(t, client, "DELETE", srv.URL+"/api/definitely-not-a-route", nil, nil)
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("unmatched route should 405, got %d", resp.StatusCode)
	}

	if v := testutil.ToFloat64(devwsmetrics.APIRequestsTotal.WithLabelValues("GET", "GET /api/auth/me", "200")); v != 1 {
		t.Fatalf("GET /api/auth/me counted %v, want 1", v)
	}
	if v := testutil.ToFloat64(devwsmetrics.APIRequestsTotal.WithLabelValues("DELETE", "unmatched", "405")); v != 1 {
		t.Fatalf("unmatched route counted %v, want 1", v)
	}
}
