# P1 · Onboard, delegate, and pay — N-01 … N-12

**The target the whole phase is built around:** a brand-new wallet to an agent paying a real API
within a $10 budget, in **under ten minutes** of setup.

Sandbox-first. The workspace starts on Surfnet with play-money USDC, and nothing real happens
until the owner switches. Prose: [09-flows.md](../09-flows.md#p1--onboard-delegate-and-pay--five-steps).

```mermaid
sequenceDiagram
    actor O as Owner
    participant D as Dashboard
    participant A as Leash API
    participant S as Signer
    participant C as Solana
    participant X as demo-402

    rect rgba(120,140,255,0.08)
    note over O,X: Step 1 — start from a template
    O->>D: N-01 Connect wallet (SIWS)
    note right of D: Workspace defaults to SANDBOX.<br/>Faucet grants 100 test USDC.<br/>The signature IS the account.
    O->>D: N-02 Pick Research / Scraper / Blank, name it
    note right of D: Template prefills the policy.<br/>The agent is a draft.
    D->>A: N-03 POST /v1/agents (Idempotency-Key required)
    note right of A: network is FIXED to the agent here.<br/>It is part of the _id — I8.
    A->>S: N-04 Provision keypair
    S-->>A: Ed25519 seed, KMS-wrapped
    A-->>D: API key, prefixed lk_test_ / lk_live_
    end

    rect rgba(120,200,140,0.08)
    note over O,X: Step 2 — budget and rules
    O->>D: N-05 Cap, expiry, allowed hosts, per-payment max, velocity
    note right of D: Every field carries its TIER BADGE (I3).<br/>Cap is on-chain. The rest is signer-tier.<br/>A wildcard is permitted — and flagged.
    end

    rect rgba(255,180,120,0.10)
    note over O,X: Step 3 — sign the delegation
    D->>A: N-06 POST /v1/agents/{id}/delegation-intents
    A-->>D: Unsigned tx, built on the agent's own network
    D->>O: N-07 Open the wallet
    alt Wallet rejects
        O-->>D: Refused
        note right of D: "The wallet rejected the transaction.<br/>Nothing was created and nothing moved."<br/>+ Try again. NOTHING is created.
    else Owner signs
        O->>C: N-08 Submit — cap + delegation land for the agent pubkey
        note over A,C: allowance = pending. Spinner, no action available.
        C-->>A: N-09 Read-back — account is readable, at a named slot
        note right of A: active ONLY after read-back (I4).<br/>The schema refuses an active allowance<br/>with no last_read_slot.
    end
    end

    rect rgba(200,140,255,0.10)
    note over O,X: Steps 4 and 5 — point the agent at Leash, and watch it pay
    A-->>D: N-10 API key shown ONCE + MCP / pay CLI / Node snippets
    O->>D: Run a test payment
    D->>A: POST /v1/test-payments
    A-->>D: 202 + job
    A->>X: N-11 Drive the real P2 loop against api.sandbox.leash.dev/demo
    note right of X: $0.01 tUSDC, always available.<br/>Challenge: scheme exact, network, payTo, amount, nonce.
    X-->>S: 402 Payment Required
    S->>S: S0–S7, then sign inside the allowance
    C-->>A: confirmed, at a slot
    A-->>D: N-12 SSE — GET /v1/test-payments/{id}/stream
    note right of D: Nine lines. One handshake_events row each,<br/>streamed FROM THE SERVER as it happens.<br/>Ends with a receipt.
    end
```

## The two steps that are most often got wrong

**N-12 — the temptation is to fake the log in the frontend.** The prototype does exactly that,
and it is the one thing that must not survive the port. A scripted animation is a demonstration
that the product works, shown to a user for whom it might not. The nine lines must be real, one
event per `handshake_events` row, streamed over SSE as the loop actually runs on Surfnet.

**N-09 — the temptation is to call it `active` on submit.** `submitted` is not `confirmed`. The
only transition into `active` is a read-back, and the copy says so:

> Waiting for on-chain confirmation… Submitted is not confirmed — this flips the moment the
> allowance is readable on Solana.

**N-07 exists because the deck insists the rejection path is built, not just the happy one.** An
interface that only handles success teaches its user that a refusal is a bug.

## One wizard, two networks, no bleed

The mechanism that makes I8 hold across this whole phase is that `network` is chosen once, at
N-03, and is then a property of the identifier itself:

```mermaid
flowchart LR
    W["workspace.activeNetwork<br/>sandbox by default"] --> N["N-03 POST /v1/agents"]
    N --> ID["_id = agt_test_… / agt_live_…<br/>MongoDB refuses to update an _id"]
    ID --> K["API key prefix<br/>lk_test_ / lk_live_"]
    ID --> R["RPC endpoint chosen<br/>per record, never per global config"]
    K --> S0["Signer rule S0 — evaluated FIRST,<br/>cannot be disabled"]
    R --> S0
    S0 -->|mismatch| E["403 NETWORK_MISMATCH"]
```

The key reference inside the signer is always looked up by **`(agent, network)`**. There is no
function that takes only an agent identifier — which means there is no code path that can fetch a
mainnet key for a sandbox challenge. [06-signer.md](../06-signer.md).
