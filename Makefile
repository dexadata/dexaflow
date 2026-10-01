# Leoflow Makefile
# All targets assume execution from the repository root.

SHELL := /usr/bin/env bash
.DEFAULT_GOAL := help

# ─── Tool versions (pinned; see ADR 0012 / 0014) ───
GOLANGCI_LINT_VERSION ?= v2.12.2
GOVULNCHECK_VERSION   ?= latest
MIGRATE_VERSION       ?= latest
SQLC_VERSION          ?= latest
# Go Report Card checkers (the site + its CLI were retired in 2026; we run the
# same checks with maintained tools via scripts/reportcard.sh — see ADR 0012).
GOCYCLO_VERSION       ?= latest
INEFFASSIGN_VERSION   ?= latest
MISSPELL_VERSION      ?= latest
OAPI_CODEGEN_VERSION  ?= v2.8.0
# changie writes the per-PR changelog fragments (see .changie.yaml).
CHANGIE_VERSION       ?= v1.24.0

# ─── Pinned Airflow UI (see ADR 0017 / docs/ui-compatibility.md) ───
AIRFLOW_UI_VERSION ?= 3.2.1
UI_ASSETS_DIR      := internal/ui/assets

# ─── Paths ───
BIN_DIR       := bin
CLI_BINARY    := $(BIN_DIR)/leoflow
SERVER_BINARY := $(BIN_DIR)/leoflow-server
AGENT_BINARY  := $(BIN_DIR)/leoflow-agent
MCP_BINARY    := $(BIN_DIR)/leoflow-mcp

# ─── Build metadata (embedded via internal/version) ───
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
GIT_COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
VERSION_PKG := github.com/neochaotic/leoflow/internal/version
LDFLAGS := -s -w \
	-X '$(VERSION_PKG).version=$(VERSION)' \
	-X '$(VERSION_PKG).gitCommit=$(GIT_COMMIT)' \
	-X '$(VERSION_PKG).buildDate=$(BUILD_DATE)'

# ─── Database (used by migrate targets; override via env) ───
DATABASE_URL ?= postgres://leoflow:leoflow@localhost:5432/leoflow?sslmode=disable
# Integration tests run against a SEPARATE database so they never pollute the
# demo/dev `leoflow` DB (which backs the local control plane and its UI stats).
TEST_DATABASE_URL ?= postgres://leoflow:leoflow@localhost:5432/leoflow_test?sslmode=disable

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

.PHONY: setup
setup: ## Install Go tools (incl. changie), Python parser, and the pre-commit hook
	go mod download
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
	go install github.com/fzipp/gocyclo/cmd/gocyclo@$(GOCYCLO_VERSION)
	go install github.com/gordonklaus/ineffassign@$(INEFFASSIGN_VERSION)
	go install github.com/client9/misspell/cmd/misspell@$(MISSPELL_VERSION)
	go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@$(MIGRATE_VERSION)
	go install github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION)
	go install github.com/miniscruff/changie@$(CHANGIE_VERSION)
	command -v python3 >/dev/null && pip install -e "./parser[dev]" && pip install -e ./runtime/python || echo "skip parser/runtime install (python3 not found)"
	install -m 0755 scripts/pre-commit .git/hooks/pre-commit
	@echo "setup complete"

.PHONY: build
build: ## Build all binaries into ./bin
	mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o $(CLI_BINARY) ./cmd/leoflow
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o $(SERVER_BINARY) ./cmd/leoflow-server
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o $(AGENT_BINARY) ./cmd/leoflow-agent
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o $(MCP_BINARY) ./cmd/leoflow-mcp

.PHONY: chaos-dogfood
chaos-dogfood: ## Pre-Lima gate (#231) — Phase 1: run all suites on the host + emit a green/red report
	@bash scripts/chaos/run.sh

CHAOS_IMAGE          ?= leoflow-chaos:local
CHAOS_GO_VERSION     ?= 1.26.6
CHAOS_LINT_VERSION   ?= v2.12.2

