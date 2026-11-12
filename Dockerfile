# syntax=docker/dockerfile:1
# PK95 Monitoring – one container per customer.
#
#   docker buildx build --platform linux/amd64,linux/arm64 --build-arg VERSION=1.0.0 -t pk95monitoring .
#
# Needs BuildKit (cache mounts, COPY heredoc): Docker Desktop and the docker-ce packages have it;
# with Debian's docker.io install docker-buildx as well, otherwise "docker build" and
# "docker compose build" fall back to the legacy builder and fail.
#
# The Go stages run on the build platform and cross-compile (CGO_ENABLED=0), so only the final
# stage (apk add) runs under emulation. The agents stage does not depend on the target platform,
# so a multi-arch build compiles the agents only once and shares them.

ARG GO_VERSION=1.26
ARG ALPINE_VERSION=3.22

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS base
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-trimpath
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .

# Agent binaries for every platform the server distributes (names: internal/agentdist.FileName).
FROM base AS agents
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
	set -e; mkdir -p /out/agents; \
	for target in linux/amd64 linux/arm64 windows/amd64; do \
		os="${target%/*}"; arch="${target#*/}"; ext=""; \
		if [ "$os" = windows ]; then ext=".exe"; fi; \
		GOOS="$os" GOARCH="$arch" go build -ldflags "-s -w -X main.version=${VERSION}" \
			-o "/out/agents/pk95agent-${os}-${arch}${ext}" ./cmd/pk95agent; \
	done

# Server for the target platform.
FROM base AS server
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
	GOOS="$TARGETOS" GOARCH="$TARGETARCH" go build -ldflags "-s -w -X main.version=${VERSION}" \
		-o /out/pk95monitoring .

FROM alpine:${ALPINE_VERSION}
ARG VERSION=dev
LABEL org.opencontainers.image.title="PK95 Monitoring" \
	org.opencontainers.image.description="Monitoring für Proxmox VE, Linux/Windows (Agent), SNMP-Geräte und Netzwerk-Checks" \
	org.opencontainers.image.source="https://github.com/PhilippKuhlmann/pk95monitoring" \
	org.opencontainers.image.version="${VERSION}"

RUN apk add --no-cache ca-certificates tzdata \
	&& addgroup -S -g 10001 pk95mon \
	&& adduser -S -D -H -u 10001 -G pk95mon -h /data -s /sbin/nologin pk95mon \
	&& mkdir -p /data \
	&& chown 10001:10001 /data \
	&& chmod 0750 /data

COPY --from=agents /out/agents/ /opt/pk95monitoring/agents/
COPY --from=server /out/pk95monitoring /usr/local/bin/pk95monitoring

# Health check: GET /api/session on the local listener – HTTPS with the (usually self-signed)
# certificate, or plain HTTP with PK95MON_NO_TLS. Host and port follow PK95MON_LISTEN; the
# NO_TLS values are the ones main.go accepts (1, true, yes, on). The heredoc (quoted, so the
# builder expands nothing) needs BuildKit, like the cache mounts above.
COPY --chmod=0755 <<'EOF' /usr/local/bin/pk95monitoring-healthcheck
#!/bin/sh
listen="${PK95MON_LISTEN:-:8443}"
port="${listen##*:}"
host="${listen%:*}"
case "$host" in
"" | 0.0.0.0 | "[::]") host=127.0.0.1 ;;
esac
scheme=https
case "$(printf '%s' "$PK95MON_NO_TLS" | tr -d ' ' | tr 'A-Z' 'a-z')" in
1 | true | yes | on) scheme=http ;;
esac
exec wget -q -T 5 --no-check-certificate -O /dev/null "${scheme}://${host}:${port}/api/session"
EOF

ENV PK95MON_LISTEN=":8443" \
	PK95MON_HTTP_REDIRECT="" \
	PK95MON_DATA_DIR="/data" \
	PK95MON_AGENT_DIR="/opt/pk95monitoring/agents" \
	PK95MON_PUBLIC_URL="" \
	PK95MON_INSTANCE="" \
	PK95MON_DEMO="" \
	PK95MON_NO_TLS=""

USER 10001:10001
VOLUME ["/data"]
EXPOSE 8443
HEALTHCHECK --interval=30s --timeout=10s --start-period=30s --retries=3 \
	CMD ["/usr/local/bin/pk95monitoring-healthcheck"]
ENTRYPOINT ["/usr/local/bin/pk95monitoring"]
