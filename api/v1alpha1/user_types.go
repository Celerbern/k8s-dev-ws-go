package v1alpha1

import (
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SSHKey is a public SSH key registered by a user. The key is used both to
// authenticate to the devws SSH gateway and to log in to the user's
// workspace pods.
type SSHKey struct {
	// Name is a short label chosen by the user (e.g. "laptop").
	Name string `json:"name"`
	// Key is a public key in OpenSSH authorized_keys format
	// (e.g. "ssh-ed25519 AAAAC3... user@host").
	Key string `json:"key"`
	// CreatedAt records when the key was registered.
	CreatedAt metav1.Time `json:"createdAt,omitempty"`
}

// APIToken is a bearer token used by the devws CLI. Only the bcrypt hash of
// the token is stored; Prefix holds the first characters of the token so
// lookups can skip bcrypt verification for non-matching tokens.
type APIToken struct {
	// Name is a short label chosen by the user (e.g. "cli").
	Name string `json:"name"`
	// Prefix is the leading characters of the token (not secret).
	Prefix string `json:"prefix,omitempty"`
	// Hash is the bcrypt hash of the full token.
	Hash string `json:"hash"`
	// CreatedAt records when the token was issued.
	CreatedAt metav1.Time `json:"createdAt,omitempty"`
	// ExpiresAt optionally limits the token lifetime.
	ExpiresAt *metav1.Time `json:"expiresAt,omitempty"`
	// LastUsedAt records the last successful authentication.
	LastUsedAt *metav1.Time `json:"lastUsedAt,omitempty"`
}

// UserQuotas defines the resource limits enforced for a single user.
// CPU/Memory/Storage are enforced with a ResourceQuota in the user's
// namespace; MaxWorkspaces is enforced by the API server and controller.
type UserQuotas struct {
	// MaxWorkspaces is the maximum number of workspaces the user may create.
	// +kubebuilder:validation:Minimum=0
	MaxWorkspaces int `json:"maxWorkspaces,omitempty"`
	// CPU is the total CPU (cores) available to the user's workspaces, e.g. "8".
	CPU resource.Quantity `json:"cpu,omitempty"`
	// Memory is the total memory available to the user's workspaces, e.g. "16Gi".
	Memory resource.Quantity `json:"memory,omitempty"`
	// Storage is the total persistent storage available, e.g. "100Gi".
	Storage resource.Quantity `json:"storage,omitempty"`
}

// UserSpec defines the desired state of a User.
type UserSpec struct {
	// Email address of the user (used for display and future notifications).
	Email string `json:"email,omitempty"`
	// DisplayName is the human-friendly name shown in the dashboard.
	DisplayName string `json:"displayName,omitempty"`
	// Role is "user" or "admin". Admins can manage all users and workspaces.
	// +kubebuilder:validation:Enum=user;admin
	// +kubebuilder:default=user
	Role string `json:"role,omitempty"`
	// Disabled prevents login, CLI token use and SSH access, and stops all
	// of the user's workspaces.
	Disabled bool `json:"disabled,omitempty"`
	// AuthMethod is how the user authenticates: "local" (password) or
	// "oidc" (external identity provider). It is exclusive: oidc users
	// have no password and sign in through the platform's OIDC provider,
	// where the configured username claim must match the user's name.
	// +kubebuilder:validation:Enum=local;oidc
	// +kubebuilder:default=local
	AuthMethod string `json:"authMethod,omitempty"`
	// PasswordHash is a bcrypt hash of the user's dashboard/CLI password.
	// Empty for oidc users.
	PasswordHash string `json:"passwordHash,omitempty"`
	// SSHPublicKeys are the public keys the user may authenticate with.
	SSHPublicKeys []SSHKey `json:"sshPublicKeys,omitempty"`
	// APITokens are bearer tokens used by the devws CLI.
	APITokens []APIToken `json:"apiTokens,omitempty"`
	// Quotas limits the resources the user may consume.
	Quotas UserQuotas `json:"quotas,omitempty"`
}

// UserStatus defines the observed state of a User.
type UserStatus struct {
	// Phase is "Ready", "Provisioning" or "Terminating".
	Phase string `json:"phase,omitempty"`
	// Namespace is the per-user namespace managed for this user.
	Namespace string `json:"namespace,omitempty"`
	// Message carries human-readable detail about the current phase.
	Message string `json:"message,omitempty"`
	// Conditions represent the latest available observations.
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,path=users
// +kubebuilder:printcolumn:name="Email",type=string,JSONPath=`.spec.email`
// +kubebuilder:printcolumn:name="Role",type=string,JSONPath=`.spec.role`
// +kubebuilder:printcolumn:name="Disabled",type=boolean,JSONPath=`.spec.disabled`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// User is the platform account. Each user gets a dedicated namespace
// (devws-<name>) holding their workspaces.
type User struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   UserSpec   `json:"spec,omitempty"`
	Status UserStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// UserList contains a list of User.
type UserList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []User `json:"items"`
}

func init() {
	SchemeBuilder.Register(&User{}, &UserList{})
}
