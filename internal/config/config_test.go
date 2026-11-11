package config

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var cfgScheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(cfgScheme))
}

func configMap(t *testing.T, data map[string]string) *corev1.ConfigMap {
	t.Helper()
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: "devws-system", Name: ConfigName},
		Data:       data,
	}
	return cm
}

func TestLoadImagePullSecrets(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{"unset", "", nil},
		{"single", "my-registry", []string{"my-registry"}},
		{"comma separated with spaces", " a , b ,c", []string{"a", "b", "c"}},
		{"drops empties", "a,,b,", []string{"a", "b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(cfgScheme).
				WithRuntimeObjects(configMap(t, map[string]string{"imagePullSecrets": tc.raw})).
				Build()
			cfg, err := Load(context.Background(), c, "devws-system")
			if err != nil {
				t.Fatal(err)
			}
			if len(cfg.ImagePullSecrets) != len(tc.want) {
				t.Fatalf("got %v, want %v", cfg.ImagePullSecrets, tc.want)
			}
			for i := range tc.want {
				if cfg.ImagePullSecrets[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", cfg.ImagePullSecrets, tc.want)
				}
			}
		})
	}
}

func TestLoadMissingConfigMapAppliesDefaults(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(cfgScheme).Build()
	cfg, err := Load(context.Background(), c, "devws-system")
	if err != nil {
		t.Fatalf("missing configmap must not be an error: %v", err)
	}
	if cfg.WorkspaceImage != Default().WorkspaceImage {
		t.Fatalf("default workspace image must apply, got %q", cfg.WorkspaceImage)
	}
	if len(cfg.ImagePullSecrets) != 0 {
		t.Fatalf("no pull secrets by default, got %v", cfg.ImagePullSecrets)
	}
}

func TestImagePullSecretRefs(t *testing.T) {
	cfg := Default()
	if refs := cfg.ImagePullSecretRefs(); refs != nil {
		t.Fatalf("no refs without configured secrets, got %v", refs)
	}
	cfg.ImagePullSecrets = []string{"a", "b"}
	refs := cfg.ImagePullSecretRefs()
	if len(refs) != 2 || refs[0].Name != "a" || refs[1].Name != "b" {
		t.Fatalf("refs wrong: %v", refs)
	}
}
