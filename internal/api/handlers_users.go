package api

import (
	"net/http"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	devwsv1alpha1 "github.com/celerbern/k8s-dev-ws-go/api/v1alpha1"
	"github.com/celerbern/k8s-dev-ws-go/internal/auth"
	"github.com/celerbern/k8s-dev-ws-go/internal/config"
	"github.com/celerbern/k8s-dev-ws-go/internal/controllers"
)

var usernameRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,28}[a-z0-9])?$`)

// ------------------------------------------------------------------ views --

type publicKeyView struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Fingerprint string `json:"fingerprint"`
	CreatedAt   string `json:"createdAt,omitempty"`
}

type quotasView struct {
	MaxWorkspaces int    `json:"maxWorkspaces"`
	CPU           string `json:"cpu"`
	Memory        string `json:"memory"`
	Storage       string `json:"storage"`
}

type userView struct {
	Username       string          `json:"username"`
	Email          string          `json:"email"`
	DisplayName    string          `json:"displayName"`
	Role           string          `json:"role"`
	Disabled       bool            `json:"disabled"`
	Namespace      string          `json:"namespace"`
	CreatedAt      string          `json:"createdAt"`
	SSHPublicKeys  []publicKeyView `json:"sshPublicKeys"`
	APITokens      []tokenView     `json:"apiTokens"`
	Quotas         quotasView      `json:"quotas"`
	WorkspaceCount int             `json:"workspaceCount"`
}

type tokenView struct {
	Name       string `json:"name"`
	CreatedAt  string `json:"createdAt,omitempty"`
	ExpiresAt  string `json:"expiresAt,omitempty"`
	LastUsedAt string `json:"lastUsedAt,omitempty"`
}

func publicUser(u *devwsv1alpha1.User, workspaceCount int) *userView {
	v := &userView{
		Username:       u.Name,
		Email:          u.Spec.Email,
		DisplayName:    u.Spec.DisplayName,
		Role:           u.Spec.Role,
		Disabled:       u.Spec.Disabled,
		Namespace:      controllers.NamespaceFor(u.Name),
		CreatedAt:      u.CreationTimestamp.UTC().Format(time.RFC3339),
		WorkspaceCount: workspaceCount,
		Quotas:         quotasView{},
	}
	if v.Role == "" {
		v.Role = "user"
	}
	for _, k := range u.Spec.SSHPublicKeys {
		pv := publicKeyView{Name: k.Name, CreatedAt: k.CreatedAt.UTC().Format(time.RFC3339)}
		if parsed, _, _, _, err := ssh.ParseAuthorizedKey([]byte(k.Key)); err == nil {
			pv.Type = parsed.Type()
			pv.Fingerprint = ssh.FingerprintSHA256(parsed)
		} else {
			pv.Type = "invalid"
		}
		v.SSHPublicKeys = append(v.SSHPublicKeys, pv)
	}
	for _, t := range u.Spec.APITokens {
		tv := tokenView{Name: t.Name, CreatedAt: t.CreatedAt.UTC().Format(time.RFC3339)}
		if t.ExpiresAt != nil {
			tv.ExpiresAt = t.ExpiresAt.UTC().Format(time.RFC3339)
		}
		if t.LastUsedAt != nil {
			tv.LastUsedAt = t.LastUsedAt.UTC().Format(time.RFC3339)
		}
		v.APITokens = append(v.APITokens, tv)
	}
	quotas := u.Spec.Quotas
	if quotas.MaxWorkspaces == 0 && quotas.CPU.IsZero() && quotas.Memory.IsZero() && quotas.Storage.IsZero() {
		quotas = config.DefaultUserQuotas()
	}
	v.Quotas.MaxWorkspaces = quotas.MaxWorkspaces
	if !quotas.CPU.IsZero() {
		v.Quotas.CPU = quotas.CPU.String()
	}
	if !quotas.Memory.IsZero() {
		v.Quotas.Memory = quotas.Memory.String()
	}
	if !quotas.Storage.IsZero() {
		v.Quotas.Storage = quotas.Storage.String()
	}
	return v
}

// workspaceCounts lists all workspaces once and groups them by namespace.
func (s *Server) workspaceCounts(r *http.Request) map[string]int {
	var list devwsv1alpha1.WorkspaceList
	counts := map[string]int{}
	if err := s.client.List(r.Context(), &list); err != nil {
		return counts
	}
	for _, ws := range list.Items {
		counts[ws.Namespace]++
	}
	return counts
}

// --------------------------------------------------------------- handlers --

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	var users devwsv1alpha1.UserList
	if err := s.client.List(r.Context(), &users); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	counts := s.workspaceCounts(r)
	views := []*userView{}
	for i := range users.Items {
		u := &users.Items[i]
		views = append(views, publicUser(u, counts[u.Status.Namespace]))
	}
	writeJSON(w, http.StatusOK, views)
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username    string `json:"username"`
		Password    string `json:"password"`
		Email       string `json:"email"`
		DisplayName string `json:"displayName"`
		Role        string `json:"role"`
		Quotas      *struct {
			MaxWorkspaces int    `json:"maxWorkspaces"`
			CPU           string `json:"cpu"`
			Memory        string `json:"memory"`
			Storage       string `json:"storage"`
		} `json:"quotas"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !usernameRE.MatchString(req.Username) {
		writeErr(w, http.StatusBadRequest, "username must be 2-30 chars of lowercase letters, digits and dashes")
		return
	}
	if len(req.Password) < 8 {
		writeErr(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}
	if req.Role != "" && req.Role != "user" && req.Role != "admin" {
		writeErr(w, http.StatusBadRequest, "role must be user or admin")
		return
	}

	ctx := r.Context()
	var existing devwsv1alpha1.User
	if err := s.client.Get(ctx, client.ObjectKey{Name: req.Username}, &existing); err == nil {
		writeErr(w, http.StatusConflict, "user already exists")
		return
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to hash password")
		return
	}
	user := &devwsv1alpha1.User{}
	user.Name = req.Username
	user.Spec.Email = req.Email
	user.Spec.DisplayName = req.DisplayName
	user.Spec.Role = req.Role
	user.Spec.PasswordHash = hash
	if req.Quotas != nil {
		user.Spec.Quotas.MaxWorkspaces = req.Quotas.MaxWorkspaces
		if req.Quotas.CPU != "" {
			user.Spec.Quotas.CPU = resource.MustParse(req.Quotas.CPU)
		}
		if req.Quotas.Memory != "" {
			user.Spec.Quotas.Memory = resource.MustParse(req.Quotas.Memory)
		}
		if req.Quotas.Storage != "" {
			user.Spec.Quotas.Storage = resource.MustParse(req.Quotas.Storage)
		}
	}
	if err := s.client.Create(ctx, user); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, publicUser(user, 0))
}

