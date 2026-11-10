package api

import (
	"net/http"
	"testing"

	devwsv1alpha1 "github.com/celerbern/k8s-dev-ws-go/api/v1alpha1"
)

// usageWorkspaces builds test workspaces for usage assertions: one running
// (small: 2 cpu / 4Gi / 10Gi), one stopped and one provisioning (tiny:
// 250m / 512Mi / 5Gi each).
func usageWorkspaces(user string) []*devwsv1alpha1.Workspace {
	running := &devwsv1alpha1.Workspace{}
	running.Name = "main"
	running.Namespace = "devws-" + user
	running.Spec.UserRef = user
	running.Spec.Class = "small"
	running.Status.Phase = devwsv1alpha1.WorkspacePhaseRunning

	stopped := &devwsv1alpha1.Workspace{}
	stopped.Name = "side"
	stopped.Namespace = "devws-" + user
	stopped.Spec.UserRef = user
	stopped.Spec.Class = "tiny"
	stopped.Status.Phase = devwsv1alpha1.WorkspacePhaseStopped

	provisioning := &devwsv1alpha1.Workspace{}
	provisioning.Name = "next"
	provisioning.Namespace = "devws-" + user
	provisioning.Spec.UserRef = user
	provisioning.Spec.Class = "tiny"
	provisioning.Status.Phase = devwsv1alpha1.WorkspacePhaseProvisioning

	return []*devwsv1alpha1.Workspace{running, stopped, provisioning}
}

func TestAdminUsageQuotas(t *testing.T) {
	admin := testUser("root", "password123", "admin")
	bob := testUser("bob", "password123", "user")
	// Partial quotas: only maxWorkspaces set; the rest stay unset.
	bob.Spec.Quotas.MaxWorkspaces = 2
	ws := usageWorkspaces("bob")
	srv := newTestServer(t, admin, bob, ws[0], ws[1], ws[2])
	client := loginClient(t, srv, "root", "password123")

	var usage struct {
		Users      int            `json:"users"`
		Workspaces int            `json:"workspaces"`
		Running    int            `json:"running"`
		PerUser    []userUsageRow `json:"perUser"`
	}
	resp := doJSON(t, client, "GET", srv.URL+"/api/admin/usage", nil, &usage)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("usage: %d", resp.StatusCode)
	}
	if usage.Users != 2 || usage.Workspaces != 3 || usage.Running != 1 {
		t.Fatalf("totals wrong: %+v", usage)
	}
	if len(usage.PerUser) != 2 {
		t.Fatalf("expected a row per user (incl. zero-workspace users), got %+v", usage.PerUser)
	}
	var bobRow *userUsageRow
	for i := range usage.PerUser {
		if usage.PerUser[i].Username == "bob" {
			bobRow = &usage.PerUser[i]
		}
	}
	if bobRow == nil {
		t.Fatal("no row for bob")
	}
	// CPU/memory count live pods only (small 2 + tiny 250m provisioning);
	// storage counts all workspaces (10Gi + 5Gi + 5Gi).
	if bobRow.Workspaces != 3 || bobRow.Running != 1 {
		t.Fatalf("bob workspaces/running wrong: %+v", bobRow)
	}
	if bobRow.CPU != "2250m" || bobRow.Memory != "4608Mi" || bobRow.Storage != "20Gi" {
		t.Fatalf("bob usage wrong: %+v", bobRow)
	}
	if bobRow.MaxWorkspaces != 2 {
		t.Fatalf("bob maxWorkspaces must come from his quota: %+v", bobRow)
	}
	if bobRow.CPUQuota != "" || bobRow.MemoryQuota != "" || bobRow.StorageQuota != "" {
		t.Fatalf("unset quota fields must stay empty: %+v", bobRow)
	}
}

func TestUserViewIncludesUsage(t *testing.T) {
	bob := testUser("bob", "password123", "user")
	ws := usageWorkspaces("bob")
	srv := newTestServer(t, bob, ws[0], ws[1], ws[2])
	client := loginClient(t, srv, "bob", "password123")

	var v userView
	resp := doJSON(t, client, "GET", srv.URL+"/api/users/bob", nil, &v)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get user: %d", resp.StatusCode)
	}
	if v.Usage == nil {
		t.Fatal("own user view must include usage")
	}
	if v.Usage.Workspaces != 3 || v.Usage.Running != 1 || v.WorkspaceCount != 3 {
		t.Fatalf("usage wrong: %+v", v.Usage)
	}
	if v.Usage.Storage != "20Gi" || v.Usage.CPU != "2250m" {
		t.Fatalf("usage resources wrong: %+v", v.Usage)
	}
}

func TestAdminUsageDeniedForUser(t *testing.T) {
	srv := newTestServer(t, testUser("bob", "password123", "user"))
	client := loginClient(t, srv, "bob", "password123")
	resp := doJSON(t, client, "GET", srv.URL+"/api/admin/usage", nil, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-admin usage must be 403, got %d", resp.StatusCode)
	}
}