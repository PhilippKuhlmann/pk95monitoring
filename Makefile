# PK95 Monitoring – development and release helpers.
#   make test | vet | fmt | check | build | agents | demo | docker

GO        ?= go
VERSION   ?= $(patsubst v%,%,$(shell git describe --tags --always --dirty 2>/dev/null || echo dev))
LDFLAGS   := -s -w -X main.version=$(VERSION)
IMAGE     ?= ghcr.io/philippkuhlmann/pk95monitoring
PLATFORMS ?= linux/amd64,linux/arm64
# Extra flags for docker buildx, e.g. DOCKER_FLAGS=--push, or --load with a single platform.
DOCKER_FLAGS ?=
# Platforms the server distributes (internal/agentdist.Targets).
AGENT_TARGETS := linux/amd64 linux/arm64 windows/amd64

.PHONY: all test vet fmt check build agents demo docker clean

all: check test build agents

test:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

fmt:
	gofmt -w .

# What CI checks besides the tests. The agent has per-OS files (*_linux.go, *_windows.go) that
# a plain "go vet" on the build host never sees, so they are vetted for every agent OS too.
check: vet
	@set -e; for os in $(sort $(foreach t,$(AGENT_TARGETS),$(firstword $(subst /, ,$(t))))); do \
		echo "GOOS=$$os go vet ./cmd/pk95agent/... ./internal/agent/..."; \
		CGO_ENABLED=0 GOOS="$$os" $(GO) vet ./cmd/pk95agent/... ./internal/agent/...; \
	done
	@files="$$(gofmt -l .)"; if [ -n "$$files" ]; then echo "not gofmt-formatted:"; echo "$$files"; exit 1; fi
	node --check web/app.js

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o pk95monitoring .

agents:
	@mkdir -p dist/agents
	@set -e; for target in $(AGENT_TARGETS); do \
		os="$${target%/*}"; arch="$${target#*/}"; ext=""; \
		if [ "$$os" = windows ]; then ext=".exe"; fi; \
		out="dist/agents/pk95agent-$$os-$$arch$$ext"; \
		echo "$$out ($(VERSION))"; \
		CGO_ENABLED=0 GOOS="$$os" GOARCH="$$arch" $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o "$$out" ./cmd/pk95agent; \
	done

# Demo with simulated Proxmox VE, SNMP devices and agents: http://127.0.0.1:18096
# (setup code in data/demo/setup-code).
demo: agents
	@mkdir -p data/demo
	PK95MON_DEMO=1 PK95MON_NO_TLS=1 PK95MON_LISTEN=127.0.0.1:18096 PK95MON_HTTP_REDIRECT= \
	PK95MON_DATA_DIR=data/demo PK95MON_AGENT_DIR=dist/agents \
		$(GO) run -ldflags "-X main.version=$(VERSION)" .

docker:
	docker buildx build --platform $(PLATFORMS) --build-arg VERSION=$(VERSION) \
		-t $(IMAGE):$(VERSION) $(DOCKER_FLAGS) .

clean:
	rm -rf dist pk95monitoring pk95agent pk95agent.exe
