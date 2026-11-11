// Package api implements the devws REST API served by the apiserver, plus
// the embedded web UI. All state is read from and written to Kubernetes
// custom resources through the controller-runtime client.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	devwsv1alpha1 "github.com/celerbern/k8s-dev-ws-go/api/v1alpha1"
	"github.com/celerbern/k8s-dev-ws-go/internal/auth"
	"github.com/celerbern/k8s-dev-ws-go/internal/config"
	devwsmetrics "github.com/celerbern/k8s-dev-ws-go/internal/metrics"
	"github.com/celerbern/k8s-dev-ws-go/internal/oidc"
	"github.com/celerbern/k8s-dev-ws-go/internal/web"
)

// Server is the HTTP control-plane server. It implements the controller
// runtime Runnable interface.
type Server struct {
	Addr       string
	client     client.Client
	scheme     *runtime.Scheme
	cfg        *config.Config
	sessions   *auth.SessionManager
	kube       KubeClient
	oidc       *oidc.Provider
	mux        *http.ServeMux
	httpServer *http.Server
}

// New builds the server and registers all routes. kube may be nil (log and
// event endpoints then answer 501); tests inject a fake. oidc may be nil,
// which disables the OIDC login flow (local passwords only).
func New(addr string, c client.Client, scheme *runtime.Scheme, cfg *config.Config, sessions *auth.SessionManager, kube KubeClient, oidcProvider *oidc.Provider) *Server {
	s := &Server{
		Addr:     addr,
		client:   c,
		scheme:   scheme,
		cfg:      cfg,
		sessions: sessions,
		kube:     kube,
		oidc:     oidcProvider,
	}
	s.routes()
	return s
}

func (s *Server) routes() {
	mux := http.NewServeMux()

	// health
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	// auth
	mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/auth/logout", s.handleLogout)
	mux.HandleFunc("GET /api/auth/me", s.requireAuth(s.handleMe))
	mux.HandleFunc("POST /api/auth/token", s.handleIssueToken)
	mux.HandleFunc("GET /api/auth/providers", s.handleAuthProviders)
	mux.HandleFunc("GET /api/auth/oidc/start", s.handleOIDCStart)
	mux.HandleFunc("GET /api/auth/oidc/callback", s.handleOIDCCallback)

	// platform config
	mux.HandleFunc("GET /api/config", s.requireAuth(s.handlePlatformConfig))

	// users (admin)
	mux.HandleFunc("GET /api/users", s.requireAdmin(s.handleListUsers))
	mux.HandleFunc("POST /api/users", s.requireAdmin(s.handleCreateUser))

	// user (self or admin)
	mux.HandleFunc("GET /api/users/{name}", s.requireAuth(s.handleGetUser))
	mux.HandleFunc("PATCH /api/users/{name}", s.requireAdmin(s.handlePatchUser))
	mux.HandleFunc("DELETE /api/users/{name}", s.requireAdmin(s.handleDeleteUser))
	mux.HandleFunc("POST /api/users/{name}/password", s.requireAuth(s.handleSetPassword))
	mux.HandleFunc("POST /api/users/{name}/keys", s.requireAuth(s.handleAddKey))
	mux.HandleFunc("DELETE /api/users/{name}/keys/{key}", s.requireAuth(s.handleDeleteKey))
	mux.HandleFunc("GET /api/users/{name}/tokens", s.requireAuth(s.handleListTokens))
	mux.HandleFunc("POST /api/users/{name}/tokens", s.requireAuth(s.handleCreateToken))
	mux.HandleFunc("DELETE /api/users/{name}/tokens/{token}", s.requireAuth(s.handleDeleteToken))

	// workspaces
	mux.HandleFunc("GET /api/workspaces", s.requireAuth(s.handleListWorkspaces))
	mux.HandleFunc("POST /api/workspaces", s.requireAuth(s.handleCreateWorkspace))
	mux.HandleFunc("GET /api/workspaces/{name}", s.requireAuth(s.handleGetWorkspace))
	mux.HandleFunc("PATCH /api/workspaces/{name}", s.requireAuth(s.handlePatchWorkspace))
	mux.HandleFunc("DELETE /api/workspaces/{name}", s.requireAuth(s.handleDeleteWorkspace))
	mux.HandleFunc("POST /api/workspaces/{name}/start", s.requireAuth(s.handleStartWorkspace))
	mux.HandleFunc("POST /api/workspaces/{name}/stop", s.requireAuth(s.handleStopWorkspace))
	mux.HandleFunc("GET /api/workspaces/{name}/logs", s.requireAuth(s.handleWorkspaceLogs))
	mux.HandleFunc("GET /api/workspaces/{name}/events", s.requireAuth(s.handleWorkspaceEvents))

	// workspace image catalog
	mux.HandleFunc("GET /api/workspace-images", s.requireAuth(s.handleListWorkspaceImages))
	mux.HandleFunc("POST /api/workspace-images", s.requireAdmin(s.handleCreateWorkspaceImage))
	mux.HandleFunc("PATCH /api/workspace-images/{name}", s.requireAdmin(s.handlePatchWorkspaceImage))
	mux.HandleFunc("DELETE /api/workspace-images/{name}", s.requireAdmin(s.handleDeleteWorkspaceImage))

	// admin
	mux.HandleFunc("GET /api/admin/usage", s.requireAdmin(s.handleAdminUsage))
	mux.HandleFunc("GET /api/admin/events", s.requireAdmin(s.handleAdminEvents))

	// web UI
	mux.Handle("GET /", web.Handler())

	s.mux = mux
}

