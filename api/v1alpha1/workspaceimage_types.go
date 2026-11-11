package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WorkspaceImageSpec defines the desired state of a WorkspaceImage.
type WorkspaceImageSpec struct {
	// Image is the full container image reference including its tag or
	// digest, e.g. "ghcr.io/acme/devws-python:3.12".
	// +kubebuilder:validation:MinLength=1
	Image string `json:"image"`
	// Description is shown in the workspace creation picker.
	Description string `json:"description,omitempty"`
	// Deprecated hides the image from the workspace creation picker.
	// Existing workspaces using it are unaffected.
	// +kubebuilder:default=false
	Deprecated bool `json:"deprecated,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,path=workspaceimages
// +kubebuilder:printcolumn:name="Image",type=string,JSONPath=`.spec.image`
// +kubebuilder:printcolumn:name="Description",type=string,JSONPath=`.spec.description`
// +kubebuilder:printcolumn:name="Deprecated",type=boolean,JSONPath=`.spec.deprecated`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// WorkspaceImage is a catalog entry users can pick when creating a
// workspace. Regular users may only run catalog images (or the platform
// default); admins may use arbitrary image references. Entries are managed
// by admins through the API/CLI or seeded by the helm chart.
//
// The REST API serves the catalog straight from these CRs (there is no
// reconciler — nothing to reconcile).
// +kubebuilder:rbac:groups=devws.io,resources=workspaceimages,verbs=get;list;watch;create;update;patch;delete
type WorkspaceImage struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec WorkspaceImageSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true

// WorkspaceImageList contains a list of WorkspaceImage.
type WorkspaceImageList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WorkspaceImage `json:"items"`
}

func init() {
	SchemeBuilder.Register(&WorkspaceImage{}, &WorkspaceImageList{})
}