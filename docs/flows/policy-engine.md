# The policy engine — S0–S7, and the signing path

Eight rules. **Any failure blocks.** Evaluation order is part of the contract, because the SDK
branches on the returned code — a killed agent whose challenge also has a network mismatch must
always report the same one. Prose: [04-policy-engine.md](../04-policy-engine.md).

```mermaid
flowchart TD
    R["POST /v1/sign<br/>challenge + agent API key"] --> S0

    S0{"S0 · Do the key prefix, the agent's network<br/>and the challenge's network all agree?"}
    S0 -->|no| E0["403 NETWORK_MISMATCH · signer tier"]
    S0 -->|yes| S1

    S1{"S1 · Is the allowance active —<br/>not expired, not revoked?"}
    S1 -->|no| E1["403 ALLOWANCE_INACTIVE · ON-CHAIN tier"]
    S1 -->|yes| S2

    S2{"S2 · Are host and payTo on the allow list?"}
    S2 -->|no| E2["403 ENDPOINT_NOT_ALLOWED · signer tier"]
    S2 -->|yes| S3

    S3{"S3 · amount ≤ per_tx_max?"}
    S3 -->|no| E3["403 PER_TX_LIMIT · signer tier"]
    S3 -->|yes| S4

    S4{"S4 · Does it fit the ten-minute<br/>velocity window?"}
    S4 -->|no| E4["403 VELOCITY_LIMIT · signer tier · RETRIABLE"]
    S4 -->|yes| S5

    S5{"S5 · remaining ≥ amount?"}
    S5 -->|no| E5["403 BUDGET_EXHAUSTED · ON-CHAIN tier"]
    S5 -->|yes| S6

    S6{"S6 · Is the mint the right network's USDC,<br/>and the scheme exact?"}
    S6 -->|no| E6["403 UNSUPPORTED_TERMS · signer tier"]
    S6 -->|yes| S7

    S7{"S7 · Is the kill switch off?"}
    S7 -->|no| E7["403 KILLED · signer now, on-chain after revoke"]
    S7 -->|yes| SIGN["Sign the draw · 200"]
```

**The order of the constants IS the evaluation order, and the evaluation order is part of the
contract** ([rule.go](../../backend/internal/domain/policy/rule.go)). On the first failure,
everything after it is reported as `NotReached` — not as a pass, which would imply it was
evaluated, and not as a failure, which would be false. That is what the drawer's *"5 other checks
not reached"* is rendered from.

**S4 and S5 are decided twice, and the second time is the one that counts.** `policy.Evaluate` is
pure and reads them off a cached snapshot; the signer then re-decides both inside a single atomic
compare-and-swap at the store, where a concurrent request cannot slip between the read and the
write. The pure pass keeps the reported order honest; the atomic pass keeps the money honest.

## The eight rules

| # | Checks | Tier (I3) | Code | Can be turned off? |
|---|---|---|---|---|
| **S0** | Key prefix matches the agent's network; the challenge's network matches too | signer | `NETWORK_MISMATCH` | **no** |
| **S1** | Allowance active, not expired, not revoked | **on-chain** | `ALLOWANCE_INACTIVE` | **no** |
| **S2** | Host and `payTo` on the allow list — a wildcard is permitted, and flags the agent | signer | `ENDPOINT_NOT_ALLOWED` | yes |
| **S3** | `amount ≤ per_tx_max` | signer | `PER_TX_LIMIT` | yes |
| **S4** | Velocity, in a ten-minute window | signer | `VELOCITY_LIMIT` | yes |
| **S5** | `remaining ≥ amount` | **on-chain once the chain catches up** | `BUDGET_EXHAUSTED` | **no** |
| **S6** | Mint is the right network's USDC; scheme is `exact` | signer | `UNSUPPORTED_TERMS` | **no** |
| **S7** | Kill switch is off | signer instantly, **on-chain** after the revoke lands | `KILLED` | yes |

