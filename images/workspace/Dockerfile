# devws workspace image: sshd (port 2222) + a pragmatic set of development
# toolchains. /home/devws is a persistent volume owned by the devws user.
#
# Build:  docker build -t devws-workspace:dev images/workspace

ARG UBUNTU_VERSION=24.04
FROM ubuntu:${UBUNTU_VERSION}

ARG GO_VERSION=1.27.3
ARG NODE_VERSION=22.14.0
ARG TARGETARCH

ENV DEBIAN_FRONTEND=noninteractive \
    HOME=/home/devws \
    TERM=xterm-256color

# Base packages + toolchains from apt.
RUN apt-get update && apt-get install -y --no-install-recommends \
      openssh-server \
      git \
      curl \
      wget \
      jq \
      zip \
      unzip \
      tar \
      gzip \
      ca-certificates \
      gnupg \
      build-essential \
      pkg-config \
      make \
      cmake \
      autoconf \
      python3 \
      python3-pip \
      python3-venv \
      sudo \
      vim \
      less \
      procps \
      net-tools \
      iproute2 \
      dnsutils \
      openssh-client \
      tzdata \
      locales \
      rsync \
      tmux \
      zsh \
    && rm -rf /var/lib/apt/lists/* \
    && locale-gen en_US.UTF-8

# Go (official tarball).
RUN case "${TARGETARCH}" in \
      amd64) GOARCH=amd64 ;; \
      arm64) GOARCH=arm64 ;; \
      *) echo "unsupported architecture: ${TARGETARCH}" >&2; exit 1 ;; \
    esac && \
    curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${GOARCH}.tar.gz" \
      | tar -C /usr/local -xz
ENV PATH=/usr/local/go/bin:${PATH}

# Node.js (official tarball).
RUN case "${TARGETARCH}" in \
      amd64) NODE_ARCH=x64 ;; \
      arm64) NODE_ARCH=arm64 ;; \
      *) echo "unsupported architecture: ${TARGETARCH}" >&2; exit 1 ;; \
    esac && \
    curl -fsSL "https://nodejs.org/dist/v${NODE_VERSION}/node-v${NODE_VERSION}-linux-${NODE_ARCH}.tar.xz" \
      | tar -xJ -C /usr/local --strip-components=1
ENV PATH=/usr/local/bin:${PATH}

# The devws development user (uid 1000). ubuntu:24.04 images ship a
# default 'ubuntu' user with this uid; remove it first. The password is
# set to '*' (no valid password, but NOT locked): sshd with UsePAM=no
# refuses locked accounts ("User devws not allowed because account is
# locked") even for publickey auth.
RUN userdel -r ubuntu 2>/dev/null || true \
    && useradd -m -u 1000 -s /bin/bash devws \
    && usermod -p '*' devws \
    && echo 'devws ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/devws \
    && chmod 0440 /etc/sudoers.d/devws

COPY entrypoint.sh /usr/local/bin/devws-entrypoint.sh
RUN chmod 0755 /usr/local/bin/devws-entrypoint.sh

# sshd listens on 2222 (the gateway maps port 22 -> 2222). authorized_keys
# is mounted from a Secret by the workspace controller.
EXPOSE 2222

USER root
WORKDIR /home/devws
ENTRYPOINT ["/usr/local/bin/devws-entrypoint.sh"]