// targetUser resolves the {name} path parameter, checking that the caller
// may act on it (self or admin).
func (s *Server) targetUser(w http.ResponseWriter, r *http.Request) (*devwsv1alpha1.User, bool) {
	name := r.PathValue("name")
	caller := UserFrom(r.Context())
	if name != caller.Name && caller.Spec.Role != "admin" {
		writeErr(w, http.StatusForbidden, "you may only access your own user")
		return nil, false
	}
	var user devwsv1alpha1.User
	if err := s.client.Get(r.Context(), client.ObjectKey{Name: name}, &user); err != nil {
		if apierrors.IsNotFound(err) {
			writeErr(w, http.StatusNotFound, "user not found")
		} else {
			writeErr(w, http.StatusInternalServerError, err.Error())
		}
		return nil, false
	}
	return &user, true
}

func (s *Server) handleGetUser(w http.ResponseWriter, r *http.Request) {
	user, ok := s.targetUser(w, r)
	if !ok {
		return
	}
	counts := map[string]int{}
	var list devwsv1alpha1.WorkspaceList
	if err := s.client.List(r.Context(), &list, client.InNamespace(controllers.NamespaceFor(user.Name))); err == nil {
		counts[controllers.NamespaceFor(user.Name)] = len(list.Items)
	}
	writeJSON(w, http.StatusOK, publicUser(user, counts[controllers.NamespaceFor(user.Name)]))
}

