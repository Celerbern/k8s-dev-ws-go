package controllers

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	devwsv1alpha1 "github.com/celerbern/k8s-dev-ws-go/api/v1alpha1"
	"github.com/celerbern/k8s-dev-ws-go/internal/config"
)

// WorkspaceReconciler reconciles Workspace objects into a pod, a persistent
// volume claim and an ssh secret inside the owner's namespace.
type WorkspaceReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder eventRecorder
	Config   *config.Config
}

// eventRecorder matches the subset of record.EventRecorder we use.
type eventRecorder interface {
	Eventf(object runtime.Object, eventtype, reason, messageFmt string, args ...interface{})
}

// +kubebuilder:rbac:groups=devws.io,resources=workspaces,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=devws.io,resources=workspaces/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups="",resources=pods/log,verbs=get
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=get;list;create;update;patch

// Reconcile drives the workspace towards its desired state.
func (r *WorkspaceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var ws devwsv1alpha1.Workspace
	if err := r.Get(ctx, req.NamespacedName, &ws); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !ws.DeletionTimestamp.IsZero() {
		// Pod/PVC/secret carry owner references and are garbage collected.
		return ctrl.Result{}, nil
	}

	var user devwsv1alpha1.User
	if err := r.Get(ctx, types.NamespacedName{Name: ws.Spec.UserRef}, &user); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, r.fail(ctx, &ws, "owning user does not exist")
		}
		return ctrl.Result{}, err
	}

	// Stopped (explicitly paused, or the owner is disabled): keep the PVC,
	// delete the pod.
	if ws.Spec.Paused || user.Spec.Disabled {
		reason := "workspace is stopped"
		if user.Spec.Disabled {
			reason = "user is disabled"
		}
		if err := r.stop(ctx, &ws, reason); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	className := ws.Spec.Class
	if className == "" {
		className = r.Config.DefaultClass
	}
	classSpec, ok := r.Config.Class(ws.Spec.Class)
	if !ok {
		return ctrl.Result{}, r.fail(ctx, &ws, fmt.Sprintf("unknown workspace class %q", ws.Spec.Class))
	}
	image := ws.Spec.Image
	if image == "" {
		image = r.Config.WorkspaceImage
	}
	storage := ws.Spec.Storage
	if storage.IsZero() {
		storage = resource.MustParse(classSpec.Storage)
	}

	if err := r.ensurePVC(ctx, &ws, storage); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensuring pvc: %w", err)
	}
	if err := r.ensureSSHSecret(ctx, &ws, &user); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensuring ssh secret: %w", err)
	}
	pod, err := r.ensurePod(ctx, &ws, &user, image, classSpec)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("ensuring pod: %w", err)
	}

	return r.updatePhase(ctx, &ws, pod)
}

// stop deletes the workspace pod and records the stopped phase.
func (r *WorkspaceReconciler) stop(ctx context.Context, ws *devwsv1alpha1.Workspace, reason string) error {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: ws.Namespace, Name: ws.Name}}
	if err := r.Delete(ctx, pod); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	desired := devwsv1alpha1.WorkspaceStatus{
		Phase:   devwsv1alpha1.WorkspacePhaseStopped,
		Message: reason,
		SSHHost: SSHHostFor(ws.Name, ws.Namespace),
	}
	return r.patchStatus(ctx, ws, desired)
}

// fail marks the workspace Failed.
func (r *WorkspaceReconciler) fail(ctx context.Context, ws *devwsv1alpha1.Workspace, msg string) error {
	desired := ws.Status.DeepCopy()
	desired.Phase = devwsv1alpha1.WorkspacePhaseFailed
	desired.Message = msg
	if err := r.patchStatus(ctx, ws, *desired); err != nil {
		return err
	}
	if r.Recorder != nil {
		r.Recorder.Eventf(ws, corev1.EventTypeWarning, "WorkspaceFailed", "%s", msg)
	}
	return nil
}

