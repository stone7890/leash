# 01 · Architecture

Two runtimes, four deployables, one MongoDB replica set.

**Next.js owns the dashboard and the ordinary API.** **Go owns the three things that need a
long-running process**: the signer, the indexer, and the sample endpoint. This chapter says what
each part is responsible for, what it is forbidden from doing, and how the forbidding is enforced
— because a boundary that exists only in prose is a boundary that erodes.

## Why the split falls where it does

The deck (chapter 5) specifies TypeScript and Fastify. This build keeps TypeScript for the API
and moves three components to Go, and the reason is not language preference — it is that three
components cannot be request/response handlers at all:

| Component | Why it cannot be a route handler |
|---|---|
| **The signer** | Its budget is under 50 ms at p50, and it only fits because four things live in process memory: the snapshot cache, the unwrapped KMS key, the blockhash, and an open change stream that evicts a killed agent in milliseconds. A serverless invocation has none of them — every call would be a cold KMS decrypt, and the kill switch's *"instantly"* would degrade to a 60-second TTL. Separately, the deck forbids the agent key and the dashboard sharing a process |
| **The indexer** | It is four loops, not endpoints. `alert-eval` runs every 30 seconds, and the interface promises *"alerts fire within 30 seconds"* — a cron with a one-minute floor cannot keep that promise |
| **The test-payment stream** | Server-sent events over a **resumable** change stream, held for the length of an onboarding session. A cursor that cannot survive a function timeout produces a stalled wizard on the most important screen in the product |

Everything else — templates, agents, policy, intents, payments, timeline, faucet, exports, alerts
— is ordinary request/response over MongoDB, and belongs in the same application as the dashboard
that consumes it.

## The four deployables

```
                       ┌──────────────────────────────────────────────┐
   Owner's wallet ────▶│  frontend/website — Next.js 15                   │
   (signs, in the      │  ├─ the dashboard (App Router, shadcn/ui)    │
    browser)           │  └─ app/api/v1/*  — 15 endpoints             │
                       │     CRUD · intents · feed · timeline         │
                       │     audit · alerts · faucet · SIWS           │
                       └───────┬─────────────────────────┬────────────┘
                               │                         │ proxy
                               ▼                         ▼
                       ┌────────────────┐      ┌──────────────────────┐
                       │   MongoDB      │◀─────│  leash-indexer (Go)  │
                       │  replica set   │      │  ├─ four loops       │
                       └────────────────┘      │  ├─ SSE test stream  │
                               ▲               │  └─ the only outward │
                               │               │     poller          │
   Agent runtime ─────▶┌───────┴────────┐      └──────────┬───────────┘
   (customer's MCP,    │ leash-signer   │                 │ read-back
    CLI or service)    │ (Go)           │────────────────▶│
                       │ KMS · S0–S7    │  draw     ┌─────▼──────┐
                       │ the only signer│──────────▶│   Solana   │
                       └────────────────┘           │  sandbox / │
                                                    │   mainnet  │
                       ┌────────────────┐           └─────▲──────┘
                       │ leash-demo402  │                 │ settles
                       │ (Go, sandbox)  │           ┌─────┴──────────┐
                       └────────────────┘           │ x402 endpoint  │
                                                    └────────────────┘
```

Read it as two independent paths that meet only in the database. The **owner path** — dashboard,
API routes, wallet, chain — is interactive and can afford a second. The **agent path** — agent
runtime, signer, chain — is machine-to-machine and has a 300 ms budget. They share no process,
and the signer is deliberately unreachable from the browser.

## Responsibilities, and prohibitions

The prohibitions matter more than the responsibilities. Each is a lesson the deck already learned.