// NeedLeaderElection implements the leader-election runnable interface.
func (s *Server) NeedLeaderElection() bool { return false }

// Handler returns the HTTP handler (for tests and custom servers).
func (s *Server) Handler() http.Handler { return s.withRequestMetrics(s.mux) }

// statusRecorder captures the response status code for metrics.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Flush keeps streaming responses (log tails) flushing through the wrapper.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// withRequestMetrics counts requests by method, route pattern and status.
// The route pattern is set by the ServeMux during routing (Go 1.23+);
// requests that match no route are counted as "unmatched".
func (s *Server) withRequestMetrics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		devwsmetrics.APIRequestsTotal.WithLabelValues(r.Method, route, strconv.Itoa(rec.status)).Inc()
	})
}

// Start runs the HTTP server until the context is cancelled.
func (s *Server) Start(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.Addr)
	if err != nil {
		return err
	}
	s.httpServer = &http.Server{
		Handler:           s.Handler(), // mux wrapped with request metrics
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.httpServer.Shutdown(shutdownCtx)
	}()
	if err := s.httpServer.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// ---------------------------------------------------------------- helpers --

type authCtxKey struct{}

// requireAuth wraps a handler so it only runs for authenticated users. The
// authenticated User is placed in the request context.
func (s *Server) requireAuth(next func(http.ResponseWriter, *http.Request)) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		user, err := s.authenticate(r)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "authentication required")
			return
		}
		// Re-check on every request in case the user was just disabled.
		if user.Spec.Disabled {
			writeErr(w, http.StatusForbidden, "user is disabled")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), authCtxKey{}, user)))
	}
}

// requireAdmin wraps a handler so it only runs for admin users.
func (s *Server) requireAdmin(next func(http.ResponseWriter, *http.Request)) func(http.ResponseWriter, *http.Request) {
	return s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		user := UserFrom(r.Context())
		if user.Spec.Role != "admin" {
			writeErr(w, http.StatusForbidden, "admin role required")
			return
		}
		next(w, r)
	})
}

// UserFrom returns the authenticated user from the request context.
func UserFrom(ctx context.Context) *devwsv1alpha1.User {
	user, _ := ctx.Value(authCtxKey{}).(*devwsv1alpha1.User)
	return user
}

// authenticate resolves the requesting user from a session cookie or a
// bearer API token.
func (s *Server) authenticate(r *http.Request) (*devwsv1alpha1.User, error) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	if ah := r.Header.Get("Authorization"); strings.HasPrefix(ah, "Bearer ") {
		token := strings.TrimSpace(strings.TrimPrefix(ah, "Bearer "))
		return s.userForToken(ctx, token)
	}

	cookie, err := r.Cookie(auth.SessionCookieName)
	if err == nil && cookie.Value != "" {
		username, err := s.sessions.Verify(cookie.Value)
		if err != nil {
			return nil, err
		}
		var user devwsv1alpha1.User
		if err := s.client.Get(ctx, client.ObjectKey{Name: username}, &user); err != nil {
			return nil, err
		}
		return &user, nil
	}
	return nil, errors.New("no credentials")
}

// userForToken finds the user owning an API token. Tokens carry a lookup
// prefix so most users can be skipped before the bcrypt comparison.
func (s *Server) userForToken(ctx context.Context, token string) (*devwsv1alpha1.User, error) {
	if !auth.TokenLooksValid(token) {
		return nil, errors.New("malformed token")
	}
	var users devwsv1alpha1.UserList
	if err := s.client.List(ctx, &users); err != nil {
		return nil, err
	}
	for i := range users.Items {
		user := &users.Items[i]
		for j := range user.Spec.APITokens {
			t := &user.Spec.APITokens[j]
			if t.Prefix != "" && !strings.HasPrefix(token, t.Prefix) {
				continue
			}
			if auth.CheckToken(t.Hash, token) {
				if t.ExpiresAt != nil && time.Now().After(t.ExpiresAt.Time) {
					return nil, errors.New("token expired")
				}
				return user, nil
			}
		}
	}
	return nil, errors.New("invalid token")
}

// ------------------------------------------------------------------- json --

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func readJSON(r *http.Request, v interface{}) error {
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return err
	}
	if len(body) == 0 {
		return errors.New("empty request body")
	}
	return json.Unmarshal(body, v)
}

// sessionCookie builds the session cookie for a response.
func sessionCookie(value string, secure bool) *http.Cookie {
	return &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(auth.DefaultSessionTTL.Seconds()),
	}
}

func clearSessionCookie(secure bool) *http.Cookie {
	return &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	}
}
