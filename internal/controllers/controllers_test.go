package controllers

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	devwsv1alpha1 "github.com/celerbern/k8s-dev-ws-go/api/v1alpha1"
	"github.com/celerbern/k8s-dev-ws-go/internal/config"
)

var ctrlScheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(ctrlScheme))
	utilruntime.Must(networkingv1.AddToScheme(ctrlScheme))
	utilruntime.Must(devwsv1alpha1.AddToScheme(ctrlScheme))
}

func newUserReconciler(t *testing.T, objects ...runtime.Object) *UserReconciler {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(ctrlScheme).
		WithStatusSubresource(&devwsv1alpha1.User{}, &devwsv1alpha1.Workspace{}).
		WithRuntimeObjects(objects...).
		Build()
	return &UserReconciler{
		Client:            c,
		Scheme:            ctrlScheme,
		PlatformNamespace: "devws-system",
	}
}

func TestUserReconcileCreatesNamespace(t *testing.T) {
	user := &devwsv1alpha1.User{}
	user.Name = "alice"
	r := newUserReconciler(t, user)

	// First pass adds the finalizer, second pass creates the namespace.
	for i := 0; i < 3; i++ {
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "alice"}}); err != nil {
			t.Fatalf("reconcile %d: %v", i, err)
		}
	}

	ns := &corev1.Namespace{}
	if err := r.Get(context.Background(), types.NamespacedName{Name: "devws-alice"}, ns); err != nil {
		t.Fatalf("namespace not created: %v", err)
	}
	if ns.Labels[LabelUser] != "alice" || ns.Labels[LabelManaged] != "true" {
		t.Fatalf("namespace labels wrong: %v", ns.Labels)
	}

	rq := &corev1.ResourceQuota{}
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "devws-alice", Name: "devws-user-quota"}, rq); err != nil {
		t.Fatalf("resource quota not created: %v", err)
	}
	if _, ok := rq.Spec.Hard[corev1.ResourceRequestsCPU]; !ok {
		t.Fatalf("default quotas must be applied: %v", rq.Spec.Hard)
	}

	np := &networkingv1.NetworkPolicy{}
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "devws-alice", Name: "devws-default"}, np); err != nil {
		t.Fatalf("network policy not created: %v", err)
	}
}

func TestUserReconcileDeletesNamespaceOnDelete(t *testing.T) {
	user := &devwsv1alpha1.User{}
	user.Name = "alice"
	r := newUserReconciler(t, user)

	for i := 0; i < 3; i++ {
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "alice"}}); err != nil {
			t.Fatal(err)
		}
	}

	// Delete the user; the namespace should be deleted too.
	if err := r.Delete(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "alice"}}); err != nil {
			t.Fatal(err)
		}
	}
	err := r.Get(context.Background(), types.NamespacedName{Name: "devws-alice"}, &corev1.Namespace{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("namespace must be deleted with the user, got %v", err)
	}
}

func newWorkspaceReconciler(t *testing.T, objects ...runtime.Object) *WorkspaceReconciler {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(ctrlScheme).
		WithStatusSubresource(&devwsv1alpha1.User{}, &devwsv1alpha1.Workspace{}).
		WithRuntimeObjects(objects...).
		Build()
	return &WorkspaceReconciler{
		Client: c,
		Scheme: ctrlScheme,
		Config: config.Default(),
	}
}

func TestWorkspaceReconcileCreatesResources(t *testing.T) {
	user := &devwsv1alpha1.User{}
	user.Name = "alice"
	user.Spec.SSHPublicKeys = []devwsv1alpha1.SSHKey{{Name: "k", Key: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI dummy test key"}}
	ws := &devwsv1alpha1.Workspace{}
	ws.Name = "main"
	ws.Namespace = "devws-alice"
	ws.Spec.UserRef = "alice"
	r := newWorkspaceReconciler(t, user, ws)

	// Several passes: create pod -> status from (missing) pod -> running.
	for i := 0; i < 4; i++ {
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "devws-alice", Name: "main"}}); err != nil {
			t.Fatalf("reconcile %d: %v", i, err)
		}
	}

	ctx := context.Background()
	pvc := &corev1.PersistentVolumeClaim{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: "devws-alice", Name: "main-home"}, pvc); err != nil {
		t.Fatalf("pvc not created: %v", err)
	}
	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: "devws-alice", Name: "main-ssh"}, secret); err != nil {
		t.Fatalf("ssh secret not created: %v", err)
	}
	if string(secret.Data["authorized_keys"]) == "" {
		t.Fatal("authorized_keys must contain the user key")
	}
	pod := &corev1.Pod{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: "devws-alice", Name: "main"}, pod); err != nil {
		t.Fatalf("pod not created: %v", err)
	}
	if pod.Labels[LabelWorkspace] != "main" {
		t.Fatalf("pod labels wrong: %v", pod.Labels)
	}
	if len(pod.Spec.Containers) != 1 || pod.Spec.Containers[0].Image != config.Default().WorkspaceImage {
		t.Fatalf("pod container wrong: %+v", pod.Spec.Containers)
	}

	// Mark the pod Running+Ready and reconcile again for the status.
	pod.Status.Phase = corev1.PodRunning
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	if err := r.Status().Update(ctx, pod); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "devws-alice", Name: "main"}}); err != nil {
		t.Fatal(err)
	}
	updated := &devwsv1alpha1.Workspace{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: "devws-alice", Name: "main"}, updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.Phase != devwsv1alpha1.WorkspacePhaseRunning {
		t.Fatalf("phase must be Running, got %q", updated.Status.Phase)
	}
	if updated.Status.SSHHost != "main.devws-alice.ws.internal" {
		t.Fatalf("ssh host wrong: %q", updated.Status.SSHHost)
	}
}

func TestWorkspacePausedDeletesPod(t *testing.T) {
	user := &devwsv1alpha1.User{}
	user.Name = "alice"
	ws := &devwsv1alpha1.Workspace{}
	ws.Name = "main"
	ws.Namespace = "devws-alice"
	ws.Spec.UserRef = "alice"
	ws.Spec.Paused = true
	r := newWorkspaceReconciler(t, user, ws)

	pod := &corev1.Pod{}
	pod.Name = "main"
	pod.Namespace = "devws-alice"
	if err := r.Create(context.Background(), pod); err != nil {
		t.Fatal(err)
	}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "devws-alice", Name: "main"}}); err != nil {
		t.Fatal(err)
	}
	err := r.Get(context.Background(), types.NamespacedName{Namespace: "devws-alice", Name: "main"}, &corev1.Pod{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("paused workspace must have no pod, got %v", err)
	}
	updated := &devwsv1alpha1.Workspace{}
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "devws-alice", Name: "main"}, updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.Phase != devwsv1alpha1.WorkspacePhaseStopped {
		t.Fatalf("paused workspace must be Stopped, got %q", updated.Status.Phase)
	}
}

func TestWorkspaceDisabledUserStopsPod(t *testing.T) {
	user := &devwsv1alpha1.User{}
	user.Name = "alice"
	user.Spec.Disabled = true
	ws := &devwsv1alpha1.Workspace{}
	ws.Name = "main"
	ws.Namespace = "devws-alice"
	ws.Spec.UserRef = "alice"
	r := newWorkspaceReconciler(t, user, ws)

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "devws-alice", Name: "main"}}); err != nil {
		t.Fatal(err)
	}
	err := r.Get(context.Background(), types.NamespacedName{Namespace: "devws-alice", Name: "main"}, &corev1.Pod{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("disabled user must not have pods, got %v", err)
	}
}