.PHONY: chaos-dogfood-docker
chaos-dogfood-docker: ## Pre-Lima gate (#231) — Phase 2a: same harness inside a clean Docker container (no host contamination by construction)
	@command -v docker >/dev/null || { echo "docker is required for the dockerized harness"; exit 2; }
	@echo "building $(CHAOS_IMAGE) (go $(CHAOS_GO_VERSION), golangci-lint $(CHAOS_LINT_VERSION))"
	@docker build \
		--build-arg GO_VERSION=$(CHAOS_GO_VERSION) \
		--build-arg GOLANGCI_LINT_VERSION=$(CHAOS_LINT_VERSION) \
		-t $(CHAOS_IMAGE) \
		-f scripts/chaos/Dockerfile scripts/chaos
	@echo "running chaos dogfood inside $(CHAOS_IMAGE)"
	@docker run --rm \
		-v "$(PWD)":/workspace \
		-e REPORT_FILE=/workspace/chaos-report.md \
		$(CHAOS_IMAGE) \
		|| { echo "report at $(PWD)/chaos-report.md"; exit 1; }
	@echo "report at $(PWD)/chaos-report.md"

.PHONY: dev-install
dev-install: ## Install the leoflow toolchain on PATH so `leoflow dev` runs from any project
	go install -trimpath -ldflags="$(LDFLAGS)" ./cmd/leoflow ./cmd/leoflow-server ./cmd/leoflow-agent

.PHONY: lite-redeploy
lite-redeploy: ## Local dev loop: rebuild + (re)start `leoflow lite` with the just-built binaries
	@bash scripts/lite-redeploy.sh
	go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@$(MIGRATE_VERSION)
	@echo "installed leoflow, leoflow-server, leoflow-agent, migrate to $$(go env GOPATH)/bin"
	@echo "ensure that dir is on your PATH, then run: leoflow dev   (the dev DB + venv are auto-provisioned)"

.PHONY: fetch-airflow-ui
fetch-airflow-ui: ## Extract the pinned Airflow UI SPA into internal/ui/assets (needs docker)
	@command -v docker >/dev/null || { echo "docker is required"; exit 1; }
	@echo "fetching Airflow $(AIRFLOW_UI_VERSION) UI bundle..."
	docker pull apache/airflow:$(AIRFLOW_UI_VERSION)
	@cid=$$(docker create apache/airflow:$(AIRFLOW_UI_VERSION)) ; \
	dist=$$(docker run --rm --entrypoint sh apache/airflow:$(AIRFLOW_UI_VERSION) -c \
		'find / -type d -path "*/airflow/ui/dist" 2>/dev/null | head -1') ; \
	if [ -z "$$dist" ]; then echo "could not locate airflow/ui/dist in image"; docker rm $$cid >/dev/null; exit 1; fi ; \
	echo "found dist at $$dist" ; \
	rm -rf $(UI_ASSETS_DIR) && mkdir -p $(UI_ASSETS_DIR) ; \
	docker cp "$$cid":"$$dist/." $(UI_ASSETS_DIR)/ ; \
	docker rm $$cid >/dev/null ; \
	echo "$(AIRFLOW_UI_VERSION)" > $(UI_ASSETS_DIR)/VERSION ; \
	echo "extracted $$(find $(UI_ASSETS_DIR) -type f | wc -l | tr -d ' ') files to $(UI_ASSETS_DIR) (VERSION=$(AIRFLOW_UI_VERSION))"
	@$(MAKE) rebrand-ui
	@echo "NOTE: the bundle is unverified until walked in a real browser (see docs/ui-compatibility.md)."

.PHONY: rebrand-ui
rebrand-ui: ## Rewrite the embedded SPA's Docs/GitHub nav links from Airflow to Leoflow
	@for js in $(UI_ASSETS_DIR)/assets/index-*.js ; do \
		perl -i -pe 's{https://github\.com/apache/airflow}{https://github.com/neochaotic/leoflow}g; s{`https://airflow\.apache\.org/docs/`,key:`documentation`}{`https://dexaflow.dexadata.ai/`,key:`documentation`}g; s{`https://airflow\.apache\.org/`,rel:`noopener}{`https://dexaflow.dexadata.ai/`,rel:`noopener}g;' "$$js" ; \
	done
	@echo "rebranded nav Docs/GitHub links to Leoflow (templated provider docs left pointing at Airflow)"

.PHONY: e2e-lite
e2e-lite: ## End-to-end Lite happy path (setup -> control plane -> login); needs local Postgres+Redis (DESTRUCTIVE: resets leoflow_dev)
	bash test/e2e/lite-login.sh

