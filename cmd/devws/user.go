package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	devwsv1alpha1 "github.com/celerbern/k8s-dev-ws-go/api/v1alpha1"
	"github.com/celerbern/k8s-dev-ws-go/internal/auth"
)

var k8sScheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(k8sScheme))
	utilruntime.Must(devwsv1alpha1.AddToScheme(k8sScheme))
}

// userCmd provides `devws user create`, the admin bootstrap path that talks
// directly to the Kubernetes API (no devws server required).
func userCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "user",
		Short: "Manage users directly via the Kubernetes API (admin bootstrap)",
	}
	cmd.AddCommand(userCreateCmd())
	return cmd
}

func userCreateCmd() *cobra.Command {
	var (
		username    string
		email       string
		displayName string
		role        string
		password    string
		passwordEnv string
		kubeconfig  string
		maxWS       int
		cpu         string
		memory      string
		storage     string
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a User custom resource (e.g. the first admin)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if username == "" {
				return fmt.Errorf("--username is required")
			}
			if password == "" && passwordEnv != "" {
				password = strings.TrimSpace(os.Getenv(passwordEnv))
			}
			if password == "" {
				fmt.Print("password: ")
				raw, err := term.ReadPassword(int(os.Stdin.Fd()))
				fmt.Println()
				if err != nil {
					return err
				}
				password = strings.TrimSpace(string(raw))
			}
			if len(password) < 8 {
				return fmt.Errorf("password must be at least 8 characters")
			}

			kcfg := kubeconfig
			if kcfg == "" {
				kcfg = os.Getenv("KUBECONFIG")
			}
			restCfg, err := clientcmd.BuildConfigFromFlags("", kcfg)
			if err != nil {
				return fmt.Errorf("loading kubeconfig: %w (run inside the cluster or pass --kubeconfig)", err)
			}
			c, err := client.New(restCfg, client.Options{Scheme: k8sScheme})
			if err != nil {
				return err
			}

			hash, err := auth.HashPassword(password)
			if err != nil {
				return err
			}
			user := &devwsv1alpha1.User{}
			user.Name = username
			user.Spec.Email = email
			user.Spec.DisplayName = displayName
			user.Spec.Role = role
			user.Spec.PasswordHash = hash
			if maxWS > 0 {
				user.Spec.Quotas.MaxWorkspaces = maxWS
			}
			if cpu != "" {
				user.Spec.Quotas.CPU = resource.MustParse(cpu)
			}
			if memory != "" {
				user.Spec.Quotas.Memory = resource.MustParse(memory)
			}
			if storage != "" {
				user.Spec.Quotas.Storage = resource.MustParse(storage)
			}
			// Idempotent: the helm bootstrap job re-runs on every upgrade,
			// so update the user (password, role, fields) when it exists.
			if err := c.Create(cmd.Context(), user); err != nil {
				if !apierrors.IsAlreadyExists(err) {
					return err
				}
				existing := &devwsv1alpha1.User{}
				if err := c.Get(cmd.Context(), types.NamespacedName{Name: username}, existing); err != nil {
					return err
				}
				existing.Spec = user.Spec
				if err := c.Update(cmd.Context(), existing); err != nil {
					return err
				}
				fmt.Printf("user %q updated\n", username)
				return nil
			}
			fmt.Printf("user %q created\n", username)
			return nil
		},
	}
	cmd.Flags().StringVar(&username, "username", "", "username (DNS label, e.g. jdoe)")
	cmd.Flags().StringVar(&email, "email", "", "email address")
	cmd.Flags().StringVar(&displayName, "display-name", "", "display name")
	cmd.Flags().StringVar(&role, "role", "user", "role: user or admin")
	cmd.Flags().StringVar(&password, "password", "", "password (prompted when empty)")
	cmd.Flags().StringVar(&passwordEnv, "password-env", "", "read the password from this environment variable")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "path to a kubeconfig (in-cluster config when empty)")
	cmd.Flags().IntVar(&maxWS, "max-workspaces", 0, "workspace quota")
	cmd.Flags().StringVar(&cpu, "cpu", "", "CPU quota (e.g. 8)")
	cmd.Flags().StringVar(&memory, "memory", "", "memory quota (e.g. 16Gi)")
	cmd.Flags().StringVar(&storage, "storage", "", "storage quota (e.g. 100Gi)")
	return cmd
}
