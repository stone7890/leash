# 12 · Security

Leash is a safety product. That has a specific consequence: **a security failure here is not a
degradation of the product, it is a refutation of it.** A customer who cannot trust the cap has
no reason to use anything else we built.

## The position

Two sentences, and everything else follows from them.

> Leash holds no funds and no owner keys. It holds one Ed25519 key per agent, and the worst that
> key can do is spend the remaining balance of one allowance.

The first sentence is invariant I1 and is the legal position as much as the technical one:
holding customer funds is money transmission. The second is what makes the design defensible —
the blast radius is bounded, small, and knowable in advance.

## What we hold, and what we do not

| | Held by Leash? | Where | If leaked |
|---|---|---|---|
| Owner private key | **never** | The customer's wallet | Not our failure mode. There is no code path that receives one |
| Owner seed phrase | **never** | — | We never ask. The wallet modal says so |
| Agent private key | **yes** | Inside the signer, wrapped by KMS | Up to the remaining balance of that one allowance |
| Agent API key | hashed | The database stores a hash | The holder can attempt payments — which the eight rules still gate |
| Session token | signed, not stored | The browser's `sessionStorage` | Dashboard access for up to 8 hours |
| Customer funds | **never** | The customer's own accounts | — |

## Agent keys

**Envelope encryption.** A KMS customer master key wraps a per-agent data key; the data key
encrypts the Ed25519 seed with AES-256-GCM; the ciphertext lives in `agent_keys`. KMS never sees
the seed and the database never sees a usable key. Compromising either alone yields nothing.

**Looked up by `(agent, network)`, always.** There is no function that takes only an agent
identifier, so there is no code path that can produce a mainnet key for a sandbox challenge (I8).

**Resident in memory for up to ten minutes.** The unwrapped key is cached, because paying a KMS
round trip on every signature would make AWS an availability dependency of every payment
([06-signer.md](06-signer.md)). This is a deliberate trade, stated rather than hidden. It does not
change the damage ceiling, and it is why the signer is a separate process with one endpoint, no
template rendering, no static file serving and no third-party HTTP client.

**Zeroed on eviction and on shutdown.**

**The absolute rule: an agent key and an owner key are never in the same process.** Since one
signer serves both networks, the corollary is that the key reference is always `(agent, network)`
— never `agent` alone.

## Authentication

**Owners — SIWS.** The wallet is the account. No password, no email, nothing to phish and nothing
to leak in a breach. The signature is verified before the payload is parsed; the comparison is
constant-time; the nonce is read-and-deleted in one operation so a replay finds nothing.

> The deck specifies SIWS with no nonce store, which leaves a signed message replayable. The
> `siws_nonces` collection closes it — a genuine gap in the specification, registered in
> [16-deck-conformance.md](16-deck-conformance.md).

**Agents — the API key.** Prefixed by network, shown once, stored as a hash, accepted by the
signer alone. Rotation kills the old key **immediately** with no grace period: a rotation is
usually a response to a suspected leak, and a grace window keeps the leaked key working through
exactly the minutes that matter.

**Authentication is never middleware.** Every handler's first statement is an explicit
`auth.RequireOwner` or `auth.RequireAgentKey`, asserted on the AST by `verify-architecture`. A
middleware you forgot to attach fails open. A first line you forgot to write fails the build.

## The append-only audit trail

Four collections may be inserted into and never modified: `sign_requests`, `handshake_events`,
`policy_revisions`, and — for deletion — `payments`.

Enforced by a database role that grants `find` and `insert` and **not** `update` or `remove`.
MongoDB roles are additive; there is no `REVOKE`, so the absence of a grant *is* the revocation
([03-data-model.md](03-data-model.md)).

**The whole guarantee rests on one negative: `bypassDocumentValidation` is not granted.** With it,
every schema validator in the system evaporates at once, silently. A single well-meaning
`grantRolesToUser(…, "readWrite")` would do it, with no error anywhere.

So it is checked twice: `leashctl verify-privileges` in CI, and a boot-time assertion in
production. And **confirm the hosting tier permits custom database roles at all** — some managed
shared tiers do not, and on those, append-only degrades to application-enforced only. That is a
materially weaker claim to make to a customer, and if it is where we end up it must be said
plainly rather than quietly.

## The threat model

### An agent key leaks

**The one the product is designed for.** Ceiling: the remaining balance of that one allowance.
Not the cap, not the wallet, not the other agents.

Runbook: kill switch (S7 blocks instantly) → on-chain revoke (hard, survives us) → rotate the key.
The first step takes effect in tens of milliseconds via the change stream; the second takes a
block.

### The signer is compromised

The worst case. Every agent key is exposed, bounded by the sum of the remaining balances.

What still holds: **the on-chain cap**, which Solana enforces regardless, and **the owner's
ability to revoke**, which does not pass through us at all (I5). What does not hold: every
soft-tier rule, including expiry under the SPL implementation
([08-onchain-adapter.md](08-onchain-adapter.md)).

