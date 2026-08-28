# OpenTAMS Makefile — convenience wrapper over the same `go` and
# `golangci-lint` commands documented in CONTRIBUTING.md and run in CI.
#
# Targets are intentionally thin pass-throughs so contributors who prefer
# to run the underlying commands directly can do so without diverging from
# CI behaviour.

.PHONY: help compile build build-server build-cli install-cli vet test test-short coverage lint fmt vuln vuln-gate integration perf \
        docker-build docker-build-local ci tools-install clean \
        release-check snapshot release-dry-run \
        api-lint api-bundle api-gen api-check \
        env stack stack-down run install-demo-deps

# --- knobs ------------------------------------------------------------------
GO              ?= go
GOLANGCI_LINT   ?= golangci-lint
GOLANGCI_VERSION?= v2.11.0
GORELEASER      ?= goreleaser
GORELEASER_VERSION ?= v2.11.0
GOVULNCHECK     ?= golang.org/x/vuln/cmd/govulncheck@v1.1.4
DOCKER          ?= docker
IMAGE           ?= opentams:dev
DOCKERFILE      ?= build/Dockerfile
BIN_DIR         ?= dist
INSTALL_DIR     ?= /usr/local/bin
COMPOSE_FILE    ?= deployments/docker/docker-compose.yml
REDOCLY         ?= npx --yes @redocly/cli@1.25.11
API_SPEC        ?= api/opentams-api-v1.yaml
API_BUNDLE      ?= api/opentams-api-bundled.yaml

# Build-time version metadata for the binaries (git-derived; falls back when
# git or tags are unavailable). Both cmd/opentams and cmd/tamsctl expose the
# same main.version/commit/date symbols, so one ldflags set serves both.
VERSION         ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT          ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE            ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS         := -s -w \
                   -X main.version=$(VERSION) \
                   -X main.commit=$(COMMIT) \
                   -X main.date=$(DATE)

# Same scope as the `Test` workflow and CONTRIBUTING.md: skip ./gen/...
# because it is generated and not the target of unit testing.
PKGS            ?= ./pkg/... ./internal/... ./cmd/...

# Multi-arch verification platforms (matches `Build` workflow).
PLATFORMS       ?= linux/amd64,linux/arm64

# --- help -------------------------------------------------------------------
help: ## Show this help
	@awk 'BEGIN {FS = ":.*##"; printf "Targets:\n"} \
	      /^[a-zA-Z_-]+:.*?##/ {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}' \
	      $(MAKEFILE_LIST)

# --- build / verify ---------------------------------------------------------
compile: ## Compile every package; emits no binaries (verify-only)
	$(GO) build ./...

build: build-server build-cli ## Build both binaries (server + cli) into $(BIN_DIR)

build-server: ## Build the opentams server into $(BIN_DIR)
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/opentams ./cmd/opentams
	@echo "built $(BIN_DIR)/opentams $(VERSION)"

build-cli: ## Build the tamsctl operator CLI into $(BIN_DIR)
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/tamsctl ./cmd/tamsctl
	@echo "built $(BIN_DIR)/tamsctl $(VERSION)"

install-cli: ## Install the already-built $(BIN_DIR)/tamsctl into $(INSTALL_DIR) (run 'make build-cli' first; may need sudo)
	@test -x $(BIN_DIR)/tamsctl || { echo "no $(BIN_DIR)/tamsctl — run 'make build-cli' first"; exit 1; }
	@mkdir -p $(INSTALL_DIR)
	install -m 0755 $(BIN_DIR)/tamsctl $(INSTALL_DIR)/tamsctl
	@echo "installed $(INSTALL_DIR)/tamsctl — run 'tamsctl version' to verify"

vet: ## Run go vet
	$(GO) vet ./...

test: ## Unit tests with race detector (matches CI)
	$(GO) test -race -count=1 $(PKGS)

test-short: ## Quick unit tests, no race detector
	$(GO) test -count=1 $(PKGS)

