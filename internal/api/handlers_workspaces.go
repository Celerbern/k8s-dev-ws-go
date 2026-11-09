package api

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	devwsv1alpha1 "github.com/celerbern/k8s-dev-ws-go/api/v1alpha1"
	"github.com/celerbern/k8s-dev-ws-go/internal/config"
	"github.com/celerbern/k8s-dev-ws-go/internal/controllers"
)

var workspaceNameRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,38}[a-z0-9])?$`)

type workspaceView struct {
	Name      string   `json:"name"`
	User      string   `json:"user"`
	Namespace string   `json:"namespace"`
	Class     string   `json:"class"`
	Image     string   `json:"image"`
	Storage   string   `json:"storage"`
	GitRepos  []string `json:"gitRepos"`
	Paused    bool     `json:"paused"`
	Phase     string   `json:"phase"`
	Message   string   `json:"message"`
	PodName   string   `json:"podName"`
	SSHHost   string   `json:"sshHost"`
	CreatedAt string   `json:"createdAt"`
}

func viewWorkspace(ws *devwsv1alpha1.Workspace) *workspaceView {
	v := &workspaceView{
		Name:      ws.Name,
		User:      ws.Spec.UserRef,
		Namespace: ws.Namespace,
		Class:     ws.Spec.Class,
		Image:     ws.Spec.Image,
		GitRepos:  ws.Spec.GitRepos,
		Paused:    ws.Spec.Paused,
		Phase:     ws.Status.Phase,
		Message:   ws.Status.Message,
		PodName:   ws.Status.PodName,
		SSHHost:   ws.Status.SSHHost,
		CreatedAt: ws.CreationTimestamp.UTC().Format(time.RFC3339),
	}
	if v.Class == "" {
		v.Class = "(default)"
	}
	if !ws.Spec.Storage.IsZero() {
		v.Storage = ws.Spec.Storage.String()
	}
	return v
}

// ownerFor returns the user whose namespace a request operates on: the
// ?user= query parameter (admin-only) or the caller.
func (s *Server) ownerFor(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	caller := UserFrom(r.Context())
	owner := r.URL.Query().Get("user")
	if owner == "" {
		return caller.Name, controllers.NamespaceFor(caller.Name), true
	}
	if caller.Spec.Role != "admin" {
		writeErr(w, http.StatusForbidden, "only admins may manage other users' workspaces")
		return "", "", false
	}
	var user devwsv1alpha1.User
	if err := s.client.Get(r.Context(), client.ObjectKey{Name: owner}, &user); err != nil {
		writeErr(w, http.StatusNotFound, "user not found")
		return "", "", false
	}
	return user.Name, controllers.NamespaceFor(user.Name), true
}

func (s *Server) handleListWorkspaces(w http.ResponseWriter, r *http.Request) {
	caller := UserFrom(r.Context())
	var list devwsv1alpha1.WorkspaceList
	if r.URL.Query().Get("scope") == "all" && caller.Spec.Role == "admin" {
		if err := s.client.List(r.Context(), &list); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	} else {
		if err := s.client.List(r.Context(), &list, client.InNamespace(controllers.NamespaceFor(caller.Name))); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	views := []*workspaceView{}
	for i := range list.Items {
		views = append(views, viewWorkspace(&list.Items[i]))
	}
	writeJSON(w, http.StatusOK, views)
}

func (s *Server) handleCreateWorkspace(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     string   `json:"name"`
		Class    string   `json:"class"`
		Image    string   `json:"image"`
		Storage  string   `json:"storage"`
		GitRepos []string `json:"gitRepos"`
		User     string   `json:"user"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !workspaceNameRE.MatchString(req.Name) {
		writeErr(w, http.StatusBadRequest, "workspace name must be 2-40 chars of lowercase letters, digits and dashes")
		return
	}
	caller := UserFrom(r.Context())
	owner := caller.Name
	if req.User != "" && req.User != caller.Name {
		if caller.Spec.Role != "admin" {
			writeErr(w, http.StatusForbidden, "only admins may create workspaces for other users")
			return
		}
		owner = req.User
		var user devwsv1alpha1.User
		if err := s.client.Get(r.Context(), client.ObjectKey{Name: owner}, &user); err != nil {
			writeErr(w, http.StatusNotFound, "user not found")
			return
		}
	}
	if req.Class != "" {
		if _, ok := s.cfg.Class(req.Class); !ok {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("unknown workspace class %q", req.Class))
			return
		}
	}

	ctx := r.Context()
	ns := controllers.NamespaceFor(owner)

	// Quota checks (nice errors ahead of the ResourceQuota).
	var user devwsv1alpha1.User
	if err := s.client.Get(ctx, client.ObjectKey{Name: owner}, &user); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	quotas := user.Spec.Quotas
	if quotas.MaxWorkspaces == 0 && quotas.CPU.IsZero() && quotas.Memory.IsZero() && quotas.Storage.IsZero() {
		quotas = config.DefaultUserQuotas()
	}
	var list devwsv1alpha1.WorkspaceList
	if err := s.client.List(ctx, &list, client.InNamespace(ns)); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if quotas.MaxWorkspaces > 0 && len(list.Items) >= quotas.MaxWorkspaces {
		writeErr(w, http.StatusConflict, fmt.Sprintf("workspace quota reached (%d)", quotas.MaxWorkspaces))
		return
	}
	storage := resource.Quantity{}
	if req.Storage != "" {
		storage = resource.MustParse(req.Storage)
	} else if classSpec, ok := s.cfg.Class(req.Class); ok {
		storage = resource.MustParse(classSpec.Storage)
	}
	if !quotas.Storage.IsZero() && !storage.IsZero() {
		var inUse resource.Quantity
		for i := range list.Items {
			q := list.Items[i].Spec.Storage
			if q.IsZero() {
				if cs, ok := s.cfg.Class(list.Items[i].Spec.Class); ok {
					q = resource.MustParse(cs.Storage)
				}
			}
			inUse.Add(q)
		}
		total := inUse.DeepCopy()
		total.Add(storage)
		if total.Cmp(quotas.Storage) > 0 {
			writeErr(w, http.StatusConflict, fmt.Sprintf("storage quota reached (%s)", quotas.Storage.String()))
			return
		}
	}

	ws := &devwsv1alpha1.Workspace{}
	ws.Name = req.Name
	ws.Namespace = ns
	ws.Labels = map[string]string{controllers.LabelManaged: "true"}
	ws.Spec.UserRef = owner
	ws.Spec.Class = req.Class
	ws.Spec.Image = req.Image
	if !storage.IsZero() {
		ws.Spec.Storage = storage
	}
	for _, repo := range req.GitRepos {
		if repo = strings.TrimSpace(repo); repo != "" {
			ws.Spec.GitRepos = append(ws.Spec.GitRepos, repo)
		}
	}
	if err := s.client.Create(ctx, ws); err != nil {
		if apierrors.IsAlreadyExists(err) {
			writeErr(w, http.StatusConflict, "workspace already exists")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, viewWorkspace(ws))
}

