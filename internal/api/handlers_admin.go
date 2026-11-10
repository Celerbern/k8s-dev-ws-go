package api

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	devwsv1alpha1 "github.com/celerbern/k8s-dev-ws-go/api/v1alpha1"
	"github.com/celerbern/k8s-dev-ws-go/internal/controllers"
)

// userUsageRow is a per-user usage-vs-quota row in the admin usage view.
type userUsageRow struct {
	Username      string `json:"username"`
	Workspaces    int    `json:"workspaces"`
	MaxWorkspaces int    `json:"maxWorkspaces"`
	Running       int    `json:"running"`
	CPU           string `json:"cpu"`
	CPUQuota      string `json:"cpuQuota"`
	Memory        string `json:"memory"`
	MemoryQuota   string `json:"memoryQuota"`
	Storage       string `json:"storage"`
	StorageQuota  string `json:"storageQuota"`
}

// handleAdminUsage reports cluster-wide counts plus, for every user, their
// workspace footprint against their quotas. CPU and memory reflect
// workspaces with a live pod; storage counts all workspaces (PVCs persist
// while stopped). Quota fields are empty when unset (partial quotas).
func (s *Server) handleAdminUsage(w http.ResponseWriter, r *http.Request) {
	var users devwsv1alpha1.UserList
	ctx := r.Context()
	if err := s.client.List(ctx, &users); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	rows := []userUsageRow{}
	totalWorkspaces, totalRunning := 0, 0
	for i := range users.Items {
		user := &users.Items[i]
		usage, err := s.userUsage(ctx, user.Name)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		quotas := effectiveUserQuotas(user.Spec.Quotas)
		row := userUsageRow{
			Username:      user.Name,
			Workspaces:    usage.Workspaces,
			MaxWorkspaces: quotas.MaxWorkspaces,
			Running:       usage.Running,
			CPU:           usage.CPU,
			Memory:        usage.Memory,
			Storage:       usage.Storage,
		}
		if !quotas.CPU.IsZero() {
			row.CPUQuota = quotas.CPU.String()
		}
		if !quotas.Memory.IsZero() {
			row.MemoryQuota = quotas.Memory.String()
		}
		if !quotas.Storage.IsZero() {
			row.StorageQuota = quotas.Storage.String()
		}
		rows = append(rows, row)
		totalWorkspaces += usage.Workspaces
		totalRunning += usage.Running
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Username < rows[j].Username })

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"users":      len(users.Items),
		"workspaces": totalWorkspaces,
		"running":    totalRunning,
		"perUser":    rows,
	})
}

// ------------------------------------------------------------------ audit --

// defaultAuditEvents caps the audit events returned when the client does
// not ask for a specific amount; maxAuditEvents is the hard ceiling.
const (
	defaultAuditEvents = 200
	maxAuditEvents     = 1000
)

// adminEventView is a kubernetes event in a user namespace, attributed to
// its user.
type adminEventView struct {
	eventView
	User      string `json:"user,omitempty"`
	Namespace string `json:"namespace,omitempty"`
}

// handleAdminEvents returns recent kubernetes events across all user
// namespaces: the workspace lifecycle audit log (WorkspaceStarting,
// WorkspaceFailed, scheduling and quota denials, pod restarts, ...).
// Query parameters: limit (default 200, max 1000).
func (s *Server) handleAdminEvents(w http.ResponseWriter, r *http.Request) {
	if s.kube == nil {
		writeErr(w, http.StatusNotImplemented, "event listing is not available")
		return
	}
	limit := defaultAuditEvents
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			writeErr(w, http.StatusBadRequest, "invalid limit parameter")
			return
		}
		if n > maxAuditEvents {
			n = maxAuditEvents
		}
		limit = n
	}

	// User namespaces are labeled by the user controller.
	var nsList corev1.NamespaceList
	if err := s.client.List(r.Context(), &nsList, client.MatchingLabels{controllers.LabelManaged: "true"}); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	events := []adminEventView{}
	for i := range nsList.Items {
		ns := &nsList.Items[i]
		nsEvents, err := s.kube.ListEvents(r.Context(), ns.Name)
		if err != nil {
			writeErr(w, http.StatusBadGateway, "listing events in "+ns.Name+": "+err.Error())
			return
		}
		user := ns.Labels[controllers.LabelUser]
		for j := range nsEvents {
			e := &nsEvents[j]
			view := adminEventView{eventView: eventView{
				Type:    e.Type,
				Reason:  e.Reason,
				Object:  strings.ToLower(e.InvolvedObject.Kind) + "/" + e.InvolvedObject.Name,
				Message: e.Message,
				Count:   e.Count,
			}, User: user, Namespace: ns.Name}
			last := e.LastTimestamp
			if last.IsZero() {
				last = metav1.Time{Time: e.EventTime.Time}
			}
			first := e.FirstTimestamp
			if first.IsZero() {
				first = last
			}
			view.FirstSeen = first.UTC().Format(time.RFC3339)
			view.LastSeen = last.UTC().Format(time.RFC3339)
			events = append(events, view)
		}
	}
	// Newest first, capped at limit.
	sort.Slice(events, func(i, j int) bool { return events[i].LastSeen > events[j].LastSeen })
	if len(events) > limit {
		events = events[:limit]
	}
	writeJSON(w, http.StatusOK, events)
}
