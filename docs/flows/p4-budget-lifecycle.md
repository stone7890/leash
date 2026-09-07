# P4 · The budget lifecycle — N-40 … N-45

New in deck v3. Top up a cap, or extend an expiry, **without a dead window**. Prose:
[09-flows.md](../09-flows.md#p4--the-budget-lifecycle--n-40--n-45).

```mermaid
sequenceDiagram
    actor O as Owner
    participant D as Dashboard
    participant A as Leash API
    participant C as Solana
    participant IX as Indexer
    participant S as Signer

    O->>D: N-40 Add budget / Extend expiry
    note right of D: The modal shows the new cap or expiry,<br/>the WALLET BALANCE and the NETWORK FEE.
    D->>A: N-41 POST /v1/agents/{id}/modify-intents
    A-->>D: Unsigned tx
    note over A,C: The OLD allowance stays in force<br/>until the new one confirms.
    D->>O: Open the wallet
    O->>C: N-42 Sign — Leash does not touch the treasury (I1)
    C->>C: N-43 The new cap or expiry lands
    note right of C: NO DEAD WINDOW — the old value applies<br/>right up to the replacing slot.
    IX->>C: N-44 Read-back
    C-->>IX: cap, drawn, expiry, at a slot
    IX-->>D: The bar and the number change — ONLY now (I2, I4)
    IX-->>S: N-45 allowance-refresh, within 60 s
    note right of S: The next signature uses the new cap.
```

## Two derived states, computed at query time and never stored

```mermaid
stateDiagram-v2
    [*] --> active
    active --> exhausted: cap fully drawn
    note right of exhausted
        DERIVED, not stored.
        The allowance is still LIVE until expiry.
        The UI prompts a top-up rather than
        showing a dead agent.
    end note
    active --> expiring_soon: under 24 h remaining
    note right of expiring_soon
        DERIVED. Amber in the Expires column,
        plus an alert if enabled.
    end note
    exhausted --> active: P4 top-up
    expiring_soon --> active: P4 extend
```

Storing them would mean a cached truth about money, which is exactly what I2 exists to forbid.

## The trap under the SPL implementation

The MVP does **not** write a new program. It wraps an existing one through an adapter — and the
adapter that actually ships is the SPL `approve`/`revoke` delegate fallback, not the
Subscriptions & Allowances primitive the deck assumed ([08-onchain-adapter.md](../08-onchain-adapter.md),
[adapter.go](../../backend/internal/chain/adapter.go)).

That changes what a top-up *is*:

> **A top-up REPLACES the delegation rather than adding to it**, so the new cap arrives with a
> fresh drawn counter. `allowances.epoch` carries this. Without it, the amount already spent would
> be counted against the new cap — and the owner who topped up $10 → $20 would find they had
> bought themselves nothing.

```mermaid
flowchart LR
    subgraph WRONG["Without epoch"]
        A1["cap $20"] --> B1["drawn still reads $9.80<br/>from the OLD delegation"] --> C1["remaining = $10.20 — wrong"]
    end
    subgraph RIGHT["With allowances.epoch"]
        A2["cap $20, epoch n+1"] --> B2["drawn resets — a NEW delegation"] --> C2["remaining = $20.00 — correct"]
    end
```

And the honest consequence of the same fallback, per I3: **expiry is not enforced on-chain.** It
is a signer-tier rule, and the interface must label the Expires field *signer*, not *on-chain*.
A controlled downgrade, declared before the sprint rather than argued about during it.
