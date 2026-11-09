// devws-apiserver is the devws control plane: kubernetes controllers for
// Users and Workspaces, the REST API + web UI, and the SSH gateway.
package main

import (
	"context"
	"flag"
	"os"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	devwsv1alpha1 "github.com/celerbern/k8s-dev-ws-go/api/v1alpha1"
	"github.com/celerbern/k8s-dev-ws-go/internal/api"
	"github.com/celerbern/k8s-dev-ws-go/internal/auth"
	"github.com/celerbern/k8s-dev-ws-go/internal/config"
	"github.com/celerbern/k8s-dev-ws-go/internal/controllers"
	"github.com/celerbern/k8s-dev-ws-go/internal/sshgateway"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(networkingv1.AddToScheme(scheme))
	utilruntime.Must(devwsv1alpha1.AddToScheme(scheme))
}

func main() {
	var (
		webAddr           string
		sshAddr           string
		metricsAddr       string
		platformNamespace string
	)
	// NOTE: --kubeconfig is registered by controller-runtime's pkg/client/config
	// init() and honored by ctrl.GetConfigOrDie() below (along with KUBECONFIG
	// and the in-cluster config).
	flag.StringVar(&webAddr, "web-addr", ":8080", "listen address for the web UI and REST API")
	flag.StringVar(&sshAddr, "ssh-addr", ":2222", "listen address for the SSH gateway")
	flag.StringVar(&metricsAddr, "metrics-addr", "", "listen address for prometheus metrics (disabled by default)")
	flag.StringVar(&platformNamespace, "platform-namespace", "devws-system", "namespace the control plane runs in")
	opts := zap.Options{Development: false}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	logger := ctrl.Log.WithName("setup")

	restCfg := ctrl.GetConfigOrDie()

	// Only cache pods that belong to devws workspaces.
	podSelector, err := labels.Parse(controllers.LabelWorkspace)
	if err != nil {
		logger.Error(err, "invalid pod selector")
		os.Exit(1)
	}

	// Disable the metrics server unless an address was given: the empty
	// string means "default to :8080" in controller-runtime, which would
	// collide with the web server.
	metricsBind := metricsAddr
	if metricsBind == "" {
		metricsBind = "0"
	}

	mgr, err := ctrl.NewManager(restCfg, ctrl.Options{
		Scheme: scheme,
		Cache: cache.Options{
			ByObject: map[client.Object]cache.ByObject{
				&corev1.Pod{}: {Label: podSelector},
			},
		},
		Metrics: metricsserver.Options{
			BindAddress: metricsBind,
		},
	})
	if err != nil {
		logger.Error(err, "cannot create manager")
		os.Exit(1)
	}

	// A direct (uncached) client for bootstrap reads/writes that must work
	// before the caches start.
	directClient, err := client.New(restCfg, client.Options{Scheme: scheme})
	if err != nil {
		logger.Error(err, "cannot create direct client")
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	platformCfg, err := config.Load(ctx, directClient, platformNamespace)
	if err != nil {
		logger.Error(err, "cannot load platform config (check RBAC for configmaps)")
		os.Exit(1)
	}
	sessions, err := auth.NewSessionManagerFromSecret(ctx, directClient, platformNamespace)
	if err != nil {
		logger.Error(err, "cannot load session key")
		os.Exit(1)
	}

	// Controllers.
	if err := (&controllers.UserReconciler{
		Client:            mgr.GetClient(),
		Scheme:            mgr.GetScheme(),
		PlatformNamespace: platformNamespace,
	}).SetupWithManager(mgr); err != nil {
		logger.Error(err, "cannot setup user controller")
		os.Exit(1)
	}
	if err := (&controllers.WorkspaceReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("devws-workspace-controller"),
		Config:   platformCfg,
	}).SetupWithManager(mgr); err != nil {
		logger.Error(err, "cannot setup workspace controller")
		os.Exit(1)
	}

	// HTTP API + web UI. The kube client (client-go) provides pod log
	// streaming and uncached event listing, which the manager's cached
	// client cannot do.
	kubeClient, err := api.NewKubeClient(restCfg)
	if err != nil {
		logger.Error(err, "cannot create kube client")
		os.Exit(1)
	}
	if err := mgr.Add(api.New(webAddr, mgr.GetClient(), mgr.GetScheme(), platformCfg, sessions, kubeClient)); err != nil {
		logger.Error(err, "cannot add api server")
		os.Exit(1)
	}

	// SSH gateway (loads its host key before the manager starts; the direct
	// client works without the cache, and serves runtime lookups so key
	// revocations apply immediately).
	gwCtx, gwCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer gwCancel()
	gateway, err := sshgateway.New(gwCtx, directClient, platformNamespace, sshAddr)
	if err != nil {
		logger.Error(err, "cannot create ssh gateway")
		os.Exit(1)
	}
	if err := mgr.Add(gateway); err != nil {
		logger.Error(err, "cannot add ssh gateway")
		os.Exit(1)
	}

	logger.Info("starting devws apiserver",
		"web", webAddr, "ssh", sshAddr, "platformNamespace", platformNamespace)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		logger.Error(err, "manager exited")
		os.Exit(1)
	}
}
