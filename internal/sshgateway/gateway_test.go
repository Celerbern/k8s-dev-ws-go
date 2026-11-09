package sshgateway

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	corev1 "k8s.io/api/core/v1"
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

// fakePodListener stands in for the workspace pod's sshd: it echoes data.
func fakePodListener(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(conn)
		}
	}()
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	var port int
	_, _ = fmt.Sscanf(portStr, "%d", &port)
	return port
}

func startGateway(t *testing.T, objects ...runtime.Object) string {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(testScheme).WithRuntimeObjects(objects...).Build()
	gw, err := New(context.Background(), c, "devws-system", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	errCh := make(chan error, 1)
	go func() { errCh <- gw.Start(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if gw.listener != nil {
			return gw.listener.Addr().String()
		}
		select {
		case err := <-errCh:
			t.Fatalf("gateway exited: %v", err)
		case <-time.After(10 * time.Millisecond):
		}
	}
	t.Fatal("gateway did not start listening")
	return ""
}

// dialWorkspace simulates what VS Code Remote-SSH does with ProxyJump:
// authenticate to the gateway, then open a direct-tcpip channel to the
// synthetic workspace host.
func dialWorkspace(t *testing.T, gatewayAddr, username string, signer ssh.Signer, host string, port int) (ssh.Channel, error) {
	t.Helper()
	clientConfig := &ssh.ClientConfig{
		User:            username,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}
	conn, err := ssh.Dial("tcp", gatewayAddr, clientConfig)
	if err != nil {
		return nil, fmt.Errorf("dialing gateway: %w", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	payload := ssh.Marshal(struct {
		Host              string
		Port              uint32
		OriginatorAddress string
		OriginatorPort    uint32
	}{Host: host, Port: uint32(port)})
	ch, reqs, err := conn.OpenChannel("direct-tcpip", payload)
	if err != nil {
		return nil, fmt.Errorf("opening direct-tcpip: %w", err)
	}
	go ssh.DiscardRequests(reqs)
	return ch, nil
}

func newKeyPair(t *testing.T) (ssh.Signer, string) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return signer, string(ssh.MarshalAuthorizedKey(signer.PublicKey()))
}

func TestGatewayRoutesToOwnWorkspace(t *testing.T) {
	signer, pubLine := newKeyPair(t)
	podPort := fakePodListener(t)

	user := &devwsv1alpha1.User{}
	user.Name = "alice"
	user.Spec.SSHPublicKeys = []devwsv1alpha1.SSHKey{{Name: "k", Key: pubLine}}
	pod := &corev1.Pod{}
	pod.Name = "ws1"
	pod.Namespace = "devws-alice"
	pod.Labels = map[string]string{"devws.io/workspace": "ws1"}
	pod.Status.Phase = corev1.PodRunning
	pod.Status.PodIP = "127.0.0.1"

	addr := startGateway(t, user, pod)

	ch, err := dialWorkspace(t, addr, "alice", signer, "ws1.devws-alice.ws.internal", podPort)
	if err != nil {
		t.Fatalf("routing to own workspace: %v", err)
	}
	msg := []byte("hello-workspace\n")
	if _, err := ch.Write(msg); err != nil {
		t.Fatal(err)
	}
	got := make(chan []byte, 1)
	go func() {
		buf := make([]byte, len(msg))
		n, _ := io.ReadFull(ch, buf)
		got <- buf[:n]
	}()
	select {
	case buf := <-got:
		if string(buf) != string(msg) {
			t.Fatalf("echo mismatch: %q != %q", buf, msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for echo from workspace")
	}
}

func TestGatewayRejectsOtherUsersWorkspace(t *testing.T) {
	_, alicePub := newKeyPair(t)
	bobSigner, bobPub := newKeyPair(t)
	podPort := fakePodListener(t)

	alice := &devwsv1alpha1.User{}
	alice.Name = "alice"
	alice.Spec.SSHPublicKeys = []devwsv1alpha1.SSHKey{{Name: "k", Key: alicePub}}
	bob := &devwsv1alpha1.User{}
	bob.Name = "bob"
	bob.Spec.SSHPublicKeys = []devwsv1alpha1.SSHKey{{Name: "k", Key: bobPub}}
	// The pod belongs to alice's namespace.
	pod := &corev1.Pod{}
	pod.Name = "ws1"
	pod.Namespace = "devws-alice"
	pod.Labels = map[string]string{"devws.io/workspace": "ws1"}
	pod.Status.Phase = corev1.PodRunning
	pod.Status.PodIP = "127.0.0.1"

	addr := startGateway(t, alice, bob, pod)

	// bob authenticates fine, but routing to alice's workspace must fail.
	if _, err := dialWorkspace(t, addr, "bob", bobSigner, "ws1.devws-alice.ws.internal", podPort); err == nil {
		t.Fatal("bob must not be able to reach alice's workspace")
	}
}

func TestGatewayRejectsUnregisteredKey(t *testing.T) {
	signer, _ := newKeyPair(t)
	alice := &devwsv1alpha1.User{}
	alice.Name = "alice"
	addr := startGateway(t, alice)

	clientConfig := &ssh.ClientConfig{
		User:            "alice",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}
	if _, err := ssh.Dial("tcp", addr, clientConfig); err == nil {
		t.Fatal("unregistered key must be rejected at the handshake")
	}
}
