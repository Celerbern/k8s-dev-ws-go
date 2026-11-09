package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	devwsv1alpha1 "github.com/celerbern/k8s-dev-ws-go/api/v1alpha1"
	"github.com/celerbern/k8s-dev-ws-go/internal/auth"
	"github.com/celerbern/k8s-dev-ws-go/internal/config"
)

// fakeKubeClient stubs KubeClient for handler tests.
type fakeKubeClient struct {
	logs   io.ReadCloser
	logErr error
	logReq logRequest
	events []corev1.Event
	evErr  error
}

type logRequest struct {
	namespace string
	pod       string
	container string
	tail      int64
	follow    bool
	previous  bool
}

func (f *fakeKubeClient) StreamPodLogs(_ context.Context, namespace, pod, container string, tail int64, follow, previous bool) (io.ReadCloser, error) {
	f.logReq = logRequest{namespace, pod, container, tail, follow, previous}
	if f.logErr != nil {
		return nil, f.logErr
	}
	return f.logs, nil
}

func (f *fakeKubeClient) ListEvents(_ context.Context, _ string) ([]corev1.Event, error) {
	return f.events, f.evErr
}

type nopCloser struct{ io.Reader }

func (nopCloser) Close() error { return nil }

func testWorkspacePod(namespace, name string) *corev1.Pod {
	pod := &corev1.Pod{}
	pod.Name = name
	pod.Namespace = namespace
	pod.Spec.InitContainers = []corev1.Container{{Name: "git-clone"}}
	pod.Spec.Containers = []corev1.Container{{Name: "workspace"}}
	pod.Status.Phase = corev1.PodRunning
	return pod
}

func testWorkspace(namespace, name, user string) *devwsv1alpha1.Workspace {
	ws := &devwsv1alpha1.Workspace{}
	ws.Name = name
	ws.Namespace = namespace
	ws.Spec.UserRef = user
	ws.Status.PodName = name
	ws.Status.Phase = devwsv1alpha1.WorkspacePhaseRunning
	return ws
}

func newInspectionTestServer(t *testing.T, kube KubeClient, objects ...runtime.Object) *httptest.Server {
	t.Helper()
	c := fake.NewClientBuilder().
		WithScheme(testScheme).
		WithRuntimeObjects(objects...).
		WithStatusSubresource(&devwsv1alpha1.User{}, &devwsv1alpha1.Workspace{}).
		Build()
	s := New("127.0.0.1:0", c, testScheme, config.Default(),
		auth.NewSessionManager([]byte("test-key-test-key-test-key-32!")), kube)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv
}

// doGet performs a GET with a client and returns the response and body.
func doGet(t *testing.T, client *http.Client, url string) (*http.Response, string) {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp, string(raw)
}

func doGetJSON(t *testing.T, client *http.Client, url string, out any) (*http.Response, string) {
	t.Helper()
	resp, body := doGet(t, client, url)
	if out != nil {
		_ = json.Unmarshal([]byte(body), out)
	}
	return resp, body
}

func TestWorkspaceLogsStreams(t *testing.T) {
	kube := &fakeKubeClient{logs: nopCloser{strings.NewReader("line1\nline2\n")}}
	srv := newInspectionTestServer(t, kube,
		testUser("alice", "password123", "user"),
		testWorkspace("devws-alice", "main", "alice"),
		testWorkspacePod("devws-alice", "main"))
	client := loginClient(t, srv, "alice", "password123")

	resp, body := doGet(t, client, srv.URL+"/api/workspaces/main/logs")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("logs status %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("content type %q", ct)
	}
	if !strings.Contains(body, "line2") {
		t.Fatalf("body %q", body)
	}
	if kube.logReq.container != "workspace" || kube.logReq.tail != defaultLogTail {
		t.Fatalf("default params wrong: %+v", kube.logReq)
	}
	if kube.logReq.namespace != "devws-alice" || kube.logReq.pod != "main" {
		t.Fatalf("target wrong: %+v", kube.logReq)
	}
}

func TestWorkspaceLogsParams(t *testing.T) {
	kube := &fakeKubeClient{logs: nopCloser{strings.NewReader("x")}}
	srv := newInspectionTestServer(t, kube,
		testUser("alice", "password123", "user"),
		testWorkspace("devws-alice", "main", "alice"),
		testWorkspacePod("devws-alice", "main"))
	client := loginClient(t, srv, "alice", "password123")

	resp, _ := doGet(t, client, srv.URL+"/api/workspaces/main/logs?container=git-clone&tail=all&follow=1&previous=true")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("logs status %d", resp.StatusCode)
	}
	if kube.logReq.container != "git-clone" || kube.logReq.tail != 0 || !kube.logReq.follow || !kube.logReq.previous {
		t.Fatalf("params not passed through: %+v", kube.logReq)
	}
}