// updatePhase derives the workspace phase from the pod state.
func (r *WorkspaceReconciler) updatePhase(ctx context.Context, ws *devwsv1alpha1.Workspace, pod *corev1.Pod) (ctrl.Result, error) {
	phase, msg := devwsv1alpha1.WorkspacePhaseProvisioning, ""
	var requeue ctrl.Result
	if pod == nil {
		msg = "workspace pod is being created"
		requeue = ctrl.Result{RequeueAfter: 2 * time.Second}
	} else {
		switch pod.Status.Phase {
		case corev1.PodRunning:
			// Ready only when the ssh port is listening.
			if podReady(pod) {
				phase = devwsv1alpha1.WorkspacePhaseRunning
			} else {
				msg = "waiting for ssh server to become ready"
				requeue = ctrl.Result{RequeueAfter: 2 * time.Second}
			}
		case corev1.PodPending:
			msg = podBlockedMessage(pod)
			if msg == "" {
				msg = "waiting for the workspace pod to be scheduled"
			}
			// A failed init container (e.g. git-clone) leaves the pod
			// Pending forever under the default restart policy — the
			// kubelet just crash-loops it. Surface that as Failed; the
			// status returns to Provisioning/Running on its own if a
			// retry eventually succeeds.
			if initContainerFailed(pod) {
				phase = devwsv1alpha1.WorkspacePhaseFailed
			}
			requeue = ctrl.Result{RequeueAfter: 3 * time.Second}
		case corev1.PodFailed:
			phase = devwsv1alpha1.WorkspacePhaseFailed
			msg = podBlockedMessage(pod)
		case corev1.PodSucceeded:
			phase = devwsv1alpha1.WorkspacePhaseFailed
			msg = "workspace container exited"
		}
	}
	desired := devwsv1alpha1.WorkspaceStatus{
		Phase:   phase,
		Message: msg,
		PodName: ws.Name,
		SSHHost: SSHHostFor(ws.Name, ws.Namespace),
	}
	if pod != nil {
		desired.PodName = pod.Name
	}
	prevPhase := ws.Status.Phase
	if err := r.patchStatus(ctx, ws, desired); err != nil {
		return ctrl.Result{}, err
	}
	// Announce failures loudly: a warning event on the transition into
	// Failed (image pull errors, git clone failures, crashed containers).
	if phase == devwsv1alpha1.WorkspacePhaseFailed && prevPhase != phase && r.Recorder != nil {
		r.Recorder.Eventf(ws, corev1.EventTypeWarning, "WorkspaceFailed", "%s", msg)
	}
	return requeue, nil
}

func podReady(pod *corev1.Pod) bool {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// initContainerFailed reports whether an init container has failed
// (terminated non-zero or crash-looping). Under the pod's default
// restart policy such a pod stays Pending forever.
func initContainerFailed(pod *corev1.Pod) bool {
	for _, s := range pod.Status.InitContainerStatuses {
		if s.State.Terminated != nil && s.State.Terminated.ExitCode != 0 {
			return true
		}
		if s.State.Waiting != nil && s.State.Waiting.Reason == "CrashLoopBackOff" {
			return true
		}
	}
	return false
}

// initFailureMessage renders a human-readable reason for a failed init
// container.
func initFailureMessage(name string, exitCode int32) string {
	if name == "git-clone" {
		if exitCode != 0 {
			return fmt.Sprintf("git clone failed (exit code %d) — check workspace logs, container git-clone", exitCode)
		}
		return "git clone failed — check workspace logs, container git-clone"
	}
	if exitCode != 0 {
		return fmt.Sprintf("init %q exited with code %d", name, exitCode)
	}
	return fmt.Sprintf("init %q is failing (crash loop)", name)
}

// podBlockedMessage extracts a human-readable reason for a stuck pod.
func podBlockedMessage(pod *corev1.Pod) string {
	for _, s := range pod.Status.InitContainerStatuses {
		if s.State.Waiting != nil && s.State.Waiting.Reason == "CrashLoopBackOff" {
			return initFailureMessage(s.Name, 0)
		}
		if s.State.Waiting != nil && s.State.Waiting.Reason != "" && s.State.Waiting.Reason != "PodInitializing" {
			return fmt.Sprintf("init: %s: %s", s.State.Waiting.Reason, s.State.Waiting.Message)
		}
		if s.State.Terminated != nil && s.State.Terminated.ExitCode != 0 {
			return initFailureMessage(s.Name, s.State.Terminated.ExitCode)
		}
	}
	for _, s := range pod.Status.ContainerStatuses {
		if s.State.Waiting != nil && s.State.Waiting.Reason != "" && s.State.Waiting.Reason != "ContainerCreating" {
			return fmt.Sprintf("%s: %s", s.State.Waiting.Reason, s.State.Waiting.Message)
		}
		if s.State.Terminated != nil && s.State.Terminated.ExitCode != 0 {
			return fmt.Sprintf("container exited with code %d", s.State.Terminated.ExitCode)
		}
	}
	if len(pod.Status.ContainerStatuses) == 0 && pod.Status.Phase == corev1.PodPending {
		return "waiting for container to be created (pulling image?)"
	}
	return ""
}

// SSHHostFor returns the synthetic hostname routed by the SSH gateway.
func SSHHostFor(name, namespace string) string {
	return fmt.Sprintf("%s.%s%s", name, namespace, SSHDomainSuffix)
}

// ensurePVC creates or grows the workspace home volume.
func (r *WorkspaceReconciler) ensurePVC(ctx context.Context, ws *devwsv1alpha1.Workspace, storage resource.Quantity) error {
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ws.Name + "-home",
			Namespace: ws.Namespace,
			Labels:    managedLabels(map[string]string{LabelWorkspace: ws.Name}),
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: storage},
			},
		},
	}
	if r.Config.StorageClass != "" {
		sc := r.Config.StorageClass
		pvc.Spec.StorageClassName = &sc
	}
	if err := controllerutil.SetControllerReference(ws, pvc, r.Scheme); err != nil {
		return err
	}
	existing := pvc.DeepCopy()
	if err := r.Get(ctx, client.ObjectKeyFromObject(pvc), existing); err != nil {
		if apierrors.IsNotFound(err) {
			return r.Create(ctx, pvc)
		}
		return err
	}
	current := existing.Spec.Resources.Requests.Storage()
	if current.Cmp(storage) == 0 {
		return nil
	}
	// Only volume expansion is supported by most provisioners; a shrink
	// will error and be surfaced by the API server.
	existing.Spec.Resources.Requests[corev1.ResourceStorage] = storage
	return r.Update(ctx, existing)
}