**S0 is evaluated first and cannot be disabled.** A network mismatch means every later comparison
would be made against the wrong world, so nothing after it is meaningful. The golden vector for
this is explicit: a killed agent *and* a network mismatch must report `NETWORK_MISMATCH`
([contracts/policy-vectors/S0.json](../../contracts/policy-vectors/S0.json)).

## The budget arithmetic

```
remaining = cap_base − drawn_onchain_base − Σ amount_base(payments ∈ {signed, submitted, unknown})
```

Integer micro-USDC throughout — no float at any tier (I6). The in-flight sum is **materialised**
as `allowances.reserved_base` so that S4 and S5 can be decided inside a single conditional
`findOneAndUpdate` whose predicate lives in the **filter**, rather than in a read-then-write.

**When several requests race for the remainder: FIFO by signing time.** The overflow gets
`403 BUDGET_EXHAUSTED`.

A materialised total can drift where a live `SUM()` cannot. Three defences, none of which removes
the risk:

```mermaid
flowchart LR
    D1["Validator: drawn + reserved ≤ cap<br/>refuses an over-admitting document outright"]
    D2["allowance-refresh recomputes from payments<br/>every 60 s and logs any delta WITH THE AMOUNT"]
    D3["The decrement happens only in the same atomic update<br/>that writes a fresh drawn_onchain_base and its slot —<br/>so every window is CONSERVATIVE:<br/>counted twice, never zero times"]
```

## The signing path, in order

Three synchronous round trips, 6–15 ms of database at `w:majority`.

```mermaid
flowchart TD
    A1["1 · auth.RequireAgentKey — the handler's FIRST statement"] --> A2
    A2["2 · challenge.Canonicalise + Hash — pure, versioned"] --> A3
    A3{"3 · store.ClaimChallenge(hash) — round trip 1"}
    A3 -->|"duplicate · done"| R1["Return the stored answer VERBATIM (I7)"]
    A3 -->|"duplicate · in flight"| R2["409 SIGN_IN_PROGRESS — retriable"]
    A3 -->|new| A4["4 · cache.Snapshot — zero round trips when warm"]
    A4 --> A5["5 · policy.Evaluate — PURE. S0, S1, S2, S3, S6, S7 decided here"]
    A5 --> A6["6 · store.AdmitDraw — round trip 2 — the compare-and-swap: S4 and S5"]
    A6 --> A7["7 · cache.PrivateKey — zero when warm, a KMS Decrypt when cold"]
    A7 --> A8["8 · build with the cached blockhash; ed25519.Sign"]
    A8 --> A9["9 · store.RecordVerdict — round trip 3 — one cluster bulkWrite"]
    A9 --> A10["10 · return 200, or 403 with the rule"]
    A10 --> A11["11 · go func — handshake phases 1–3, AFTER the verdict"]
```

**Step 11 comes after step 10, and that ordering is a merge gate.** Writing the timeline before
returning the verdict would put an audit write on the critical path of a payment. The queue is
**bounded** — an unbounded one turns a database stall into an out-of-memory kill. On a full queue
the event is dropped and a counter increments: a lost timeline row is an audit gap to alarm on,
not a reason to fail a payment that has already been signed and returned.

## The latency budget

| Hop | p50 | p99 |
|---|---|---|
| Parse, canonicalise, idempotency | 5 ms | 15 ms |
| `evalPolicy` S0–S7 | 10 ms | 30 ms |
| KMS unwrap | 20 ms | 80 ms |
| Build and sign, blockhash cached 30 s per network | 15 ms | 40 ms |
| **Total added** | **< 50 ms** | **< 300 ms** |

**Forbidden, and a PR is rejected for either:** an outbound HTTP call inside the signing path, or
writing handshake phases 1–3 before the verdict is returned.

## Why the rules are a pure package

`policy.Evaluate` takes a snapshot, a request and a clock, and returns a verdict. No I/O. That is
what makes the golden vectors possible — [contracts/policy-vectors/](../../contracts/policy-vectors/)
is executed by the Go engine **and** by the TypeScript renderer of blocked verdicts, against the
same JSON files — and it is what will let the V2 self-hosted sidecar reuse the engine unchanged.
