package oidc

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	devwsv1alpha1 "github.com/celerbern/k8s-dev-ws-go/api/v1alpha1"
)

var cfgScheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(cfgScheme))
	utilruntime.Must(devwsv1alpha1.AddToScheme(cfgScheme))
}

func oidcSecret(data map[string]string) *corev1.Secret {
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "devws-system", Name: SecretName},
		Data:       map[string][]byte{},
	}
	for k, v := range data {
		sec.Data[k] = []byte(v)
	}
	return sec
}

func TestLoadMissingSecretDisablesOIDC(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(cfgScheme).Build()
	cfg, err := Load(context.Background(), c, "devws-system")
	if err != nil {
		t.Fatalf("missing secret must not be an error: %v", err)
	}
	if cfg != nil {
		t.Fatalf("missing secret must disable oidc, got %+v", cfg)
	}
}

func TestLoadCompleteSecret(t *testing.T) {
	c := fake.NewClientBuilder().
		WithScheme(cfgScheme).
		WithRuntimeObjects(oidcSecret(map[string]string{
			"issuerUrl":     "https://idp.example.com/dex",
			"clientId":      "devws",
			"clientSecret":  "s3cret",
			"redirectUrl":   "https://devws.example.com/api/auth/oidc/callback",
			"usernameClaim": "",
			"scopes":        "openid, profile ,email",
		})).
		Build()
	cfg, err := Load(context.Background(), c, "devws-system")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.IssuerURL != "https://idp.example.com/dex" || cfg.ClientID != "devws" || cfg.ClientSecret != "s3cret" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if cfg.UsernameClaim != DefaultUsernameClaim {
		t.Fatalf("empty usernameClaim must default to %q, got %q", DefaultUsernameClaim, cfg.UsernameClaim)
	}
	if len(cfg.Scopes) != 3 || cfg.Scopes[0] != "openid" || cfg.Scopes[1] != "profile" || cfg.Scopes[2] != "email" {
		t.Fatalf("scopes must be split and trimmed, got %v", cfg.Scopes)
	}
}

func TestLoadIncompleteSecretFails(t *testing.T) {
	base := map[string]string{
		"issuerUrl":    "https://idp.example.com",
		"clientId":     "devws",
		"clientSecret": "s3cret",
		"redirectUrl":  "https://devws.example.com/callback",
	}
	for field := range base {
		data := map[string]string{}
		for k, v := range base {
			if k != field {
				data[k] = v
			}
		}
		c := fake.NewClientBuilder().
			WithScheme(cfgScheme).
			WithRuntimeObjects(oidcSecret(data)).
			Build()
		if _, err := Load(context.Background(), c, "devws-system"); err == nil {
			t.Fatalf("secret without %q must fail validation", field)
		}
	}
}

func TestLoadForcesOpenIDScope(t *testing.T) {
	c := fake.NewClientBuilder().
		WithScheme(cfgScheme).
		WithRuntimeObjects(oidcSecret(map[string]string{
			"issuerUrl":    "https://idp.example.com",
			"clientId":     "devws",
			"clientSecret": "s3cret",
			"redirectUrl":  "https://devws.example.com/callback",
			"scopes":       "profile",
		})).
		Build()
	cfg, err := Load(context.Background(), c, "devws-system")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Scopes[0] != "openid" {
		t.Fatalf("openid scope must be forced, got %v", cfg.Scopes)
	}
}

func TestLoadRejectsBadIssuer(t *testing.T) {
	c := fake.NewClientBuilder().
		WithScheme(cfgScheme).
		WithRuntimeObjects(oidcSecret(map[string]string{
			"issuerUrl":    "not a url",
			"clientId":     "devws",
			"clientSecret": "s3cret",
			"redirectUrl":  "https://devws.example.com/callback",
		})).
		Build()
	if _, err := Load(context.Background(), c, "devws-system"); err == nil {
		t.Fatal("invalid issuerUrl must fail validation")
	}
}

func TestLoadClientAuthStyle(t *testing.T) {
	base := map[string]string{
		"issuerUrl":    "https://idp.example.com",
		"clientId":     "devws",
		"clientSecret": "s3cret",
		"redirectUrl":  "https://devws.example.com/callback",
	}
	for style, want := range map[string]string{"": "", "basic": "basic", "post": "post", "auto": "auto"} {
		data := map[string]string{}
		for k, v := range base {
			data[k] = v
		}
		if style != "" {
			data["clientAuthStyle"] = style
		}
		c := fake.NewClientBuilder().
			WithScheme(cfgScheme).
			WithRuntimeObjects(oidcSecret(data)).
			Build()
		cfg, err := Load(context.Background(), c, "devws-system")
		if err != nil {
			t.Fatalf("clientAuthStyle %q: %v", style, err)
		}
		if cfg.ClientAuthStyle != want {
			t.Fatalf("clientAuthStyle %q: got %q, want %q", style, cfg.ClientAuthStyle, want)
		}
	}

	data := map[string]string{"clientAuthStyle": "nonsense"}
	for k, v := range base {
		data[k] = v
	}
	c := fake.NewClientBuilder().
		WithScheme(cfgScheme).
		WithRuntimeObjects(oidcSecret(data)).
		Build()
	if _, err := Load(context.Background(), c, "devws-system"); err == nil {
		t.Fatal("unknown clientAuthStyle must fail validation")
	}
}

func TestLoadRejectsNonHTTPRedirectURL(t *testing.T) {
	c := fake.NewClientBuilder().
		WithScheme(cfgScheme).
		WithRuntimeObjects(oidcSecret(map[string]string{
			"issuerUrl":    "https://idp.example.com",
			"clientId":     "devws",
			"clientSecret": "s3cret",
			"redirectUrl":  "ftp://devws.example.com/callback",
		})).
		Build()
	if _, err := Load(context.Background(), c, "devws-system"); err == nil {
		t.Fatal("non-http(s) redirectUrl must fail validation")
	}
}
