# Leash — the root Makefile delegates and proves. It builds little itself.
#
# On a fresh VM:
#
#     make start      build and start everything, then print where to go
#
# Every target carries a ## description, and `make help` prints them.

COMPOSE   := docker compose
BACKEND   := backend
HTTP_PORT ?= 80

# Local (non-container) development points at the compose MongoDB and validator.
MONGO_PORT ?= 27017
export MONGO_URI ?= mongodb://localhost:$(MONGO_PORT)/leash?replicaSet=rs0&directConnection=true
export MONGO_DB  ?= leash

CTL := cd $(BACKEND) && go run ./cmd/leashctl

.DEFAULT_GOAL := help

.PHONY: help
help:  ## Print every target with its purpose.
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2}'

# ── Running it ───────────────────────────────────────────────────────────────

.env:
	@cp .env.example .env
	@# SESSION_KEY is never defaulted, so one is generated rather than shipped. A shipped default
	@# would be the same secret on every deployment that ever ran `make start`.
	@KEY=$$(openssl rand -base64 48 | tr -d '\n'); \
		INT=$$(openssl rand -hex 32); \
		sed -i.bak "s|^SESSION_KEY=.*|SESSION_KEY=$$KEY|; s|^INTERNAL_TOKEN=.*|INTERNAL_TOKEN=$$INT|" .env \
		&& rm -f .env.bak
	@echo "created .env with fresh secrets — review it before exposing this to a network"

## Everything in containers, behind nginx, exactly as it runs on a server. This is what a fresh
## VM needs: it builds the images, starts the stack, waits for the front door, and prints the URL.
.PHONY: prod
prod: .env  ## Run the whole stack in Docker, as it runs on a VM.
	$(COMPOSE) up -d --build
	@printf "waiting for the front door"
	@for i in $$(seq 1 90); do \
		if curl -fsS "http://localhost:$(HTTP_PORT)/healthz" >/dev/null 2>&1; then \
			echo ""; echo ""; \
			echo "  Leash is up:  http://localhost:$(HTTP_PORT)"; \
			echo "  sandbox validator on :$${VALIDATOR_PORT:-8899}, MongoDB on :$(MONGO_PORT)"; \
			echo ""; \
			echo "  make demo     run the whole loop against it"; \
			echo "  make logs     follow everything"; \
			echo "  make stop     stop it"; \
			exit 0; fi; \
		printf "."; sleep 2; done; \
		echo ""; echo "the front door did not answer. Try: make logs"; exit 1

## The infrastructure in Docker, everything we wrote on the host — so a rebuild is seconds and a
## debugger can reach it. MongoDB and the validator are identical either way, and are not worth
## reproducing by hand.
##
## Ctrl-C stops the services. The database keeps running, because its state is what makes the next
## start fast; `make stop` takes it down when you mean it.
.PHONY: dev
dev: .env  ## Run backend and website on the host, infrastructure in Docker.
	@./scripts/dev.sh

.PHONY: start
start: prod  ## Alias for `make prod`.

.PHONY: stop
stop:  ## Stop everything, keeping its data.
	$(COMPOSE) down

.PHONY: restart
restart: stop prod  ## Stop and start again.

.PHONY: clean
clean:  ## Stop everything AND destroy its data, ledger and local state.
	$(COMPOSE) down -v
	@rm -rf .dev-state .dev-bin .dev-logs
	@echo "gone: containers, volumes, the sandbox ledger, and the local development state"

.PHONY: ps
ps:  ## What is running.
	$(COMPOSE) ps

.PHONY: logs
logs:  ## Follow every service's logs.
	$(COMPOSE) logs -f --tail=100

