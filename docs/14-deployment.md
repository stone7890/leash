# 14 · Deployment

Four deployables, one database. This chapter covers configuration, boot order, the replica set,
and the traps that cost an afternoon each.

## Two ways to run it

```
make prod    everything in containers, behind nginx, exactly as it runs on a server
make dev     the infrastructure in containers, everything we wrote on the host
```

**`make prod` is what a VM needs.** It builds every image, starts the stack, waits for the front
door to answer, and prints the URL. Nothing else is required on a fresh machine: `.env` is
generated with fresh secrets on first run, and `bootstrap` prepares the sandbox before any service
that depends on it starts.

**`make dev` is for changing it.** MongoDB and the Solana validator still run in containers —
neither is worth reproducing by hand, and both behave identically either way — while the signer,
the indexer, the sample endpoint and the website run on the host, where a rebuild is seconds and a
debugger can reach them. Ctrl-C stops the services and leaves the database up, because its state is
what makes the next start fast; `make stop` takes everything down when you mean it.

| | `make dev` | `make prod` |
|---|---|---|
| MongoDB, validator | containers, shared | containers, shared |
| signer, indexer, demo-402 | host binaries, `.dev-bin/` | containers |
| website | `next dev`, hot reload | built, standalone |
| front door | none — `:3000` directly | nginx on `:80` |
| database | `leash_dev` | `leash` |
| sandbox state | `.dev-state/` | the `leashstate` volume |
| run the loop | `make demo-dev` | `make demo` |

### Why the two have separate databases

They share MongoDB and the validator, but each bootstraps **its own sandbox USDC mint** into its
own state directory. A shared database would mean an agent created under one seeing a mint created
by the other, and the signer refusing its payments with `UNSUPPORTED_TERMS` — correct behaviour
(rule S6 doing exactly its job), arrived at very confusingly. Separate databases, one ledger, no
surprises.

### Why `/v1` works without nginx

In production nginx maps `/v1/…` onto the route handlers at `/api/v1/…`, and sends `/v1/sign` and
the onboarding stream to the Go services instead. In development there is no nginx, so
`next.config.mjs` carries the same two rewrites. They are harmless in production: nginx matches the
signer and stream paths first and never forwards them to Next at all.

## Running the sandbox on devnet or testnet

The sandbox normally points at the local validator, where the ledger is ours. Pointing it at a
public cluster instead is one command. Devnet and testnet are both first class and entirely
independent — running one does not disturb the other:

```
make devnet          make testnet          # the stack
make devnet-keys     make testnet-keys     # the one address to fund, and what it holds
make devnet-faucet   make testnet-faucet   # ask the cluster for that SOL
make devnet-demo     make testnet-demo     # the end-to-end demo against it
```

**The target sets the endpoint**, rather than asking anyone to edit `.env` first. That is what
makes the name the truth: `make devnet` cannot quietly run against testnet because a file said so.
`RPC_SANDBOX_URL` in `.env` is then only the local default, for `make start` and `make dev`, and
`make start` refuses to run if it has been pointed somewhere public — a local validator nothing
talks to is a stack that looks local and settles elsewhere.

**Each cluster keeps its own state volume and database** (`leash_state_devnet`, `leash_devnet`).
The keys and the recorded mint only mean anything on the ledger they were made against, so sharing
them would mean a mint absent after every switch, a fresh one created in its place, and a database
still describing the old — an agent meeting a mint it was not created against and being refused
with `UNSUPPORTED_TERMS`. Correct behaviour, arrived at confusingly. It is the same split
`make dev` already makes, and it has a second benefit: each cluster's treasury stays funded across
switches.

`make clean` therefore does **not** destroy them, and says so. Those volumes hold keys a
rate-limited faucet was talked into funding, which can be a day's work to replace; destroying them
as a side effect of clearing a local sandbox would be the most expensive possible reading of
"clean". `make clean-clusters` is the deliberate version.

Both targets layer `docker-compose.public.yml` on top of the usual file. That overlay does two
things and no more: it stops the local validator from starting, and it drops the `depends_on` that
made `bootstrap` wait for it. It names no cluster, because it has nothing cluster-specific in it —
every service already reads `RPC_SANDBOX_URL`, so the endpoint alone decides, exactly as I8
requires. A hosted provider is the same shape: set `RPC_SANDBOX_URL` and use `make start`'s
overlay, or add a line to `CLUSTER_RPC_*` in the Makefile.

```
RPC_SANDBOX_FALLBACK_URL=            # a SECOND endpoint on the SAME cluster, if you have one
```

