# 07 · The indexer and the background jobs

The indexer is what makes invariant I2 and invariant I4 true. It is the only component that polls
outward, the only writer of `confirmed`, and the only thing that ever changes an allowance's
cached numbers.

**It is its own Go process, `leash-indexer`, and it is long-running by necessity.** Four goroutine
loops, spawned before the port binds and shut down after the server stops. No queue, no cron, no
Redis: one fewer moving part, and a job that cannot outlive its process cannot wake up after a
deploy and act on a world that has moved.

It is a separate deployable from the API for two reasons that are not negotiable. `alert-eval`
runs every **30 seconds**, and the interface promises alerts within thirty seconds of the event
they describe — a scheduler with a one-minute floor cannot keep that promise. And `payment-sweep`
holds cursors and RPC clients across iterations, which an invocation-scoped runtime does not have.

It binds a port for one thing only: **the onboarding SSE stream**, `GET
/v1/test-payments/{id}/stream`, which needs a resumable change stream held open for the length of
a wizard session. It is proxied through Next.js so the browser sees a single origin
([01-architecture.md](01-architecture.md)). The same process runs the test-payment loop that
produces the events it streams.

## The four loops

Each is spawned **once per configured network**.

| Job | Every | Does |
|---|---|---|
| `allowance-refresh` | 60 s | Re-reads active allowance accounts; updates `drawn_onchain_base` and `last_read_slot`; catches expiries and revocations that happened out of band; recomputes `reserved_base` and reports drift |
| `payment-sweep` | 5 min | `signed \| submitted \| unknown` older than 90 s → check by signature. Older than 24 h with no trace → `failed` plus an alert. Writes handshake phases 5–6 |
| `alert-eval` | 30 s | Burn rate at 80% and 95%, unusual blocks minus suppressions, a never-seen endpoint, an allowance expiring within 24 h |
| `sandbox-faucet-guard` | 1 h | Rate-limits the faucet per wallet. Sandbox only — spawning it for mainnet is a configuration error and the boot says so |

**Per network, not iterating networks internally.** A job that loops over networks inside its body
is one `if` away from reading a sandbox record with a mainnet RPC client. The network is bound at
spawn time, lives in the closure, and appears in every log line the job emits (I8).

## The supervisor

```go
type Job struct {
    Name    string
    Network network.Network
    Every   time.Duration
    Run     func(ctx context.Context, now time.Time) error
}
```

Four properties, each of which exists because its absence causes a specific failure:

- **The first tick is jittered.** Four jobs across two networks all firing at `t=0` on every
  instance of a rolling deploy is a self-inflicted thundering herd against the RPC.
- **Ticks do not stack.** A run that overruns its interval drops the next tick and counts it. Two
  overlapping payment sweeps would race on the same payments.
- **Every iteration has a deadline** of three times the interval, so a hung RPC cannot wedge a
  loop for ever.
- **A panic is recovered and logged with its stack**, and the loop continues. One malformed
  document must not stop reconciliation for every other record.

On `SIGTERM`, the context is cancelled and the supervisor waits up to 30 seconds, so an in-flight
read-back finishes and writes its result rather than being lost and re-done.

## Read-back, and why it is the only exit

**Invariant I4: no state becomes `confirmed` except by reading the chain back, by signature, and
recording the slot.**

Not by an HTTP 200 from the endpoint. Not by a facilitator's acknowledgement. Not by a timeout
elapsing without an error. Those are all evidence that a transaction was *submitted*, and
submitted is not confirmed.

The mechanism is a schema constraint rather than a convention: `handshake_events` refuses a
`confirmed` row whose `writer` is not `indexer`, or whose `read_back_slot` is missing. A code path
that tries to write `confirmed` from anywhere else gets `DocumentFailedValidation`, at the
server, in production.

`verify-architecture` additionally asserts that the sweep package never calls a signing function.
The forbidden repair for a stuck payment is to sign it again; the only repair is to read again.

## The payment state machine