func (s *Server) handlePatchUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email       *string `json:"email"`
		DisplayName *string `json:"displayName"`
		Role        *string `json:"role"`
		Disabled    *bool   `json:"disabled"`
		Password    *string `json:"password"`
		Quotas      *struct {
			MaxWorkspaces *int    `json:"maxWorkspaces"`
			CPU           *string `json:"cpu"`
			Memory        *string `json:"memory"`
			Storage       *string `json:"storage"`
		} `json:"quotas"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	user, ok := s.targetUser(w, r)
	if !ok {
		return
	}
	caller := UserFrom(r.Context())

	if req.Role != nil && *req.Role != user.Spec.Role {
		if user.Name == caller.Name {
			writeErr(w, http.StatusBadRequest, "you cannot change your own role")
			return
		}
		if user.Spec.Role == "admin" && !s.hasOtherAdmin(r, user.Name) {
			writeErr(w, http.StatusBadRequest, "cannot demote the last admin")
			return
		}
	}
	if req.Disabled != nil && *req.Disabled && user.Name == caller.Name {
		writeErr(w, http.StatusBadRequest, "you cannot disable your own user")
		return
	}

	if req.Email != nil {
		user.Spec.Email = *req.Email
	}
	if req.DisplayName != nil {
		user.Spec.DisplayName = *req.DisplayName
	}
	if req.Role != nil {
		user.Spec.Role = *req.Role
	}
	if req.Disabled != nil {
		user.Spec.Disabled = *req.Disabled
	}
	if req.Password != nil {
		if len(*req.Password) < 8 {
			writeErr(w, http.StatusBadRequest, "password must be at least 8 characters")
			return
		}
		hash, err := auth.HashPassword(*req.Password)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "failed to hash password")
			return
		}
		user.Spec.PasswordHash = hash
	}
	if req.Quotas != nil {
		if req.Quotas.MaxWorkspaces != nil {
			user.Spec.Quotas.MaxWorkspaces = *req.Quotas.MaxWorkspaces
		}
		if req.Quotas.CPU != nil {
			if *req.Quotas.CPU == "" {
				user.Spec.Quotas.CPU = resource.Quantity{}
			} else {
				user.Spec.Quotas.CPU = resource.MustParse(*req.Quotas.CPU)
			}
		}
		if req.Quotas.Memory != nil {
			if *req.Quotas.Memory == "" {
				user.Spec.Quotas.Memory = resource.Quantity{}
			} else {
				user.Spec.Quotas.Memory = resource.MustParse(*req.Quotas.Memory)
			}
		}
		if req.Quotas.Storage != nil {
			if *req.Quotas.Storage == "" {
				user.Spec.Quotas.Storage = resource.Quantity{}
			} else {
				user.Spec.Quotas.Storage = resource.MustParse(*req.Quotas.Storage)
			}
		}
	}
	if err := s.client.Update(r.Context(), user); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, publicUser(user, 0))
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	user, ok := s.targetUser(w, r)
	if !ok {
		return
	}
	caller := UserFrom(r.Context())
	if user.Name == caller.Name {
		writeErr(w, http.StatusBadRequest, "you cannot delete your own user")
		return
	}
	if user.Spec.Role == "admin" && !s.hasOtherAdmin(r, user.Name) {
		writeErr(w, http.StatusBadRequest, "cannot delete the last admin")
		return
	}
	if err := s.client.Delete(r.Context(), user); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleting"})
}

// hasOtherAdmin reports whether at least one other enabled admin exists.
func (s *Server) hasOtherAdmin(r *http.Request, exclude string) bool {
	var users devwsv1alpha1.UserList
	if err := s.client.List(r.Context(), &users); err != nil {
		return false
	}
	for i := range users.Items {
		u := &users.Items[i]
		if u.Name != exclude && u.Spec.Role == "admin" && !u.Spec.Disabled {
			return true
		}
	}
	return false
}

func (s *Server) handleSetPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	user, ok := s.targetUser(w, r)
	if !ok {
		return
	}
	caller := UserFrom(r.Context())
	// Admins may reset other users' passwords without the current one.
	if user.Name == caller.Name || caller.Spec.Role != "admin" {
		if user.Spec.PasswordHash == "" || !auth.CheckPassword(user.Spec.PasswordHash, req.CurrentPassword) {
			writeErr(w, http.StatusUnauthorized, "current password is incorrect")
			return
		}
	}
	if len(req.NewPassword) < 8 {
		writeErr(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}
	hash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to hash password")
		return
	}
	user.Spec.PasswordHash = hash
	if err := s.client.Update(r.Context(), user); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ------------------------------------------------------------------- keys --

func (s *Server) handleAddKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
		Key  string `json:"key"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	user, ok := s.targetUser(w, r)
	if !ok {
		return
	}
	key := strings.Join(strings.Fields(req.Key), " ")
	parsed, _, _, _, err := ssh.ParseAuthorizedKey([]byte(key))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "not a valid SSH public key")
		return
	}
	if req.Name == "" {
		writeErr(w, http.StatusBadRequest, "key name is required")
		return
	}
	for _, existing := range user.Spec.SSHPublicKeys {
		if existing.Name == req.Name {
			writeErr(w, http.StatusConflict, "a key with this name already exists")
			return
		}
	}
	user.Spec.SSHPublicKeys = append(user.Spec.SSHPublicKeys, devwsv1alpha1.SSHKey{
		Name:      req.Name,
		Key:       strings.TrimSpace(string(ssh.MarshalAuthorizedKey(parsed))),
		CreatedAt: metav1.Now(),
	})
	if err := s.client.Update(r.Context(), user); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, publicUser(user, 0))
}