**A public cluster's faucet answers 429 once the day's allowance is gone**, per address and per
IP, on testnet as on devnet. That is the expected answer, not a fault, so bootstrap reports it in
one line rather than dumping the RPC error — `solana-go` renders an `RPCError` as a spew dump of
the struct, which makes a routine rate limit look like a panic.

`make <cluster>-faucet` is the one step of bootstrap worth retrying on its own: re-running all of
bootstrap to retry a single airdrop means waiting through the migrations and a mint check to learn
whether the faucet's mood has changed.

```
make devnet-faucet                                       top the treasury up
make devnet-faucet ADDRESS=<pubkey>                      fund some other wallet
make devnet-faucet ADDRESS=<pubkey> SOL=0.05 USDC=100    amounts, explicitly
```

The SOL and the test USDC come from different places and fail differently, which is why they are
reported separately. The SOL is the *cluster's*, handed out by a faucet that may refuse. The USDC
is *ours*, minted by the authority bootstrap created — it cannot be rate limited and works on any
cluster, which is the whole reason the sandbox does not depend on a published test token. Minting
needs a prepared sandbox; topping up the treasury does not.

All three commands agree on how much the treasury needs, because they call the same function: a
faucet that topped up to less than bootstrap requires would send somebody round the loop again for
no reason they could see.

**Do not `make clean` while waiting to fund.** The keys live in the `leashstate` volume and
`make clean` destroys it, so the next run generates a different treasury address and any SOL sent
to the old one is stranded. Fund, then run again.

**The offers name the real cluster.** `leash-demo402` asks the chain for its genesis hash at boot
and derives the cluster from it — Solana's CAIP-2 identifier is `solana:` plus the first 32
characters of that hash, so the chain identifies itself and no variable can be set wrongly. A
sandbox on testnet publishes `solana:4uhcVJyU9pJkvQyS88uRDiswHXSCkY3z`; one on a local validator,
whose genesis matches no public cluster, publishes devnet's, because a private ledger has no
identifier any client could act on.

**On the v1 wire, testnet is where Leash departs from the reference producer.** That producer maps
devnet-family to `solana-devnet` and *everything else* to `solana` — so a v1 offer from a testnet
server announces itself as **mainnet**. Leash emits `solana-testnet`, which the same implementation
recognises on parse. Saying the true thing costs nothing; saying the reference thing would be
dangerous.

**The fallback must be the same ledger.** `RPC_SANDBOX_FALLBACK_URL` is a second endpoint onto the
network the primary serves — another devnet provider. Testnet is not devnet's understudy: it is a
different ledger, where the sandbox mint does not exist, so failing over to it would turn every
read into an account-not-found and every write into a failure naming the wrong cause.

**Switching an existing stack between chains loses its records.** The keys survive — `bootstrap`
reuses everything in the state directory — but the mint does not: `bootstrap` reads the recorded
mint back from the chain, finds it absent, says so, and creates a fresh one. Agents and allowances
already in MongoDB still refer to the old mint and are dead. `make clean` destroys the database,
the ledger and the state together, which is the only coherent way to start over.

**`bootstrap` cannot fund itself there.** A local validator airdrops on request; devnet's faucet
is rate limited per IP *and* per address, and frequently exhausted. So `bootstrap` asks, and when
it is refused it stops and names **one** address:

```
this network will not fund the sandbox, and its treasury holds 0.0000 SOL.

    send at least 0.40 SOL to  5b8SxVYtRBATr6QKxbxK6Qm8dTqSZvbcuJDqQ9dbQ1Mt
```

One, because the mint authority is the sandbox's **treasury** and bootstrap pays the other keys
from it. That is not an arbitrary choice: it is the key that must hold SOL under every
circumstance — rent on the mint, rent on each token account, the fee on every faucet grant — so it
needs funding whatever else happens, and the others can be paid out of it.

On a local validator this changes nothing; both shapes work. On devnet it is the whole experience,
because three addresses means three individually-refusable faucet requests spread over a day
before anything runs.

The **faucet key is not funded at all**. It is created and published in `sandbox.json`, but nothing
ever signs with it — the in-app faucet mints with the mint authority. The demo endpoint's recipient
is not funded either; it only receives.

`BOOTSTRAP_FUND_SOL` lowers the per-key target from its default of 2. Devnet's allowance is real
and rate limited, and a sandbox that creates one mint, one token account and a handful of transfers
needs a fraction of it:

```
BOOTSTRAP_FUND_SOL=0.2 make devnet
```

`make devnet-keys` answers the same question at any time, so the address never has to be
recovered from the scrollback of the run that failed:

```
  state  not prepared — bootstrap has not made a mint here yet

  mint authority (treasury)       5b8S…Q1Mt   0.0000 SOL  NEEDS FUNDING
  faucet                          BVT8…2C2X   0.0000 SOL  never signs anything — never needs SOL
  sample endpoint's facilitator   6dbZ…T1c1   0.0000 SOL  paid from the treasury
  demo endpoint's recipient       Ebxr…zhhw   0.0000 SOL  receives only — never needs SOL
```

It asks bootstrap's question rather than an approximation of it: it reads the recorded mint back
from the chain to decide whether the sandbox is *prepared*, and the threshold changes with the
answer. Before, the treasury needs its target plus whatever the facilitator is short. After, it
needs only fee money — a prepared sandbox sits below its funding target, because it has just paid
rent on a mint, and reporting that as broken would be worse than not reporting at all.

On a public cluster bootstrap does not stop there — it **waits**. It prints the address, then
re-reads the balance every five seconds for `BOOTSTRAP_FUND_WAIT` (default 30m), reporting anything
partial that arrives:

```
  received 0.2000 SOL — still 0.2000 short of 0.4000
  still waiting for 0.2000 SOL — 28m30s left
```

Send the SOL and it carries on by itself: pays the facilitator, creates the mint, and the services
queued behind it start. Nothing has to be re-run, because bootstrap is the one service everything
else waits on — exiting would stop the stack over a step the machine can simply watch for.

It waits only where the money comes from a person. A local validator airdrops on demand, so a
refusal there is a fault and fails at once rather than hiding behind a wait nobody can satisfy.
`BOOTSTRAP_FUND_WAIT=0` restores the immediate exit, which is what an unattended build wants —
nothing is going to send SOL to a CI runner's log.

Either way the keys are written to the state directory before bootstrap stops, so funding the
address from <https://faucet.solana.com> or any funded wallet and running it again works too: it is
idempotent, sees the balance, pays the facilitator, and carries on to the mint.

Once funded, everything else is unchanged — the CAIP-2 identifier for the sandbox is devnet's, so
the x402 wire format is already correct.

**The in-app faucet makes the same judgement.** It asks devnet for SOL, and when that is refused it
checks what the wallet already holds: 0.05 SOL is hundreds of signatures, far more than the wizard
needs, so a refused airdrop is not by itself a failure. The 100 test USDC never depended on the
public faucet at all — it is minted by the authority `bootstrap` created, on a mint we own. It
fails, naming the address and the amount, only when the wallet is genuinely unable to pay its way.

`FAUCET_PER_WALLET_PER_HOUR` is the limit behind *"the faucet has already funded this wallet
recently"* (429). It is a cost limit, not a safety one — the sandbox is play money by design — and
raising it costs nothing on a mint we own.

### What the read paths are tested against

`internal/chain/spl/devnet_test.go` runs the adapter's reads against a real cluster: the slot and
blockhash, an account that exists and one that does not, a decode that must **refuse** a mint
offered as a token account, and a read-back of a transaction the test did not send. Skipped
unless an endpoint is named, because CI must not depend on a public RPC's mood:

```
DEVNET_RPC=https://api.devnet.solana.com go test ./internal/chain/spl -run Devnet -v
```

Reads only. A write test would fail on the faucet rather than on a defect.

## Environment variables

**A missing variable is a boot failure that names it.** Not a warning, not a default. The process
prints to stderr *and* logs, then exits non-zero.

**Every missing variable is named at once**, not the first one found. Fixing one variable per
restart is a bad afternoon, and this is the one place this specification deliberately differs
from failing fast.

