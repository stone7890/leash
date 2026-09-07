#!/usr/bin/env bash
#
# Development: the infrastructure in Docker, everything we wrote on the host.
#
# The split is deliberate. MongoDB needs a replica set and Solana needs a validator — neither is
# worth reproducing by hand, and both are identical in development and production. The four things
# under development run on the host, where a rebuild is seconds rather than a Docker layer, and
# where a debugger can reach them.
#
#   make prod  runs the same code the same way it runs on a server, in containers, behind nginx.
#   make dev   runs it the way you want it while you are changing it.
#
# Ctrl-C stops everything it started, including the containers' port forwards. It does NOT stop
# MongoDB or the validator: their state is what makes a restart fast, and `make stop` takes them
# down when you actually mean it.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

STATE_DIR="$ROOT/.dev-state"
LOG_DIR="$ROOT/.dev-logs"
mkdir -p "$STATE_DIR" "$LOG_DIR"

# ── configuration ────────────────────────────────────────────────────────────
#
# The same .env production uses, with the container hostnames swapped for localhost. Nothing else
# differs, so a variable that works here works there.

if [ ! -f .env ]; then
	echo "no .env — run 'make .env' first" >&2
	exit 1
fi
set -a
# shellcheck disable=SC1091
. ./.env
set +a

MONGO_PORT="${MONGO_PORT:-27017}"
VALIDATOR_PORT="${VALIDATOR_PORT:-8899}"

# A DIFFERENT database from the containerised stack, on the same server.
#
# The two share MongoDB and the validator, but each bootstraps its own sandbox USDC mint into its
# own state directory. Sharing a database would mean an agent created under one seeing a mint
# created by the other, and the signer refusing its payments with UNSUPPORTED_TERMS — correct
# behaviour, arrived at confusingly. Separate databases, one ledger, no surprises.
export MONGO_DB="${MONGO_DB:-leash}_dev"
export MONGO_URI="mongodb://localhost:${MONGO_PORT}/${MONGO_DB}?replicaSet=rs0&directConnection=true"
export RPC_SANDBOX_URL="http://localhost:${VALIDATOR_PORT}"
export LEASH_STATE_DIR="$STATE_DIR"
export SIGNER_BASE_URL="http://localhost:4100"
export INDEXER_BASE_URL="http://localhost:4300"
export DEMO_ENDPOINT_URL="http://localhost:4200"
export NETWORKS="${NETWORKS:-sandbox}"
export KMS_LOCAL="${KMS_LOCAL:-true}"
export LOG_LEVEL="${LOG_LEVEL:-info}"

PIDS=()
cleanup() {
	echo ""
	echo "stopping…"
	for pid in "${PIDS[@]:-}"; do
		[ -n "${pid:-}" ] && kill "$pid" 2>/dev/null || true
	done
	wait 2>/dev/null || true
	echo "the database and the validator are still up — 'make stop' takes them down."
}
trap cleanup EXIT INT TERM

# ── infrastructure ───────────────────────────────────────────────────────────

echo "starting MongoDB and the Solana validator"
docker compose up -d mongo validator >/dev/null

printf "  waiting for a writable primary"
for _ in $(seq 1 40); do
	# TWICE running, not once: rs.initiate() returns before the node has been elected, so the first
	# w:majority write fails with NotWritablePrimary. See docs/14-deployment.md.
	if docker compose exec -T mongo mongosh --quiet \
		--eval 'quit(db.hello().isWritablePrimary?0:1)' >/dev/null 2>&1 &&
		docker compose exec -T mongo mongosh --quiet \
			--eval 'quit(db.hello().isWritablePrimary?0:1)' >/dev/null 2>&1; then
		echo " — ready"
		break
	fi
	printf "."
	sleep 2
done

printf "  waiting for the validator"
for _ in $(seq 1 60); do
	if curl -fsS -m 3 -X POST -H 'Content-Type: application/json' \
		-d '{"jsonrpc":"2.0","id":1,"method":"getHealth"}' \
		"http://localhost:${VALIDATOR_PORT}" 2>/dev/null | grep -q ok; then
		echo " — ready"
		break
	fi
	printf "."
	sleep 2
done

# ── build and prepare ────────────────────────────────────────────────────────

echo "building"
(cd backend && go build -o "$ROOT/.dev-bin/" ./cmd/...)

echo "preparing the sandbox"
# Idempotent, and it verifies its own recorded state against the chain — so a validator that lost
# its ledger is noticed here rather than as a confusing error three steps later.
"$ROOT/.dev-bin/leashctl" bootstrap 2>&1 | sed 's/^/  /'

# ── the services ─────────────────────────────────────────────────────────────

start() {
	local name="$1"
	shift
	"$@" >"$LOG_DIR/$name.log" 2>&1 &
	PIDS+=($!)
	echo "  $name → .dev-logs/$name.log"
}

echo "starting services"
start signer   "$ROOT/.dev-bin/leash-signer"
start indexer  "$ROOT/.dev-bin/leash-indexer"
start demo402  "$ROOT/.dev-bin/leash-demo402"

# Give them a moment to bind, so a failure shows up now rather than as a confusing 502 later.
sleep 3
for svc in signer:4100 indexer:4300 demo402:4200; do
	name="${svc%%:*}"
	port="${svc##*:}"
	if ! curl -fsS -m 2 "http://localhost:$port/healthz" >/dev/null 2>&1; then
		echo ""
		echo "$name did not come up. Its log:" >&2
		tail -20 "$LOG_DIR/$name.log" >&2
		exit 1
	fi
done

if [ ! -d frontend/website/node_modules ]; then
	echo "installing website dependencies"
	(cd frontend/website && npm install --silent --ignore-scripts)
fi

echo ""
echo "  Leash is running:  http://localhost:3000"
echo ""
echo "  signer   :4100     indexer :4300     demo-402 :4200"
echo "  mongo    :$MONGO_PORT    validator :$VALIDATOR_PORT    database: $MONGO_DB"
echo ""
echo "  make demo-dev   run the whole loop against this"
echo "  Ctrl-C          stop the services (the database keeps running)"
echo ""

# The website last, in the foreground: its output is the one you want to watch, and Ctrl-C on it
# takes the rest down through the trap.
cd frontend/website
exec npm run dev
