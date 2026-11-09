# devws apiserver image: control plane (API + controllers + SSH gateway)
# plus the devws CLI for the admin bootstrap job.
#
# Build from the repo root:
#   docker build -f images/apiserver/Dockerfile -t devws-apiserver:dev .

# --- stage 1: build the web UI -------------------------------------------
FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json* ./
RUN npm install --no-fund --no-audit
COPY web/ ./
RUN npm run build

# --- stage 2: build the Go binaries --------------------------------------
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY api/ api/
COPY internal/ internal/
COPY cmd/ cmd/
# The web assets are embedded at build time (built in the web stage).
COPY --from=web /src/internal/web/dist internal/web/dist/
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/devws-apiserver ./cmd/devws-apiserver \
 && CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/devws ./cmd/devws

# --- stage 3: final image ------------------------------------------------
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates tzdata \
    && rm -rf /var/lib/apt/lists/* \
    && useradd -m -u 65532 -s /usr/sbin/nologin devws
COPY --from=build /out/devws-apiserver /usr/local/bin/devws-apiserver
COPY --from=build /out/devws /usr/local/bin/devws
USER 65532:65532
EXPOSE 8080 2222
ENTRYPOINT ["/usr/local/bin/devws-apiserver"]