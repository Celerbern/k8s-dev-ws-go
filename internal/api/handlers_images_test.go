package api

import (
	"net/http"
	"testing"

	devwsv1alpha1 "github.com/celerbern/k8s-dev-ws-go/api/v1alpha1"
)

// catalogImage builds a catalog entry for tests.
func catalogImage(name, image string, deprecated bool) *devwsv1alpha1.WorkspaceImage {
	wi := &devwsv1alpha1.WorkspaceImage{}
	wi.Name = name
	wi.Spec.Image = image
	wi.Spec.Description = name + " toolchain"
	wi.Spec.Deprecated = deprecated
	return wi
}

// ----------------------------------------------------------------- authz --

func TestCatalogCRUDRequiresAdmin(t *testing.T) {
	admin := testUser("root", "password123", "admin")
	user := testUser("alice", "password123", "user")
	seed := catalogImage("python", "ghcr.io/acme/devws-python:3.12", false)
	srv := newTestServer(t, admin, user, seed)
	userClient := loginClient(t, srv, "alice", "password123")

	resp := doJSON(t, userClient, "POST", srv.URL+"/api/workspace-images",
		map[string]string{"name": "node", "image": "ghcr.io/acme/devws-node:20"}, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-admin create must be 403, got %d", resp.StatusCode)
	}
	resp = doJSON(t, userClient, "PATCH", srv.URL+"/api/workspace-images/python",
		map[string]any{"deprecated": true}, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-admin patch must be 403, got %d", resp.StatusCode)
	}
	resp = doJSON(t, userClient, "DELETE", srv.URL+"/api/workspace-images/python", nil, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-admin delete must be 403, got %d", resp.StatusCode)
	}

	// Listing is open to any authenticated user (it feeds the picker).
	var list []*workspaceImageView
	resp = doJSON(t, userClient, "GET", srv.URL+"/api/workspace-images", nil, &list)
	if resp.StatusCode != http.StatusOK || len(list) != 1 {
		t.Fatalf("non-admin list: %d, %d entries", resp.StatusCode, len(list))
	}

	// Admins may do all three.
	adminClient := loginClient(t, srv, "root", "password123")
	var created *workspaceImageView
	resp = doJSON(t, adminClient, "POST", srv.URL+"/api/workspace-images",
		map[string]string{"name": "node", "image": "ghcr.io/acme/devws-node:20"}, &created)
	if resp.StatusCode != http.StatusCreated || created.Name != "node" {
		t.Fatalf("admin create: %d %+v", resp.StatusCode, created)
	}
	resp = doJSON(t, adminClient, "PATCH", srv.URL+"/api/workspace-images/node",
		map[string]any{"deprecated": true}, &created)
	if resp.StatusCode != http.StatusOK || !created.Deprecated {
		t.Fatalf("admin patch: %d %+v", resp.StatusCode, created)
	}
	resp = doJSON(t, adminClient, "DELETE", srv.URL+"/api/workspace-images/node", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin delete: %d", resp.StatusCode)
	}
}

func TestCatalogListUsedByOnlyForAdmins(t *testing.T) {
	admin := testUser("root", "password123", "admin")
	user := testUser("alice", "password123", "user")
	seed := catalogImage("python", "ghcr.io/acme/devws-python:3.12", false)
	ws := &devwsv1alpha1.Workspace{}
	ws.Name = "main"
	ws.Namespace = "devws-alice"
	ws.Spec.UserRef = "alice"
	ws.Spec.Image = "ghcr.io/acme/devws-python:3.12"
	srv := newTestServer(t, admin, user, seed, ws)

	var list []*workspaceImageView
	resp := doJSON(t, loginClient(t, srv, "alice", "password123"),
		"GET", srv.URL+"/api/workspace-images", nil, &list)
	if resp.StatusCode != http.StatusOK || len(list) != 1 {
		t.Fatalf("user list: %d, %d entries", resp.StatusCode, len(list))
	}
	if list[0].UsedBy != 0 {
		t.Fatalf("regular users must not see usedBy counts, got %d", list[0].UsedBy)
	}

	list = nil
	resp = doJSON(t, loginClient(t, srv, "root", "password123"),
		"GET", srv.URL+"/api/workspace-images", nil, &list)
	if resp.StatusCode != http.StatusOK || len(list) != 1 {
		t.Fatalf("admin list: %d, %d entries", resp.StatusCode, len(list))
	}
	if list[0].UsedBy != 1 {
		t.Fatalf("admin must see usedBy=1, got %d", list[0].UsedBy)
	}
}

