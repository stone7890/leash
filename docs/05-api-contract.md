# 05 · API contract

Seventeen endpoints across three runtimes.

| Endpoints | Served by | Auth |
|---|---|---|
| Fifteen: SIWS, templates, agents, intents, policy, keys, payments, timeline, `POST /v1/test-payments`, faucet, exports, jobs, alerts | **Next.js route handlers**, in the same deployable as the dashboard | owner session |
| `POST /v1/sign` | **leash-signer** (Go) | agent key |
| `GET /v1/test-payments/{id}/stream` | **leash-indexer** (Go), proxied through Next.js so the browser sees one origin | owner session |

They never share a process, and **the browser has no route to the signer at all** — that is a
deployment fact as well as a design one ([01-architecture.md](01-architecture.md)).

This document is the contract. `contracts/endpoints.json` is its machine-readable form, and CI
fails if an endpoint documented here is not routed in the runtime that claims it, or a routed
endpoint is not documented here.

## Conventions

**Paths are versioned.** Everything is under `/v1`. Unlike the sibling project this repository
borrows its discipline from, the version is in the path — because an agent SDK in a customer's
process is a client we do not control and cannot ask to redeploy.

**Field names are `snake_case`.** Successful responses are the bare resource; there is no success
envelope. Errors always have one. The store layer speaks the app's own camelCase types and
`lib/api/shapes.ts` is the one place they become wire names — handing a query result straight to
the serialiser is how `perTxMax` and `readBackSlot` once reached callers who were promised
`per_tx_max` and `read_back_slot`.

**Money is a string, in both directions.** `"7.420000"`, always six decimal places.
`{"amount":"0.31"}` is accepted and `{"amount":0.31}` is rejected with `INVALID_AMOUNT`, because
JSON numbers are IEEE doubles (I6).

**Timestamps are RFC 3339 with a `Z`.** Slots are strings too, for the same reason as money.

**Every response that describes on-chain state carries the slot it was read at** (I2). If the
value is a cache and we have not read since, the response says when we last did.

### Status discipline

| Status | Means | Used by |
|---|---|---|
| `200` | Here is the answer | reads, and `/v1/sign` when allowed |
| `202` | Accepted, not done. Poll the job, or wait for the read-back | every endpoint that touches the chain, and every export |
| `400` | The request is malformed | schema failures |
| `401` | Not authenticated | a missing or expired session, a dead API key |
| `403` | A policy verdict, or a forbidden action | `/v1/sign` when blocked; the eight rule codes live here |
| `404` | No such resource, or not yours | a mismatched organisation returns this, never `403` |
| `409` | A conflict | idempotency conflict, an in-flight claim, a second live allowance |
| `422` | A business rule refused it | validation that needed domain knowledge |
| `429` | Rate limited | the faucet |
| `503` | A dependency is unavailable, retriable | RPC failover exhausted |

**Every chain-touching endpoint returns `202`, never `200`.** There is no path by which a client
can read *"the API returned OK"* as *"the money moved"*. This is I4 expressed in the transport.

### The error envelope

Always exactly one top-level key.

```json
{
  "error": {
    "code": "ENDPOINT_NOT_ALLOWED",
    "message": "api.unknown.xyz is not on this agent's allow list",
    "field": "host",
    "details": { "rule": "S2", "tier": "signer", "checks_reached": 2, "checks_total": 8 },
    "retriable": false,
    "trace_id": "01J8XKQ2M4Z7YB3F9C5R6D8W1H"
  }
}
```

- `code` is **stable and never renamed**. It is an interface: the SDK branches on it and runbooks
  map it to actions. [11-errors-and-codes.md](11-errors-and-codes.md) is the frozen register.
- `message` is for a person, and may be translated.
- `field` lets the interface put the error under the input that caused it, rather than in a
  generic toast.
- `details` is structured and optional.
- `retriable` is **always present, even when `false`** — an SDK must not have to infer it from
  the code.
- Absent optional members are **omitted, never `null`**.
- `trace_id` joins the response, the log line and the `handshake_events` detail. Give it to
  support and they can find the request.

## Authentication

### Owners — Sign-In With Solana

The wallet is the account. There is no password and no email address anywhere in the system.

```
POST /v1/auth/siws/nonce      → { nonce, expires_at, statement, domain }
POST /v1/auth/siws/verify     → { session, expires_at, org }
```

The message the wallet signs states the domain, the nonce, and what signing means. The nonce is
**issued, stored, and consumed by deletion** — read-and-delete in one operation, so a replay finds
nothing. It expires by TTL after five minutes.

> **The deck specifies SIWS and specifies no nonce store.** Without one, a signed sign-in message
> is replayable for as long as it exists: anyone who observes it can present it again. This is a
> gap in the specification, not an artefact of the stack, and the `siws_nonces` collection fills
> it. Registered in [16-deck-conformance.md](16-deck-conformance.md).