| Component | Responsible for | **Must never** |
|---|---|---|
| **Dashboard** (Next.js pages) | Onboarding, the environment switch, the feed and timeline, the kill switch, one-tap recovery, top-up, audit | Hold a key; compute a balance itself; write to an RPC directly |
| **API routes** (Next.js `app/api/v1`) | CRUD, unsigned intents, feed, timeline, audit, alerts, the faucet, export jobs | **Sign anything**; import the KMS client; issue a query that could span both networks |
| **leash-signer** (Go) | Holding every agent key; evaluating S0–S7; signing transfers inside an allowance; writing handshake phases 1–3. The only holder of keys and the only signer | Hold an owner key; make an outbound HTTP call inside the signing path; sign a challenge whose network differs from the key's |
| **leash-indexer** (Go) | Read-back per network, reconciliation, alert evaluation, handshake phases 5–6, the test-payment runner and its SSE stream. The only component that polls outward | Infer `confirmed` from an HTTP response; write to an append-only collection twice; call a signing function |
| **leash-demo402** (Go) | One sample x402 endpoint at $0.01 test-USDC; self-verifies and submits to the sandbox | Accept a mainnet request |

**One prohibition sits above all of them:** the agent key and the owner key are never in the same
process. And one signer process serves both networks — but the key reference is always looked up
by `(agent, network)`, so **there is no code path that can fetch a mainnet key for a sandbox
challenge.**

## Lanes, and the edges that are forbidden

| Forbidden edge | Why |
|---|---|
| Agent → Solana, signing or submitting for itself | It would bypass every soft rule. The agent never sees a key |
| **Browser → Signer** | Narrows the attack surface around the keys. The signer is not routable from the public dashboard origin |
| API routes → Solana, **writing** | Only the owner's wallet signs treasury transactions, and only the signer signs agent transactions. This is I1 |
| Signer → x402 endpoint | The signer signs. The agent retries. An outbound call would blow the latency budget and widen the trust boundary |
| Any lane → a network other than the record's | I8. Absolute isolation, including the sample endpoint, which lives only on the sandbox |

## Where each endpoint lives

Seventeen endpoints. [05-api-contract.md](05-api-contract.md) is the contract; this is the map.

| Endpoints | Runtime |
|---|---|
| SIWS nonce and verify; templates; agents create/get/list; delegation, modify and revoke intents; policy and allow-hosts; key rotation; payments list; timeline; `POST /v1/test-payments`; faucet; audit exports; jobs; alerts | **Next.js** `app/api/v1/*` |
| `POST /v1/sign` | **leash-signer** |
| `GET /v1/test-payments/:id/stream` | **leash-indexer**, proxied through Next.js so the browser sees one origin |

The proxy is a catch-all route reading its target **per request** from the environment, not a
`next.config` rewrite — a rewrite bakes the URL in at build time, and the target differs between
sandbox and mainnet deployments.

## The TypeScript side

```
frontend/website/
├─ app/
│  ├─ (dashboard)/            the nine screens
│  │  ├─ agents/ · payments/ · alerts/ · audit/ · billing/ · settings/
│  │  └─ onboarding/
│  ├─ api/v1/                 the fifteen route handlers
│  │  ├─ agents/route.ts · agents/[id]/route.ts
│  │  ├─ agents/[id]/delegation-intents/route.ts · modify-intents · revoke-intents
│  │  ├─ agents/[id]/policy/route.ts · policy/allow-hosts/route.ts
│  │  ├─ agents/[id]/keys/rotate/route.ts · payments/route.ts
│  │  ├─ payments/[id]/timeline/route.ts · test-payments/route.ts
│  │  ├─ sandbox/faucet/route.ts · audit/exports/route.ts · jobs/[id]/route.ts
│  │  ├─ alerts/route.ts · templates/route.ts · auth/siws/*
│  │  └─ [...proxy]/route.ts  → leash-indexer, for the SSE stream
│  └─ layout.tsx · globals.css
├─ lib/
│  ├─ domain/                 ─── pure. No I/O, no clock, no driver ───
│  │  ├─ money.ts             bigint micro-USDC. String on the wire. No `number` exists here
│  │  ├─ network.ts · ids.ts · state.ts · tier.ts · fault.ts
│  ├─ store/                  ─── THE ONLY module that imports the Mongo driver ───
│  ├─ chain/                  intent building, read-only. No signing
│  ├─ auth/                   SIWS verification. Called explicitly, never middleware
│  ├─ config.ts               a missing secret is a boot failure
│  └─ api-client.ts           the browser's typed client
└─ components/               + components/ui (shadcn)
```

## The Go side

One module, `github.com/stone7890/leash`. Three binaries plus a tool.