| Variable | Used by | Note |
|---|---|---|
| `MONGO_URI` | all | Must include `replicaSet=`. A standalone connection is refused at boot |
| `MONGO_DB` | all | |
| `SESSION_KEY` | web | Signs the owner session. **Never defaulted** |
| `SIWS_DOMAIN` | web | The domain stated in the signed message. A mismatch is a phishing vector |
| `NETWORKS` | all | `sandbox`, or `sandbox,mainnet`. Drives everything below |
| `RPC_SANDBOX_URL` · `RPC_SANDBOX_FALLBACK_URL` | indexer, signer | |
| `RPC_MAINNET_URL` · `RPC_MAINNET_FALLBACK_URL` | indexer, signer | Required **only if** `NETWORKS` contains `mainnet` |
| `USDC_MINT_SANDBOX` · `USDC_MINT_MAINNET` | signer, indexer, web | Each with its token program |
| `TOKEN_PROGRAM_SANDBOX` · `TOKEN_PROGRAM_MAINNET` | same | Classic SPL or Token-2022 |
| `KMS_KEY_ARN` · `AWS_REGION` | **signer only** | Envelope encryption. **Never defaulted** |
| `SIGNER_BASE_URL` | web | Shown in the onboarding snippets |
| `INDEXER_BASE_URL` | web | The proxy target for the SSE stream, read **per request** |
| `TELEGRAM_BOT_TOKEN` | indexer | Required **only if** `ALERTS_ENABLED` |
| `ALLOWED_ORIGINS` | web | |
| `BOOTSTRAP_FUND_SOL` | leashctl | Default 2. SOL wanted in each sandbox key |
| `BOOTSTRAP_FUND_WAIT` | leashctl | Default 30m. How long bootstrap waits to be funded by hand on a public cluster; 0 exits instead |
| `FAUCET_PER_WALLET_PER_HOUR` | web, indexer | Default 1. The 429 on the sandbox faucet |
| `LOG_LEVEL` | all | Default `info` |

### There is no `RPC_URL`

Only the per-network forms. A single global endpoint is the one variable capable of sending a
sandbox payment to mainnet, so it does not exist — and a configuration test reads the config
source and fails if a generic one ever quietly reappears (I8).

### Half-configured is a boot failure

If `NETWORKS` contains `mainnet` and `RPC_MAINNET_URL` is unset, **the process refuses to start**,
naming the variable. The alternative is mainnet enabled and silently pointing somewhere else,
which is the exact failure I8 exists to prevent.

The same applies to alerts: enabled with no bot token is a boot failure, not a service that
quietly never alerts.

### A test reads the configuration source

Straight from the sibling project. The test opens `config.go` and asserts that `SESSION_KEY`,
`KMS_KEY_ARN` and `MONGO_URI` still appear inside a `require(...)` call. If somebody helpfully
gives one a default, the test fails and says why.

The TypeScript side has the same test against `lib/config.ts`.

## Boot order

Load-bearing, in every deployable. Everything that can fail is given the chance to fail **before
the port is bound.**

```
1 · configuration          half-configured fails here, naming everything missing
2 · database + migrations  connect, then apply. Before anything else
2b · schema assertions     the expected indexes and validators exist
2c · privilege assertions  the app role has no bypassDocumentValidation, and no update
                           on an append-only collection
3 · chain probe            report each network's slot. A WARNING, never fatal
4 · background loops       (indexer only) one set per network
4b · warm the caches       (signer only) blockhash per network, before serving
5 · bind the port          LAST
6 · serve until a signal, then drain
```

**Step 2b and 2c are before step 5 on purpose.** A service that starts serving and then discovers
its schema is wrong reports itself healthy while being useless, and a load balancer believes it.
The privilege assertion in particular catches the failure that would silently un-do the entire
audit trail ([12-security.md](12-security.md)).

**Step 3 is a warning, not a failure.** A transient RPC outage should not keep the read paths
down; it should be loud and raise the degraded banner.

**Step 4b exists** because a cold blockhash on the first signature is a p99 spike on the first
payment a new customer makes — which is the demo.

On `SIGTERM`: stop accepting, let in-flight work finish within 30 seconds, drain the signer's
handshake queue, zero every cached key, exit.

## MongoDB

**A replica set, always, including on a laptop.** Multi-document transactions and change streams
both need one, and both are load-bearing — the kill switch's *"instantly"* is a change stream
([06-signer.md](06-signer.md)).

```yaml
services:
  mongo:
    image: mongo:8.0
    command: ["mongod","--replSet","rs0","--bind_ip_all","--keyFile","/etc/mongo/keyfile"]
    ports: ["${MONGO_PORT:-27017}:27017"]     # overridable — a machine often runs two stacks
    volumes:
      - mongodata:/data/db
      - ./scripts/mongo-keyfile:/etc/mongo/keyfile:ro
    healthcheck:
      test: ["CMD","mongosh","--quiet","--eval",
             "try{rs.status().ok}catch(e){rs.initiate({_id:'rs0',members:[{_id:0,host:'mongo:27017'}]}).ok}"]
      interval: 5s
      timeout: 3s
      retries: 30
```

### The trap: `rs.initiate()` returns before the node is writable

It returns before an election has completed, so the first `w:majority` write fails with
`NotWritablePrimary`. Every harness that waits for MongoDB must wait for
`db.hello().isWritablePrimary === true` **twice in a row**, not once.

