# 04 · Policy engine

Eight rules. Any failure blocks, and nothing is signed.

The engine lives in `internal/domain/policy` and is **pure**: it takes a snapshot and a request
and `now`, and returns a verdict. It performs no I/O, opens no connection and reads no clock.
That is what makes the golden vectors in [13-testing.md](13-testing.md) possible, and the golden
vectors are the most valuable tests in the repository.

## The rules

| # | Checks | Tier (I3) | 403 code | Default |
|---|---|---|---|---|
| **S0** | The API-key prefix matches the agent's network, and the mint and network in the challenge match the agent's network | soft — signer | `NETWORK_MISMATCH` | on, cannot be disabled |
| **S1** | The allowance is active — not expired, not revoked. Cached at most 60 s, reconciled against the chain | hard — chain | `ALLOWANCE_INACTIVE` | on, cannot be disabled |
| **S2** | The host and `payTo` are on the allow-list. A wildcard is permitted but flags the agent with a warning | soft | `ENDPOINT_NOT_ALLOWED` | on |
| **S3** | `amount ≤ per_tx_max` | soft | `PER_TX_LIMIT` | on |
| **S4** | The velocity total over a ten-minute window | soft | `VELOCITY_LIMIT` | on |
| **S5** | `remaining ≥ amount` | hard, once the chain catches up | `BUDGET_EXHAUSTED` | on, cannot be disabled |
| **S6** | The mint is the correct network's USDC, and the scheme is `exact` | soft | `UNSUPPORTED_TERMS` | on, cannot be disabled |
| **S7** | The kill-switch flag is off | soft immediately, hard once the revoke lands | `KILLED` | on |

The codes are frozen. They are an interface: the agent SDK branches on them, and an operator
runbook maps them to actions. Renaming one is a breaking change, and
[11-errors-and-codes.md](11-errors-and-codes.md) is the register that makes it a deliberate act.

## Evaluation order is part of the contract

**S0, then S1, then S2 … S7. The first violation wins, and it is the one reported.**

This is not an implementation detail. An agent that receives `KILLED` behaves differently from
one that receives `ENDPOINT_NOT_ALLOWED` — the first should stop, the second may try a different
endpoint. If a killed agent with a bad host sometimes got one code and sometimes the other, the
SDK's behaviour would be nondeterministic. A golden vector asserts the order explicitly for the
case where two rules fail at once.

S0 is first because it is the network check, and a network mismatch means every subsequent
comparison would be made against the wrong world.

## The enforcement tiers, honestly

Invariant I3 requires the interface to say which tier enforced a rule. The table above is the
answer, and one row of it is a change from the deck.

**Expiry is a signer-tier rule.** The shipping on-chain implementation is the SPL
`approve`/`revoke` delegate, which enforces a cap and a revocation but has **no concept of
expiry**. The deck's chapter 13 anticipated this as its `[Q1]` fallback and said the interface
must relabel the tier; this specification does that, and stores the tier as data
(`allowances.expiry_tier`) rather than hardcoding it, because the adapter is expected to change.
See [08-onchain-adapter.md](08-onchain-adapter.md).

What survives Leash being unavailable, therefore:

```
survives    the cap          Solana refuses a draw beyond it
survives    the revoke       an owner-signed transaction, straight to the chain
does not    everything else  but an unreachable signer signs nothing, so the failure is safe
```

That last clause is the reason the soft tier is acceptable at all: **blocking is the safe
direction.** A signer that is down does not leak money; it stops payments. The incident runbook
in [12-security.md](12-security.md) says so in as many words.

## Budget arithmetic

```
remaining = cap_base − drawn_onchain_base − Σ amount_base( payments ∈ {signed, submitted, unknown} )
```

Three terms, and the third is the one that causes trouble.

- **`cap_base`** — the on-chain ceiling. Changed only by a top-up the owner signs.
- **`drawn_onchain_base`** — what has actually left the account, read back from the chain with a
  slot. Written only by the indexer.
- **The in-flight sum** — payments we have signed but not yet seen resolve. `unknown` is in this
  set, and that is the point of I4: a payment whose outcome we have not observed still holds its
  money, because assuming it failed and freeing the budget is how a challenge gets paid twice.