```
backend/
└─ cmd/
   ├─ leash-signer/     POST /v1/sign. The only process linked against internal/kms
├─ leash-indexer/    the four loops, the test-payment runner, the SSE stream
├─ leash-demo402/    the $0.01 tUSDC sample endpoint. Sandbox only
└─ leashctl/         migrate · verify-architecture · verify-constraints · verify-privileges
                     verify-migrations · verify-referential-integrity · verify-contracts · seed

internal/
├─ domain/           ─── layer 0 · imports nothing outside the standard library ───
│  ├─ money/ network/ ids/ state/ tier/ challenge/ budget/ fault/
│  └─ policy/        S0–S7 as pure functions over a Snapshot. THE policy engine
├─ store/            ─── layer 1 · the only Go package that imports the driver ───
├─ migrate/          ─── layer 1 · numbered Go migrations, the boot-time runner ───
├─ chain/            ─── layer 1 · the only package that talks to Solana ───
│  ├─ adapter.go · spl/ · subs/(stub) · rpc/ · blockhash/
├─ kms/              AWS KMS envelope encryption. Imported by the signer ALONE
├─ auth/ config/ obs/ wire/ jobs/ alerting/
├─ signer/           ─── layer 2 ───
│  ├─ hot/           THE SIGNING PATH, its own package so the bans are checkable
│  └─ cache/         snapshot · unwrapped key · blockhash
├─ indexer/          ─── layer 2 ───  allowancerefresh · paymentsweep · alerteval
│                                     faucetguard · testrunner · stream
└─ demo402/          ─── layer 2 ───
```

`internal/domain` depends on nothing and everything depends on it, because it is the arithmetic
and the rules — and rules that reached for a database or a clock would not be testable in the way
that makes them trustworthy. The policy engine takes `now` as a parameter for exactly this reason.

## Two runtimes, one database, and how that is kept safe

This is the cost of the split, and it is paid deliberately.

**There are now two MongoDB access layers** — `internal/store` in Go and `lib/store` in
TypeScript — where the discipline elsewhere is *all database access in one place*. That
duplication is survivable for one reason, and it is the reason the schema work in
[03-data-model.md](03-data-model.md) matters so much:

> **The enforcement point is the database, not either client.** The validators, the partial unique
> indexes and the role privileges constrain both runtimes identically, and neither can weaken
> them. `verify-constraints` tests the server, so it proves the rule for Go and TypeScript at
> once.

Three further mechanisms keep the two sides from drifting:

- **`contracts/` is a shared source of truth**, committed and language-neutral:
  `error-codes.json` (the frozen table from [11-errors-and-codes.md](11-errors-and-codes.md)),
  `endpoints.json` (path, method, status codes, auth), and the S0–S7 golden vectors. Both
  runtimes are checked against it by `leashctl verify-contracts` and by a Vitest suite, so a code
  added on one side and not the other fails the build.
- **The golden vectors are executed twice.** The Go policy engine runs them, and so does the
  TypeScript side, against the same JSON files. The API routes do not evaluate policy — that is
  the signer's job — but they render blocked verdicts and must agree on what the codes mean.
- **Money has one definition in each language and a shared test corpus.** `money.Base` in Go and
  `money.ts` in TypeScript both parse and format from `contracts/money-vectors.json`, including
  the values that break a `number`.

**Ownership stays single, even though access does not.** Each collection has exactly one writer,
per the table below, and a checker asserts it — the TypeScript store has no method that writes
`agent_keys`, `sign_requests` or an allowance's chain-read fields.

## Who owns what

| Runtime | Owns | Timing |
|---|---|---|
| **Next.js API** | `orgs` `agents` `templates` `policies` `policy_revisions` `alerts` `suppressions` `audit_jobs` `siws_nonces` | Synchronous, except the audit export, which is 202 plus a job |
| **leash-signer** | `agent_keys` `sign_requests` `sign_claims`; **inserts** `payments`; handshake phases 1–3 | Synchronous, under 300 ms at p99 |
| **leash-indexer** | `allowances`; `payments` state transitions; handshake phases 5–6 | Asynchronous, on the cadences in [07-indexer-and-jobs.md](07-indexer-and-jobs.md) |

