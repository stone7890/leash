# Leash — build documentation

Specification for **Leash**: a non-custodial spend-management layer for AI agents on Solana. An
owner delegates an on-chain spending allowance — a cap and an expiry — to each agent. The agent
then pays for APIs by itself over the x402 protocol, inside those limits, and every cent is
attributed.

> Give every agent a budget. Not your wallet.

**This documentation covers the whole system**: the API, the signer, the indexer, the sample
x402 endpoint, the shared core, and the dashboard. Leash writes **no smart contract** — it calls
an existing Solana program through an adapter, and [08-onchain-adapter.md](08-onchain-adapter.md)
is where that boundary is drawn.

## Source of truth

Everything here derives from two files:

```
resources/specs/leash-technical-deck-v3-1.html    the specification
resources/specs/leash-app-prototype-v2.html       the interface
```

| File | What it is | Authority |
|---|---|---|
| `leash-technical-deck-v3-1.html` | 17 chapters, Vietnamese prose: 8 invariants, 6 business phases, 8 safety rules, a 13-table DDL, 17 endpoints, a 4-week plan | **The specification.** References to *chapter N* point into it, numbered in display order |
| `leash-app-prototype-v2.html` | A clickable fake-data SPA: 9 screens, 4 modals, the real copy and the real colour tokens | **The interface source of truth** — the deck says so itself, in chapter 15 |

Where the deck contradicts itself, contradicts the prototype, or leaves something unspecified,
this documentation records the resolution and the reasoning behind it.
[16-deck-conformance.md](16-deck-conformance.md) is where every such resolution is registered.

**If you are about to implement something and this set does not answer your question, the answer
does not exist yet** — add it to [16-deck-conformance.md](16-deck-conformance.md) rather than
inventing it in code.

## Reading order

| # | Document | Covers |
|---|---|---|
| 00 | [00-overview.md](00-overview.md) | What Leash is, the actors, the scope boundary, what is deliberately not built |
| 01 | [01-architecture.md](01-architecture.md) | The four apps and the core, the dependency DAG, the lanes, the forbidden edges |
| 02 | [02-invariants.md](02-invariants.md) | I1–I8: the rule, what breaking it costs, and the exact mechanism that enforces it |
| 03 | [03-data-model.md](03-data-model.md) | Every collection, its validator, its indexes, and the PostgreSQL→MongoDB mapping |
| 04 | [04-policy-engine.md](04-policy-engine.md) | S0–S7, enforcement tiers, budget arithmetic, the FIFO rule, the latency budget |
| 05 | [05-api-contract.md](05-api-contract.md) | All 17 endpoints, the error envelope, status discipline, idempotency, SIWS |
| 06 | [06-signer.md](06-signer.md) | Key custody, the caches, the ordered signing path, deterministic replay |
| 07 | [07-indexer-and-jobs.md](07-indexer-and-jobs.md) | The four loops, read-back by signature, reconciliation, degraded mode |
| 08 | [08-onchain-adapter.md](08-onchain-adapter.md) | The adapter interface, the SPL implementation, the Solana traps |
| 09 | [09-flows.md](09-flows.md) | P1–P6 step by step, and the three state machines |
| 10 | [10-ui-spec.md](10-ui-spec.md) | The 9 screens, the state rules, the tokens, the copy that must be used verbatim |
| 11 | [11-errors-and-codes.md](11-errors-and-codes.md) | The frozen error-code table |
| 12 | [12-security.md](12-security.md) | Threat model, key residency, append-only, blast radius, the runbook |
| 13 | [13-testing.md](13-testing.md) | Golden vectors, the constraint proofs, the sandbox end-to-end run, CI |
| 14 | [14-deployment.md](14-deployment.md) | Environment variables, boot order, the replica set, compose |
| 15 | [15-roadmap.md](15-roadmap.md) | The four weeks, the definition of done, the reject-merge-if list |
| 16 | [16-deck-conformance.md](16-deck-conformance.md) | **The audit record** — the deck line by line against what we build |

One more file sits outside the numbering:

- [if-leash-is-down.md](if-leash-is-down.md) — the public, self-serve revoke guide. It exists
  because invariant **I5** promises the kill switch survives Leash being down, and a promise like
  that is only true if the instructions are published where a customer can reach them without us.
  The Settings screen links to it. It is not internal documentation.

## Running it

```
make prod    everything in containers, behind nginx — what a VM needs
make dev     infrastructure in containers, the rest on the host — what you want while changing it
make demo    the whole loop: a wallet, a budget, a real x402 payment, a read-back
```

[14-deployment.md](14-deployment.md) covers the difference, and why the two keep separate
databases.

## Shape of the system in one page

