package api

import (
	"net/http"
	"sort"

	devwsv1alpha1 "github.com/celerbern/k8s-dev-ws-go/api/v1alpha1"
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