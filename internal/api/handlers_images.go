package api

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	devwsv1alpha1 "github.com/celerbern/k8s-dev-ws-go/api/v1alpha1"
	"github.com/celerbern/k8s-dev-ws-go/internal/controllers"
)

// ------------------------------------------------------------------ views --

type workspaceImageView struct {
	Name        string `json:"name"`
	Image       string `json:"image"`
	Description string `json:"description"`
	Deprecated  bool   `json:"deprecated"`
	// UsedBy counts the workspaces (cluster-wide) running this image. It
	// is only computed for admin callers.
	UsedBy int `json:"usedBy,omitempty"`
}

func viewWorkspaceImage(wi *devwsv1alpha1.WorkspaceImage, usedBy int) *workspaceImageView {
	return &workspaceImageView{
		Name:        wi.Name,
		Image:       wi.Spec.Image,
		Description: wi.Spec.Description,
		Deprecated:  wi.Spec.Deprecated,
		UsedBy:      usedBy,
	}
}

// imageUsage counts workspaces per image reference, cluster-wide.
func (s *Server) imageUsage(r *http.Request) map[string]int {
	counts := map[string]int{}
	var list devwsv1alpha1.WorkspaceList
	if err := s.client.List(r.Context(), &list); err != nil {
		return counts
	}
	for i := range list.Items {
		if img := list.Items[i].Spec.Image; img != "" {
			counts[img]++
		}
	}
	return counts
}

// -------------------------------------------------------------- validation --

// imageRefCharsetRE is a light sanity check on image references: a
// lowercase-ish path with no spaces or quotes.
var imageRefCharsetRE = regexp.MustCompile(`^[a-z0-9._\-/:@]+$`)

// imageHasTagOrDigest reports whether an image reference is pinned to an
// explicit ":tag" or "@digest" (bare references like "ubuntu" or
// "localhost:5000/myimage" are rejected — the platform default image
// already covers the unpinned case).
func imageHasTagOrDigest(ref string) bool {
	if i := strings.Index(ref, "@"); i > 0 {
		return strings.Contains(ref[i:], ":")
	}
	i := strings.LastIndex(ref, ":")
	if i < 0 {
		return false
	}
	return i > strings.LastIndex(ref, "/")
}

// validateImageRef checks that a workspace image reference is well-formed
// enough for the catalog: no spaces/symbols, and pinned to an explicit
// tag or digest so workspace images are reproducible.
func validateImageRef(ref string) error {
	if ref == "" {
		return fmt.Errorf("image is required")
	}
	if !imageRefCharsetRE.MatchString(ref) {
		return fmt.Errorf("image %q is not a valid image reference", ref)
	}
	if !imageHasTagOrDigest(ref) {
		return fmt.Errorf("image %q must carry an explicit :tag or @sha256: digest", ref)
	}
	return nil
}

// joinImageTag appends a separate tag to an image reference that does not
// carry one yet (the UI collects image and tag separately).
func joinImageTag(image, tag string) string {
	if tag == "" || imageHasTagOrDigest(image) {
		return image
	}
	return image + ":" + tag
}

// conflictingImageName returns the name of another workspace image using
// the same spec.image ("" when the reference is free).
func (s *Server) conflictingImageName(ctx context.Context, image, exclude string) (string, error) {
	var list devwsv1alpha1.WorkspaceImageList
	if err := s.client.List(ctx, &list); err != nil {
		return "", err
	}
	for i := range list.Items {
		if list.Items[i].Name != exclude && list.Items[i].Spec.Image == image {
			return list.Items[i].Name, nil
		}
	}
	return "", nil
}

// --------------------------------------------------------------- handlers --

// handleListWorkspaceImages serves the image catalog. Any authenticated
// user may list (it feeds the workspace creation picker); only admins get
// the cluster-wide usedBy counts.
func (s *Server) handleListWorkspaceImages(w http.ResponseWriter, r *http.Request) {
	var list devwsv1alpha1.WorkspaceImageList
	if err := s.client.List(r.Context(), &list); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	var usage map[string]int
	if UserFrom(r.Context()).Spec.Role == "admin" {
		usage = s.imageUsage(r)
	}
	views := []*workspaceImageView{}
	for i := range list.Items {
		img := &list.Items[i]
		views = append(views, viewWorkspaceImage(img, usage[img.Spec.Image]))
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Name < views[j].Name })
	writeJSON(w, http.StatusOK, views)
}

