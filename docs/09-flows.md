# 09 · Flows

Six phases, P1 to P6. The step numbers — N-01 and so on — are the deck's, and they are kept so
that a conversation about "N-12" means the same thing here, in the deck, and in a pull request.

**The diagrams live in [flows/](flows/)**, one file per phase, using these same step numbers. This
chapter is the prose; that directory is the picture.

## The happy path, end to end

| # | Milestone | Steps | Who |
|---|---|---|---|
| 1 | The agent exists on the sandbox, with a template and a key | N-01…N-04 | Owner, signer |
| 2 | The budget is live on-chain — active only after read-back | N-05…N-09 | Owner, Solana |
| 3 | A nine-line test payment runs through the sample endpoint, with a receipt | N-10…N-12 | Signer, demo-402 |
| 4 | The first production payment, with a six-phase timeline in the drawer | P2 | Agent, signer |
| 5 | A blocked payment is recovered in one tap, and the next attempt passes | P3 | Owner |
| 6 | The cap goes $10 → $20 with no dead window; expiry or kill; a CSV audit | P4–P6 | Owner, API |

The target the whole of P1 is built around: **a brand-new wallet to an agent paying a real API
within a $10 budget, in under ten minutes of setup.**

---

## P1 · Onboard, delegate, and pay — five steps

Sandbox-first. The workspace starts on Surfnet with play-money USDC, and nothing real happens
until the owner switches.

### Step 1 — Start from a template · N-01 … N-04

| Step | What happens |
|---|---|
| **N-01** | The workspace defaults to **sandbox**. The faucet grants 100 test USDC |
| **N-02** | The owner picks Research, Scraper, or Blank. The template prefills the policy. The agent is a draft |
| **N-03** | `POST /v1/agents`. `Idempotency-Key` is mandatory. **The network is fixed to the agent at creation** (I8) |
| **N-04** | An Ed25519 keypair is generated inside the signer and wrapped with KMS. The API key carries the `lk_test_` / `lk_live_` prefix matching the network |

The copy that must survive the port: *"Leash generates a dedicated keypair for this agent inside
the signer. The key never leaves it — your agent gets an API key instead."*

### Step 2 — Budget and rules · N-05

The cap, with presets $5 / $10 / $50 and a live estimate — *"≈ 830 Exa searches at $0.012
each"* — the expiry, the allowed endpoints as chips, the per-payment maximum, and the ten-minute
limit.

**Every field carries its tier badge** (I3). Cap is on-chain. Everything else, expiry included, is
signer-tier under the SPL implementation — see [08-onchain-adapter.md](08-onchain-adapter.md).

A wildcard is permitted and warned about: *"Type `*` to allow every endpoint — we'll flag the
agent with a warning if you do."*

### Step 3 — Sign the delegation · N-06 … N-09

| Step | What happens |
|---|---|
| **N-06** | An unsigned transaction, built on the agent's own network, returned to the owner's wallet |
| **N-07** | **The rejection branch.** The wallet refuses → *"The wallet rejected the transaction. Nothing was created and nothing moved."* plus **Try again**. Nothing is created |
| **N-08** | The cap and the delegation land for the agent's pubkey, on the matching network |
| **N-09** | **`active` only after the account is readable** (I4). Until then: `pending`, with a spinner and no action |

N-07 exists because the deck insists the wallet-rejection path is built, not just the happy one.
An interface that only handles success teaches its user that a refusal is a bug.

The pending copy is fixed: *"Waiting for on-chain confirmation… Submitted is not confirmed — this
flips the moment the allowance is readable on Solana."*

> **The review card must state what actually happens.** Under the SPL implementation the owner's
> funds move into a per-agent token account they still own. The card shows the cap, the network,
> the delegate, the network fee **and the rent**, and the copy says the money moves.

### Step 4 — Point your agent at Leash · N-10

The API key is shown **once**, with three snippets: MCP for Claude Desktop, the `pay` CLI, and
Node. Then **Run a test payment** or **Skip — open dashboard**.

### Step 5 — Watch the first payment · N-11 … N-12

| Step | What happens |
|---|---|
| **N-11** | `api.sandbox.leash.dev/demo` — $0.01 tUSDC, always available. The challenge carries scheme `exact`, network, `payTo`, amount, nonce |
| **N-12** | The **real** P2 loop runs, and the log streams to the wizard over SSE. It ends with a receipt and a link to the dashboard |

