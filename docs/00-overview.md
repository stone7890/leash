# 00 · Overview

## What Leash is

An AI agent that can call paid APIs needs money. Today the only ways to give it money are to hand
it a wallet — which is to hand it everything — or to hand it your API keys and pay the bill
afterwards, which is the same thing with a delay. Neither has a cap, and neither has a stop
button that works while you are asleep.

**Leash gives each agent a budget instead of a wallet.** The owner signs one transaction that
delegates a capped, expiring spending permission to a keypair the agent never sees. The agent
then pays for APIs by itself, over the x402 protocol, and every payment is attributed to an
agent, an endpoint and an on-chain signature.

The product exists in the gap between two facts:

- **The chain can enforce a cap.** It cannot be argued with, it does not need Leash to be
  running, and an agent that tries to exceed it gets a rejected transaction rather than a polite
  error.
- **The chain cannot enforce a policy.** *"Only these hosts, at most twenty-five cents a call, no
  more than a dollar in ten minutes"* is not something a token program knows how to check.

So Leash is two layers, and it is honest about which is which. **This honesty is invariant I3**,
and it is the difference between a safety product and a safety-shaped product.

## The one-sentence version

> Delegate an on-chain spending cap to each AI agent. It pays for APIs over x402 by itself —
> inside the limits you set, with every cent attributed.

And the promise underneath it, which appears on the sign-in screen and must never become untrue:

> Non-custodial. Your funds stay in your wallet; Leash holds keys to nothing.

Read that literally. It is not marketing copy that engineering tolerates — it is
[02-invariants.md](02-invariants.md) I1, and there is a CI gate whose only job is to prove the
repository contains no path by which an owner's key could arrive.

## Who it is for

One developer, or a small team, running AI agents that call paid APIs. In the MVP:

**One owner = one workspace = one organisation.** There is no multi-user access control, no
roles, no invitations, no seat billing. The owner's wallet *is* the account — there is no
password and no email address anywhere in the system. The deck removed the `users` table from
version 1 for exactly this reason, and it stays removed.

The agents themselves are the customer's: an MCP server that Claude talks to, a CLI script, or a
hosted service. Leash does not run them and does not want to.

## The actors

| Actor | Is | Who operates it |
|---|---|---|
| **Owner** | A human with a Solana wallet. Signs every transaction that touches the treasury | The customer |
| **Dashboard** | The Next.js application the owner uses | Us |
| **Leash API** | CRUD, unsigned transaction intents, feed, timeline, audit, alerts, faucet. Next.js route handlers, in the same deployable as the dashboard | Us |
| **Policy Signer** | A separate Go process holding the agent keys. Evaluates the rules and signs | Us |
| **Indexer** | A separate Go process. Reads the chain back, reconciles, and streams the onboarding log | Us |
| **Agent runtime** | The customer's MCP server, CLI or hosted service | The customer |
| **x402 endpoint** | An API that answers `402 Payment Required` and settles | A third party |
| **demo-402** | Our own sample endpoint, $0.01 test-USDC, for onboarding step 5 | Us, sandbox only |
| **Solana** | Surfnet sandbox, or mainnet | Nobody |

[01-architecture.md](01-architecture.md) turns these into lanes and states which edges between
them are forbidden.

## The loop, end to end

```
  1. The owner connects a wallet. There is no sign-up: the signature IS the account.
  2. They pick a template, name an agent, and set a budget and some rules.
  3. They sign ONE transaction. It delegates a capped permission — it does not send money away.
  4. Leash hands back an API key. The agent's private key stays inside the signer, for ever.
  5. The agent calls a paid API and gets 402 Payment Required.
  6. It forwards the challenge to the signer, with its API key.
  7. The signer checks eight rules, signs a transfer inside the allowance, and answers.
  8. The agent retries the call carrying the payment. The endpoint verifies and settles it.
  9. The indexer reads the chain back and marks the payment confirmed, at a named slot.
 10. The owner sees it in the feed, with a six-phase timeline behind it.
```

Steps 5 to 9 repeat, unattended, until the cap is spent, the allowance expires, or the owner
stops it. Steps 3 and 10 are the product: a control you can see, and a receipt you can audit.

## Sandbox first