coverage: ## Unit tests with coverage profile and per-func summary
	$(GO) test -race -count=1 -coverprofile=coverage.out $(PKGS)
	$(GO) tool cover -func=coverage.out | tail -20
	@echo
	@$(GO) tool cover -func=coverage.out | awk '/^total:/ {print "Total: " $$3}'

# The linter version is part of the gate, not a detail: different minors
# report different findings, so a local run against a different binary than
# CI installs is not a check, it is a guess. These recipes use whatever is on
# PATH only when its version matches GOLANGCI_VERSION, and otherwise fall back
# to `go run` at the pinned version. Run `make tools-install` to get the fast
# path.
GOLANGCI_PINNED = $(GO) run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)
define golangci
	@if command -v $(GOLANGCI_LINT) >/dev/null 2>&1 && \
	   $(GOLANGCI_LINT) version 2>/dev/null | grep -q "$(patsubst v%,%,$(GOLANGCI_VERSION))"; then \
	  $(GOLANGCI_LINT) $(1); \
	else \
	  echo "note: golangci-lint $(GOLANGCI_VERSION) not on PATH, running via go run (slower)"; \
	  $(GOLANGCI_PINNED) $(1); \
	fi
endef

lint: ## Run golangci-lint with the strict project config
	$(call golangci,run --timeout=5m)

fmt: ## Apply gofmt and goimports via golangci-lint formatters
	$(call golangci,fmt)

vuln-gate: ## Fail on reachable advisories not accepted in .govulncheck-allow.yaml
	./scripts/govulncheck-gate.sh

vuln: ## Report vulnerabilities govulncheck can reach from our code
	# Exits non-zero only on advisories reachable from OpenTAMS code.
	# Advisories in modules we require but never call are reported in the
	# summary and do not fail the target -- upgrading for those is a
	# judgement call, not a gate. Current status and the remaining
	# blocked upgrades: docs/vulnerability-status.md
	$(GO) run $(GOVULNCHECK) ./...

integration: ## Integration tests (testcontainers; also runs on every PR)
	$(GO) test -tags=integration -race -count=1 -timeout=20m ./...

perf: ## Performance budgets (testcontainers; runs nightly in CI)
	# No -race: budgets are calibrated for an unraced binary. Each budget
	# takes an OPENTAMS_PERF_*_MS override when a slower machine needs one;
	# the defaults live next to the assertions.
	$(GO) test -tags=perf -count=1 -timeout=20m ./...

# --- docker ------------------------------------------------------------------
docker-build: ## Multi-arch Docker build verification (no push, no load)
	$(DOCKER) buildx build \
	    --platform $(PLATFORMS) \
	    --file $(DOCKERFILE) \
	    --tag $(IMAGE) \
	    .

docker-build-local: ## Single-arch Docker build, loaded into the local engine
	$(DOCKER) buildx build \
	    --load \
	    --file $(DOCKERFILE) \
	    --tag $(IMAGE) \
	    .

# --- release --------------------------------------------------------
# These targets exercise the goreleaser pipeline locally without ever
# pushing to a registry or creating a GitHub Release. They require
# `goreleaser` and `docker buildx` on your PATH; cosign/syft are only
# needed for the actual release run, which happens in CI.
release-check: ## Validate .goreleaser.yml against the current schema
	$(GORELEASER) check

snapshot: ## Build a local snapshot release (no push, no sign, no publish)
	$(GORELEASER) release --snapshot --clean --skip=publish,sign

release-dry-run: ## Full release dry-run (builds + dockers, never publishes)
	$(GORELEASER) release --snapshot --clean --skip=publish,sign,announce

# --- terraform ---------------------------------------------------------------
lint-tf: ## Scan Terraform (AWS) with tfsec, trivy config, and checkov
	tfsec deployments/terraform/aws
	trivy config deployments/terraform/aws
	checkov -d deployments/terraform/aws

# --- meta --------------------------------------------------------------------
# --- api contract -----------------------------------------------------------
api-lint: ## Lint the OpenAPI contract against redocly.yaml
	$(REDOCLY) lint