// ensureSSHSecret maintains the authorized_keys secret mounted into the pod.
func (r *WorkspaceReconciler) ensureSSHSecret(ctx context.Context, ws *devwsv1alpha1.Workspace, user *devwsv1alpha1.User) error {
	keys := make([]string, 0, len(user.Spec.SSHPublicKeys))
	for _, k := range user.Spec.SSHPublicKeys {
		if s := strings.TrimSpace(k.Key); s != "" {
			keys = append(keys, s)
		}
	}
	sort.Strings(keys)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ws.Name + "-ssh",
			Namespace: ws.Namespace,
			Labels:    managedLabels(map[string]string{LabelWorkspace: ws.Name}),
		},
		Data: map[string][]byte{"authorized_keys": []byte(strings.Join(keys, "\n") + "\n")},
	}
	if err := controllerutil.SetControllerReference(ws, secret, r.Scheme); err != nil {
		return err
	}
	return createOrUpdate(ctx, r.Client, secret)
}

// ensurePod creates the workspace pod, recreating it when the image changed.
// It returns the pod (nil when it had to be created).
func (r *WorkspaceReconciler) ensurePod(ctx context.Context, ws *devwsv1alpha1.Workspace, user *devwsv1alpha1.User, image string, class config.ClassSpec) (*corev1.Pod, error) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: ws.Namespace, Name: ws.Name}}
	if err := r.Get(ctx, client.ObjectKeyFromObject(pod), pod); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, err
		}
		desired := buildWorkspacePod(r.Scheme, ws, user, image, class, r.Config)
		if r.Recorder != nil {
			r.Recorder.Eventf(ws, corev1.EventTypeNormal, "WorkspaceStarting", "creating workspace pod (image %s)", image)
		}
		if err := r.Create(ctx, desired); err != nil {
			return nil, err
		}
		return nil, nil
	}
	// Recreate on image drift (everything else persists on the PVC).
	for i := range pod.Spec.Containers {
		if pod.Spec.Containers[i].Name == "workspace" &&
			pod.Spec.Containers[i].Image != image {
			if r.Recorder != nil {
				r.Recorder.Eventf(ws, corev1.EventTypeNormal, "WorkspaceRestarting",
					"recreating pod for image change %s -> %s", pod.Spec.Containers[i].Image, image)
			}
			if err := r.Delete(ctx, pod); err != nil && !apierrors.IsNotFound(err) {
				return nil, err
			}
			return nil, nil
		}
	}
	// Recreate when gitRepos changed, so newly listed repos are cloned on
	// the next start (already-cloned repos are skipped by the init
	// container).
	if want := gitReposHash(ws.Spec.GitRepos); pod.Annotations[gitReposAnnotation] != want {
		if r.Recorder != nil {
			r.Recorder.Eventf(ws, corev1.EventTypeNormal, "WorkspaceRestarting",
				"recreating pod for git repository list change")
		}
		if err := r.Delete(ctx, pod); err != nil && !apierrors.IsNotFound(err) {
			return nil, err
		}
		return nil, nil
	}
	return pod, nil
}

