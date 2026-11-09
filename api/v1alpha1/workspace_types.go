package v1alpha1

import (
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WorkspaceSpec defines the desired state of a Workspace.
type WorkspaceSpec struct {
	// UserRef is the username (User CR name) that owns this workspace.
	// +kubebuilder:validation:MinLength=1
	UserRef string `json:"userRef"`
	// Image overrides the workspace container image for this workspace.
	// Defaults to the platform-configured image.
	Image string `json:"image,omitempty"`
	// Class selects a workspace class (size template) such as small,
	// medium or large. Defaults to the platform-configured class.
	Class string `json:"class,omitempty"`
	// Storage overrides the persistent volume size for this workspace.
	// Defaults to the class storage size.
	Storage resource.Quantity `json:"storage,omitempty"`
	// GitRepos lists public git repositories cloned into /home/devws on
	// first start (e.g. "https://github.com/org/repo.git").
	GitRepos []string `json:"gitRepos,omitempty"`
	// Paused stops the workspace: the pod is deleted but the persistent
	// volume and workspace definition are kept.
	Paused bool `json:"paused,omitempty"`
}

// WorkspaceStatus defines the observed state of a Workspace.
type WorkspaceStatus struct {
	// Phase is one of Provisioning, Running, Stopped, Failed.
	Phase string `json:"phase,omitempty"`
	// Message carries human-readable detail about the current phase.
	Message string `json:"message,omitempty"`
	// PodName is the name of the workspace pod (empty when stopped).
	PodName string `json:"podName,omitempty"`
	// SSHHost is the synthetic hostname users connect through via the SSH
	// gateway, e.g. "main.devws-jdoe.ws.internal".
	SSHHost string `json:"sshHost,omitempty"`
	// Conditions represent the latest available observations.
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// Workspace phases.
const (
	WorkspacePhaseProvisioning = "Provisioning"
	WorkspacePhaseRunning      = "Running"
	WorkspacePhaseStopped      = "Stopped"
	WorkspacePhaseFailed       = "Failed"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,path=workspaces
// +kubebuilder:printcolumn:name="User",type=string,JSONPath=`.spec.userRef`
// +kubebuilder:printcolumn:name="Class",type=string,JSONPath=`.spec.class`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Workspace is a single development environment: one pod (sshd + toolchains)
// plus one persistent volume for /home/devws, placed in the owner's
// namespace.
type Workspace struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WorkspaceSpec   `json:"spec,omitempty"`
	Status WorkspaceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// WorkspaceList contains a list of Workspace.
type WorkspaceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Workspace `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Workspace{}, &WorkspaceList{})
}