```
Owner (wallet)   signs every transaction that touches the treasury. Leash holds no owner key.
frontend/website Next.js 15. The dashboard, AND 15 of the 17 API endpoints as route handlers.
                 Never signs. Never imports a KMS client.
leash-signer     Go, separate process, AWS KMS. Holds every agent key. Evaluates S0–S7.
                 The only holder of keys, and the only signer. Serves POST /v1/sign.
leash-indexer    Go, long-running. Four loops: read-back, reconciliation, alerts, faucet guard.
                 The only component that polls outward. The only writer of `confirmed`.
                 Also serves the onboarding SSE stream, which needs a resumable change stream.
leash-demo402    Go. A sample x402 endpoint at $0.01 test-USDC, for onboarding step 5. Sandbox only.
Solana           Surfnet sandbox, or mainnet. The hard source of truth on both.

Money            USDC only, as an integer count of micro-USDC. Never a float, anywhere.
Networks         sandbox and mainnet, isolated absolutely, from the identifier upward.
Enforcement      hard on-chain: cap, revoke.   soft at the signer: allow-list, per-tx,
                 velocity, expiry.   The interface must say which, every time.
Custody          NONE. Leash holds no funds and no owner keys, and there is no code path
                 by which it could.
```

## Decisions that override the deck

Four places where this specification deliberately differs from chapter 5 and chapter 13. Each is
argued in full in [16-deck-conformance.md](16-deck-conformance.md).

| The deck says | We build | Why |
|---|---|---|
| Node 22 + Fastify + zod + PostgreSQL 16, as a pnpm monorepo | **Next.js route handlers for the API, Go for the signer, the indexer and demo-402, MongoDB 8.0** | Fifteen endpoints are ordinary request/response and belong with the dashboard that consumes them. Three components cannot be route handlers at all — the signer needs in-process caches to meet 300 ms, the indexer is four loops on a 30-second cadence, and the onboarding stream needs a resumable change stream. §B-1 |
| `agents.network` is made immutable by a trigger | The network is a **component of the document `_id`**, which the storage engine refuses to update, with a validator pinning the field to the prefix | Stronger than the trigger, and visible to the naked eye in a URL, a log line and a support ticket — §B-2 |
| The budget race is `SELECT … FOR UPDATE` over a live `SUM()` | A conditional `findOneAndUpdate` whose predicate is in the **filter**, deciding velocity and budget in one atomic act | MongoDB has no row lock. This is the better primitive for the problem, but it forces the in-flight total to be **stored**, and a stored derived number can drift — §B-3 |
| Cap **and expiry** are enforced on-chain | Cap and revoke are on-chain. **Expiry is a signer-tier rule** until the Subscriptions & Allowances primitive is verified | SPL delegate has no expiry. Invariant I3 forbids us from claiming otherwise, so the tier is stored as data and the interface reads it — §B-4 |

## Gaps in the deck this specification fills

| Gap | Where it is filled |
|---|---|
| SIWS is specified with no nonce store, which makes a signed message replayable | [05-api-contract.md](05-api-contract.md) §SIWS, and the `siws_nonces` collection |
| SPL `approve` permits exactly **one** delegate per token account, so one owner cannot have two agents on one account | [08-onchain-adapter.md](08-onchain-adapter.md) §The per-agent token account |
| "FIFO by signing time" is not a total order at millisecond resolution | [04-policy-engine.md](04-policy-engine.md) §What FIFO means operationally |
| The prototype says "6 of 6 checks", the deck's mock of the same screen says "7/7", and there are eight rules | [10-ui-spec.md](10-ui-spec.md) §The check count, and §B-5 of the conformance record |
| Phase 4 of the handshake, `replayed`, happens inside the customer's agent and is unobservable | [07-indexer-and-jobs.md](07-indexer-and-jobs.md) §The phase we cannot see |
| Nothing says what happens when the signer's cache and the chain disagree about a revoke | [06-signer.md](06-signer.md) §Eviction, and why a TTL is not enough |

## The rules this repository follows

Seven of them. Each is enforced by something that fails a build, not by a reviewer remembering.

- **Leash never holds a key that can move an owner's money.** Nothing under `frontend/website` may
  import a KMS client, and `cmd/leash-indexer` may not reach `internal/kms` — only the signer
  may. CI proves both. The gates have self-tests, and the self-tests run first.
- **The chain is the source of truth.** `confirmed` is written only after a read-back by
  signature, only by the indexer, and only with the slot it was read at. The database refuses a
  `confirmed` row that lacks one.
- **`unknown` is not `failed`.** A payment whose outcome we have not seen is a first-class state
  with its own screen, its own copy and its own retry cadence. Re-signing it is forbidden;
  re-reading it is the only exit.
- **Money is never a float.** `int64` micro-USDC in Go, `long` in MongoDB, `u64` in the protocol,
  and a **string** on the wire — JSON numbers are IEEE doubles. The money type refuses to
  unmarshal a double rather than coercing it.
- **Sandbox and mainnet never meet.** The network is part of every identifier, checked by a
  validator on every write, and every store method takes it as an argument. A query that could
  span both does not compile.
- **The database is the enforcement point, not either runtime.** Two languages reach MongoDB, so
  the validators, the partial unique indexes and the role privileges are what actually hold the
  line — and `verify-constraints` proves them against the server, for both sides at once.
- **Correctness comes from the database**, so a second instance is always safe. Sixteen writes
  the schema must refuse are listed in [13-testing.md](13-testing.md), and CI performs all
  sixteen against a real server.
- **Secrets are never defaulted and never committed.** A missing one is a boot failure naming the
  variable. A test reads this repository's own configuration source to make sure nobody softens
  it into a default.