func (s *Server) handleDeleteKey(w http.ResponseWriter, r *http.Request) {
	user, ok := s.targetUser(w, r)
	if !ok {
		return
	}
	keyName := r.PathValue("key")
	kept := user.Spec.SSHPublicKeys[:0]
	found := false
	for _, k := range user.Spec.SSHPublicKeys {
		if k.Name == keyName {
			found = true
			continue
		}
		kept = append(kept, k)
	}
	if !found {
		writeErr(w, http.StatusNotFound, "key not found")
		return
	}
	user.Spec.SSHPublicKeys = kept
	if err := s.client.Update(r.Context(), user); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, publicUser(user, 0))
}

// ----------------------------------------------------------------- tokens --

func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request) {
	user, ok := s.targetUser(w, r)
	if !ok {
		return
	}
	v := publicUser(user, 0)
	writeJSON(w, http.StatusOK, v.APITokens)
}

func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"name"`
		ExpiresDays int    `json:"expiresDays"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	user, ok := s.targetUser(w, r)
	if !ok {
		return
	}
	if req.Name == "" {
		writeErr(w, http.StatusBadRequest, "token name is required")
		return
	}
	for _, t := range user.Spec.APITokens {
		if t.Name == req.Name {
			writeErr(w, http.StatusConflict, "a token with this name already exists")
			return
		}
	}
	token, prefix, hash, err := auth.NewToken()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to generate token")
		return
	}
	entry := devwsv1alpha1.APIToken{
		Name:      req.Name,
		Prefix:    prefix,
		Hash:      hash,
		CreatedAt: metav1.Now(),
	}
	if req.ExpiresDays > 0 {
		t := metav1.NewTime(time.Now().AddDate(0, 0, req.ExpiresDays))
		entry.ExpiresAt = &t
	}
	user.Spec.APITokens = append(user.Spec.APITokens, entry)
	if err := s.client.Update(r.Context(), user); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp := map[string]interface{}{"token": token, "name": req.Name}
	if entry.ExpiresAt != nil {
		resp["expiresAt"] = entry.ExpiresAt.UTC().Format(time.RFC3339)
	}
	writeJSON(w, http.StatusCreated, resp)
}

func (s *Server) handleDeleteToken(w http.ResponseWriter, r *http.Request) {
	user, ok := s.targetUser(w, r)
	if !ok {
		return
	}
	tokenName := r.PathValue("token")
	kept := user.Spec.APITokens[:0]
	found := false
	for _, t := range user.Spec.APITokens {
		if t.Name == tokenName {
			found = true
			continue
		}
		kept = append(kept, t)
	}
	if !found {
		writeErr(w, http.StatusNotFound, "token not found")
		return
	}
	user.Spec.APITokens = kept
	if err := s.client.Update(r.Context(), user); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