This is the same shape of trap as a Postgres readiness probe answering during `initdb`. The
comment in the test harness says so, so that nobody removes the doubled check for looking
redundant.

### Two database users

| User | Role | Why |
|---|---|---|
| `leash_app` | The enumerated custom role in [03-data-model.md](03-data-model.md) | Cannot update an append-only collection, cannot bypass validation |
| `leash_migrator` | `dbAdmin` + `readWrite` | Exists so the running application literally cannot `collMod` a validator away |

**Confirm the hosting tier supports custom database roles before committing to this.** Some
managed shared tiers do not, and on those the append-only guarantee degrades to
application-enforced only — a materially weaker claim, which must be written down rather than
quietly accepted.

### Production

Three nodes across availability zones. `w:majority` then costs 2–5 milliseconds per write, and
the signer's budget is built on that number.

Size the **oplog** deliberately. The onboarding SSE stream resumes a change stream by token, and
a token that has aged out of the oplog during a rolling restart leaves a user watching a stalled
wizard on the most important screen in the product. The client falls back to polling the timeline
endpoint, but the oplog should make that rare rather than routine.

**Backups need a real answer.** The audit trail's credibility rests entirely on it, and a nightly
dump is not point-in-time recovery. Budget for it, or say plainly that it is not there yet.

## Where each deployable runs

| Deployable | Runtime | Constraint |
|---|---|---|
| `frontend/website` | Next.js — serverless, or a long-running Node server | No long-lived connections are needed, because the SSE stream lives elsewhere |
| `leash-signer` | Go, long-running | **Same region as the database primary.** Three round trips at `w:majority` across regions eat the p50 budget on their own. Also near the RPC, for the blockhash refresher |
| `leash-indexer` | Go, long-running, **one instance** | Never serverless. Holds cursors and change streams |
| `leash-demo402` | Go | **Sandbox only.** It must not be reachable with mainnet configuration |

### The signer's ingress

Not a preference — a boundary ([01-architecture.md](01-architecture.md)):

- **No public route from the dashboard's origin.**
- Accepts only agent-key authentication.
- No CORS configuration naming the dashboard's origin.
- No debug endpoints, no profiler on a public port, no request-body logging. It holds key
  material.

**The post-deploy smoke test asserts this** by making a browser-shaped request and requiring it to
fail. *"The browser cannot reach the signer"* is not expressible in a linter, so it is verified
where it is actually true or false.

## Make targets

```
make help              every target with its purpose
make up · down · logs  the compose stack
make demo-up           mongo + web + signer + indexer + demo-402, seeded
make migrate           apply migrations
make verify            everything below, on a throwaway replica set
  verify-architecture    --selftest first, then the real checks (both runtimes)
  verify-migrations      up · snapshot · down · assert empty · up · assert identical
  verify-constraints     the sixteen writes the database must refuse
  verify-privileges      append-only, proved as leash_app
  verify-contracts       Go and TypeScript agree on codes, endpoints, vectors, money
  verify-referential-integrity   zero orphans
make test              units and the S0–S7 golden vectors, both runtimes
make e2e-sandbox       create · delegate · read back · pay · timeline · revoke
```

Every target carries a `##` one-line description, and `make help` prints them.

## The runbook

| Incident | Symptom | Do |
|---|---|---|
| Primary RPC dies | Read-back lags; the degraded banner appears | Automatic failover. `unknown` is first-class, so **data becomes late, never wrong** |
| The signer dies | Agents get network errors from `/v1/sign` | **Nothing is signed, so no money is lost. Blocking is safe.** Restart. The on-chain cap still holds. Do not add a bypass |
| The indexer dies | Payments stay `signed`; the slot chip stops advancing | Restart. Every job body is re-runnable — `one_phase_per_payment` refuses a duplicate — so no repair is needed |
| Leash is entirely down | — | [if-leash-is-down.md](if-leash-is-down.md). This is why Settings carries that section (I5) |
| A suspected agent-key leak | An unfamiliar payment in the feed | Kill switch → on-chain revoke → rotate. Ceiling is that allowance's remainder |
| `reserved_base` drift | A warning from `allowance-refresh` with a delta | The refresh repairs it. **Investigate the cause** — drift is a bug, not weather |
| A change stream drops | An error log; kill switches take up to 60 s | The TTL is the floor, so it still works, more slowly. Restart the signer |
| A privilege assertion fails at boot | The process refuses to start | Somebody granted a broader role. Find out who and why **before** restoring it |
| A migration checksum mismatch | The process refuses to start | An applied migration was edited. Do not "fix" the checksum — work out what diverged |
