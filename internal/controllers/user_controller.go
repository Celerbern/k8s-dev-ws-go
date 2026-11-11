package controllers

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	devwsv1alpha1 "github.com/celerbern/k8s-dev-ws-go/api/v1alpha1"
	"github.com/celerbern/k8s-dev-ws-go/internal/config"
)

// UserReconciler reconciles User objects: it maintains a namespace per user
// containing a ResourceQuota, a default NetworkPolicy and copies of the
// configured image pull secrets, and cleans the namespace up when the user
// is deleted.
type UserReconciler struct {
	client.Client
	Scheme            *runtime.Scheme
	PlatformNamespace string
	// Config carries the platform configuration (image pull secrets).
	Config *config.Config
}

// +kubebuilder:rbac:groups=devws.io,resources=users,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=devws.io,resources=users/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;create;delete;update;patch
// +kubebuilder:rbac:groups="",resources=resourcequotas,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop.
func (r *UserReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("user", req.Name)

	var user devwsv1alpha1.User
	if err := r.Get(ctx, types.NamespacedName{Name: req.Name}, &user); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Deletion: remove the user namespace first, then drop the finalizer.
	if !user.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&user, UserFinalizer) {
			ns := NamespaceFor(user.Name)
			gone, err := r.deleteNamespace(ctx, ns)
			if err != nil {
				return ctrl.Result{}, err
			}
			if !gone {
				_ = r.setStatus(ctx, &user, "Terminating", ns, "waiting for namespace to be deleted")
				return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
			}
			controllerutil.RemoveFinalizer(&user, UserFinalizer)
			if err := r.Update(ctx, &user); err != nil {
				return ctrl.Result{}, err
			}
			logger.Info("user deleted", "namespace", ns)
		}
		return ctrl.Result{}, nil
	}

	if !controllerutil.ContainsFinalizer(&user, UserFinalizer) {
		controllerutil.AddFinalizer(&user, UserFinalizer)
		if err := r.Update(ctx, &user); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	ns := NamespaceFor(user.Name)

	if err := r.ensureNamespace(ctx, &user, ns); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensuring namespace: %w", err)
	}
	if err := r.ensureResourceQuota(ctx, &user, ns); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensuring resource quota: %w", err)
	}
	if err := r.ensureNetworkPolicy(ctx, ns); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensuring network policy: %w", err)
	}
	if err := r.ensureImagePullSecrets(ctx, logger, ns); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensuring image pull secrets: %w", err)
	}

	if err := r.setStatus(ctx, &user, "Ready", ns, ""); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// deleteNamespace deletes the namespace and reports whether it is gone.
func (r *UserReconciler) deleteNamespace(ctx context.Context, name string) (bool, error) {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if err := r.Delete(ctx, ns); err != nil && !apierrors.IsNotFound(err) {
		return false, err
	}
	var check corev1.Namespace
	if err := r.Get(ctx, types.NamespacedName{Name: name}, &check); err != nil {
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	}
	return false, nil
}

// ensureNamespace creates or updates the per-user namespace.
func (r *UserReconciler) ensureNamespace(ctx context.Context, user *devwsv1alpha1.User, name string) error {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: managedLabels(map[string]string{LabelUser: user.Name}),
		},
	}
	// PodSecurityAdmission: user workspaces run arbitrary development
	// tooling (up to container-ish workloads), so enforcement opts out of
	// the pod security standards. Audit and warn stay at baseline so that
	// anything beyond it is recorded and surfaced to kubectl users; today's
	// workspace pods pass baseline. See docs/security.md for the full
	// decision record.
	ns.Labels["pod-security.kubernetes.io/enforce"] = "privileged"
	ns.Labels["pod-security.kubernetes.io/audit"] = "baseline"
	ns.Labels["pod-security.kubernetes.io/warn"] = "baseline"
	return createOrUpdate(ctx, r.Client, ns)
}

// ensureResourceQuota enforces the user's quotas at the namespace level.
func (r *UserReconciler) ensureResourceQuota(ctx context.Context, user *devwsv1alpha1.User, ns string) error {
	quotas := user.Spec.Quotas
	if quotas.MaxWorkspaces == 0 && quotas.CPU.IsZero() && quotas.Memory.IsZero() && quotas.Storage.IsZero() {
		quotas = config.DefaultUserQuotas()
	}
	hard := corev1.ResourceList{}
	if !quotas.CPU.IsZero() {
		hard[corev1.ResourceRequestsCPU] = quotas.CPU
	}
	if !quotas.Memory.IsZero() {
		hard[corev1.ResourceRequestsMemory] = quotas.Memory
	}
	if !quotas.Storage.IsZero() {
		hard[corev1.ResourceRequestsStorage] = quotas.Storage
	}
	if quotas.MaxWorkspaces > 0 {
		hard[corev1.ResourcePersistentVolumeClaims] = resource.MustParse(fmt.Sprintf("%d", quotas.MaxWorkspaces))
	}
	rq := &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "devws-user-quota",
			Namespace: ns,
			Labels:    managedLabels(nil),
		},
		Spec: corev1.ResourceQuotaSpec{Hard: hard},
	}
	return createOrUpdate(ctx, r.Client, rq)
}

