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
public cluster instead is two variables and one manual step:

```
RPC_SANDBOX_URL=https://api.devnet.solana.com
RPC_SANDBOX_FALLBACK_URL=https://api.testnet.solana.com
```

**`bootstrap` cannot fund itself there.** A local validator airdrops on request; devnet's faucet
is rate limited per IP and frequently exhausted, and mainnet has none at all. So `bootstrap`
attempts all three keys, then stops and prints every address it could not fund, with the amount:

```
this network will not fund 3 of the keys this sandbox needs, and they hold nothing.

    send at least 2.00 SOL to  Ghpz…4gQ5   (mint authority)
    send at least 2.00 SOL to  6Xne…jWBh   (faucet)
    send at least 2.00 SOL to  HBXY…ZiVJ   (sample endpoint's facilitator)
```

All three at once, deliberately — the same reason the configuration loader names every missing
variable at once, except that here each extra round trip also costs a rate-limited faucet. The
keys are already written to the state directory before it stops, so fund them from
<https://faucet.solana.com> or any funded wallet and run `bootstrap` again: it is idempotent, sees
the balances, and carries on to the mint.

Once funded, everything else is unchanged — the CAIP-2 identifier for the sandbox is devnet's, so
the x402 wire format is already correct.

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
| `FAUCET_PER_WALLET_PER_HOUR` | web, indexer | Default 1 |
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