`payments` is the one collection two runtimes touch. The signer inserts, the indexer transitions,
and neither contends on the other's fields. Nobody may delete one — a database privilege, not a
rule.

## Enforcing the boundaries

Go has no crate privacy and TypeScript has no module privacy. Both sides use a checker.

### Go — `leashctl verify-architecture`

Over the real package graph with `go/packages` and the real type graph with `go/types`. Not a
grep: greps miss dot-imports, aliases and blank imports.

| Rule | Catches |
|---|---|
| `domain/...` imports only the standard library | The day someone needs "just one" driver type inside a rule |
| Only `store`, `migrate` and `leashctl` import the driver | Ad-hoc queries growing inside handlers |
| **No driver type in `store`'s exported API**, recursively through struct fields | The real failure: a `store.Coll(name) *mongo.Collection` helper, after which the boundary is decorative while a grep still passes |
| `cmd/leash-indexer` does not reach `internal/kms` transitively | Only the signer holds keys |
| `signer/hot` does not directly import `net/http`, `chain/rpc` or `alerting` | An outbound call creeping into the 300 ms path |
| `indexer` does not import `signer/hot` or any signing function | I4 — a stuck payment is repaired by reading, never by re-signing |
| Every network-scoped store method takes `network.Network` | I8, made mechanical |
| No `float64`, `ParseFloat` or `map[string]any` decode in a money path | I6 |

### TypeScript — `dependency-cruiser`, plus an ESLint rule

| Rule | Catches |
|---|---|
| `lib/domain/**` imports nothing but the standard library and itself | The same erosion, from the other direction |
| Only `lib/store/**` imports `mongodb` | Ad-hoc queries in route handlers |
| **`app/**` never imports `mongodb` directly** | The most likely accident, given how easy a route handler makes it |
| **Nothing under `frontend/website` imports an AWS KMS client** | I1, on the TypeScript side. There is no key material in this deployable, and this is what says so |
| `lib/store` exports no `Collection` or `Db` type | The driver-leak rule, again |
| Every route handler's first statement is `await requireOwner(req)` | Auth that fails closed — a middleware you forgot to attach fails open |
| `lib/domain/money.ts` contains no `number` arithmetic and no `parseFloat` | I6 |
| No `NEXT_PUBLIC_*` variable exists | Everything is read server-side at runtime |

Every check on both sides ships with a self-test that plants each violation and asserts rejection,
and **the self-tests run first in CI**. A gate nobody has watched reject something is a comment.

### The one check that is not an import rule

Import graphs cannot see through a function pointer, and `store` reaches `net/http` transitively
through the driver anyway. So the ban on outbound calls in the signing path has a third layer, a
test rather than a rule:

> Replace `http.DefaultTransport` with one that panics. Warm the caches. Run a thousand
> signatures. Assert nothing panicked.

That test fails the moment somebody adds a "quick" RPC call to refresh a blockhash, or an alert on
a blocked verdict — neither of which would be caught by reading the diff.

### And one that is a deployment fact

*"The browser cannot reach the signer"* is not expressible as a lint rule, because the browser is
not in the build graph. It is enforced by deployment: **the signer has no public route from the
dashboard's origin**, its ingress accepts only agent-key authentication, and it has no CORS
configuration permitting the dashboard's origin. [14-deployment.md](14-deployment.md) states it as
a requirement, and the smoke test after each deploy asserts it.

## What runs where

| | Development | Production |
|---|---|---|
| `frontend/website` | `:3000` — dashboard and API routes | Serverless, or a long-running Node server |
| `leash-signer` | `:4100` | **Same region as the database primary** — three round trips at `w:majority` otherwise eat the p50 budget |
| `leash-indexer` | `:4300` | One instance. Long-running, never serverless |
| `leash-demo402` | `:4200` | Sandbox only |
| MongoDB | Single-node replica set, in Compose | Three nodes across availability zones |

**There is no standalone MongoDB mode, ever, including on a laptop.** Multi-document transactions
and change streams both require a replica set, and both are load-bearing.
[14-deployment.md](14-deployment.md) covers the single-node set-up and the election-timing trap
that comes with it.