The in-flight sum is materialised as `allowances.reserved_base`. PostgreSQL would not have needed
that — a `SELECT … FOR UPDATE` can compute it live — and the fact that MongoDB does is registered
as a real deviation in [03-data-model.md](03-data-model.md) and
[16-deck-conformance.md](16-deck-conformance.md). Three defences keep it honest:

1. The schema validator refuses any document where `drawn + reserved > cap`. **Over-admission is
   impossible even if the admission filter is wrong.**
2. `allowance-refresh` recomputes the sum from `payments` every 60 seconds and logs any delta as
   a warning with the amount. Drift is a bug and must be visible before it is a loss.
3. `reserved_base` is decremented **only** in the same atomic update that writes a fresh
   `drawn_onchain_base` and its slot. So the money is never in neither term — every window
   counts it twice rather than zero times, which under-reports the budget and never overspends
   it.

## The admission decision — S4 and S5 together

S0, S1, S2, S3, S6 and S7 are decided against a cached snapshot, purely, in microseconds. **S4 and
S5 are decided by the database, in one atomic operation**, because both depend on state that
concurrent requests are competing to change.

MongoDB has no `SELECT … FOR UPDATE`. It does not need one: a `findOneAndUpdate` whose **filter**
contains the predicate is an atomic compare-and-swap on a single document, and WiredTiger retries
write conflicts server-side and transparently.

```
filter:   _id = <allowance>  AND  network = <network>  AND  state = "active"
          AND  cap − drawn − reserved  ≥  amount                        ← S5
          AND  amount + Σ(vel entries newer than now−window)  ≤  velocity_max   ← S4

update:   reserved += amount
          vel = (entries newer than now−window) ++ [{t: now, a: amount}]
```

Two requests racing for the last five cents do not both read *"five cents remaining"*. The
loser's filter simply does not match, and it gets nothing back.

Three things this buys, each of which would be lost by doing it any other way:

- **No transaction on the hot path.** A multi-document transaction aborts on write conflict and
  must be retried whole — and under the FIFO race, write conflict is not the exception, it is the
  case the design exists for. A transaction there converts the contended case into a retry storm
  and blows the p99 in exactly the scenario that matters.
- **The velocity window is correct with more than one signer process.** An in-memory ring buffer
  would be correct for a single instance and would silently become wrong the day you scale. The
  window lives on the allowance document precisely so it cannot.
- **The window is pruned in the same act it is appended to**, by an update pipeline, so entries
  older than ten minutes never accumulate.

### Naming the rule when the compare-and-swap refuses

The database returns "no match", which means S4 *or* S5, without saying which. In the ordinary
case the signer already knows: it evaluated both against its snapshot before attempting the
write. When the snapshot said *allowed* and the compare-and-swap said no, **that is the race** —
and the loser does one extra read of the fresh allowance and re-runs the pure engine over it to
name the rule precisely. One extra round trip, on the rare losing path, is the right place to
spend it.

## What FIFO means operationally

The deck resolves its open question Q3 as: *several requests racing for the remainder are served
FIFO by signing time; the overflow gets `403 BUDGET_EXHAUSTED`.*

BSON dates are **millisecond** resolution, where `timestamptz` is microsecond. So "by signing
time" is not a total order under contention, and a specification that promises one is promising
something the clock cannot deliver.

**The operational definition:** FIFO is the order in which requests reach the allowance
compare-and-swap on the primary. That order is reconstructible from the ULID `_id` of the
`sign_requests` documents, which are minted immediately after admission and are monotonic within
a millisecond.

This is a redefinition, not a restatement, and it is registered as such.

## The latency budget

The signing path's *added* latency. These are requirements, not aspirations.

| Hop | p50 | p99 | What it is here |
|---|---|---|---|
| Parse, canonicalise, idempotency | 5 ms | 15 ms | Hashing, plus one round trip to claim |
| Evaluate S0–S7 | 10 ms | 30 ms | Pure over the cache, plus one round trip for the compare-and-swap |
| KMS unwrap | 20 ms | 80 ms | Zero when the key cache is warm; an AWS call when cold |
| Build and sign | 15 ms | 40 ms | Cached blockhash plus Ed25519, roughly 50 µs, plus one round trip to record |
| **Total added** | **< 50 ms** | **< 300 ms** | Three round trips at `w:majority`, 6–15 ms of database |

**Forbidden in the signing path:**

- **Any outbound HTTP call.** Not to an RPC, not to a facilitator, not to an alerting service.
- **Writing handshake phases 1–3 synchronously.** They are enqueued *after* the verdict is
  returned, fire-and-forget.