// ------------------------------------------------------------ validation --

func TestCatalogValidation(t *testing.T) {
	admin := testUser("root", "password123", "admin")
	seed := catalogImage("python", "ghcr.io/acme/devws-python:3.12", false)
	srv := newTestServer(t, admin, seed)
	client := loginClient(t, srv, "root", "password123")

	cases := []struct {
		name string
		body map[string]any
		want int
	}{
		{"bad name", map[string]any{"name": "Bad_Name", "image": "reg.io/a/b:1"}, http.StatusBadRequest},
		{"name too long", map[string]any{"name": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "image": "reg.io/a/b:1"}, http.StatusBadRequest},
		{"missing image", map[string]any{"name": "x1", "image": ""}, http.StatusBadRequest},
		{"no tag", map[string]any{"name": "x1", "image": "ubuntu"}, http.StatusBadRequest},
		{"registry port is not a tag", map[string]any{"name": "x1", "image": "localhost:5000/myimage"}, http.StatusBadRequest},
		{"spaces", map[string]any{"name": "x1", "image": "reg.io/a/b:1 evil"}, http.StatusBadRequest},
		{"uppercase", map[string]any{"name": "x1", "image": "reg.io/A:1"}, http.StatusBadRequest},
		{"duplicate name", map[string]any{"name": "python", "image": "reg.io/other:1"}, http.StatusConflict},
		{"duplicate image", map[string]any{"name": "other", "image": "ghcr.io/acme/devws-python:3.12"}, http.StatusConflict},
		{"tag joined", map[string]any{"name": "x1", "image": "ghcr.io/acme/devws-node", "tag": "20"}, http.StatusCreated},
		{"explicit tag kept", map[string]any{"name": "x2", "image": "ghcr.io/acme/devws-node:22", "tag": "20"}, http.StatusCreated},
		{"digest ok", map[string]any{"name": "x3", "image": "ghcr.io/acme/img@sha256:abc123"}, http.StatusCreated},
	}
	for _, tc := range cases {
		resp := doJSON(t, client, "POST", srv.URL+"/api/workspace-images", tc.body, nil)
		if resp.StatusCode != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, resp.StatusCode, tc.want)
		}
	}

	// "tag joined" must have stored the combined reference.
	var list []*workspaceImageView
	resp := doJSON(t, client, "GET", srv.URL+"/api/workspace-images", nil, &list)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list: %d", resp.StatusCode)
	}
	for _, img := range list {
		if img.Name == "x1" && img.Image != "ghcr.io/acme/devws-node:20" {
			t.Fatalf("tag must be joined onto the image, got %q", img.Image)
		}
	}
}

