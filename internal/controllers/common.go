// Package controllers implements the devws operators: the User controller
// (per-user namespaces, quotas, network policies) and the Workspace
// controller (pods, PVCs, ssh secrets).
package controllers

import (
	"context"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// LabelManaged marks objects created and owned by devws.
	LabelManaged = "devws.io/managed"
	// LabelUser marks objects belonging to a specific user.
	LabelUser = "devws.io/user"
	// LabelWorkspace marks objects belonging to a specific workspace.
	LabelWorkspace = "devws.io/workspace"
	// UserFinalizer ensures user namespaces are cleaned up on deletion.
	UserFinalizer = "devws.io/cleanup"
	// WorkspaceSSHPort is the port sshd listens on inside workspace pods.
	WorkspaceSSHPort = 2222
	// SSHDomainSuffix is the synthetic domain the SSH gateway resolves.
	SSHDomainSuffix = ".ws.internal"
	// WorkspaceUID is the uid of the in-pod development user.
	WorkspaceUID = 1000
	// WorkspaceUser is the in-pod development user name.
	WorkspaceUser = "devws"
	// WorkspaceHome is the persistent home directory of the dev user.
	WorkspaceHome = "/home/devws"
)

// NamespaceFor returns the per-user namespace for a username.
func NamespaceFor(username string) string {
	return "devws-" + username
}

// managedLabels returns the standard labels for objects owned by devws.
func managedLabels(extra map[string]string) map[string]string {
	labels := map[string]string{LabelManaged: "true"}
	for k, v := range extra {
		labels[k] = v
	}
	return labels
}

// createOrUpdate creates obj if missing, or updates it when the existing
// object differs from the desired one. The desired object must be fully
// populated; comparison uses semantic equality of the whole object (ignoring
// metadata managed by the system).
func createOrUpdate(ctx context.Context, c client.Client, desired client.Object) error {
	existing := desired.DeepCopyObject().(client.Object)
	if err := c.Get(ctx, client.ObjectKeyFromObject(desired), existing); err != nil {
		if apierrors.IsNotFound(err) {
			return c.Create(ctx, desired)
		}
		return err
	}
	// Keep server-managed metadata, then overwrite with desired state.
	desired.SetResourceVersion(existing.GetResourceVersion())
	desired.SetUID(existing.GetUID())
	desired.SetCreationTimestamp(existing.GetCreationTimestamp())
	return c.Update(ctx, desired)
}

// objectChanged reports whether two objects of the same type differ in a way
// we care about (used to skip no-op status updates).
func statusChanged(a, b interface{}) bool {
	return !equality.Semantic.DeepEqual(a, b)
}
