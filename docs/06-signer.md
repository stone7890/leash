# 06 · The Policy Signer

One process, one endpoint, and every agent key in the system.

The signer is the smallest component and the one with the most rules attached to it, because it
is the only thing in Leash that can move money. Everything in this chapter exists to keep two
properties true at once: **it answers in under 300 milliseconds at p99**, and **it never signs
the same challenge twice**.

## What it is, and what it must never be

| Responsible for | Must never |
|---|---|
| Holding every agent key, envelope-encrypted with AWS KMS | Hold an owner key — that is I1, and it is a compile-graph fact |
| Evaluating S0–S7 before every signature | Make an outbound HTTP call inside the signing path |
| Signing transfers **inside** an allowance | Sign a challenge whose network differs from the key's (I8) |
| Writing handshake phases 1–3 | Write those phases *before* the verdict is returned |
| Being the only holder of keys, and the only signer | Be reachable from the dashboard |

The dashboard cannot reach the signer. That is not a firewall rule to be forgotten in a
migration — `internal/api` importing `internal/signer` fails the build.

## Key custody

**Envelope encryption.** A KMS customer master key wraps a per-agent data key; the data key
(AES-256-GCM) encrypts the agent's Ed25519 seed; the ciphertext lives in `agent_keys`. KMS never
sees the seed, and the database never sees a usable key.

**The key reference is always looked up by `(agent, network)`.** One signer process serves both
networks — that is a version 3 change — so this is the mechanism that makes I8 hold inside the
process. There is no function that takes only an agent identifier, which means there is no code
path that can fetch a mainnet key for a sandbox challenge.

**The blast radius of a leaked agent key is the remaining balance of that one allowance.** Not
the cap; the remaining. That bound is the product, and it is why the runbook for a suspected leak
is calm: kill switch, then on-chain revoke, then rotate. [12-security.md](12-security.md).

## The caches

The latency budget is unachievable with a round trip per lookup, so five things are cached in
process. Each has a TTL chosen for a reason, and each has an eviction path that does not wait for
the TTL.

| Cache | Holds | TTL | Why that number |
|---|---|---|---|
| **Snapshot** | agent, policy and allowance summary, keyed by the hash of the API key | **60 s** | S1's specification says "cached at most 60 s". Anything longer would make the number indefensible |
| **Unwrapped key** | the derived `ed25519.PrivateKey` | **10 min** | See below — this one is a deliberate trade |
| **Blockhash** | one per network | **30 s** | The deck's number. A blockhash lasts 60–90 s; refreshing at half that leaves margin |
| **Claim** | nothing — claims are always a round trip | — | Idempotency is enforced at storage, never in memory (I7) |
| **Velocity** | nothing — the window lives on the allowance document | — | An in-memory ring buffer would be correct for one instance and silently wrong the day you scale |

The snapshot is populated on a miss by **one** `$lookup` aggregation across agents, policies and
allowances — one round trip, not three — and refreshed proactively at 45 seconds for hot agents,
so a miss is rare rather than periodic.

### The unwrapped-key cache, stated plainly

`kms.Decrypt` is an outbound HTTPS call. The deck budgets 20 ms at p50 and 80 ms at p99 for it, so
it is in the path by design. But paying it on **every** signature makes KMS the p99 and makes AWS
an availability dependency of every payment a customer's agent makes.

So the derived private key is cached for ten minutes, evicted immediately on kill, revoke or
rotation, and zeroed on eviction and on shutdown.

**The trade, said out loud:** an agent's private key is resident in the signer's memory for up to
ten minutes. The deck already accepts that the signer holds keys — that is the whole design — and
this does not change the damage ceiling, which remains the remaining balance of one allowance. It
does mean a memory-disclosure bug in the signer is worse than it would otherwise be, which is why
the signer is a separate process with one endpoint and no template rendering, no file serving and
no third-party HTTP client.

### Eviction, and why a TTL is not enough

A 60-second TTL is far too slow for S7. The kill switch's entire promise, in the modal the owner
reads before confirming, is:

> **Instantly:** The signer refuses every new payment from this agent.

A minute is not instantly. So eviction does not wait for expiry: the signer watches a **change
stream** on `agents`, filtered to updates that touch `killed`, and drops the entry within tens of
milliseconds. The same stream handles a revoked allowance and a rotated key.

Change streams require a replica set — which [03-data-model.md](03-data-model.md) already requires
for other reasons — and the `changeStream` privilege, which the application role grants on
`agents` and `handshake_events` and nowhere else.

**If the change stream drops**, the TTL is the floor. That is a degraded state, not a silent one:
it logs at error level and raises the degraded banner. The kill switch still works; it is up to
sixty seconds slower, and an operator can see that it is.

## The signing path, in order

```
 1  auth.RequireAgentKey(c)                    explicit, and the first statement of the handler
 2  challenge.Canonicalise + Hash              pure, versioned
 3  store.ClaimChallenge(hash)                 ── round trip 1 ──
       ├─ duplicate, state = done       → return the stored answer verbatim            (I7)
       └─ duplicate, state = in_flight  → 409 SIGN_IN_PROGRESS, retriable
 4  cache.Snapshot(apiKeyHash)                 zero round trips when warm
 5  policy.Evaluate(snapshot, req, now)        pure. S0, S1, S2, S3, S6, S7 decided here
 6  store.AdmitDraw(...)                       ── round trip 2 ── the compare-and-swap: S4 and S5
 7  cache.PrivateKey(agentID)                  zero when warm; a KMS Decrypt when cold
 8  build the message with the cached blockhash; ed25519.Sign
 9  store.RecordVerdict(signRequest, payment)  ── round trip 3 ── one cluster bulkWrite
10  return 200, or 403 with the rule
11  go func(){ handshake phases 1–3; finish the claim }()   ← fire-and-forget, AFTER the verdict
```