func TestCatalogPatchValidation(t *testing.T) {
	admin := testUser("root", "password123", "admin")
	other := catalogImage("node", "ghcr.io/acme/devws-node:20", false)
	seed := catalogImage("python", "ghcr.io/acme/devws-python:3.12", false)
	srv := newTestServer(t, admin, seed, other)
	client := loginClient(t, srv, "root", "password123")

	cases := []struct {
		name string
		body map[string]any
		want int
	}{
		{"untagged image", map[string]any{"image": "ubuntu"}, http.StatusBadRequest},
		{"duplicate image", map[string]any{"image": "ghcr.io/acme/devws-node:20"}, http.StatusConflict},
		{"same own image ok", map[string]any{"image": "ghcr.io/acme/devws-python:3.13"}, http.StatusOK},
	}
	for _, tc := range cases {
		resp := doJSON(t, client, "PATCH", srv.URL+"/api/workspace-images/python", tc.body, nil)
		if resp.StatusCode != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, resp.StatusCode, tc.want)
		}
	}

	resp := doJSON(t, client, "PATCH", srv.URL+"/api/workspace-images/missing",
		map[string]any{"deprecated": true}, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("patch missing: got %d", resp.StatusCode)
	}
	resp = doJSON(t, client, "DELETE", srv.URL+"/api/workspace-images/missing", nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("delete missing: got %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------- ws policy --

// TestWorkspaceImagePolicy walks the create/patch matrix: empty image =
// platform default; catalog images for regular users (deprecated ones
// excluded); anything well-formed for admins.
func TestWorkspaceImagePolicy(t *testing.T) {
	admin := testUser("root", "password123", "admin")
	user := testUser("alice", "password123", "user")
	offered := catalogImage("python", "ghcr.io/acme/devws-python:3.12", false)
	old := catalogImage("legacy", "ghcr.io/acme/devws-legacy:1.0", true)
	srv := newTestServer(t, admin, user, offered, old)
	userClient := loginClient(t, srv, "alice", "password123")
	adminClient := loginClient(t, srv, "root", "password123")

	create := func(client *http.Client, name, image string) int {
		body := map[string]any{"name": name}
		if image != "" {
			body["image"] = image
		}
		return doJSON(t, client, "POST", srv.URL+"/api/workspaces", body, nil).StatusCode
	}

	if code := create(userClient, "ok-empty", ""); code != http.StatusCreated {
		t.Fatalf("user + default image: %d", code)
	}
	if code := create(userClient, "ok-catalog", "ghcr.io/acme/devws-python:3.12"); code != http.StatusCreated {
		t.Fatalf("user + catalog image: %d", code)
	}
	if code := create(userClient, "bad-unknown", "ghcr.io/acme/not-in-catalog:1"); code != http.StatusBadRequest {
		t.Fatalf("user + unknown image must be 400, got %d", code)
	}
	if code := create(userClient, "bad-deprecated", "ghcr.io/acme/devws-legacy:1.0"); code != http.StatusBadRequest {
		t.Fatalf("user + deprecated image must be 400, got %d", code)
	}
	if code := create(adminClient, "ok-admin", "ghcr.io/acme/anything:latest"); code != http.StatusCreated {
		t.Fatalf("admin + arbitrary image: %d", code)
	}
	if code := create(adminClient, "bad-admin", "ubuntu"); code != http.StatusBadRequest {
		t.Fatalf("admin + untagged image must be 400, got %d", code)
	}

	// The policy follows the caller, not the target: an admin setting a
	// non-catalog image on a regular user's workspace is allowed.
	resp := doJSON(t, adminClient, "POST", srv.URL+"/api/workspaces",
		map[string]any{"name": "for-alice", "user": "alice", "image": "ghcr.io/acme/custom:1"}, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("admin creating for user with custom image: %d", resp.StatusCode)
	}
}

func TestPatchWorkspaceImagePolicy(t *testing.T) {
	admin := testUser("root", "password123", "admin")
	user := testUser("alice", "password123", "user")
	offered := catalogImage("python", "ghcr.io/acme/devws-python:3.12", false)
	ws := &devwsv1alpha1.Workspace{}
	ws.Name = "main"
	ws.Namespace = "devws-alice"
	ws.Spec.UserRef = "alice"
	srv := newTestServer(t, admin, user, offered, ws)
	userClient := loginClient(t, srv, "alice", "password123")
	adminClient := loginClient(t, srv, "root", "password123")

	patch := func(client *http.Client, image string) (*workspaceView, int) {
		var out workspaceView
		resp := doJSON(t, client, "PATCH", srv.URL+"/api/workspaces/main",
			map[string]any{"image": image}, &out)
		return &out, resp.StatusCode
	}

	if _, code := patch(userClient, "ghcr.io/acme/devws-python:3.12"); code != http.StatusOK {
		t.Fatalf("user patch to catalog image: %d", code)
	}
	if _, code := patch(userClient, "ghcr.io/acme/other:1"); code != http.StatusBadRequest {
		t.Fatalf("user patch to unknown image must be 400, got %d", code)
	}
	// Empty string clears the override back to the platform default.
	out, code := patch(userClient, "")
	if code != http.StatusOK || out.Image != "" {
		t.Fatalf("user patch clearing image: %d, image %q", code, out.Image)
	}
	// The policy follows the caller: an admin may set a non-catalog
	// image on alice's workspace (scope ?user=alice).
	var adminOut workspaceView
	resp := doJSON(t, adminClient, "PATCH", srv.URL+"/api/workspaces/main?user=alice",
		map[string]any{"image": "ghcr.io/acme/custom:1"}, &adminOut)
	if resp.StatusCode != http.StatusOK || adminOut.Image != "ghcr.io/acme/custom:1" {
		t.Fatalf("admin patch to arbitrary image: %d %+v", resp.StatusCode, adminOut)
	}
}