The session is a signed token, `SESSION_SECONDS` = 8 hours. The signature is verified **before**
the payload is parsed, and the comparison is constant-time. Every sign-in failure returns one
identical `401`, but the real reason is always logged.

**Authentication is not middleware.** Every handler's first statement is
`auth.RequireOwner(c)`, and `verify-architecture` asserts it on the AST. A middleware you forgot
to attach fails open; a first line you forgot to write fails the build.

### Agents — the API key

`lk_test_…` on the sandbox, `lk_live_…` on mainnet. Shown once at creation, and once again after
a rotation. Stored as a hash; we cannot show it to you again, and the interface says so.

The key authenticates against the **signer only**. The Next.js API routes never accept one. Rotation
invalidates the old key immediately — there is no grace period, and the interface warns before
you rotate.

## Idempotency

`Idempotency-Key` is **required on every creating POST**, and its absence is a `400`. It is
claimed in the database before any work happens.

| Outcome | Response |
|---|---|
| Claimed, work done | the normal `200` / `202` |
| Same key, same body, already done | the **original** response, replayed |
| Same key, still in flight | `409` with `retriable: true` — wait and retry with the same key |
| Same key, **different** body | `409 IDEMPOTENCY_CONFLICT` — the key is spent |

Enforced by a unique index, not an application check (I7): two instances behind a load balancer
defeat an application check the first time they race, and a unique index does not race.

## Pagination

Cursor-based, on the collections that grow without bound.

```
GET /v1/agents/{id}/payments?cursor=<opaque>&limit=50
→ { "items": [...], "next_cursor": "…" }     // next_cursor omitted when exhausted
```

`limit` defaults to 50 and is capped at 200. The cursor is an opaque encoding of the ULID
`_id` — offsets skip and duplicate rows under concurrent inserts, which on a payment feed means
an owner is shown a payment twice or not at all.

---

## The endpoints

### Templates

```
GET /v1/templates                                                    owner   200
```

The three seeded templates and their prefills. Read-only.

```json
[ { "id": "research", "name": "Research agent",
    "description": "Search & content APIs. Small payments, tight per-call limit.",
    "prefills": { "cap": "10.000000", "per_tx_max": "0.250000",
                  "velocity_max": "1.000000", "velocity_window_s": 600,
                  "allow_hosts": ["api.exa.ai", "api.helius.dev"] } } ]
```

### Agents

```
POST /v1/agents                                                      owner   201
GET  /v1/agents/{id}                                                 owner   200
GET  /v1/agents?network=sandbox&cursor=…                             owner   200
```

`POST` requires `Idempotency-Key`. The body names `network`, and **the network is fixed at
creation and can never change** (I8) — it becomes part of the agent's identifier. Creating an
agent generates its keypair inside the signer, wraps it with KMS, and returns the API key **once**.

The template supplies every limit; each one can be overridden in the body, and the same two rules
`PATCH /v1/agents/{id}/policy` applies hold here — `velocity_window_s` is 1 to 86400 seconds, and a
per-payment maximum above the window's own limit is a `422` rather than an agent whose first
payment contradicts the screen that created it.

```jsonc
// request — everything but `name` is optional; the template fills the rest
{ "name": "research-bot-01", "template_id": "research", "network": "sandbox",
  "runs_as": "mcp",
  "cap": "10.000000", "expiry_days": 7,
  "per_tx_max": "0.250000", "velocity_max": "1.000000", "velocity_window_s": 600,
  "allow_hosts": ["api.exa.ai"] }

// response — api_key appears exactly once, here
{ "id": "agt_test_01J8XK…", "name": "research-bot-01", "network": "sandbox",
  "pubkey": "Gk7v…tR2m", "killed": false, "created_at": "2026-09-04T14:00:00Z",
  "api_key": "lk_test_9f2c41ab…e77d",
  "policy": { "allow_hosts": ["api.exa.ai","api.helius.dev"], "allow_all": false,
              "per_tx_max": "0.250000", "velocity_max": "1.000000",
              "velocity_window_s": 600 },
  "allowance": null }
```

`GET` returns the agent with its policy and its allowance, the latter carrying `last_read_slot`
and `expiry_tier` — the interface renders the tier from that field, never from a constant (I3).

### Transaction intents

```
POST /v1/agents/{id}/delegation-intents                              owner   202
POST /v1/agents/{id}/modify-intents                                  owner   202
POST /v1/agents/{id}/revoke-intents                                  owner   202
```

Each returns an **unsigned transaction**, base64, built for the agent's own network. The owner's
wallet signs it in the browser. Leash never signs a treasury transaction (I1), and these three
endpoints are the only place the API composes one.

