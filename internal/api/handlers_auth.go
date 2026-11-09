package api

import (
	"net/http"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	devwsv1alpha1 "github.com/celerbern/k8s-dev-ws-go/api/v1alpha1"
	"github.com/celerbern/k8s-dev-ws-go/internal/auth"
)

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Username == "" || req.Password == "" {
		writeErr(w, http.StatusBadRequest, "username and password are required")
		return
	}
	var user devwsv1alpha1.User
	ctx := r.Context()
	if err := s.client.Get(ctx, client.ObjectKey{Name: req.Username}, &user); err != nil {
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if user.Spec.Disabled || user.Spec.PasswordHash == "" || !auth.CheckPassword(user.Spec.PasswordHash, req.Password) {
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	http.SetCookie(w, sessionCookie(s.sessions.Sign(user.Name), r.TLS != nil))
	writeJSON(w, http.StatusOK, publicUser(&user, 0))
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, clearSessionCookie(r.TLS != nil))
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	user := UserFrom(r.Context())
	writeJSON(w, http.StatusOK, publicUser(user, 0))
}

// handleIssueToken exchanges username+password for an API token. This is the
// `devws login` flow.
func (s *Server) handleIssueToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username    string `json:"username"`
		Password    string `json:"password"`
		Name        string `json:"name"`
		ExpiresDays int    `json:"expiresDays"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Username == "" || req.Password == "" {
		writeErr(w, http.StatusBadRequest, "username and password are required")
		return
	}
	var user devwsv1alpha1.User
	ctx := r.Context()
	if err := s.client.Get(ctx, client.ObjectKey{Name: req.Username}, &user); err != nil {
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if user.Spec.Disabled || user.Spec.PasswordHash == "" || !auth.CheckPassword(user.Spec.PasswordHash, req.Password) {
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	name := req.Name
	if name == "" {
		name = "cli-" + time.Now().UTC().Format("20060102")
	}
	token, prefix, hash, err := auth.NewToken()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to generate token")
		return
	}
	entry := devwsv1alpha1.APIToken{
		Name:      name,
		Prefix:    prefix,
		Hash:      hash,
		CreatedAt: metav1.Now(),
	}
	if req.ExpiresDays > 0 {
		t := metav1.NewTime(time.Now().AddDate(0, 0, req.ExpiresDays))
		entry.ExpiresAt = &t
	}
	user.Spec.APITokens = append(user.Spec.APITokens, entry)
	if err := s.client.Update(ctx, &user); err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to store token")
		return
	}
	resp := map[string]interface{}{"token": token, "name": name}
	if entry.ExpiresAt != nil {
		resp["expiresAt"] = entry.ExpiresAt.UTC()
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handlePlatformConfig(w http.ResponseWriter, _ *http.Request) {
	sshHost, sshPort := s.cfg.SSHHostPort()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"sshHost":        sshHost,
		"sshPort":        sshPort,
		"classes":        s.cfg.Classes,
		"defaultClass":   s.cfg.DefaultClass,
		"workspaceImage": s.cfg.WorkspaceImage,
		"storageClass":   s.cfg.StorageClass,
	})
}
