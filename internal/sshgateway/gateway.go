// Package sshgateway implements the devws SSH entry point. It authenticates
// users with their registered public keys and routes raw TCP connections
// ("direct-tcpip" channels, i.e. jump-host style ProxyJump traffic) to the
// user's own workspace pods. Local VS Code Remote-SSH and JetBrains Gateway
// connect through this gateway.
package sshgateway

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	devwsv1alpha1 "github.com/celerbern/k8s-dev-ws-go/api/v1alpha1"
	"github.com/celerbern/k8s-dev-ws-go/internal/controllers"
)

// HostKeySecretName is the Secret holding the gateway's SSH host key.
const HostKeySecretName = "devws-ssh-gateway-hostkey"

// HostKeyDataKey is the Secret data key of the PEM-encoded host key.
const HostKeyDataKey = "hostkey"

// Gateway is a controller-runnable SSH server.
type Gateway struct {
	addr     string
	client   client.Client
	listener net.Listener
	config   *ssh.ServerConfig
}

// New builds a Gateway, loading (or creating) the host key Secret. The
// client must be usable before the manager's cache starts (pass a direct,
// uncached client when constructing in main); it is also used for runtime
// lookups so key/permission changes apply immediately.
func New(ctx context.Context, c client.Client, platformNamespace, addr string) (*Gateway, error) {
	signer, err := loadOrCreateHostKey(ctx, c, platformNamespace)
	if err != nil {
		return nil, fmt.Errorf("loading ssh gateway host key: %w", err)
	}
	g := &Gateway{addr: addr, client: c}
	g.config = &ssh.ServerConfig{
		ServerVersion: "SSH-2.0-devws-gateway",
		PublicKeyCallback: func(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			// Deliberately self-contained: the construction context may be
			// short-lived; authentication runs for the server's lifetime.
			authCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := g.authenticate(authCtx, conn.User(), key); err != nil {
				return nil, err
			}
			return &ssh.Permissions{}, nil
		},
	}
	g.config.AddHostKey(signer)
	return g, nil
}

// NeedLeaderElection implements the controller-runtime leader-election
// runnable interface; the gateway runs on every replica.
func (g *Gateway) NeedLeaderElection() bool { return false }

// Start runs the SSH server until the context is cancelled.
func (g *Gateway) Start(ctx context.Context) error {
	ln, err := net.Listen("tcp", g.addr)
	if err != nil {
		return err
	}
	g.listener = ln
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go g.handleConn(ctx, conn)
	}
}

func (g *Gateway) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second)) // handshake timeout
	sconn, chans, reqs, err := ssh.NewServerConn(conn, g.config)
	if err != nil {
		return
	}
	defer sconn.Close()
	_ = conn.SetDeadline(time.Time{})
	go ssh.DiscardRequests(reqs)
	for newChannel := range chans {
		go g.handleChannel(ctx, sconn, newChannel)
	}
}

// directTCPPayload is the SSH "direct-tcpip" channel payload.
type directTCPPayload struct {
	Host              string
	Port              uint32
	OriginatorAddress string
	OriginatorPort    uint32
}

func (g *Gateway) handleChannel(ctx context.Context, sconn *ssh.ServerConn, newChannel ssh.NewChannel) {
	if newChannel.ChannelType() != "direct-tcpip" {
		msg := "devws gateway: only TCP forwarding is supported. Connect to a workspace host, not the gateway itself (see `devws ssh-config`)."
		_ = newChannel.Reject(ssh.UnknownChannelType, msg)
		return
	}
	var req directTCPPayload
	if err := ssh.Unmarshal(newChannel.ExtraData(), &req); err != nil {
		_ = newChannel.Reject(ssh.Prohibited, "malformed direct-tcpip request")
		return
	}

	username := sconn.User()
	target, err := g.resolveTarget(ctx, username, req.Host, int(req.Port))
	if err != nil {
		_ = newChannel.Reject(ssh.Prohibited, err.Error())
		return
	}

	dialer := net.Dialer{Timeout: 10 * time.Second}
	upstream, err := dialer.DialContext(ctx, "tcp", target)
	if err != nil {
		_ = newChannel.Reject(ssh.ConnectionFailed, fmt.Sprintf("workspace unreachable: %v", err))
		return
	}

	channel, requests, err := newChannel.Accept()
	if err != nil {
		_ = upstream.Close()
		return
	}
	go ssh.DiscardRequests(requests)

	// Pipe the workspace pod's raw SSH stream through the gateway.
	go pipe(upstream, channel)
	go pipe(channel, upstream)
}