// ensureNetworkPolicy isolates the namespace: ingress is only allowed from
// within the namespace and from the platform namespace (SSH gateway).
func (r *UserReconciler) ensureNetworkPolicy(ctx context.Context, ns string) error {
	all := &metav1.LabelSelector{}
	np := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "devws-default",
			Namespace: ns,
			Labels:    managedLabels(nil),
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: *all,
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{
				{
					From: []networkingv1.NetworkPolicyPeer{
						{PodSelector: all},
						{NamespaceSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								"kubernetes.io/metadata.name": r.PlatformNamespace,
							},
						}},
					},
				},
			},
		},
	}
	return createOrUpdate(ctx, r.Client, np)
}

// LabelImagePullSecret marks the secret copies the user controller syncs
// from the platform namespace; only secrets with this label are pruned.
const LabelImagePullSecret = "devws.io/image-pull-secret"

// ensureImagePullSecrets copies each configured pull secret from the
// platform namespace into the user namespace (kubelet only reads pull
// secrets from the pod's own namespace) and prunes copies of secrets that
// are no longer configured. Secrets without the marker label are never
// touched. A missing source secret is logged and skipped — pod pull
// failures will surface it.
func (r *UserReconciler) ensureImagePullSecrets(ctx context.Context, logger logr.Logger, ns string) error {
	if r.Config == nil {
		return nil
	}
	wanted := make(map[string]bool, len(r.Config.ImagePullSecrets))
	for _, name := range r.Config.ImagePullSecrets {
		wanted[name] = true
	}
	for _, name := range r.Config.ImagePullSecrets {
		src := &corev1.Secret{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: r.PlatformNamespace, Name: name}, src); err != nil {
			if apierrors.IsNotFound(err) {
				logger.Info("configured image pull secret is missing in the platform namespace",
					"secret", name, "namespace", r.PlatformNamespace)
				continue
			}
			return err
		}
		desired := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: ns,
				Labels: managedLabels(map[string]string{
					LabelImagePullSecret: "true",
				}),
			},
			Type: src.Type,
			Data: src.DeepCopy().Data,
		}
		if err := createOrUpdate(ctx, r.Client, desired); err != nil {
			return err
		}
	}
	// Prune stale copies: marker-labeled secrets no longer configured.
	var secrets corev1.SecretList
	if err := r.List(ctx, &secrets,
		client.InNamespace(ns),
		client.MatchingLabels{LabelImagePullSecret: "true"}); err != nil {
		return err
	}
	for i := range secrets.Items {
		name := secrets.Items[i].Name
		if wanted[name] {
			continue
		}
		logger.Info("pruning stale image pull secret copy", "secret", name, "namespace", ns)
		if err := r.Delete(ctx, &secrets.Items[i]); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func (r *UserReconciler) setStatus(ctx context.Context, user *devwsv1alpha1.User, phase, ns, msg string) error {
	desired := devwsv1alpha1.UserStatus{Phase: phase, Namespace: ns, Message: msg}
	if statusChanged(user.Status, desired) {
		user.Status = desired
		return r.Status().Update(ctx, user)
	}
	return nil
}

// SetupWithManager registers the controller with the manager. It also
// watches Secrets in the platform namespace: a change to a configured
// image pull secret (credential rotation) re-syncs the copies in every
// user namespace. kubelet re-reads pull secrets on each pull attempt, so
// rotated credentials apply to running pods without restarts.
func (r *UserReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&devwsv1alpha1.User{}).
		Watches(&corev1.Secret{},
			handler.EnqueueRequestsFromMapFunc(r.usersForPullSecret),
			builder.WithPredicates(predicate.NewPredicateFuncs(r.isWatchedPullSecret))).
		Complete(r)
}

// usersForPullSecret enqueues every user; the reconciler then refreshes
// the pull-secret copies in each user namespace.
func (r *UserReconciler) usersForPullSecret(ctx context.Context, obj client.Object) []reconcile.Request {
	_ = obj
	var users devwsv1alpha1.UserList
	if err := r.List(ctx, &users); err != nil {
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(users.Items))
	for i := range users.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Name: users.Items[i].Name}})
	}
	return reqs
}

// isWatchedPullSecret reports whether a secret event should re-sync
// users: the secret lives in the platform namespace and is configured as
// an image pull secret.
func (r *UserReconciler) isWatchedPullSecret(obj client.Object) bool {
	if r.Config == nil || obj.GetNamespace() != r.PlatformNamespace {
		return false
	}
	for _, name := range r.Config.ImagePullSecrets {
		if obj.GetName() == name {
			return true
		}
	}
	return false
}
