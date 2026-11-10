package controllers

import (
	"context"
	"fmt"
	"strings"
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
	// PodSecurityAdmission: privileged enforcement for dev tooling, but
	// audited and warned at baseline (see docs/security.md).
	for label, want := range map[string]string{
		"pod-security.kubernetes.io/enforce": "privileged",
		"pod-security.kubernetes.io/audit":   "baseline",
		"pod-security.kubernetes.io/warn":    "baseline",
	} {
		if ns.Labels[label] != want {
			t.Fatalf("psa label %s = %q, want %q", label, ns.Labels[label], want)
		}
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
	if pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken != false {
		t.Fatal("workspace pods must not automount a service account token")
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

// fakeRecorder collects controller events.
type fakeRecorder struct {
	events []string
}

func (f *fakeRecorder) Eventf(_ runtime.Object, eventtype, reason, messageFmt string, args ...interface{}) {
	f.events = append(f.events, eventtype+"/"+reason+": "+fmt.Sprintf(messageFmt, args...))
}

func reconcileNTimes(t *testing.T, r *WorkspaceReconciler, ns, name string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}}); err != nil {
			t.Fatalf("reconcile %d: %v", i, err)
		}
	}
}

func TestGitCloneContainerAndAnnotation(t *testing.T) {
	user := &devwsv1alpha1.User{}
	user.Name = "alice"
	ws := &devwsv1alpha1.Workspace{}
	ws.Name = "main"
	ws.Namespace = "devws-alice"
	ws.Spec.UserRef = "alice"
	ws.Spec.GitRepos = []string{"https://example.com/repo.git"}
	r := newWorkspaceReconciler(t, user, ws)
	reconcileNTimes(t, r, "devws-alice", "main", 3)

	pod := &corev1.Pod{}
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "devws-alice", Name: "main"}, pod); err != nil {
		t.Fatalf("pod not created: %v", err)
	}
	found := false
	for _, c := range pod.Spec.InitContainers {
		if c.Name == "git-clone" {
			found = true
		}
	}
	if !found {
		t.Fatal("git-clone init container must exist when gitRepos are set")
	}
	if want := gitReposHash(ws.Spec.GitRepos); pod.Annotations[gitReposAnnotation] != want {
		t.Fatalf("pod must carry the git repos hash %q, got %q", want, pod.Annotations[gitReposAnnotation])
	}
}

func TestGitCloneFailureFailsWorkspace(t *testing.T) {
	user := &devwsv1alpha1.User{}
	user.Name = "alice"
	ws := &devwsv1alpha1.Workspace{}
	ws.Name = "main"
	ws.Namespace = "devws-alice"
	ws.Spec.UserRef = "alice"
	ws.Spec.GitRepos = []string{"https://example.com/repo.git"}
	rec := &fakeRecorder{}
	r := newWorkspaceReconciler(t, user, ws)
	r.Recorder = rec
	reconcileNTimes(t, r, "devws-alice", "main", 3)

	ctx := context.Background()
	pod := &corev1.Pod{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: "devws-alice", Name: "main"}, pod); err != nil {
		t.Fatal(err)
	}
	// Simulate the git-clone init container failing. Under the pod's
	// default restart policy the kubelet crash-loops it while the pod
	// itself stays Pending — the controller must still surface Failed.
	pod.Status.Phase = corev1.PodPending
	pod.Status.InitContainerStatuses = []corev1.ContainerStatus{{
		Name:  "git-clone",
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 128}},
	}}
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
	if updated.Status.Phase != devwsv1alpha1.WorkspacePhaseFailed {
		t.Fatalf("failed clone must fail the workspace, got %q", updated.Status.Phase)
	}
	if !strings.Contains(updated.Status.Message, "git clone failed") {
		t.Fatalf("message must point at the clone failure, got %q", updated.Status.Message)
	}
	warned := false
	for _, e := range rec.events {
		if strings.HasPrefix(e, "Warning/WorkspaceFailed:") && strings.Contains(e, "git clone failed") {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("a warning event must be recorded on the Failed transition, got %v", rec.events)
	}

	// The crash-looping waiting state must be detected as well.
	pod.Status.InitContainerStatuses = []corev1.ContainerStatus{{
		Name:  "git-clone",
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
	}}
	if err := r.Status().Update(ctx, pod); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "devws-alice", Name: "main"}}); err != nil {
		t.Fatal(err)
	}
	updated = &devwsv1alpha1.Workspace{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: "devws-alice", Name: "main"}, updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.Phase != devwsv1alpha1.WorkspacePhaseFailed {
		t.Fatalf("crash-looping init must keep the workspace Failed, got %q", updated.Status.Phase)
	}
	if !strings.Contains(updated.Status.Message, "git clone failed") {
		t.Fatalf("crash-loop message must point at the clone failure, got %q", updated.Status.Message)
	}
}

func TestGitReposChangeRecreatesPod(t *testing.T) {
	user := &devwsv1alpha1.User{}
	user.Name = "alice"
	ws := &devwsv1alpha1.Workspace{}
	ws.Name = "main"
	ws.Namespace = "devws-alice"
	ws.Spec.UserRef = "alice"
	ws.Spec.GitRepos = []string{"https://example.com/a.git"}
	r := newWorkspaceReconciler(t, user, ws)
	reconcileNTimes(t, r, "devws-alice", "main", 3)

	ctx := context.Background()
	pod := &corev1.Pod{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: "devws-alice", Name: "main"}, pod); err != nil {
		t.Fatal(err)
	}
	if got := pod.Annotations[gitReposAnnotation]; got != gitReposHash(ws.Spec.GitRepos) {
		t.Fatalf("initial hash wrong: %q", got)
	}

	// Add a repo; the pod must be recreated with the new hash.
	fresh := &devwsv1alpha1.Workspace{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: "devws-alice", Name: "main"}, fresh); err != nil {
		t.Fatal(err)
	}
	fresh.Spec.GitRepos = []string{"https://example.com/a.git", "https://example.com/b.git"}
	if err := r.Update(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	reconcileNTimes(t, r, "devws-alice", "main", 3)

	pod = &corev1.Pod{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: "devws-alice", Name: "main"}, pod); err != nil {
		t.Fatalf("pod must be recreated after gitRepos change: %v", err)
	}
	if want := gitReposHash(fresh.Spec.GitRepos); pod.Annotations[gitReposAnnotation] != want {
		t.Fatalf("pod must carry the new git repos hash %q, got %q", want, pod.Annotations[gitReposAnnotation])
	}
}