```json
{ "intent_id": "int_01J8XK…", "network": "sandbox",
  "transaction": "AQABA…", "blockhash": "8Wq…k2N",
  "expires_at": "2026-09-04T14:01:30Z",
  "summary": { "cap": "10.000000", "expiry_ts": "2026-09-11T14:00:00Z",
               "delegate_to": "Gk7v…tR2m", "network_fee": "0.000200",
               "rent": "0.002039" } }
```

**`expires_at` is real and short.** A Solana blockhash lasts 60–90 seconds, so the intent is
built immediately before the wallet opens, and the interface rebuilds it if more than 45 seconds
pass before the user signs. The prototype's *"Try again"* branch exists for this.

`modify-intents` is the top-up and the extension. **The old allowance keeps working until the new
transaction confirms** — there is no dead window, and the interface must not show one.

`revoke-intents` additionally sets `killed = true` **immediately**, before the transaction is
even built, so the signer starts refusing at once (S7). The on-chain revoke lands later. That gap
is the nature of the chain and the interface keeps saying `revoking…` until the read-back — see
[09-flows.md](09-flows.md) P5.

> The revoke intent contains **two** instructions on the SPL implementation: the revocation *and*
> a sweep of the remaining balance back to the owner. See
> [08-onchain-adapter.md](08-onchain-adapter.md).

### Policy

```
PATCH /v1/agents/{id}/policy                                         owner   200
PATCH /v1/agents/{id}/policy/allow-hosts                             owner   200
```

The first changes the two signer-tier limits. The second **adds exactly one host** and is the
endpoint behind the one-tap recovery button in the payment drawer.

```jsonc
// PATCH /v1/agents/{id}/policy — every field optional; absent means unchanged
{ "per_tx_max": "0.250000", "velocity_max": "1.000000", "velocity_window_s": 600 }

// PATCH /v1/agents/{id}/policy/allow-hosts
{ "host": "api.unknown.xyz" }
```

Absent is **unchanged**, not zero: a caller raising the per-payment maximum must not silently reset
a velocity limit it never mentioned. `velocity_window_s` is a whole number of seconds from 1 to
86400, and a per-payment maximum above the window's own limit is a `422` — it would read as "up to
$1 a payment" on a screen where the window makes $0.50 the real answer.

It does **not** touch the cap or the expiry. Those are the on-chain delegation: moving them takes a
transaction the owner signs in their wallet, and offering them here would put a signer-tier promise
on numbers the interface calls enforced by Solana.

Both write a `policy_revisions` row in the same transaction as the change, so the audit row and
the change land together or not at all.

**The one-host rule is enforced server-side, not just in the interface.** A wildcard, a parent
domain, or a list is a `422`. The trap the deck names for flow P3 is a recovery button that
quietly widens the allow-list beyond the host that was actually blocked, and the server is where
that has to be prevented — the button is not the only caller.

Policy changes take effect **from the next payment**, never retroactively — and not instantly: the
signer holds a snapshot of the agent for up to 60 seconds, and its change stream watches kills,
revocations and key rotations, not policies. So a saved limit binds on the first payment after that
snapshot expires, and the interface says exactly that rather than promising immediacy the cache
does not deliver.

### Keys

```
POST /v1/agents/{id}/keys/rotate                                     owner   200
```

Returns the new key once. **The old key dies immediately.** There is no overlap window: a
rotation is usually a response to a suspected leak, and a grace period would keep the leaked key
working through exactly the minutes that matter.

### Signing — on `leash-signer`

```
POST /v1/sign                                                        agent key   200 | 403
```

The only endpoint on the signer, and the only route in the system authenticated by an agent key.

```jsonc
// request — the challenge, forwarded verbatim by the agent
{ "challenge": { "scheme": "exact", "network": "sandbox",
                 "pay_to": "Ex4…j8Wq", "amount": "0.010000",
                 "mint": "…", "nonce": "…", "version": "x402/1" },
  "host": "api.sandbox.leash.dev" }

// 200 — allowed
{ "payment_id": "pay_test_01J8XK…", "signature": "3xk…9Qe",
  "payload": "…base64 X-PAYMENT…",
  "allowance": { "remaining": "7.410000", "read_at_slot": "356442108" } }

// 403 — blocked. Nothing was signed and nothing moved.
{ "error": { "code": "ENDPOINT_NOT_ALLOWED", "message": "…", "retriable": false,
             "details": { "rule": "S2", "tier": "signer",
                          "checks": [ {"rule":"S0","tier":"signer","result":"pass"},
                                      {"rule":"S1","tier":"onchain","result":"pass"},
                                      {"rule":"S2","tier":"signer","result":"fail"} ],
                          "checks_total": 8 } } }
```

Retrying with the same challenge returns the **same** answer, byte for byte — the challenge hash
is the idempotency key, enforced by a unique index (I7). A challenge is never signed twice, and
[06-signer.md](06-signer.md) explains why that holds even across a crash mid-signature.