func (s *Server) getWorkspace(w http.ResponseWriter, r *http.Request) (*devwsv1alpha1.Workspace, bool) {
	_, ns, ok := s.ownerFor(w, r)
	if !ok {
		return nil, false
	}
	name := r.PathValue("name")
	var ws devwsv1alpha1.Workspace
	if err := s.client.Get(r.Context(), types.NamespacedName{Namespace: ns, Name: name}, &ws); err != nil {
		if apierrors.IsNotFound(err) {
			writeErr(w, http.StatusNotFound, "workspace not found")
		} else {
			writeErr(w, http.StatusInternalServerError, err.Error())
		}
		return nil, false
	}
	return &ws, true
}

func (s *Server) handleGetWorkspace(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.getWorkspace(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, viewWorkspace(ws))
}

func (s *Server) handlePatchWorkspace(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Paused   *bool    `json:"paused"`
		Class    *string  `json:"class"`
		Image    *string  `json:"image"`
		GitRepos []string `json:"gitRepos"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	ws, ok := s.getWorkspace(w, r)
	if !ok {
		return
	}
	if req.Class != nil && *req.Class != "" {
		if _, ok := s.cfg.Class(*req.Class); !ok {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("unknown workspace class %q", *req.Class))
			return
		}
		ws.Spec.Class = *req.Class
	}
	if req.Image != nil {
		ws.Spec.Image = *req.Image
	}
	if req.Paused != nil {
		ws.Spec.Paused = *req.Paused
	}
	if req.GitRepos != nil {
		ws.Spec.GitRepos = nil
		for _, repo := range req.GitRepos {
			if repo = strings.TrimSpace(repo); repo != "" {
				ws.Spec.GitRepos = append(ws.Spec.GitRepos, repo)
			}
		}
	}
	if err := s.client.Update(r.Context(), ws); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, viewWorkspace(ws))
}

func (s *Server) handleDeleteWorkspace(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.getWorkspace(w, r)
	if !ok {
		return
	}
	if err := s.client.Delete(r.Context(), ws); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleting"})
}

func (s *Server) setPaused(w http.ResponseWriter, r *http.Request, paused bool) {
	ws, ok := s.getWorkspace(w, r)
	if !ok {
		return
	}
	ws.Spec.Paused = paused
	if err := s.client.Update(r.Context(), ws); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, viewWorkspace(ws))
}

func (s *Server) handleStartWorkspace(w http.ResponseWriter, r *http.Request) {
	s.setPaused(w, r, false)
}

func (s *Server) handleStopWorkspace(w http.ResponseWriter, r *http.Request) {
	s.setPaused(w, r, true)
}

// ------------------------------------------------------------------ admin --

func (s *Server) handleAdminUsage(w http.ResponseWriter, r *http.Request) {
	var users devwsv1alpha1.UserList
	var workspaces devwsv1alpha1.WorkspaceList
	ctx := r.Context()
	if err := s.client.List(ctx, &users); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.client.List(ctx, &workspaces); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	perUser := map[string]*struct {
		Workspaces int    `json:"workspaces"`
		Storage    string `json:"storage"`
		storage    resource.Quantity
	}{}
	running := 0
	for i := range workspaces.Items {
		ws := &workspaces.Items[i]
		user := ws.Spec.UserRef
		entry, ok := perUser[user]
		if !ok {
			entry = &struct {
				Workspaces int    `json:"workspaces"`
				Storage    string `json:"storage"`
				storage    resource.Quantity
			}{}
			perUser[user] = entry
		}
		entry.Workspaces++
		q := ws.Spec.Storage
		if q.IsZero() {
			if cs, ok := s.cfg.Class(ws.Spec.Class); ok {
				q = resource.MustParse(cs.Storage)
			}
		}
		entry.storage.Add(q)
		if ws.Status.Phase == devwsv1alpha1.WorkspacePhaseRunning {
			running++
		}
	}
	type row struct {
		Workspaces int    `json:"workspaces"`
		Storage    string `json:"storage"`
	}
	rows := map[string]row{}
	for u, e := range perUser {
		rows[u] = row{Workspaces: e.Workspaces, Storage: e.storage.String()}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"users":      len(users.Items),
		"workspaces": len(workspaces.Items),
		"running":    running,
		"perUser":    rows,
	})
}