.PHONY: e2e-lite-selfheal
e2e-lite-selfheal: ## Lite boot self-heal gate (#404); needs local Postgres (DESTRUCTIVE: resets leoflow_dev)
	PYTHONPATH=parser bash test/e2e/lite-selfheal.sh

.PHONY: runtime-images
runtime-images: ## Build the task base images for each supported Python version
	for v in 3.10 3.11 3.12 3.13; do \
		docker build -f runtime/Dockerfile --build-arg PYTHON_VERSION=$$v -t leoflow-base:py$$v . ; \
	done

.PHONY: migrate-image
migrate-image: ## Build the migrate image (migrations + golang-migrate) for the Helm migration Job
	docker build -f deploy/Dockerfile.migrate -t leoflow-migrate:$(VERSION) .

.PHONY: rc-smoke
rc-smoke: ## Run the full RC pre-cut smoke battery (gates + k3d e2es). SKIP_E2E=1 for gates only.
	bash scripts/rc-smoke.sh

.PHONY: e2e
e2e: ## Run the k3d end-to-end smoke test (needs k3d, kubectl, docker, jq; run make dev-up + make build first)
	bash test/e2e/e2e.sh

.PHONY: chaos-runtime
chaos-runtime: ## Runtime fault-injection chaos e2e (#231 Phase 2): kill scheduler/task pod, assert at-most-once + recovery. Run inside Lima. Destructive.
	bash test/e2e/chaos-runtime.sh

.PHONY: soak
soak: ## Long-running resilience soak (test/soak): 30 min by default, local Postgres + Lite, asserts invariants continuously. Bounded and safe to leave unattended.
	bash test/soak/soak.sh

.PHONY: soak-credential-ceiling
soak-credential-ceiling: ## Prove the credential-renewal ceiling is enforced: lowers it below soak_token's runtime and REQUIRES the task to fail for that reason.
	@SOAK_CREDENTIAL_CEILING=4m SOAK_TOKEN_SECONDS=540 \
	  bash test/soak/soak.sh --duration 20m --mode credential-ceiling \
	    --label credential-ceiling --out .soak/credential-ceiling

.PHONY: soak-selftest
soak-selftest: ## Prove the soak's assertions can fail: injects a real outage that outlives its declared window. MUST exit 1 with recorded violations.
	@bash test/soak/soak.sh --duration 6m --faults selftest-red --label selftest-red --out .soak/selftest-red; \
	code=$$?; \
	if [ $$code -ne 1 ]; then \
	  echo "soak-selftest FAILED: expected exit 1 (violations), got $$code."; \
	  echo "  exit 0 means the battery reported PASS through an undeclared outage;"; \
	  echo "  exit 2 means the harness never ran, which proves nothing about the assertions."; \
	  exit 1; \
	fi; \
	python3 -c 'import json,sys; v=json.load(open(".soak/selftest-red/verdict.json")); \
	by=v.get("violations_by_check") or {}; \
	sys.exit(0) if v["verdict"].startswith("FAIL") and by else sys.exit("soak-selftest FAILED: exit 1 but verdict=%s violations=%s" % (v["verdict"], by))' \
	  || exit 1; \
	echo "soak-selftest OK: the battery went red (exit 1, violations recorded) as designed"

.PHONY: soak-warmpool-ab
soak-warmpool-ab: ## Warm-pool A/B on k3d (ADR 0058): same workload with pools off and on, interleaved. Needs k3d + docker. Written, not yet executed.
	bash test/soak/warmpool-ab.sh

.PHONY: e2e-timeout
e2e-timeout: ## Run the k3d execution_timeout e2e (#925/#930: agent clock beats the kubelet, durable reason, agent-lost reap; needs k3d, kubectl, docker, jq, migrate; run make dev-up + make build first)
	bash test/e2e/execution-timeout-e2e.sh

.PHONY: e2e-sso
e2e-sso: ## Run the sign-in page SSO e2e (#1160 the flow is offered, #1191 a break-glass login replaces a live SSO session and neither session cookie is readable by a script; Linux only, needs openssl, python3, node with playwright-core, local Postgres; run make build first)
	bash test/e2e/sso-login-page.sh

.PHONY: e2e-split
e2e-split: ## Run the k3d two-process api/scheduler split e2e (ADR 0049; needs k3d, kubectl, docker, jq; run make dev-up + make build first)
	bash test/e2e/split-two-process.sh

