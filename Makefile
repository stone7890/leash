# Leash — the root Makefile delegates and proves. It builds little itself.
#
# On a fresh VM:
#
#     make start      build and start everything, then print where to go
#
# Every target carries a ## description, and `make help` prints them.

BACKEND   := backend
HTTP_PORT ?= 80

# Local (non-container) development points at the compose MongoDB and validator.
MONGO_PORT ?= 27017
export MONGO_URI ?= mongodb://localhost:$(MONGO_PORT)/leash?replicaSet=rs0&directConnection=true
export MONGO_DB  ?= leash

# ── which ledger the sandbox lives on ────────────────────────────────────────
#
# CLUSTER empty means the local validator in docker-compose.yml, which is what `make start` runs
# and what .env points at. Naming a cluster layers docker-compose.public.yml on top — that drops
# the validator and nothing else — and sets the endpoint HERE rather than asking anyone to edit
# .env first. The target name is then the truth: `make devnet` cannot quietly run against testnet
# because a file said so.
#
# Each cluster gets its own state volume and its own database. The keys and the recorded mint only
# mean anything on the ledger they were made against, so sharing them would mean a mint that is
# absent after every switch, a fresh one created in its place, and a database still describing the
# old one — an agent meeting a mint it was not created against, refused with UNSUPPORTED_TERMS.
# Correct behaviour, arrived at confusingly. It is the same split `make dev` already makes, and it
# has a second benefit: each cluster's treasury stays funded across switches.
CLUSTER ?=
CLUSTER_RPC_devnet  := https://api.devnet.solana.com
CLUSTER_RPC_testnet := https://api.testnet.solana.com

ifneq ($(CLUSTER),)
ifeq ($(CLUSTER_RPC_$(CLUSTER)),)
$(error unknown CLUSTER "$(CLUSTER)" — try devnet or testnet, or set RPC_SANDBOX_URL yourself)
endif
COMPOSE := docker compose -f docker-compose.yml -f docker-compose.public.yml
export RPC_SANDBOX_URL := $(CLUSTER_RPC_$(CLUSTER))
export STATE_VOLUME    := leash_state_$(CLUSTER)
export MONGO_DB        := $(MONGO_DB)_$(CLUSTER)
else
COMPOSE := docker compose
endif

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
	@# Only for the local validator. A cluster target sets RPC_SANDBOX_URL on the command line, so
	@# it never reaches here; what this catches is a .env left pointing at a public cluster, which
	@# would start a local validator that nothing talks to — a stack that looks local and settles
	@# somewhere else entirely.
ifeq ($(CLUSTER),)
	@RPC=$$(sed -n 's/^RPC_SANDBOX_URL=//p' .env 2>/dev/null | tail -1); \
		case "$$RPC" in \
		""|*validator:*|*localhost:*|*127.0.0.1:*) ;; \
		*) echo "RPC_SANDBOX_URL in .env is $$RPC — not the local validator."; \
		   echo ""; \
		   echo "  make devnet     run against Solana devnet"; \
		   echo "  make testnet    run against Solana testnet"; \
		   echo "  or set RPC_SANDBOX_URL=http://validator:8899 in .env for a local sandbox"; \
		   exit 1 ;; \
		esac
endif
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
		echo ""; \
		if $(COMPOSE) ps --status running --services 2>/dev/null | grep -qx bootstrap; then \
			echo ""; \
			echo "  bootstrap is still running, which on a public cluster means it is WAITING to"; \
			echo "  be funded. It printed one address to send SOL to:"; \
			echo ""; \
			echo "      $(COMPOSE) logs bootstrap"; \
			echo ""; \
			echo "  Send it, and the rest of the stack starts by itself — nothing to re-run."; \
		else \
			echo "the front door did not answer. Try: make logs"; \
		fi; exit 1

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

## The same stack, with the sandbox on a public Solana cluster instead of the local validator.
## The target sets the endpoint itself, so there is no .env to edit and no way for the name to
## disagree with the ledger. Each cluster keeps its own state volume and database.
##
##   make devnet / make testnet          the stack
##   make devnet-keys                    the one address to fund, and what it holds
##   make devnet-faucet                  ask the cluster for that SOL
##   make devnet-demo                    the end-to-end demo against it
##
## A public faucet answers 429 once the day's allowance is gone, so bootstrap may stop and name one
## address to fund. It is idempotent: fund it and run this again. Do NOT `make clean` in between —
## that destroys the state volume and the next run generates a different address.
##
## Written out per cluster rather than generated: `make help` reads this file's text, so a target
## produced by $(eval) is a target nobody can find.
.PHONY: devnet
devnet:  ## Run the whole stack with the sandbox on Solana devnet.
	@$(MAKE) --no-print-directory CLUSTER=devnet prod

.PHONY: devnet-keys
devnet-keys:  ## Print the sandbox's addresses and balances on devnet.
	@$(MAKE) --no-print-directory CLUSTER=devnet keys

.PHONY: devnet-faucet
devnet-faucet:  ## Ask devnet for SOL (and optionally mint test USDC).
	@$(MAKE) --no-print-directory CLUSTER=devnet faucet ADDRESS="$(ADDRESS)" SOL="$(SOL)" USDC="$(USDC)"

