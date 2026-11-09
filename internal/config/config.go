// Package config loads the devws platform configuration from a ConfigMap
// (devws-config in the platform namespace) and provides compiled-in
// defaults for when the ConfigMap is absent or incomplete.
package config

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"

	devwsv1alpha1 "github.com/celerbern/k8s-dev-ws-go/api/v1alpha1"
)

// ConfigName is the name of the ConfigMap holding platform configuration.
const ConfigName = "devws-config"

// ClassSpec is a workspace size template.
type ClassSpec struct {
	CPU     string `json:"cpu"`
	Memory  string `json:"memory"`
	Storage string `json:"storage"`
}

// Config is the resolved platform configuration.
type Config struct {
	// WorkspaceImage is the default container image for workspaces.
	WorkspaceImage string
	// DefaultClass is used when a workspace does not specify a class.
	DefaultClass string
	// StorageClass is used for workspace PVCs ("" = cluster default).
	StorageClass string
	// SSHHost is the advertised SSH gateway endpoint (host or host:port).
	SSHHost string
	// Classes maps class names to size templates.
	Classes map[string]ClassSpec
}

// Default returns the built-in configuration used when the ConfigMap is
// missing or incomplete.
func Default() *Config {
	return &Config{
		WorkspaceImage: "ghcr.io/celerbern/devws-workspace:latest",
		DefaultClass:   "medium",
		StorageClass:   "",
		SSHHost:        "ssh.devws.local",
		Classes: map[string]ClassSpec{
			"tiny":   {CPU: "250m", Memory: "512Mi", Storage: "5Gi"},
			"small":  {CPU: "2", Memory: "4Gi", Storage: "10Gi"},
			"medium": {CPU: "4", Memory: "8Gi", Storage: "20Gi"},
			"large":  {CPU: "8", Memory: "16Gi", Storage: "50Gi"},
		},
	}
}

// DefaultUserQuotas returns the quotas applied to users that do not specify
// their own.
func DefaultUserQuotas() devwsv1alpha1.UserQuotas {
	return devwsv1alpha1.UserQuotas{
		MaxWorkspaces: 5,
		CPU:           resource.MustParse("8"),
		Memory:        resource.MustParse("16Gi"),
		Storage:       resource.MustParse("100Gi"),
	}
}

// Load reads the ConfigMap in the given namespace and merges it over the
// defaults. Missing keys keep their default values. A missing ConfigMap is
// not an error (defaults apply), but other failures — e.g. RBAC denials —
// are returned so misconfiguration fails loudly instead of silently
// falling back.
func Load(ctx context.Context, c client.Reader, namespace string) (*Config, error) {
	cfg := Default()
	cm := &corev1.ConfigMap{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: ConfigName}, cm); err != nil {
		if apierrors.IsNotFound(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("reading configmap %s/%s: %w", namespace, ConfigName, err)
	}
	if v := strings.TrimSpace(cm.Data["workspaceImage"]); v != "" {
		cfg.WorkspaceImage = v
	}
	if v := strings.TrimSpace(cm.Data["defaultClass"]); v != "" {
		cfg.DefaultClass = v
	}
	if v, ok := cm.Data["storageClass"]; ok {
		cfg.StorageClass = strings.TrimSpace(v)
	}
	if v := strings.TrimSpace(cm.Data["sshHost"]); v != "" {
		cfg.SSHHost = v
	}
	if raw := strings.TrimSpace(cm.Data["classes"]); raw != "" {
		var classes map[string]ClassSpec
		if err := json.Unmarshal([]byte(raw), &classes); err == nil && len(classes) > 0 {
			cfg.Classes = classes
		}
	}
	return cfg, nil
}

// Class returns the spec for a class name.
func (c *Config) Class(name string) (ClassSpec, bool) {
	if name == "" {
		name = c.DefaultClass
	}
	spec, ok := c.Classes[name]
	return spec, ok
}

// SSHHostPort splits the advertised SSH endpoint into host and port
// (default port 22).
func (c *Config) SSHHostPort() (string, int) {
	host := c.SSHHost
	if host == "" {
		return "", 22
	}
	if h, p, err := net.SplitHostPort(host); err == nil {
		if port, err := strconv.Atoi(p); err == nil {
			return h, port
		}
	}
	return host, 22
}