```
                    ┌──────────────────────────────────────────┐
                    │                                          │
   signed ──────▶ submitted ──────▶ confirmed                  │
      │               │                                        │
      │               │                                        │
      └───────────────┴──────▶ unknown ──────────────┬─────────┘
                                  │                  │
                                  │  >24h, no trace  │  read-back finds it
                                  ▼                  ▼
                               failed            confirmed
```

- **`signed`** — the signer produced a signature. The money has not moved.
- **`submitted`** — we have seen the transaction on the network, unconfirmed.
- **`confirmed`** — read back by signature, at a named slot. Terminal.
- **`failed`** — 24 hours with no trace of the signature anywhere. Terminal, and it raises an
  alert, because a signature that never landed is worth a human look.
- **`unknown`** — we have not been able to determine the outcome. **Not a failure.** It holds its
  money in `reserved_base` and it is re-checked for as long as it takes.

The only transitions out of `signed`, `submitted` and `unknown` are read-backs. There is no
transition that re-signs, and none that infers.

### `unknown` is a first-class state, in the data and on the screen

The interface has a dedicated presentation for it, with fixed copy:

> This payment timed out before we saw a result. It is neither failed nor confirmed — we keep
> checking the chain, and it will never be signed twice.

Merging `unknown` into `failed` would be the shortest path to paying twice for one challenge, and
merging it into `confirmed` would be lying. It gets its own colour, its own drawer variant, and
its own line in the state table in [10-ui-spec.md](10-ui-spec.md).

## The phase we cannot see

The handshake has six phases, and the indexer writes two of them. The signer writes the first
three. Phase 4 is nobody's.

| Phase | Written by | When |
|---|---|---|
| 1 `challenge_received` | signer | The challenge arrives |
| 2 `rules_evaluated` | signer | S0–S7 have a verdict — the per-rule results go in `detail` |
| 3 `signed` | signer | A signature exists |
| 4 `replayed` | **often nobody** | The agent retries the call carrying the payment |
| 5 `broadcast` | indexer | The transaction is seen on the network |
| 6 `confirmed` | indexer | Read back, with a slot |

**Phase 4 happens inside the customer's agent runtime, which we do not run and cannot observe.**
It can sometimes be inferred when the endpoint responds in a way that reveals it, and when it
cannot be, the row is simply absent.

The interface renders an absent phase 4 **dimmed**, not as a failure and not as a gap to be
apologised for. It is a phase in someone else's process. Fabricating a timestamp for it would be
inventing evidence in an audit trail, which is the one thing an audit trail may not do.

For a **blocked** payment, the timeline stops at phase 2 and the remaining phases were never
reached — a different presentation again, and the drawer says *"5 other checks not reached"*
rather than showing them as failures.

## `allowance-refresh`, and the drift report

Every 60 seconds, per network, for every allowance in `pending` or `active`:

1. Read the on-chain account. Get `cap`, `drawn`, `state` and the slot.
2. Write `drawn_onchain_base`, `last_read_slot`, `last_read_at`.
3. If the allowance is `pending` and the account now reads, promote it to `active` — **this is
   the only way an allowance becomes active** (I4). Before that, the interface shows `pending`
   with a spinner and no available action.
4. Catch out-of-band changes: an expiry that has passed, a revocation the owner performed from
   their wallet or the CLI without telling us. Both are expected — I5 makes the second one a
   documented, supported path.
5. **Recompute `reserved_base`** from the payments still in flight, and compare it to the stored
   value. Any difference is logged at warning level **with the delta**.

Step 5 is the compensating control for the one thing MongoDB made worse. `reserved_base` is a
stored derived number that PostgreSQL would not have needed
([03-data-model.md](03-data-model.md)), and drift in it is either a payment refused that should
have gone through, or one admitted that should not. **Drift is a bug. It must be visible before
it is a loss.**

### The decrement rule

The way to get this wrong is to decrement `reserved_base` when a payment confirms, before
`drawn_onchain_base` has been re-read. For a moment the money would be in neither term, and the
signer would over-admit.

