# P5 · Kill and revoke — N-50 … N-55

Two tiers, in order, **and the interface says so before the owner confirms**. Prose:
[09-flows.md](../09-flows.md#p5--kill-and-revoke--n-50--n-55).

```mermaid
sequenceDiagram
    actor O as Owner
    participant D as Dashboard
    participant A as Leash API
    participant S as Signer
    participant C as Solana
    participant AG as Agent runtime

    O->>D: N-50 Kill switch
    D-->>O: Confirmation modal — "Two things happen, in order."

    rect rgba(255,140,140,0.10)
    note over S: SOFT TIER — instant
    O->>A: Confirm
    A->>S: N-51 killed = true
    note right of S: S7 refuses every NEW /v1/sign immediately.<br/>This happens the moment the intent is<br/>requested — BEFORE the tx is even built —<br/>so the soft tier engages even if the owner<br/>then abandons the wallet prompt.
    AG->>S: POST /v1/sign
    S-->>AG: 403 KILLED
    end

    rect rgba(140,255,180,0.10)
    note over C: HARD TIER — on-chain
    A->>A: N-52 Build an unsigned revoke tx
    note right of A: The docs also print how to sign this<br/>BY HAND with a wallet or CLI — I5.
    D->>O: Open the wallet
    O->>C: N-53 Sign the revoke
    C->>C: N-54 The revoke lands
    note right of C: From this slot, even a LEAKED KEY<br/>can no longer draw.
    C-->>A: N-55 Read-back → state = revoked
    end
```

## The revoke window is the nature of the chain, not a bug

```mermaid
flowchart LR
    N51["N-51<br/>killed = true"] --> W["THE WINDOW<br/>a few seconds"] --> N54["N-54<br/>revoke lands on-chain"]
    W --> T1["Traffic THROUGH Leash:<br/>S7 closes it to ~0"]
    W --> T2["An agent calling a facilitator DIRECTLY<br/>with a payload signed earlier<br/>can still settle"]
```

**Saying `revoked` before the read-back is the trap the deck names for this flow, and it is on the
reject-merge-if list.** It would tell an owner that a compromised agent is contained at a moment
when it is not yet. The interface keeps saying `revoking…` until N-55, and the kill button is
relabelled `Revoke pending` and disabled.

The modal copy is fixed:

> **Instantly:** The signer refuses every new payment from this agent.
> **On-chain:** Your wallet signs a revoke — once confirmed, the allowance is gone even if Leash
> is offline.

That second sentence is invariant **I5** written as product copy. Revoke is a transaction the
owner signs directly against the chain; it does not need Leash to be running, and
[if-leash-is-down.md](../if-leash-is-down.md) publishes the manual procedure.

## What `BuildRevoke` actually emits

Under the SPL implementation, **two** instructions, not one:

```mermaid
flowchart LR
    BR["BuildRevoke"] --> I1["1 · revoke the delegate"]
    BR --> I2["2 · sweep the remaining balance<br/>back to the owner"]
```

The sweep exists because the SPL fallback moves the owner's funds into a per-agent token account
they still own. Revoking the delegation without sweeping would leave money stranded in an account
with no delegate. [08-onchain-adapter.md](../08-onchain-adapter.md).

## Blast radius

The bound this whole flow exists to guarantee: **a leaked agent key is worth the remaining balance
of that one allowance.** Not the cap — the remaining. That bound is the product, and it is why
the runbook for a suspected leak is calm: kill switch, then on-chain revoke, then rotate.
[12-security.md](../12-security.md).