func (s *Server) handleCreateWorkspaceImage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"name"`
		Image       string `json:"image"`
		Tag         string `json:"tag"`
		Description string `json:"description"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !workspaceNameRE.MatchString(req.Name) {
		writeErr(w, http.StatusBadRequest, "image name must be 2-40 chars of lowercase letters, digits and dashes")
		return
	}
	image := joinImageTag(strings.TrimSpace(req.Image), strings.TrimSpace(req.Tag))
	if err := validateImageRef(image); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	ctx := r.Context()
	var existing devwsv1alpha1.WorkspaceImage
	if err := s.client.Get(ctx, client.ObjectKey{Name: req.Name}, &existing); err == nil {
		writeErr(w, http.StatusConflict, "workspace image already exists")
		return
	}
	if other, err := s.conflictingImageName(ctx, image, req.Name); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	} else if other != "" {
		writeErr(w, http.StatusConflict, fmt.Sprintf("image %q is already offered as %q", image, other))
		return
	}

	wi := &devwsv1alpha1.WorkspaceImage{
		ObjectMeta: metav1.ObjectMeta{
			Name:   req.Name,
			Labels: map[string]string{controllers.LabelManaged: "true"},
		},
	}
	wi.Spec.Image = image
	wi.Spec.Description = req.Description
	if err := s.client.Create(ctx, wi); err != nil {
		if apierrors.IsAlreadyExists(err) {
			writeErr(w, http.StatusConflict, "workspace image already exists")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, viewWorkspaceImage(wi, 0))
}

func (s *Server) handlePatchWorkspaceImage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Image       *string `json:"image"`
		Tag         *string `json:"tag"`
		Description *string `json:"description"`
		Deprecated  *bool   `json:"deprecated"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	var wi devwsv1alpha1.WorkspaceImage
	if err := s.client.Get(r.Context(), client.ObjectKey{Name: r.PathValue("name")}, &wi); err != nil {
		if apierrors.IsNotFound(err) {
			writeErr(w, http.StatusNotFound, "workspace image not found")
		} else {
			writeErr(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	if req.Image != nil {
		image := strings.TrimSpace(*req.Image)
		tag := ""
		if req.Tag != nil {
			tag = strings.TrimSpace(*req.Tag)
		}
		image = joinImageTag(image, tag)
		if err := validateImageRef(image); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if other, err := s.conflictingImageName(r.Context(), image, wi.Name); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		} else if other != "" {
			writeErr(w, http.StatusConflict, fmt.Sprintf("image %q is already offered as %q", image, other))
			return
		}
		wi.Spec.Image = image
	}
	if req.Description != nil {
		wi.Spec.Description = *req.Description
	}
	if req.Deprecated != nil {
		wi.Spec.Deprecated = *req.Deprecated
	}
	if err := s.client.Update(r.Context(), &wi); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, viewWorkspaceImage(&wi, 0))
}

// handleDeleteWorkspaceImage removes a catalog entry. Existing workspaces
// are never touched — they keep their resolved spec.image, and the image
// policy only runs at workspace create/patch time.
func (s *Server) handleDeleteWorkspaceImage(w http.ResponseWriter, r *http.Request) {
	wi := &devwsv1alpha1.WorkspaceImage{
		ObjectMeta: metav1.ObjectMeta{Name: r.PathValue("name")},
	}
	if err := s.client.Delete(r.Context(), wi); err != nil {
		if apierrors.IsNotFound(err) {
			writeErr(w, http.StatusNotFound, "workspace image not found")
		} else {
			writeErr(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleting"})
}

// ------------------------------------------------------------ ws policy --

// validateWorkspaceImage applies the image policy for workspace
// create/patch. An empty image means the platform default (always
// allowed). Admins may use any well-formed reference. Regular users must
// pick an image offered by the catalog and not deprecated. The policy
// follows the caller, not the target user (consistent with ownerFor).
// Existing workspaces are never retro-validated.
func (s *Server) validateWorkspaceImage(w http.ResponseWriter, r *http.Request, caller *devwsv1alpha1.User, image string) bool {
	if image == "" {
		return true
	}
	if caller.Spec.Role == "admin" {
		if err := validateImageRef(image); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return false
		}
		return true
	}
	var list devwsv1alpha1.WorkspaceImageList
	if err := s.client.List(r.Context(), &list); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return false
	}
	for i := range list.Items {
		if list.Items[i].Spec.Image == image && !list.Items[i].Spec.Deprecated {
			return true
		}
	}
	writeErr(w, http.StatusBadRequest, fmt.Sprintf("unknown workspace image %q", image))
	return false
}