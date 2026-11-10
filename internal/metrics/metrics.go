// Package metrics defines devws' custom prometheus metrics. They are
// registered into the controller-runtime global registry, which is served
// on the address configured with --metrics-addr (alongside controller-runtime's
// and client-go's built-in metrics).
package metrics

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	devwsv1alpha1 "github.com/celerbern/k8s-dev-ws-go/api/v1alpha1"
)

// APIRequestsTotal counts REST API requests by method, matched route
// pattern and response status code.
var APIRequestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "devws_api_requests_total",
	Help: "REST API requests by method, route and response code.",
}, []string{"method", "route", "code"})

// GatewayConnectionsActive is the number of SSH sessions currently open
// through the SSH gateway (per replica; sum across replicas for the total).
var GatewayConnectionsActive = prometheus.NewGauge(prometheus.GaugeOpts{
	Name: "devws_gateway_connections_active",
	Help: "SSH sessions currently open through the gateway.",
})

// GatewayConnectionsTotal counts SSH sessions accepted by the gateway
// since process start.
var GatewayConnectionsTotal = prometheus.NewCounter(prometheus.CounterOpts{
	Name: "devws_gateway_connections_total",
	Help: "SSH sessions accepted by the gateway since start.",
})

// Register registers all custom devws metrics with the controller-runtime
// global registry. The workspace/user collector reads through c (the
// manager's informer-backed client, which is served on every replica —
// caches run independently of leader election).
func Register(c client.Reader) {
	metrics.Registry.MustRegister(
		APIRequestsTotal,
		GatewayConnectionsActive,
		GatewayConnectionsTotal,
		newWorkspaceCollector(c),
	)
}

// ---------------------------------------------------------------- collector --

var (
	workspacesDesc = prometheus.NewDesc(
		"devws_workspaces",
		"Number of workspaces known to devws.",
		nil, nil,
	)
	workspacesByPhaseDesc = prometheus.NewDesc(
		"devws_workspaces_by_phase",
		"Number of workspaces by status phase.",
		[]string{"phase"}, nil,
	)
	usersDesc = prometheus.NewDesc(
		"devws_users",
		"Number of devws users.",
		[]string{"state"}, nil,
	)
)

// workspaceCollector computes workspace and user gauges at scrape time
// from the informer cache, so every replica serves the cluster state
// without needing to run the controllers.
type workspaceCollector struct {
	client client.Reader
}

func newWorkspaceCollector(c client.Reader) *workspaceCollector {
	return &workspaceCollector{client: c}
}

// Describe implements prometheus.Collector as an unchecked collector (the
// list of series depends on the observed phases).
func (c *workspaceCollector) Describe(chan<- *prometheus.Desc) {}

// Collect implements prometheus.Collector.
func (c *workspaceCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var workspaces devwsv1alpha1.WorkspaceList
	if err := c.client.List(ctx, &workspaces); err == nil {
		byPhase := map[string]float64{}
		for i := range workspaces.Items {
			phase := workspaces.Items[i].Status.Phase
			if phase == "" {
				phase = "Unknown"
			}
			byPhase[phase]++
		}
		ch <- prometheus.MustNewConstMetric(workspacesDesc, prometheus.GaugeValue, float64(len(workspaces.Items)))
		for phase, n := range byPhase {
			ch <- prometheus.MustNewConstMetric(workspacesByPhaseDesc, prometheus.GaugeValue, n, phase)
		}
	}

	var users devwsv1alpha1.UserList
	if err := c.client.List(ctx, &users); err == nil {
		active, disabled := 0.0, 0.0
		for i := range users.Items {
			if users.Items[i].Spec.Disabled {
				disabled++
			} else {
				active++
			}
		}
		ch <- prometheus.MustNewConstMetric(usersDesc, prometheus.GaugeValue, active, "active")
		ch <- prometheus.MustNewConstMetric(usersDesc, prometheus.GaugeValue, disabled, "disabled")
	}
}