Every workspace starts on the **sandbox** — a Surfnet test network with play-money USDC and a
faucet. Nothing real happens until the owner switches. This is not a developer convenience; it is
a design constraint that reaches the data model, and it produced invariant I8.

The consequence to internalise now: **there is no global "current network".** There is no
`RPC_URL` environment variable, no `network` field in a config file, no default. The network is a
property of each record, chosen at the moment that record is created, immutable for ever after,
and every code path that touches the chain reads it from the record it is acting on. See
[02-invariants.md](02-invariants.md) I8 and [03-data-model.md](03-data-model.md).

## The scope boundary

### What Leash is responsible for

Creating agents and their keys · building unsigned transactions for the owner to sign ·
evaluating the eight rules before every signature · signing transfers inside an allowance ·
reading the chain back and reconciling · the payment feed and the six-phase timeline · alerts ·
the audit export · the kill switch · the sandbox faucet · the sample x402 endpoint.

### What Leash is explicitly not responsible for

| Not built | Why not |
|---|---|
| **Custody of funds or of owner keys** | I1. This is the product's legal position as much as its architecture: holding customer funds is money transmission |
| **Fiat on-ramp and off-ramp** | Partners do this. `pay topup` and MoonPay exist; we are not a payments company |
| **KYC and KYB** | Deliberately absent. Being non-custodial is what makes it unnecessary — that is I1 doing legal work, not just technical work |
| **Confidential amounts, and human payroll** | V2. It conflicts with the x402 `exact` scheme, and native USDC has no confidential extension |
| **EVM, Base, and multi-chain** | Solana only. The policy engine is written abstractly so this can change, but nothing else is |
| **A marketplace or endpoint discovery** | `pay.sh` does this well and we link to it |
| **Merchant billing, invoices, checkout** | A different product entirely |
| **Mobile** | The dashboard is responsive to 900px. There is no app |
| **Multi-user roles and permissions** | One owner, one workspace. No RBAC in the MVP |
| **A self-hosted signer sidecar** | V2. It will speak the same `/v1/sign` contract, which is why the policy engine is a package with no I/O |

If you find yourself building something on the right-hand column, stop and read
[16-deck-conformance.md](16-deck-conformance.md) — either it moved and the record was not
updated, or it did not move.

## What we charge for, and what we never will

| Plan | Price | Includes |
|---|---|---|
| Free | $0 | 1 agent, 1 budget, live feed |
| Pro | $29/mo | 10 agents, the rules engine, alerts, the kill switch, 90-day audit history |
| Team | $149/mo | Unlimited agents, API access, exportable audit, webhooks |

Billed on-chain in USDC through a Solana subscription the owner approves from their wallet — the
same class of primitive Leash itself is built on. Cancelling means revoking it; access runs to
the end of the period.

Two things are deliberately not monetised, and both are business decisions with a technical
consequence:

- **No percentage fee on the x402 micro-payments themselves.** A take rate on a $0.012 API call
  breaks the economics of the thing we are trying to make possible.
- **No charge for the kill switch, the revoke, or the audit trail.** Putting the safety features
  behind a plan is how a safety product stops being one. The kill switch works on the free plan,
  and it works when the subscription lapses.

## The vocabulary

Used precisely throughout. Where a word here has a narrower meaning than in ordinary use, it is
because a wider one caused a bug.

| Term | Means |
|---|---|
| **Allowance** | The on-chain object holding the cap and the delegation. One per agent per mint that can still spend |
| **Cap** | The maximum an allowance may ever draw. Enforced by Solana |
| **Drawn** | How much has actually left the account, as read back from the chain |
| **Reserved** | How much is committed by payments we have signed but not yet seen confirmed |
| **Remaining** | `cap − drawn − reserved`. The number the signer admits against |
| **Challenge** | The `402` response: scheme, network, `payTo`, amount, nonce |
| **Verdict** | `allowed` or `blocked`. Every `/v1/sign` produces exactly one, and it is recorded |
| **Handshake** | The six-phase life of one payment, from challenge to confirmation |
| **Read-back** | Reading the chain by signature to learn what actually happened. The only way any state becomes `confirmed` |
| **Slot** | Solana's clock. Every number we show about the chain carries the slot it was read at |
| **Tier** | Whether a rule is enforced on-chain (hard) or by the signer (soft). Never hidden |
| **Network** | `sandbox` or `mainnet`. A property of a record, never of the process |