// pipe copies in one direction and half-closes the destination afterwards so
// graceful connection teardown works for both ssh channels and TCP conns.
func pipe(dst io.WriteCloser, src io.Reader) {
	_, _ = io.Copy(dst, src)
	if c, ok := dst.(interface{ CloseWrite() error }); ok {
		_ = c.CloseWrite()
	} else {
		_ = dst.Close()
	}
}

// resolveTarget maps a synthetic workspace hostname to the workspace pod
// address, enforcing that the authenticated user only reaches their own
// namespace.
func (g *Gateway) resolveTarget(ctx context.Context, username, host string, port int) (string, error) {
	if !strings.HasSuffix(host, controllers.SSHDomainSuffix) {
		return "", fmt.Errorf("unknown destination %q (devws routes <workspace>.<namespace>%s)", host, controllers.SSHDomainSuffix)
	}
	rest := strings.TrimSuffix(host, controllers.SSHDomainSuffix)
	parts := strings.Split(rest, ".")
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid destination %q", host)
	}
	wsName, namespace := parts[0], parts[1]
	if namespace != controllers.NamespaceFor(username) {
		return "", fmt.Errorf("access denied: workspace %q does not belong to user %q", wsName, username)
	}

	var pod corev1.Pod
	if err := g.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: wsName}, &pod); err != nil {
		return "", fmt.Errorf("workspace %q not found", wsName)
	}
	if pod.Labels[controllers.LabelWorkspace] != wsName {
		return "", fmt.Errorf("access denied")
	}
	if pod.Status.PodIP == "" {
		return "", fmt.Errorf("workspace %q is still starting", wsName)
	}
	if port == 22 {
		port = controllers.WorkspaceSSHPort
	}
	return net.JoinHostPort(pod.Status.PodIP, strconv.Itoa(port)), nil
}

// authenticate verifies the presented key against the user's registered keys.
func (g *Gateway) authenticate(ctx context.Context, username string, key ssh.PublicKey) error {
	if username == "" {
		return errors.New("specify your devws username as the ssh user")
	}
	var user devwsv1alpha1.User
	if err := g.client.Get(ctx, types.NamespacedName{Name: username}, &user); err != nil {
		return fmt.Errorf("unknown user %q", username)
	}
	if user.Spec.Disabled {
		return fmt.Errorf("user %q is disabled", username)
	}
	for _, k := range user.Spec.SSHPublicKeys {
		parsed, _, _, _, err := ssh.ParseAuthorizedKey([]byte(k.Key))
		if err != nil {
			continue
		}
		if keysEqual(parsed, key) {
			return nil
		}
	}
	return fmt.Errorf("key is not registered for user %q (add it with `devws key add` or the web UI)", username)
}

// keysEqual compares two public keys by wire format.
func keysEqual(a, b ssh.PublicKey) bool {
	return a.Type() == b.Type() && bytes.Equal(a.Marshal(), b.Marshal())
}

// loadOrCreateHostKey loads the gateway host key, generating and storing a
// fresh ed25519 key on first start so host keys survive restarts.
func loadOrCreateHostKey(ctx context.Context, c client.Client, namespace string) (ssh.Signer, error) {
	sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: HostKeySecretName}}
	err := c.Get(ctx, client.ObjectKeyFromObject(sec), sec)
	if err == nil {
		if pem, ok := sec.Data[HostKeyDataKey]; ok {
			return ssh.ParsePrivateKey(pem)
		}
		return nil, fmt.Errorf("secret %s/%s exists but has no %q data", namespace, HostKeySecretName, HostKeyDataKey)
	}
	if !apierrors.IsNotFound(err) {
		return nil, err
	}

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, err
	}
	block, err := ssh.MarshalPrivateKey(priv, "devws ssh gateway host key")
	if err != nil {
		return nil, err
	}
	sec.Data = map[string][]byte{HostKeyDataKey: pem.EncodeToMemory(block)}
	if err := c.Create(ctx, sec); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return nil, err
		}
		// Lost a create race; re-read.
		if err := c.Get(ctx, client.ObjectKeyFromObject(sec), sec); err == nil {
			if pemBytes, ok := sec.Data[HostKeyDataKey]; ok {
				return ssh.ParsePrivateKey(pemBytes)
			}
		}
		return nil, err
	}
	return signer, nil
}
