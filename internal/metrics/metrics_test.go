package metrics

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	devwsv1alpha1 "github.com/celerbern/k8s-dev-ws-go/api/v1alpha1"
)

var testScheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(testScheme))
	utilruntime.Must(devwsv1alpha1.AddToScheme(testScheme))
}

func testWorkspace(name, namespace, phase string) *devwsv1alpha1.Workspace {
	ws := &devwsv1alpha1.Workspace{}
	ws.Name = name
	ws.Namespace = namespace
	ws.Status.Phase = phase
	return ws
}

func TestWorkspaceCollector(t *testing.T) {
	c := fake.NewClientBuilder().
		WithScheme(testScheme).
		WithRuntimeObjects(
			testWorkspace("main", "devws-alice", devwsv1alpha1.WorkspacePhaseRunning),
			testWorkspace("side", "devws-alice", devwsv1alpha1.WorkspacePhaseStopped),
			testWorkspace("bare", "devws-alice", ""), // phase-less counts as Unknown
		).Build()

	admin := &devwsv1alpha1.User{}
	admin.Name = "admin"
	disabled := &devwsv1alpha1.User{}
	disabled.Name = "bob"
	disabled.Spec.Disabled = true
	if err := c.Create(t.Context(), admin); err != nil {
		t.Fatal(err)
	}
	if err := c.Create(t.Context(), disabled); err != nil {
		t.Fatal(err)
	}

	expected := `
# HELP devws_users Number of devws users.
# TYPE devws_users gauge
devws_users{state="active"} 1
devws_users{state="disabled"} 1
# HELP devws_workspaces Number of workspaces known to devws.
# TYPE devws_workspaces gauge
devws_workspaces 3
# HELP devws_workspaces_by_phase Number of workspaces by status phase.
# TYPE devws_workspaces_by_phase gauge
devws_workspaces_by_phase{phase="Running"} 1
devws_workspaces_by_phase{phase="Stopped"} 1
devws_workspaces_by_phase{phase="Unknown"} 1
`
	if err := testutil.CollectAndCompare(newWorkspaceCollector(c), strings.NewReader(expected)); err != nil {
		t.Fatal(err)
	}
}