Three synchronous round trips, 6–15 milliseconds of database at `w:majority`. Step 9 uses a
cluster-level `bulkWrite` spanning two collections — not atomic across them, which is not needed,
but one network hop instead of two.

**Step 11 is after step 10 for a reason that is a merge gate.** Writing the timeline before
returning the verdict would put an audit write on the critical path of a payment. The queue is
**bounded**: an unbounded one turns a database stall into an out-of-memory kill. On a full queue
the event is dropped and a counter increments — a lost timeline row is an audit gap to alarm on,
not a reason to fail a payment that has already been signed and returned.

Duplicate enqueues are harmless: `one_phase_per_payment` refuses them, and the batch insert is
unordered so the rest of it still lands.

## Why there is no transaction on the hot path

A multi-document transaction that hits a write conflict aborts with a transient error and must be
**retried whole**. Under the FIFO budget race, a write conflict on the allowance document is not
the exception — it is the case the design exists for. A transaction there converts the contended
case into a retry storm and blows the p99 in exactly the scenario that matters.

A **single-document** `findOneAndUpdate` has the opposite property: the storage engine retries
the conflict server-side and transparently, and single-document atomicity needs no transaction at
all. So the race is resolved on one document, and the rest of the path is ordered so that any
crash leaves a **conservative** state.

### The crash windows

| Crash between | State left behind | Repair | Money at risk |
|---|---|---|---|
| 3 and 6 | A claim in flight, nothing else | The claim sweeper releases claims older than 60 s that have no sign request | none |
| 6 and 9 | A leaked reservation | `allowance-refresh` recomputes `reserved_base` from the payments | **The budget under-reports. Fail-safe.** Never over-admitted |
| after signing, before 9 | A signature that exists nowhere | See below | **none** |
| after 9, before 11 | A claim in flight, a payment that exists | The sweeper sees the payment for that challenge and marks the claim done | none |

### The signed-but-not-recorded window, closed without a transaction

This is the window that looks like it needs a transaction, and does not.

Ed25519 is **deterministic** — RFC 8032 specifies the nonce as a hash of the key and the message,
so there is no randomness. The only non-deterministic input to a Solana transaction message is
the recent blockhash. So **the blockhash is pinned into the claim document at step 3**, and on
retry the canonical message is rebuilt from it.

Re-signing the same challenge after a crash therefore produces **byte-identical output**. There
is no second signature to double-spend. There is one signature, produced twice.

That is what makes I7 hold without a transaction, and it is worth understanding before anyone
proposes "simplifying" the claim document away.

## What the signer is forbidden from doing, and how each is caught

**No outbound HTTP in the signing path.** Three layers, in increasing order of what they catch:

1. `internal/signer/hot` is its own package, and it may not directly import `net/http`,
   `internal/chain/rpc`, or `internal/alerting`.
2. `hot.New` takes exactly `(Store, KeyCache, BlockhashCache, Clock)`. There is no field on the
   struct capable of an outbound call, and adding one means changing the constructor, which fails
   `verify-architecture`.
3. **The test that actually catches it.** Replace `http.DefaultTransport` with one that panics,
   warm the caches, run a thousand signatures, assert nothing panicked. That test fails the moment
   somebody adds a "quick" RPC call to check the blockhash, or an alert on a blocked verdict —
   neither of which would be caught by reading the diff.

The KMS call is the deliberate exception, and it sits *outside* `hot`'s direct imports because it
goes through the key cache's interface. It is in the budget, at 20 ms and 80 ms.

**No signing outside an allowance.** The adapter's `buildDraw` is the only method that produces a
signed transaction, and it takes an allowance address. There is no general-purpose signing
function.

**No network crossing.** S0 runs first, and the key lookup is by `(agent, network)`, so even a
bug in S0 cannot produce a key for the wrong network.

## Blockhash refreshing

A goroutine refreshes one blockhash per network every 30 seconds and stores it in an atomic
pointer. The hot path reads a pointer; it never calls an RPC.

**The signer's `chain/rpc` client exists for the refresher alone.** That is why the import ban is
on `hot` rather than on the whole `signer` package: the process talks to an RPC, and the signing
path does not.

The signer warms the blockhash **before binding its port**. A cold blockhash on the first
signature is a p99 spike on the first payment a new customer makes, which is the demo.

## Deployment consequences

- **Same region as the database primary.** Three round trips at `w:majority` across regions eat
  the p50 budget on their own.
- **A fixed region near the RPC**, as the deck says, for the refresher.
- The process holds key material, so it gets no debug endpoints, no profiling handler on a public
  port, and no verbose request logging of bodies.
- On `SIGTERM`: stop accepting, drain the handshake queue within the grace period, zero every
  cached key, exit.

## What happens when the signer is down

Nothing gets signed. Agents receive a network error from `/v1/sign`.

**That is the safe direction, and it is worth being explicit about it**: blocking loses no money.
The on-chain cap continues to be enforced by Solana regardless, and the owner's ability to revoke
does not pass through us at all (I5). The incident runbook in [12-security.md](12-security.md)
says: restart it, and do not be tempted to add a bypass.

There is no fallback signer, no degraded "allow if we cannot check" mode, and no queue of pending
signatures to catch up on. A payment that was not signed simply did not happen, and the agent
retries on its own cadence.