Mitigations: one endpoint, no outbound HTTP in the signing path, no owner key present, keys
resident for at most ten minutes, and a separate process so a compromise of the API is not a
compromise of the keys.

### The API is compromised

**No keys are present.** Nothing under `frontend/website` may import a KMS client, and CI proves it
with a self-tested gate — the deployable that serves the dashboard and the API has no key
material and no means of decrypting any.

An attacker could return a malicious unsigned transaction and hope the owner signs it — which is
why the wallet shows what it is signing and why the review card states the cap, the delegate, the
fee and the rent before the prompt opens.

### The database is compromised

Agent keys are ciphertext without the KMS key. The audit trail is append-only at the privilege
level, so tampering requires the compromise to include role administration.

### A malicious agent

The agent is the customer's own software, and it is untrusted by design. It never holds a key,
never submits its own transaction, and cannot bypass the signer — those are forbidden lane edges
in [01-architecture.md](01-architecture.md). Everything it can do is gated by the eight rules,
and the cap binds absolutely.

### A malicious endpoint

An x402 endpoint could over-charge within the per-transaction maximum, or issue challenges
repeatedly. S3 and S4 bound the damage per payment and per ten minutes; S2 means it must be on the
allow-list at all; and the alert on a never-seen endpoint is how the owner finds out.

### A network-crossing bug

The one that would be worst, and the one with the most machinery against it: the network is part
of every identifier, pinned by a validator, taken as a parameter by every store method, bound
into every job at spawn time, and there is deliberately no global RPC endpoint. See I8.

## The incident runbook

Written before the fire, not during it.

| Incident | Symptom | Do |
|---|---|---|
| **Primary RPC dies** | Read-back lags; the degraded banner appears | Automatic failover to the fallback. `unknown` is a first-class state, so **no data becomes wrong — only late** |
| **The signer dies** | Agents get network errors from `/v1/sign` | **Nothing is signed, so no money is lost. Blocking is safe.** Restart it. The on-chain cap is still enforced. Do not add a bypass |
| **Leash is entirely down** | — | The public revoke guide (I5). Point customers to it. This is why the Settings screen carries that section |
| **A suspected agent-key leak** | An unfamiliar payment in the feed | Kill switch → on-chain revoke → rotate. Ceiling is that allowance's remainder |
| **`reserved_base` drift** | A warning from `allowance-refresh` with a delta | The refresh repairs it. **Investigate the cause** — drift is a bug, not weather |
| **The change stream drops** | An error log; kill switches take up to 60 s | The TTL is the floor, so the kill switch still works, more slowly. Restart the signer |
| **A privilege assertion fails at boot** | The process refuses to start | Somebody granted a broader role. Find out who, and why, before restoring it |

The second row is the one to internalise: **the failure direction is safe.** A signer that is down
does not leak money; it stops payments.

## Secrets

- **Never defaulted.** A default secret is a secret in production. A test reads the configuration
  source and fails if a required secret is ever softened into an optional one with a fallback.
- **Never committed.** A CI job greps for anything shaped like a Solana secret key and refuses a
  committed `.env`.
- **Half-configured is a boot failure**, naming every missing variable at once.
- Production secrets come from the platform's secret store, never from an image.

## Logging

- **An agent private key, a seed, or an API key is never logged**, at any level, including debug.
  The API key is logged only as its hash prefix.
- **`network` is a mandatory attribute on every money-path line.** A log that does not say which
  network it concerns is unusable during an I8 incident.
- **An amount is logged through its string form**, never as an integer or a float. A log is the
  last place a float sneaks back in, and a CI check enforces it.
- Every request carries a `trace_id`, echoed in the error envelope and in
  `handshake_events.detail`, so a support ticket, a log line and a chain event join on one value.

## What we deliberately do not do

- **No KYC, no KYB.** Being non-custodial is what makes it unnecessary. That is I1 doing legal
  work as well as technical work.
- **No fund custody, ever.** Not in escrow, not "temporarily", not for a feature.
- **No bypass mode.** There is no configuration in which the signer allows a payment it could not
  check. If it cannot check, it refuses.
- **No emergency signing key**, no break-glass credential that can move an owner's money. It
  would be indistinguishable from custody, and it would be the first thing an attacker looked for.

## The evidence we present

Four claims, three of them negative, each demonstrable on demand. Details in
[13-testing.md](13-testing.md).

| Claim | Proof |
|---|---|
| Hard enforcement is real | An over-cap draw is rejected **by the chain**, visible in an explorer |
| We never pay twice | The same challenge twice yields exactly one signature |
| We are non-custodial | No code path receives an owner key; every treasury transaction has the owner's wallet as its signer |
| The networks are isolated | An `lk_test_` key plus a mainnet challenge returns `403 NETWORK_MISMATCH`, with zero transactions |
