# P3 · Monitoring and recovery — N-30 … N-35

The flow that turns a block from a dead end into a one-tap fix. New in deck v3, taken from
prototype v2. Prose: [09-flows.md](../09-flows.md#p3--monitoring-and-recovery--n-30--n-35).

```mermaid
sequenceDiagram
    actor O as Owner
    participant D as Dashboard
    participant A as Leash API
    participant S as Signer
    participant AG as Agent runtime

    note over S: A payment was blocked in P2 at N-14
    S-->>A: sign_requests { verdict: blocked, failed_rule }
    A-->>D: N-30 Red pill in the feed — "blocked · not allowed"
    A-->>O: Telegram alert, if enabled

    O->>D: Open the drawer
    D-->>O: N-31 The timeline stops at phase 2, naming the rule
    note right of D: "Nothing was signed, nothing moved."<br/>"5 other checks not reached."

    note over O: N-32 THE OWNER DECIDES.<br/>Leash does not guess on their behalf.

    alt The endpoint is legitimate
        O->>D: Add <host> to allow list
        D->>A: N-33 PATCH /v1/agents/{id}/policy/allow-hosts
        note right of A: EXACTLY ONE host, from the challenge.<br/>Never a wildcard. Never the parent domain.<br/>Enforced SERVER-SIDE, not just in the button.
        A->>A: Write policy_revisions — who changed what, when
        A-->>D: Toast: "In effect from the next payment"
    else The block was correct
        O->>D: This block is correct
        D->>A: N-34 POST /v1/agents/{id}/suppressions
        note right of A: Suppression for (agent, host).<br/>Silences the ALERT only.<br/>The block still happens, every time.
    end

    AG-->>AG: N-35 The agent retries on ITS OWN cadence
    AG->>S: POST /v1/sign — same host, next attempt
    S-->>AG: 200 — S2 now passes
    note right of S: A fresh timeline, all six phases.
```

## The trap the deck names for this flow

**The one-tap button must add only the exact host from the challenge.** Not a wildcard, not
`*.unknown.xyz`, not the registrable domain. Broadening beyond one host is a decision the owner
makes deliberately on the Rules tab — it is not something a recovery button does on their behalf.

It is enforced server-side as well as in the interface, **because the button is not the only
caller** ([05-api-contract.md](../05-api-contract.md)).

## Two things that look similar and are not

```mermaid
flowchart LR
    B["A blocked payment"] --> R1["Add host to allow list"]
    B --> R2["This block is correct"]
    R1 --> E1["Rule S2 now PASSES for that host"]
    R1 --> E2["policy_revisions row — auditable"]
    R2 --> E3["Rule S2 still BLOCKS"]
    R2 --> E4["The alert is silenced for (agent, host)"]
```

A suppression is not a permission. Conflating the two would mean an owner who wanted to stop
being paged had quietly opened a spending path instead.

## Policy changes are never retroactive

`N-35` is the agent's own retry, on its own schedule. Leash does not replay a blocked payment —
that would cross a forbidden lane edge, and it would be a payment the customer did not ask for.
The drawer copy is fixed and says exactly this:

> If this endpoint is legitimate, allow it and the agent's next attempt will go through.

and the toast afterwards: *"In effect from the next payment."*