func TestWorkspaceLogsNotRunning(t *testing.T) {
	kube := &fakeKubeClient{}
	srv := newInspectionTestServer(t, kube,
		testUser("alice", "password123", "user"),
		testWorkspace("devws-alice", "main", "alice")) // no pod
	client := loginClient(t, srv, "alice", "password123")

	resp, _ := doGet(t, client, srv.URL+"/api/workspaces/main/logs")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("stopped workspace logs must be 409, got %d", resp.StatusCode)
	}
}

func TestWorkspaceLogsUnknownContainer(t *testing.T) {
	kube := &fakeKubeClient{}
	srv := newInspectionTestServer(t, kube,
		testUser("alice", "password123", "user"),
		testWorkspace("devws-alice", "main", "alice"),
		testWorkspacePod("devws-alice", "main"))
	client := loginClient(t, srv, "alice", "password123")

	resp, body := doGet(t, client, srv.URL+"/api/workspaces/main/logs?container=nope")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown container must be 404, got %d", resp.StatusCode)
	}
	if !strings.Contains(body, "git-clone") {
		t.Fatalf("error should list available containers: %q", body)
	}
}

func TestWorkspaceLogsForeignWorkspaceDenied(t *testing.T) {
	kube := &fakeKubeClient{}
	srv := newInspectionTestServer(t, kube,
		testUser("alice", "password123", "user"),
		testUser("mallory", "password123", "user"),
		testWorkspace("devws-alice", "main", "alice"),
		testWorkspacePod("devws-alice", "main"))
	client := loginClient(t, srv, "mallory", "password123")

	resp, _ := doGet(t, client, srv.URL+"/api/workspaces/main/logs")
	if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign workspace logs must be denied, got %d", resp.StatusCode)
	}
	resp, _ = doGet(t, client, srv.URL+"/api/workspaces/main/logs?user=alice")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-admin ?user= must be 403, got %d", resp.StatusCode)
	}
}

func TestWorkspaceEvents(t *testing.T) {
	now := metav1.Now()
	kube := &fakeKubeClient{events: []corev1.Event{
		{ // unrelated object, must be filtered out
			InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "other", Namespace: "devws-alice"},
			Reason:         "Scheduled", Type: corev1.EventTypeNormal, Message: "nope", Count: 1,
			LastTimestamp: now,
		},
		{
			InvolvedObject: corev1.ObjectReference{Kind: "Workspace", Name: "main", Namespace: "devws-alice"},
			Reason:         "WorkspaceStarting", Type: corev1.EventTypeNormal, Message: "creating workspace pod", Count: 1,
			FirstTimestamp: metav1.Time{Time: now.Add(-2 * time.Minute)}, LastTimestamp: now,
		},
		{
			InvolvedObject: corev1.ObjectReference{Kind: "PersistentVolumeClaim", Name: "main-home", Namespace: "devws-alice"},
			Reason:         "ProvisioningFailed", Type: corev1.EventTypeWarning, Message: "waiting for a volume", Count: 3,
			FirstTimestamp: metav1.Time{Time: now.Add(-1 * time.Minute)}, LastTimestamp: metav1.Time{Time: now.Add(-30 * time.Second)},
		},
	}}
	srv := newInspectionTestServer(t, kube,
		testUser("alice", "password123", "user"),
		testWorkspace("devws-alice", "main", "alice"),
		testWorkspacePod("devws-alice", "main"))
	client := loginClient(t, srv, "alice", "password123")

	var events []eventView
	resp, _ := doGetJSON(t, client, srv.URL+"/api/workspaces/main/events", &events)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("events status %d", resp.StatusCode)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 related events, got %d: %+v", len(events), events)
	}
	// Sorted oldest first: the pvc event's last timestamp is 30s ago,
	// the workspace event's is now.
	if events[0].Reason != "ProvisioningFailed" || events[1].Reason != "WorkspaceStarting" {
		t.Fatalf("wrong order: %+v", events)
	}
	if events[0].Object != "persistentvolumeclaim/main-home" || events[0].Count != 3 {
		t.Fatalf("event view wrong: %+v", events[0])
	}
}
