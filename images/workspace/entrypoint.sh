#!/bin/bash
# devws workspace entrypoint: runs sshd in the foreground.
#
# - Host keys live on the persistent volume so they stay stable across pod
#   restarts (no "host key changed" warnings).
# - authorized_keys is mounted read-only at /ssh by the controller, but
#   sshd's secure-file checks reject files on the kubelet's secret tmpfs
#   ("bad ownership or modes for directory /ssh"), so we sync it into
#   ~/.ssh (user-owned, 0700/0600). A background loop keeps the copy in
#   sync so newly registered keys apply to running workspaces.
# - Sessions run as the devws user (uid 1000).
set -euo pipefail

DEVWS_USER=devws
DEVWS_UID=1000
HOME_DIR=/home/devws
HOSTKEY_DIR="$HOME_DIR/.devws/hostkeys"
SSH_DIR="$HOME_DIR/.ssh"
SSHD_PORT=2222

mkdir -p /run/sshd

# Ensure the persistent home belongs to the devws user (fresh volumes come
# up root-owned on some provisioners; this is a no-op afterwards). sshd
# requires every path component above authorized_keys to be owned by the
# user (or root) and not group/world-writable, so normalize the mode too.
if [ "$(stat -c %u "$HOME_DIR" 2>/dev/null || echo "$DEVWS_UID")" != "$DEVWS_UID" ]; then
  echo "devws: fixing ownership of $HOME_DIR"
  chown -R "$DEVWS_UID:$DEVWS_UID" "$HOME_DIR"
fi
if [ "$(stat -c %a "$HOME_DIR" 2>/dev/null || echo 755)" != "755" ]; then
  echo "devws: fixing permissions of $HOME_DIR"
  chmod 0755 "$HOME_DIR"
fi

mkdir -p "$HOSTKEY_DIR"
chown -R "$DEVWS_UID:$DEVWS_UID" "$HOME_DIR/.devws" 2>/dev/null || true

if [ ! -f "$HOSTKEY_DIR/ssh_host_ed25519_key" ]; then
  echo "devws: generating workspace ssh host key"
  ssh-keygen -q -t ed25519 -N '' -f "$HOSTKEY_DIR/ssh_host_ed25519_key"
  chown "$DEVWS_UID:$DEVWS_UID" "$HOSTKEY_DIR/ssh_host_ed25519_key"*
fi

# Sync the controller-managed authorized_keys into ~/.ssh.
sync_keys() {
  mkdir -p "$SSH_DIR"
  chown "$DEVWS_UID:$DEVWS_UID" "$SSH_DIR"
  chmod 0700 "$SSH_DIR"
  if ! cmp -s /ssh/authorized_keys "$SSH_DIR/authorized_keys"; then
    cp /ssh/authorized_keys "$SSH_DIR/authorized_keys.next"
    chown "$DEVWS_UID:$DEVWS_UID" "$SSH_DIR/authorized_keys.next"
    chmod 0600 "$SSH_DIR/authorized_keys.next"
    mv -f "$SSH_DIR/authorized_keys.next" "$SSH_DIR/authorized_keys"
    echo "devws: updated authorized_keys"
  fi
}
sync_keys || echo "devws: warning: initial authorized_keys sync failed"
( while true; do sleep 10; sync_keys || true; done ) &

# Readiness marker for tooling that inspects the workspace.
printf 'devws workspace ready\n' > /home/devws/.devws/started

exec /usr/sbin/sshd -D -e -p "$SSHD_PORT" \
  -h "$HOSTKEY_DIR/ssh_host_ed25519_key" \
  -o "AuthorizedKeysFile=.ssh/authorized_keys" \
  -o PermitRootLogin=no \
  -o PasswordAuthentication=no \
  -o KbdInteractiveAuthentication=no \
  -o UsePAM=no \
  -o AllowUsers="$DEVWS_USER" \
  -o PrintMotd=no \
  -o X11Forwarding=no \
  -o AllowAgentForwarding=yes \
  -o AllowTcpForwarding=yes \
  -o ClientAliveInterval=60 \
  -o ClientAliveCountMax=3 \
  -o LogLevel=INFO