api-bundle: ## Regenerate the committed bundled spec from the source spec
	$(REDOCLY) bundle $(API_SPEC) -o $(API_BUNDLE)

api-gen: ## Regenerate gen/api from the bundled spec
	$(GO) generate ./...

api-check: ## Fail if the committed bundle or generated code is stale
	@tmp=$$(mktemp -d); \
	cp $(API_BUNDLE) $$tmp/bundle.yaml; \
	cp -R gen $$tmp/gen; \
	rc=0; \
	$(MAKE) --no-print-directory api-bundle >/dev/null || { echo "api-bundle FAILED"; rc=1; }; \
	$(MAKE) --no-print-directory api-gen >/dev/null || { echo "api-gen FAILED — regeneration is broken, not just stale"; rc=1; }; \
	if [ $$rc -eq 0 ]; then \
	  diff -q $$tmp/bundle.yaml $(API_BUNDLE) >/dev/null || { echo "stale: $(API_BUNDLE) — run 'make api-bundle'"; rc=1; }; \
	  diff -qr $$tmp/gen gen >/dev/null || { echo "stale: gen/ — run 'make api-gen'"; rc=1; }; \
	fi; \
	cp $$tmp/bundle.yaml $(API_BUNDLE); \
	rm -rf gen && cp -R $$tmp/gen gen; \
	rm -rf $$tmp; \
	if [ $$rc -eq 0 ]; then echo "api contract, bundle, and generated code agree"; fi; \
	exit $$rc

ci: compile vet lint vuln-gate test ## Run the full CI gate locally (no integration)

tools-install: ## Install pinned golangci-lint, goreleaser, and wire up the repo pre-commit hook
	$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)
	$(GO) install github.com/goreleaser/goreleaser/v2@$(GORELEASER_VERSION)
	@# Point git at the tracked .githooks/ directory so .githooks/pre-commit
	@# fires on every `git commit`. Idempotent — re-running tools-install is
	@# safe. Bypassable per-commit via `git commit --no-verify` if needed.
	git config core.hooksPath .githooks
	@echo "pre-commit hook activated (.githooks/pre-commit). Bypass with 'git commit --no-verify'."

clean: ## Remove generated test artefacts
	rm -rf coverage.out coverage.html dist/

# --- local dev ---------------------------------------------------------------
# `stack` brings up just the storage dependencies (postgres + minio) in
# docker compose; the server runs on the host so its presigned URLs use
# `localhost:9000`, which is reachable from both the server and from
# clients on the host. `run` chains the two.
#
# `env` is a prerequisite of `stack` so `make run` from a fresh clone
# bootstraps the .env from the template; existing .env files (with the
# operator's edits) are never clobbered.
env: ## Provision deployments/docker/.env from the template if missing
	@if [ ! -f deployments/docker/.env ]; then \
	  cp deployments/docker/.env.example deployments/docker/.env; \
	  echo "Created deployments/docker/.env from template — review before production use."; \
	fi

stack: env ## Bring up the dev storage stack (postgres + minio) via docker compose
	# First pass brings up every service (including the minio-init one-shot
	# that creates the bucket and exits 0). --remove-orphans cleans up
	# anything left over from previous compose schemas (e.g. the legacy
	# `migrate` service).
	$(DOCKER) compose -f $(COMPOSE_FILE) up -d --remove-orphans
	# Second pass waits on healthchecks of the long-running services only.
	# We can't pass --wait on the first call because it blocks forever on
	# the minio-init container, which exits 0 instead of staying running.
	$(DOCKER) compose -f $(COMPOSE_FILE) up -d --wait postgres minio

stack-down: ## Tear down the dev storage stack and drop volumes
	$(DOCKER) compose -f $(COMPOSE_FILE) down -v

run: stack ## Start the OpenTAMS dev server on the host (brings stack up first)
	./scripts/start.sh

install-demo-deps: ## Install host-side demo deps (ffmpeg, vlc) via the platform's package manager
	./scripts/install-demo-deps.sh