.PHONY: e2e-dbt
e2e-dbt: ## Run the k3d dbt pod-per-node e2e (ADR 0042; needs k3d, kubectl, docker, jq, dbt; run make dev-up + make build first)
	bash test/e2e/dbt-e2e.sh

.PHONY: e2e-dbt-conn
e2e-dbt-conn: ## Run the k3d dbt managed-connection e2e (ADR 0043; needs k3d, kubectl, docker, jq, dbt; run make dev-up + make build first)
	bash test/e2e/dbt-connection-e2e.sh

.PHONY: e2e-dbt-mixing
e2e-dbt-mixing: ## Run the k3d dbt+operators mixing e2e (ADR 0043; needs k3d, kubectl, docker, jq, dbt, python3; run make dev-up + make build first)
	bash test/e2e/dbt-mixing-e2e.sh

.PHONY: e2e-netpol-rwx
e2e-netpol-rwx: ## Pro split real-cluster verify (#526): kind + Calico enforces NetworkPolicy + NFS RWX shared logs (needs kind, helm, kubectl, docker, openssl). Destructive; ~15 min.
	bash test/e2e/pro-netpol-rwx.sh

.PHONY: test
test: ## Run Go and Python tests with coverage
	go test -race -coverprofile=coverage.out -covermode=atomic ./...
	command -v pytest >/dev/null && (cd parser && pytest -v --cov=leoflow_parser) || echo "skip pytest (not installed)"
	command -v pytest >/dev/null && (cd runtime/python && pytest -v --cov=leoflow_runtime) || echo "skip runtime pytest (not installed)"

.PHONY: test-db
test-db: ## Create (if missing) and migrate the isolated integration-test database
	@docker compose exec -T postgres psql -U leoflow -d postgres -tc \
		"SELECT 1 FROM pg_database WHERE datname = 'leoflow_test'" | grep -q 1 || \
		docker compose exec -T postgres psql -U leoflow -d postgres -c "CREATE DATABASE leoflow_test"
	migrate -path migrations -database "$(TEST_DATABASE_URL)" up

.PHONY: test-integration
test-integration: test-db ## Run //go:build integration tests against the isolated test DB
	DATABASE_URL="$(TEST_DATABASE_URL)" go test -tags integration -race ./...

.PHONY: cover
cover: test ## Show total Go coverage
	go tool cover -func=coverage.out | tail -1

.PHONY: lint
lint: ## Run golangci-lint and ruff
	golangci-lint run ./...
	command -v ruff >/dev/null && (cd parser && ruff check .) || echo "skip ruff (not installed)"
	command -v ruff >/dev/null && (cd runtime/python && ruff check .) || echo "skip runtime ruff (not installed)"

.PHONY: fmt
fmt: ## Format Go code
	gofmt -w .

.PHONY: changelog
changelog: ## Record this change as a CHANGELOG fragment (.changes/unreleased/), which never conflicts
	@command -v changie >/dev/null || { echo "changie is not installed: brew install changie (or see https://changie.dev)"; exit 1; }
	changie new

.PHONY: reportcard
reportcard: ## Verify the Go Report Card A+ floor (ADR 0012) with maintained tools
	bash scripts/reportcard.sh

.PHONY: vuln
vuln: ## Run govulncheck (ADR 0014)
	govulncheck ./...

.PHONY: ci-local
ci-local: ## Run every CI gate locally — pre-push tripwire so a PR does not arrive red
	@echo "▸ gofmt"
	@test -z "$$(gofmt -l . | tee /dev/stderr)" || (echo "FAIL: gofmt"; exit 1)
	@echo "▸ go build"
	@go build ./...
	@echo "▸ golangci-lint"
	@golangci-lint run ./...
	@echo "▸ go test"
	@go test ./...
	@echo "▸ govulncheck (advisories DB is fetched at runtime — catches new stdlib CVEs CI will see)"
	@command -v govulncheck >/dev/null && govulncheck ./... \
		|| ($$(go env GOPATH)/bin/govulncheck ./... 2>/dev/null) \
		|| (echo "skip govulncheck (run: go install golang.org/x/vuln/cmd/govulncheck@latest)"; exit 0)
	@echo "▸ helm unittest (chart contracts)"
	@command -v helm >/dev/null && command -v helm-unittest >/dev/null \
		&& (cd helm/leoflow && helm unittest .) \
		|| (command -v helm >/dev/null && helm plugin list 2>/dev/null | grep -q unittest \
			&& (cd helm/leoflow && helm unittest .) \
			|| echo "skip helm unittest (install: helm plugin install https://github.com/helm-unittest/helm-unittest)")
	@echo "▸ python parser tests"
	@command -v python3 >/dev/null && (cd parser && python3 -m pytest -q) || echo "skip pytest (no python3)"
	@echo "▸ hugo --gc --minify (website/)"
	@if ! command -v hugo >/dev/null; then \
		echo "skip hugo (not installed: https://gohugo.io/installation/ — needs the extended build)"; \
	elif [ ! -x website/node_modules/.bin/postcss ]; then \
		echo "skip hugo (Docsy PostCSS toolchain missing: run 'npm --prefix website ci')"; \
	else \
		(cd website && hugo --gc --minify --quiet); \
	fi
	@echo "✅ ci-local clean — push when ready"