.PHONY: dev-logs
dev-logs:  ## Follow the host services' logs from `make dev`.
	@tail -f "$(CURDIR)"/.dev-logs/*.log

# ── Build and test ───────────────────────────────────────────────────────────

.PHONY: build
build:  ## Compile every Go binary into backend/bin/.
	cd $(BACKEND) && go build -o bin/ ./cmd/...
	@ls -1 "$(BACKEND)/bin/"

.PHONY: test
test:  ## Run the Go tests, including the S0–S7 golden vectors.
	cd $(BACKEND) && go test ./...

.PHONY: vectors
vectors:  ## Run only the golden vectors, so a failure names the rule.
	cd $(BACKEND) && go test ./internal/domain/policy/ -run TestGoldenVectors -v

.PHONY: fmt
fmt:  ## Format every Go file.
	cd $(BACKEND) && gofmt -w .

.PHONY: lint
lint:  ## gofmt check and go vet. CI runs the same two.
	@cd $(BACKEND) && test -z "$$(gofmt -l . | grep -v '^$$')" \
		|| { echo "unformatted:"; cd $(BACKEND) && gofmt -l .; exit 1; }
	cd $(BACKEND) && go vet ./...

.PHONY: migrate
migrate:  ## Apply migrations. Services do this at boot too — this is for when you want it now.
	$(CTL) migrate

# ── The proofs ───────────────────────────────────────────────────────────────
#
# Each turns a claim in docs/ into a build failure. Every one that can be fooled ships with a
# --selftest, and the self-test runs FIRST — a gate nobody has watched reject something is a
# comment with a shell prompt in front of it.

.PHONY: verify
verify: lint test verify-migrations verify-constraints  ## Everything below, in order.
	@echo
	@echo "all proofs passed"

## Applies every Up, snapshots the schema WITH its validators, applies every Down, asserts nothing
## survives, applies every Up again, and asserts the snapshot is byte-identical. That last step is
## what catches a Down that drops something its Up creates as a side effect.
.PHONY: verify-migrations
verify-migrations:  ## Prove every migration reverses, and reproduces exactly.
	cd $(BACKEND) && MONGO_DB=$(MONGO_DB)_verify go run ./cmd/leashctl verify-migrations

## Performs, against a real server, every write the schema must refuse. The self-test replays each
## against a database with no rules and requires it to be ACCEPTED there — a write refused even
## without constraints was never testing a constraint.
.PHONY: verify-constraints
verify-constraints:  ## Prove the schema refuses what the documentation says it refuses.
	$(CTL) verify-constraints --selftest
	@echo
	$(CTL) verify-constraints

.PHONY: verify-privileges
verify-privileges:  ## Prove the append-only collections are append-only, as the app role.
	$(CTL) verify-privileges --selftest
	$(CTL) verify-privileges

.PHONY: verify-architecture
verify-architecture:  ## Prove the dependency and type boundaries hold.
	$(CTL) verify-architecture --selftest
	$(CTL) verify-architecture

## Runs inside the compose network, because it needs the sandbox keys on the shared state volume
## and the service names the other containers use. Everything it does is real: a real delegation on
## a real chain, a real 402, a real signature, and a real read-back.
## Against `make dev`: the binaries and the sandbox state are on the host, so it runs there too.
.PHONY: demo-dev
demo-dev:  ## Run the end-to-end demo against `make dev`.
	@LEASH_STATE_DIR="$(CURDIR)/.dev-state" \
		MONGO_DB="$(MONGO_DB)_dev" \
		MONGO_URI="mongodb://localhost:$(MONGO_PORT)/$(MONGO_DB)_dev?replicaSet=rs0&directConnection=true" \
		RPC_SANDBOX_URL=http://localhost:$${VALIDATOR_PORT:-8899} \
		SIGNER_BASE_URL=http://localhost:4100 \
		DEMO_ENDPOINT_URL=http://localhost:4200 \
		"$(CURDIR)/.dev-bin/leashctl" demo

.PHONY: demo
demo:  ## Run the end-to-end demo against `make prod`.
	@$(COMPOSE) run --rm --no-deps \
		-e SIGNER_BASE_URL=http://signer:4100 \
		-e DEMO_ENDPOINT_URL=http://demo402:4200 \
		-e MONGO_URI="mongodb://mongo:27017/$(MONGO_DB)?replicaSet=rs0&directConnection=true" \
		-e RPC_SANDBOX_URL=http://validator:8899 \
		bootstrap /bin/leashctl demo