.PHONY: devnet-demo
devnet-demo:  ## Run the end-to-end demo against `make devnet`.
	@$(MAKE) --no-print-directory CLUSTER=devnet demo

.PHONY: testnet
testnet:  ## Run the whole stack with the sandbox on Solana testnet.
	@$(MAKE) --no-print-directory CLUSTER=testnet prod

.PHONY: testnet-keys
testnet-keys:  ## Print the sandbox's addresses and balances on testnet.
	@$(MAKE) --no-print-directory CLUSTER=testnet keys

.PHONY: testnet-faucet
testnet-faucet:  ## Ask testnet for SOL (and optionally mint test USDC).
	@$(MAKE) --no-print-directory CLUSTER=testnet faucet ADDRESS="$(ADDRESS)" SOL="$(SOL)" USDC="$(USDC)"

.PHONY: testnet-demo
testnet-demo:  ## Run the end-to-end demo against `make testnet`.
	@$(MAKE) --no-print-directory CLUSTER=testnet demo

## Which addresses this sandbox owns, what they hold, and what is still short. On a public cluster
## the operator funds them by hand, so they need to be readable somewhere other than the scrollback
## of the bootstrap run that failed.
.PHONY: keys
keys:  ## Print the sandbox's addresses and balances.
	@# --build, because the addresses are read out of the binary: an image built before the last
	@# change would print a confident answer from stale code.
	@$(COMPOSE) run --rm --no-deps --build bootstrap /bin/leashctl keys

## Ask the sandbox's chain for SOL — the one step of bootstrap worth retrying on its own, because
## on a public cluster it is the step that fails and re-running all of bootstrap to retry one
## airdrop means waiting through migrations to learn whether the faucet's mood has changed.
##
##   make faucet                        top the treasury up to BOOTSTRAP_FUND_SOL
##   make faucet ADDRESS=<pubkey>       fund that address instead
##   make faucet SOL=1 USDC=100         amounts, explicitly. USDC needs a prepared sandbox
.PHONY: faucet
faucet:  ## Ask the sandbox chain for SOL (and optionally mint test USDC).
	@$(COMPOSE) run --rm --no-deps --build bootstrap /bin/leashctl faucet \
		$(if $(SOL),--sol $(SOL),) $(if $(USDC),--usdc $(USDC),) $(ADDRESS)

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
	@# A cluster's state volume is NOT destroyed here, and saying so matters. It holds the keys a
	@# public faucet was talked into funding, which on a rate-limited cluster can be a day's work
	@# to replace — destroying it as a side effect of clearing a local sandbox would be the most
	@# expensive possible reading of "clean". `make clean-clusters` is the deliberate version.
	@for c in devnet testnet; do \
		docker volume inspect "leash_state_$$c" >/dev/null 2>&1 && \
		echo "kept: leash_state_$$c — $$c's funded keys. 'make clean-clusters' removes them."; \
	done; true

.PHONY: clean-clusters
clean-clusters:  ## Destroy the devnet and testnet sandboxes, including their funded keys.
	@# Deliberate and irreversible: the keys go with the volume, and the SOL in them is not
	@# recoverable from anywhere else. Named separately from `clean` for exactly that reason.
	@for c in devnet testnet; do \
		$(MAKE) --no-print-directory CLUSTER=$$c stop >/dev/null 2>&1 || true; \
		docker volume rm "leash_state_$$c" >/dev/null 2>&1 && echo "gone: leash_state_$$c" || true; \
	done
	@echo "the cluster sandboxes are gone. Their treasuries will have new addresses."

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
	@# The endpoint comes from .env, exactly as scripts/dev.sh reads it, so this demo runs against
	@# whichever ledger `make dev` bootstrapped — local validator or public cluster.
	@RPC=$$(sed -n 's/^RPC_SANDBOX_URL=//p' .env | tail -1); \
		LEASH_STATE_DIR="$(CURDIR)/.dev-state" \
		MONGO_DB="$(MONGO_DB)_dev" \
		MONGO_URI="mongodb://localhost:$(MONGO_PORT)/$(MONGO_DB)_dev?replicaSet=rs0&directConnection=true" \
		RPC_SANDBOX_URL="$${RPC:-http://localhost:$${VALIDATOR_PORT:-8899}}" \
		SIGNER_BASE_URL=http://localhost:4100 \
		DEMO_ENDPOINT_URL=http://localhost:4200 \
		"$(CURDIR)/.dev-bin/leashctl" demo

.PHONY: demo
demo:  ## Run the end-to-end demo against `make prod`.
	@# RPC_SANDBOX_URL is deliberately NOT pinned here: the compose file resolves it, so this
	@# demo runs against whichever ledger the stack itself is using.
	@$(COMPOSE) run --rm --no-deps \
		-e SIGNER_BASE_URL=http://signer:4100 \
		-e DEMO_ENDPOINT_URL=http://demo402:4200 \
		-e MONGO_URI="mongodb://mongo:27017/$(MONGO_DB)?replicaSet=rs0&directConnection=true" \
		bootstrap /bin/leashctl demo