.PHONY: install-pre-push-hook
install-pre-push-hook: ## Install a pre-push hook that runs `make ci-local` automatically
	@mkdir -p .git/hooks
	@printf '#!/usr/bin/env bash\n# Auto-installed by `make install-pre-push-hook`.\n# Runs every CI gate locally so a PR never lands red on infra-class checks\n# (govulncheck advisories, helm tests, hugo build) the per-commit hook\n# does not cover. Skip with: git push --no-verify.\nset -euo pipefail\nexec make ci-local\n' > .git/hooks/pre-push
	@chmod +x .git/hooks/pre-push
	@echo "▸ installed .git/hooks/pre-push → runs 'make ci-local' on every push"

.PHONY: dev-up
dev-up: ## Start local Postgres + Redis (wait healthy) and apply migrations
	docker compose up -d --wait
	$(MAKE) migrate-up

.PHONY: dev-down
dev-down: ## Stop local Postgres + Redis, keeping data
	docker compose down

.PHONY: dev-reset
dev-reset: ## Wipe local Postgres + Redis data and restart fresh
	docker compose down -v
	$(MAKE) dev-up

.PHONY: migrate-up
migrate-up: ## Apply all up migrations
	migrate -path migrations -database "$(DATABASE_URL)" up

.PHONY: migrate-down
migrate-down: ## Roll back the most recent migration
	migrate -path migrations -database "$(DATABASE_URL)" down 1

.PHONY: sqlc
sqlc: ## Regenerate sqlc code
	sqlc generate

.PHONY: proto
proto: ## Regenerate protobuf/gRPC code from proto/ via buf
	buf generate

.PHONY: pkg-client
pkg-client: ## Regenerate the typed /api/v2 client in pkg/client from the OpenAPI spec
	go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@$(OAPI_CODEGEN_VERSION) \
		-config pkg/client/oapi-codegen.yaml docs/api/openapi.yaml

.PHONY: pkg-client-check
pkg-client-check: pkg-client ## Anti-drift: regenerate pkg/client in place and fail if it changed
	@git diff --exit-code -- pkg/client/client.gen.go || \
		{ echo "pkg/client/client.gen.go is out of date — run 'make pkg-client' and commit"; exit 1; }

.PHONY: gen-connectors
gen-connectors: ## Regenerate internal/connectors/catalog.json from the pinned providers (ADR 0039)
	python3 -m venv /tmp/conngen
	/tmp/conngen/bin/pip install --quiet -r scripts/connectors-providers.lock.txt
	/tmp/conngen/bin/python scripts/gen_connectors.py internal/connectors/catalog.json

.PHONY: gen-connectors-check
gen-connectors-check: ## Anti-drift: regenerate to a temp file and diff against the committed catalog.json
	python3 -m venv /tmp/conngen
	/tmp/conngen/bin/pip install --quiet -r scripts/connectors-providers.lock.txt
	/tmp/conngen/bin/python scripts/gen_connectors.py /tmp/catalog.check.json
	@diff -u internal/connectors/catalog.json /tmp/catalog.check.json && echo "catalog.json is in sync with the pinned providers"

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf $(BIN_DIR) coverage.out

.PHONY: test-mcp-e2e
test-mcp-e2e: ## Build leoflow-mcp and drive it over the real MCP protocol (stdio + http) against a seeded control plane
	go test -tags e2e ./internal/mcp/