func (r *WorkspaceReconciler) patchStatus(ctx context.Context, ws *devwsv1alpha1.Workspace, desired devwsv1alpha1.WorkspaceStatus) error {
	if statusChanged(ws.Status, desired) {
		ws.Status = desired
		return r.Status().Update(ctx, ws)
	}
	return nil
}

// gitReposAnnotation on workspace pods carries a hash of spec.gitRepos; a
// change triggers pod recreation so new repos get cloned.
const gitReposAnnotation = "devws.io/git-repos"

// gitReposHash identifies a gitRepos list.
func gitReposHash(repos []string) string {
	cleaned := make([]string, 0, len(repos))
	for _, r := range repos {
		if s := strings.TrimSpace(r); s != "" {
			cleaned = append(cleaned, s)
		}
	}
	if len(cleaned) == 0 {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.Join(cleaned, "\n")))
	return fmt.Sprintf("sha256-%x", sum)
}

// buildWorkspacePod renders the workspace pod spec.
func buildWorkspacePod(scheme *runtime.Scheme, ws *devwsv1alpha1.Workspace, user *devwsv1alpha1.User, image string, class config.ClassSpec, cfg *config.Config) *corev1.Pod {
	var gitRepos []string
	for _, r := range ws.Spec.GitRepos {
		if s := strings.TrimSpace(r); s != "" {
			gitRepos = append(gitRepos, s)
		}
	}

	requests := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse(class.CPU),
		corev1.ResourceMemory: resource.MustParse(class.Memory),
	}

	initContainers := []corev1.Container{
		{
			Name:            "fix-permissions",
			Image:           image,
			ImagePullPolicy: corev1.PullIfNotPresent,
			Command: []string{"/bin/sh", "-ec",
				fmt.Sprintf(`if [ "$(stat -c %%u %s)" != "%d" ]; then chown -R %d:%d %s; fi`,
					WorkspaceHome, WorkspaceUID, WorkspaceUID, WorkspaceUID, WorkspaceHome)},
			VolumeMounts: []corev1.VolumeMount{{Name: "home", MountPath: WorkspaceHome}},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("10m"),
					corev1.ResourceMemory: resource.MustParse("32Mi"),
				},
			},
			SecurityContext: &corev1.SecurityContext{
				RunAsUser:                ptr.To(int64(0)),
				AllowPrivilegeEscalation: ptr.To(false),
			},
		},
	}
	if len(gitRepos) > 0 {
		initContainers = append(initContainers, corev1.Container{
			Name:            "git-clone",
			Image:           image,
			ImagePullPolicy: corev1.PullIfNotPresent,
			// Clones each repo that is not present yet. A failed clone
			// does not stop the remaining repos, but fails the container
			// so the failure is surfaced in the workspace status, events
			// and this container's logs.
			Command: []string{"/bin/sh", "-ec", `
status=0
while IFS= read -r repo; do
  [ -z "$repo" ] && continue
  name=$(basename "$repo" .git)
  if [ ! -d "` + WorkspaceHome + `/$name/.git" ]; then
    echo "devws: cloning $repo"
    if ! git clone "$repo" "` + WorkspaceHome + `/$name"; then
      echo "devws: ERROR: failed to clone $repo" >&2
      status=1
    fi
  fi
done <<EOF
$DEVWS_GIT_REPOS
EOF
exit $status
`},
			Env: []corev1.EnvVar{
				{Name: "HOME", Value: WorkspaceHome},
				{Name: "DEVWS_GIT_REPOS", Value: strings.Join(gitRepos, "\n")},
			},
			VolumeMounts: []corev1.VolumeMount{{Name: "home", MountPath: WorkspaceHome}},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("50m"),
					corev1.ResourceMemory: resource.MustParse("64Mi"),
				},
			},
			SecurityContext: &corev1.SecurityContext{
				RunAsUser:                ptr.To(int64(WorkspaceUID)),
				RunAsGroup:               ptr.To(int64(WorkspaceUID)),
				AllowPrivilegeEscalation: ptr.To(false),
			},
		})
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ws.Namespace,
			Name:      ws.Name,
			Labels: managedLabels(map[string]string{
				LabelUser:      user.Name,
				LabelWorkspace: ws.Name,
			}),
			Annotations: map[string]string{gitReposAnnotation: gitReposHash(ws.Spec.GitRepos)},
		},
		Spec: corev1.PodSpec{
			SecurityContext: &corev1.PodSecurityContext{
				FSGroup:        ptr.To(int64(WorkspaceUID)),
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
			},
			// Workspaces never talk to the kubernetes API; do not even give
			// them a service account token (docs/security.md).
			AutomountServiceAccountToken: ptr.To(false),
			Volumes: []corev1.Volume{
				{
					Name: "home",
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: ws.Name + "-home"},
					},
				},
				{
					Name: "ssh",
					VolumeSource: corev1.VolumeSource{
						Secret: &corev1.SecretVolumeSource{
							SecretName:  ws.Name + "-ssh",
							DefaultMode: ptr.To[int32](0o400),
						},
					},
				},
			},
			InitContainers: initContainers,
			Containers: []corev1.Container{
				{
					Name:            "workspace",
					Image:           image,
					ImagePullPolicy: corev1.PullIfNotPresent,
					Command:         []string{"/usr/local/bin/devws-entrypoint.sh"},
					Env:             []corev1.EnvVar{{Name: "HOME", Value: WorkspaceHome}},
					WorkingDir:      WorkspaceHome,
					Ports:           []corev1.ContainerPort{{Name: "ssh", ContainerPort: WorkspaceSSHPort, Protocol: corev1.ProtocolTCP}},
					VolumeMounts: []corev1.VolumeMount{
						{Name: "home", MountPath: WorkspaceHome},
						{Name: "ssh", MountPath: "/ssh", ReadOnly: true},
					},
					Resources: corev1.ResourceRequirements{
						Requests: requests,
						Limits:   requests,
					},
					ReadinessProbe: &corev1.Probe{
						ProbeHandler: corev1.ProbeHandler{
							TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(WorkspaceSSHPort)},
						},
						InitialDelaySeconds: 2,
						PeriodSeconds:       5,
						FailureThreshold:    60,
					},
					LivenessProbe: &corev1.Probe{
						ProbeHandler: corev1.ProbeHandler{
							TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(WorkspaceSSHPort)},
						},
						InitialDelaySeconds: 30,
						PeriodSeconds:       15,
						FailureThreshold:    4,
					},
					SecurityContext: &corev1.SecurityContext{
						RunAsUser:                ptr.To(int64(0)),
						AllowPrivilegeEscalation: ptr.To(true),
						Capabilities: &corev1.Capabilities{
							Drop: []corev1.Capability{"ALL"},
							// SYS_CHROOT is required by sshd's pre-auth
							// privilege separation (chroot to /run/sshd);
							// FOWNER lets the entrypoint normalize
							// permissions on the persistent volume.
							Add: []corev1.Capability{"CHOWN", "DAC_OVERRIDE", "FOWNER", "SETUID", "SETGID", "AUDIT_WRITE", "KILL", "SYS_CHROOT"},
						},
					},
				},
			},
		},
	}
	_ = controllerutil.SetControllerReference(ws, pod, scheme)
	return pod
}

// SetupWithManager registers the controller. It also watches User objects so
// key or disable changes are propagated to workspaces promptly, and the
// owned Pod/PVC so deleted or evicted workspace pods are repaired
// (recreated) without waiting for another event.
func (r *WorkspaceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&devwsv1alpha1.Workspace{}).
		Owns(&corev1.Pod{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Watches(&devwsv1alpha1.User{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
			var list devwsv1alpha1.WorkspaceList
			if err := r.List(ctx, &list, client.InNamespace(NamespaceFor(obj.GetName()))); err != nil {
				return nil
			}
			reqs := make([]reconcile.Request, 0, len(list.Items))
			for i := range list.Items {
				reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{
					Namespace: list.Items[i].Namespace,
					Name:      list.Items[i].Name,
				}})
			}
			return reqs
		})).
		Complete(r)
}