`403` is the verdict status. It is not an error in the sense of something having gone wrong: a
block is the product working. The `details.checks` array is what the interface renders as the
timeline's phase 2, and its length is where the check count comes from.

### Payments and the timeline

```
GET /v1/agents/{id}/payments?cursor=…&status=…                       owner   200
GET /v1/payments/{id}/timeline                                       owner   200
GET /v1/payments?agent=…&status=…&host=…&cursor=…                    owner   200
```

The timeline returns the handshake phases as recorded — up to six, and **a phase that has not
happened is absent, not `null`**. The interface renders absence as "not reached" for a blocked
payment and as a spinner for one still in flight, and those are different presentations.

```json
{ "payment_id": "pay_test_01J8XK…", "state": "confirmed",
  "phases": [
    { "phase": "challenge_received", "at": "…", "writer": "signer",
      "detail": { "scheme": "exact", "amount": "0.012000" } },
    { "phase": "rules_evaluated",    "at": "…", "writer": "signer",
      "detail": { "passed": 8, "total": 8 } },
    { "phase": "signed",             "at": "…", "writer": "signer",
      "detail": { "allowance": "3xk…9Qe", "signer_ms": 41 } },
    { "phase": "broadcast",          "at": "…", "writer": "indexer" },
    { "phase": "confirmed",          "at": "…", "writer": "indexer",
      "read_back_slot": "356441977", "detail": { "remaining": "7.420000" } } ]}
```

Phase 4, `replayed`, happens inside the customer's agent and is usually **unobservable** — see
[07-indexer-and-jobs.md](07-indexer-and-jobs.md). The interface renders it dimmed rather than
pretending it did not happen.

### The test payment

```
POST /v1/test-payments                                               owner   202
GET  /v1/test-payments/{id}/stream                                   owner   200 (SSE)
```

Onboarding step 5. `POST` starts a **real** payment loop against `demo-402` on the sandbox and
returns a job identifier. `GET` streams the log as server-sent events.

> **The deck is emphatic that this is the most often botched step.** The nine-line log must be
> real: one event per `handshake_events` row, streamed from the server as it happens. It must not
> be a scripted animation in the frontend. A faked log here would be a demonstration that the
> product works, given to a user for whom it might not.

The stream is a change stream on `handshake_events`, resumable. If the connection drops, the
client falls back to polling the timeline endpoint — an onboarding session that spans a rolling
restart must not leave the user watching a stalled wizard on the single most important screen in
the product.

### Faucet

```
POST /v1/sandbox/faucet                                              owner   202
```

Sends 100 test USDC to the owner's wallet. **Sandbox only** — a mainnet call is a `422`, not a
`403`, because it is a nonsensical request rather than a forbidden one. Rate limited per wallet,
per hour, by `sandbox-faucet-guard`; over the limit is a `429` naming when it resets.

### Audit exports and jobs

```
POST /v1/audit/exports                                               owner   202
GET  /v1/jobs/{id}                                                   owner   200
```

**Always `202`.** The export is prepared in the background; the endpoint queues it. The
interface says *"Export queued. It appears below when ready — nothing to wait on."* and the
exports table shows it as `preparing` until it flips to `ready`.

```json
{ "id": "job_01J8XK…", "kind": "audit_export", "status": "ready",
  "range_from": "2026-08-01", "range_to": "2026-09-04",
  "url": "https://…", "expires_at": "…" }
```

One CSV row per payment: the agent, the endpoint, the amount, the status history and the on-chain
signature. The export is streamed from a cursor rather than buffered, because a wide date range
over months of payments does not fit in memory and the whole point of the 202 was to have room.

### Alerts and settings

```
GET   /v1/alerts?cursor=…                                            owner   200
GET   /v1/alert-settings                                             owner   200
PATCH /v1/alert-settings                                             owner   200
POST  /v1/alert-settings/test-webhook                                owner   202
POST  /v1/agents/{id}/suppressions                                   owner   201
```

`suppressions` is the *"This block is correct"* button. It silences the alert for one
`(agent, host)` pair and **does not** stop the blocking — the interface says so, because a
customer who believes it whitelisted the host will be confused by the next block.

## What the API never does

Worth stating as a list, because each is enforced somewhere:

- **It never signs anything.** Not a treasury transaction, not a draw. **Nothing under
  `frontend/website` may import a KMS client**, asserted by `dependency-cruiser` — there is no key
  material in that deployable at all.
- **It never touches an agent key.** Key material exists only inside the signer.
- **It never writes to the chain.** It composes unsigned transactions and reads.
- **It never issues a query that could span both networks.** Every network-scoped store method
  takes the network as an argument, asserted on the AST.
- **It never returns `200` for something that has not yet happened on-chain.**