Both are pull-request rejections, and both are machine-checked — the import rules in
[01-architecture.md](01-architecture.md), plus a test that swaps in a panicking HTTP transport and
runs a thousand signatures.

The deployment consequence: **the signer must run in the same region as the database primary.**
Three round trips at `w:majority` across regions eat the p50 budget on their own.
[14-deployment.md](14-deployment.md) says so in the runbook.

## Rules that cannot be turned off

S0, S1, S5 and S6 have no toggle, anywhere in the interface or the API. They are, respectively:
you cannot cross networks; you cannot spend from a dead allowance; you cannot exceed the budget;
and you cannot pay in the wrong token or under an unsupported scheme. A product that let a
customer disable those would not be a spend-control product.

S2, S3, S4 and S7 are configurable. S2 accepts a wildcard — and an agent with `allow_all` carries
a visible warning in the interface, permanently, which is the deck's chosen trade between
flexibility and honesty.

## The snapshot

What the engine is given. Everything it needs, and nothing that would let it do I/O.

```
Snapshot
  agent      id · network · pubkey · killed
  policy     allow_hosts[] · allow_all · per_tx_max_base · velocity_max_base · velocity_window_s
  allowance  id · state · cap_base · drawn_onchain_base · reserved_base
             expiry_ts · expiry_tier · last_read_slot · mint · token_program_id
  key        the API key's prefix and network

Request
  host · pay_to · amount_base · mint · scheme · network · nonce · challenge_version

now         passed in by the caller. The engine never reads a clock.
```

The snapshot is cached in the signer for **60 seconds**, because S1's specification says "cached
at most 60 s". Anything longer would make the number indefensible. Kill, revoke and key-rotation
do not wait for that TTL — they are evicted within milliseconds by a change stream, for the
reason given in [06-signer.md](06-signer.md): a kill switch whose whole promise is *"the signer
refuses immediately"* cannot be a minute late.

## The verdict, and what is recorded

Every call to `/v1/sign` produces exactly one verdict, and it is written to `sign_requests` —
append-only — whether it allowed or blocked.

```
verdict       allowed | blocked
failed_rule   "S2", when blocked. Absent when allowed
checks        one entry per rule: its identifier, its tier, and its result
requirements  the canonicalised challenge, plus its raw text
```

**`checks` is the authoritative carrier of what was evaluated**, and the interface renders its
count rather than a literal. This resolves a contradiction in the source material: the prototype's
step-5 log and payment drawer say *"6 of 6 checks"*, the deck's mock of the same screen says
*"7/7"*, and the rule set S0–S7 is eight. Rendering from the array means it cannot drift again.
See [10-ui-spec.md](10-ui-spec.md).

`checks` also carries the **tier** of each rule, which is how the timeline can show, per payment,
which of the eight were chain-enforced at the moment it ran — rather than assuming, from a
constant, something the adapter may have changed.

## Golden vectors

Because the engine is pure and takes `now` as a parameter, a test case is a JSON file. Eight
files, one per rule, **at least three cases each — pass, fail, and boundary** — plus a meta-test
that fails when a rule is under-covered, since a vector nobody wrote is a coverage gap nobody can
see.

The boundaries are the point. Full details in [13-testing.md](13-testing.md), but the ones that
matter:

| Rule | The boundary that must be pinned |
|---|---|
| S0 | Correct key prefix, but the challenge's **mint** belongs to the other network — the case that catches a half-implemented check |
| S1 | `expiry_ts` exactly equal to `now` is **expired** — `>=`, not `>` |
| S2 | A host that is on the list but a `payTo` that differs — the case where checking only the host is wrong. And a subdomain of an allowed host is **not** allowed |
| S3 | Exactly `per_tx_max` is allowed; one micro-USDC more is not |
| S4 | An entry exactly 600 seconds old is **outside** the window |
| S5 | Exactly the remaining budget is allowed; one micro-USDC more is not |
| S6 | `scheme: "upto"` is refused; a missing recipient token account is refused with a `details` explaining it |
| S7 | Killed *and* another rule failing — asserts **which** code is returned |

The S4 boundary is tested twice: once against the pure engine in Go, and once against the real
server through the compare-and-swap's `$filter`. The two can disagree, and if they ever do, the
server is right and the engine is the bug.