**So `reserved_base` is decremented only in the same atomic update that writes a fresh
`drawn_onchain_base` and its slot, from a chain read whose slot is at least the payment's
confirmation slot.** One document, one moment. Every window is therefore conservative — the money
is counted twice, never zero times — which under-reports the budget and never overspends it.

That is the only code path that decrements it, and it lives in one function.

## `payment-sweep`

Every five minutes, per network:

```
for each payment in {signed, submitted, unknown} older than 90 seconds:
    getSignatureStatuses(signature)
      confirmed  → state = confirmed, write phases 5 and 6 with the slot,
                   and in the SAME update: drawn += amount, reserved -= amount, last_read_slot
      seen       → state = submitted, write phase 5
      not found  → state = unknown, and re-check next cycle
      not found, and older than 24 hours
                 → state = failed, reserved -= amount, raise an alert
```

The 90-second threshold exists so a payment confirming normally is not swept while it is still
perfectly healthy. The 24-hour threshold is deliberately long: a signature that has not landed in
a day is genuinely gone, and shortening it trades a real risk of freeing budget for money that
later moves against a small gain in tidiness.

## `alert-eval`

Every 30 seconds. **Alerts fire within 30 seconds of the on-chain event they describe** — the
interface promises this, so the cadence is a contract.

| Alert | Condition | Default |
|---|---|---|
| Budget warning | Spend crosses 80% of cap | on, configurable to 50 or 95 |
| Budget critical | Spend crosses 95% of cap | on, configurable to 90 |
| Blocked payment | A `blocked` verdict, minus suppressions | on |
| Never-seen endpoint | An agent pays a host it has not paid before | on |
| Expiring soon | Under 24 hours to expiry | on |

**Suppressions silence the alert and never the block.** The *"This block is correct"* button
creates a suppression for one `(agent, host)` pair; the next attempt is still blocked, and the
interface says so explicitly, because a customer who believes they whitelisted the host will be
confused by the next refusal.

Delivery is Telegram and an optional webhook. Both are on the alerting path, which is emphatically
not the signing path — `signer/hot` may not import `alerting`, and the build enforces it.

## `sandbox-faucet-guard`

Hourly, sandbox only. Rate-limits the faucet per wallet.

The sandbox has no other limits by design: it is play money and the point is that a developer can
spend it freely. The faucet is limited only because it is the one part of the sandbox that costs
us something.

Spawning this job for mainnet is a configuration error, and the boot fails naming it, rather than
starting a loop that has nothing to do.

## Degraded mode

When read-back falls behind, the system does not hide it.

```
GET /v1/health/chain → { "sandbox": { "lag_seconds": 14, "degraded": true,
                                      "using": "fallback" } }
```

The dashboard raises the banner:

> On-chain data is lagging 14s behind — balances may be stale. Payments are unaffected.

Three things about that sentence are load-bearing. It states the lag as a number rather than a
vague apology. It says balances may be stale, which is honest about I2. And it says payments are
unaffected, which is true — the signer's admission does not depend on the indexer being current,
only on the cached snapshot and the atomic compare-and-swap.

**Hiding this banner for aesthetic reasons is listed as forbidden** in
[10-ui-spec.md](10-ui-spec.md).

### RPC failover

Each network has a primary and a fallback endpoint. On failure the indexer fails over
automatically and raises the degraded banner. `unknown` being a first-class state is what makes
this safe: **no data becomes wrong during an RPC outage, only late.**

There is deliberately no generic `RPC_URL`. The endpoint is chosen from the record's network
([02-invariants.md](02-invariants.md) I8), and a configuration test asserts a global one never
appears.

## What the indexer must never do

- **Infer `confirmed` from an HTTP response.** The schema refuses it.
- **Mutate an append-only collection.** The database role does not grant `update` on
  `sign_requests`, `handshake_events` or `policy_revisions`.
- **Sign anything, or repair a stuck payment by re-signing.** `verify-architecture` asserts it.
- **Run a query that is not filtered by network.** Every store method takes it as a parameter.
- **Write a phase twice.** `one_phase_per_payment` refuses it, which makes every job body safely
  re-runnable — the property that lets a tick be dropped or an iteration be retried without
  reasoning about it.