**This is the step the deck says is most often botched.** The nine lines must be real — one event
per `handshake_events` row, streamed from the server as it happens. A scripted animation in the
frontend would be a demonstration that the product works, shown to a user for whom it might not.

```
$ agent requests https://api.sandbox.leash.dev/demo/search
← 402 Payment Required · exact · $0.010 USDC · payTo Ex4…j8Wq
→ challenge forwarded to Leash signer
✓ rules passed — allow-list, per-payment, velocity, budget, expiry, mint
✓ signed within allowance 3xk…9Qe · $0.010
→ request replayed with X-PAYMENT
… broadcast by endpoint — waiting for on-chain read-back
✓ confirmed · slot 356 442 231 · sig 8Wq…k2N
← 200 OK · receipt attached
```

The check count on line 4 is rendered from the `checks` array, not written as a literal — see
[10-ui-spec.md](10-ui-spec.md).

---

## P2 · The agent pays · N-10 … N-19

The loop that runs unattended, thousands of times.

| Step | What happens | Who |
|---|---|---|
| **N-10** | The agent calls a paid API without payment | Agent |
| **N-11** | `402`, with the payment requirements: `scheme exact`, network, `payTo`, amount, nonce | Endpoint |
| **N-12** | The agent forwards the challenge verbatim to the signer, with its API key | Agent |
| **N-13** | S0–S7: network, allowance liveness, allow-list, per-transaction max, velocity, budget, terms, kill switch | Signer |
| **N-14** | **On a block:** a `sign_requests` row is written, append-only, naming the rule. The agent gets the reason. Nothing is signed | Signer |
| **N-15** | A draw inside the allowance, for exactly the challenge's amount, signed with the agent key | Signer |
| **N-16** | The agent calls the endpoint again with the payment in the `X-PAYMENT` header | Agent |
| **N-17** | The endpoint or its facilitator verifies and submits the transaction | Endpoint |
| **N-18** | USDC moves from the account to `payTo`; the allowance is drawn down on-chain | Solana |
| **N-19** | The indexer polls by signature → `confirmed`, attributed to the agent and the endpoint | Indexer |

**The agent never sees a key, and never submits its own transaction.** Both are forbidden lane
edges in [01-architecture.md](01-architecture.md).

**Leash never retries on the agent's behalf.** The agent retries on its own cadence. A retry
initiated by us would be a payment the customer did not ask for.

---

## P3 · Monitoring and recovery · N-30 … N-35

The flow that turns a block from a dead end into a one-tap fix.

| Step | What happens |
|---|---|
| **N-30** | A red `blocked · not allowed` pill in the feed, and a Telegram alert if enabled |
| **N-31** | The drawer names the rule and says *"nothing was signed, nothing moved"* |
| **N-32** | **The owner decides.** Leash does not guess on their behalf |
| **N-33** | **Add \<host\> to allow list** → exactly one host, never a wildcard, never the parent domain. Writes `policy_revisions` |
| **N-34** | **This block is correct** → a suppression for `(agent, host)`. It silences the alert only; the block still happens |
| **N-35** | The agent retries on its own cadence. The new timeline runs all six phases |

**The trap, called out by the deck:** the one-tap button must add only the exact host from the
challenge. Not a wildcard, not `*.unknown.xyz`, not the registrable domain. It is enforced
server-side as well as in the interface, because the button is not the only caller
([05-api-contract.md](05-api-contract.md)).

The drawer's copy is fixed: *"If this endpoint is legitimate, allow it and the agent's next
attempt will go through."* — and the toast afterwards says *"In effect from the next payment"*,
because policy changes are never retroactive.

---

## P4 · The budget lifecycle · N-40 … N-45

New in version 3. Top up a cap, or extend an expiry, without a dead window.

| Step | What happens |
|---|---|
| **N-40** | The modal: a new cap or a new expiry. Shows the wallet balance and the network fee |
| **N-41** | An unsigned transaction. **The old allowance stays in force until the new one confirms** |
| **N-42** | The owner signs. Leash does not touch the treasury (I1) |
| **N-43** | The new cap or expiry lands. **No dead window** — the old value applies right up to the replacing slot |
| **N-44** | Read-back. The bar and the number change **only** after the read-back, with its slot (I2, I4) |
| **N-45** | The signer reloads its cache within 60 seconds; the next signature uses the new cap |

Two derived states appear here, computed at query time and never stored:

- **`exhausted`** — the cap is fully drawn but the allowance is live until expiry. The interface
  prompts a top-up rather than showing a dead agent.
- **`expiring-soon`** — under 24 hours. Amber in the Expires column, and an alert if enabled.

> **Under the SPL implementation, a top-up replaces the delegation rather than adding to it**, so
> the new cap comes with a fresh drawn counter. `allowances.epoch` carries this; without it the
> already-spent amount would be counted against the new cap.
> [08-onchain-adapter.md](08-onchain-adapter.md).

---

## P5 · Kill and revoke · N-50 … N-55

Two tiers, in order, and the interface says so before the owner confirms.

| Step | What happens |
|---|---|
| **N-50** | The confirmation modal: *"Two things happen, in order."* |
| **N-51** | `killed = true`. The signer refuses every new `/v1/sign` **instantly** — soft tier |
| **N-52** | An unsigned revoke transaction is built |
| **N-53** | The owner signs it in their wallet |
| **N-54** | The revoke lands. **Hard tier** — even a leaked key can no longer draw |
| **N-55** | Read-back → the state becomes `revoked` |

**The window between N-51 and N-54 is the nature of the chain, not a bug.** The interface keeps
saying `revoking…` until N-55, and the kill button is relabelled `Revoke pending` and disabled.

Saying "revoked" before the read-back is **the trap the deck names for this flow**, and it is on
the reject-merge-if list. It would tell an owner that a compromised agent is contained at a moment
when it is not yet.

The modal copy is fixed:

> **Instantly:** The signer refuses every new payment from this agent.
> **On-chain:** Your wallet signs a revoke — once confirmed, the allowance is gone even if Leash
> is offline.

N-51 happens the moment the intent is requested, before the transaction is even built — so the
soft tier engages even if the owner then abandons the wallet prompt.

> `BuildRevoke` emits **two** instructions on the SPL implementation: the revoke, and a sweep of
> the remaining balance back to the owner. [08-onchain-adapter.md](08-onchain-adapter.md).

---

## P6 · Audit export

Always asynchronous. `POST /v1/audit/exports` returns `202` and a job; the exports table shows it
as `preparing` and then `ready`.

One CSV row per payment: the agent, the endpoint, the amount, the status history, and the on-chain
signature.

The copy is fixed: *"Large ranges take a minute. The export is prepared in the background — the
button queues it, this list shows it when it's ready."* and the toast, *"Export queued. It appears
below when ready — nothing to wait on."*

---

## The state machines

### Allowance

```
   pending ──── read-back ────▶ active ────┬──── cap drawn ────▶ exhausted (derived)
      │            (I4)           │        │
      │                           │        ├──── expiry passes ─▶ expired
      │                           │        │
      └── wallet rejected ──▶ (nothing)    └──── revoke lands ──▶ revoked
```

`pending` and `active` are the states subject to `one_active_allowance`: at most one per
`(agent, mint)`. The transition out of them removes the materialised key in the same update that
changes the state ([03-data-model.md](03-data-model.md)).

**`pending → active` happens only through a read-back.** There is no other transition into
`active`, and the schema refuses an `active` allowance with no `last_read_slot`.

### Payment

```
   signed ──▶ submitted ──▶ confirmed
      │           │
      └───────────┴──▶ unknown ──┬── read-back finds it ──▶ confirmed
                                 └── 24h, no trace ───────▶ failed
```

Covered in [07-indexer-and-jobs.md](07-indexer-and-jobs.md). The rule that matters: **every exit
from `signed`, `submitted` and `unknown` is a read-back.** There is no transition that re-signs
and none that infers.

### The handshake

```
 1 challenge_received ──▶ 2 rules_evaluated ──▶ 3 signed ──▶ 4 replayed ──▶ 5 broadcast ──▶ 6 confirmed
   ╰────────── signer ──────────╯               ╰─ signer ─╯   ╰ often  ╯   ╰──── indexer ────╯
                                                                 nobody
```

- **Blocked** → the timeline stops at phase 2, carrying `failed_rule`. The drawer shows the rule
  and *"5 other checks not reached"*, plus the two recovery buttons.
- **Unknown** → phase 5 exists, phase 6 does not. The drawer shows a spinner and the time of the
  next read-back.
- **Phase 4** happens inside the customer's agent and is usually unobservable. It renders dimmed.
  Fabricating a timestamp for it would be inventing evidence in an audit trail.

**Only the indexer may write `confirmed`**, and the database enforces it.
