# 08 · The on-chain adapter

**Leash writes no smart contract.** It calls programs that already exist, through one interface,
isolated in one package, so that when the assumptions behind them turn out to be wrong only that
package changes.

The deck's chapter 13 makes this explicit and gives a reason: the real instruction and account
layout of Solana's **Subscriptions & Allowances** primitive was an open question at the time of
writing — the deck's `[Q1]` — and the pre-designed fallback was the standard SPL
`approve`/`revoke` delegate. This specification ships the fallback.

## The interface

```go
// internal/chain/adapter.go
type AllowanceAdapter interface {
    BuildCreate(CreateParams) (UnsignedTx, error)   // owner signs
    BuildModify(ModifyParams) (UnsignedTx, error)   // owner signs — top-up and extend
    BuildRevoke(RevokeParams) (UnsignedTx, error)   // owner signs — and see below
    BuildDraw(DrawParams)     (SignedTx,   error)   // the signer signs, with the agent key
    ReadAllowance(ctx, addr)  (AllowanceState, error)

    Capabilities() Capabilities   // what this implementation actually enforces on-chain
}
```

Two implementations behind it:

| Package | Status | Enforces on-chain |
|---|---|---|
| `chain/spl` | **shipping** | cap, revoke |
| `chain/subs` | stubbed | cap, revoke, **expiry** — once `[Q1]` is answered |

`Capabilities()` is the mechanism that keeps invariant I3 honest across that swap. It reports
which rules the implementation genuinely enforces, that answer is written to
`allowances.expiry_tier` when the allowance is created, and the interface renders the tier from
the stored value. **No tier is ever hardcoded in the frontend**, because the frontend would then
be asserting something the adapter had stopped doing.

## The permission matrix

| Operation | Who signs | Moves assets? | Precondition |
|---|---|---|---|
| Create an allowance | **the owner's wallet** | yes — see below | A valid SIWS session; the agent belongs to the org |
| Modify — top up or extend | **the owner's wallet** | yes, on a top-up | Same |
| Revoke | **the owner's wallet** | yes — the sweep | Same. Also settable from the CLI without Leash (I5) |
| Draw | **the agent key, inside the signer** | yes | Verdict `allowed` across S0–S7 |
| Read | nobody | no | — |
| **Anything, by the Leash backend** | **never** | — | I1. There is no code path |

## The finding: SPL `approve` allows exactly one delegate per token account

This is the largest constraint in the design, and it is a product problem before it is a coding
problem.

`spl_token::instruction::approve` sets **the** delegate on a token account — singular. A second
`approve` on the same account replaces the first. So an owner with three agents cannot delegate
three separate allowances from one USDC account: agent two silently steals agent one's
delegation, and agent one's cap becomes agent two's cap.

The deck's fallback plan does not mention this, and it invalidates the naive reading of it.

### The resolution: a token account per agent

`BuildCreate` becomes three owner-signed instructions:

```
1. create a token account for this agent      owner-owned, agent-specific
2. transfer `cap` USDC into it                from the owner's main account
3. approve the agent's pubkey as delegate     for exactly `cap`
```

`allowances.onchain_addr` is that token account, and `one_allowance_per_onchain_account` becomes
the constraint that actually carries the rule.

### The four consequences, none of them optional

**1 · The owner's funds move.** They move into an account the owner still owns and can drain at
any time, so invariant I1 holds without qualification — Leash never has custody. But *"delegate
an allowance"* sounds like nothing moves, and something does.

**The onboarding copy must say so.** The review card at step 3 of onboarding gains a line, and
the sentence *"Funds stay put — you're granting a capped permission, not sending money"* must be
corrected to describe what actually happens. Writing that correctly is a
[10-ui-spec.md](10-ui-spec.md) task, not an afterthought: the current sentence would be false.

**2 · Rent, per agent.** Each token account must be rent-exempt, roughly 0.00204 SOL. The
transaction summary already shows a network fee; it must now show rent as a separate line, before
the owner signs. The deck's chapter 13 flags rent generally; here it is per agent and recurring
with each new one.

**3 · Revoke is two instructions.** Revoking the delegation leaves the remaining balance sitting
in the per-agent account. So `BuildRevoke` emits:

```
1. revoke      the delegation — the agent can no longer draw
2. transfer    the remaining balance back to the owner's main account
```

**And [if-leash-is-down.md](if-leash-is-down.md) must show both.** An owner following the
self-serve guide during our outage, who revokes but does not sweep, has stranded their own money
in an account they will not think to look at. That page is the one piece of documentation whose
incorrectness costs a customer directly.

**4 · Top-up replaces rather than adds.** `approve` sets the delegated amount; it does not
increase it. Raising a cap from $10 to $20 means transferring the difference in and approving
$20 — against a fresh delegation whose drawn counter starts at zero.

`allowances.epoch` exists for this. It increments on each modification, and the cached `drawn`
resets with it. Without the epoch, a top-up would make the previous drawn amount look like part
of the new allowance's consumption, and the budget would be wrong by exactly the amount already
spent.

**The old allowance keeps working until the new transaction confirms.** There is no dead window,
and the interface must not show one — flow P4 in [09-flows.md](09-flows.md).

### Verify this on day one

It sits beside `[Q1]` on the risk register in [15-roadmap.md](15-roadmap.md), at the same
severity. If the Subscriptions & Allowances primitive turns out to support multiple concurrent
allowances against one account, most of this section disappears and the copy reverts. If it does
not, this is the shape of the product.

## Expiry is not enforced on-chain

SPL delegate has a delegated **amount**. It has no expiry.

So expiry is a signer-tier rule, `allowances.expiry_tier` is `"signer"`, and the Rules tab shows
"Expires" in the dashed-border, hollow-dot card rather than the solid green one. This is invariant
I3 working as intended: we are not permitted to draw a rule as chain-enforced when the chain does
not enforce it, however much better it would look.

What this costs, stated plainly: **if the signer is bypassed, an expired allowance can still be
drawn against, up to its cap.** The signer cannot be bypassed by the agent, which never holds the
key — so the exposure is a compromise of the signer itself, which is already the worst case in
[12-security.md](12-security.md). The cap still binds, and the owner can still revoke.

When `chain/subs` ships, `Capabilities()` reports on-chain expiry, new allowances are created with
`expiry_tier: "onchain"`, and the interface follows automatically. **Existing allowances keep
their stored tier**, because they were created under the old implementation and relabelling them
would be a lie about a live delegation.

## Solana platform traps

Each of these has bitten somebody, and each has a specific mitigation.

**Blockhash expiry.** A blockhash lasts 60–90 seconds. Between building a transaction and the
owner actually signing it in their wallet, a person can be interrupted.

> Build the transaction immediately before opening the wallet. If more than **45 seconds** pass
> before the signature comes back, rebuild it. The interface's *"Try again"* branch exists for
> this, and the intent response carries a real `expires_at`.

**The recipient's token account may not exist.** Under the x402 `exact` scheme, creating the
recipient's associated token account is the endpoint's or the facilitator's responsibility, not
ours. The signer verifies existence at build time and refuses with `403 UNSUPPORTED_TERMS` and a
`details` explaining precisely which account is missing, so the developer on the other end can
act on it.

**USDC is classic SPL, not Token-2022.** The two have different program identifiers, and using
the wrong one produces a confusing failure rather than an obvious one. The adapter takes
`tokenProgramId` from the per-network mint configuration — `USDC_MINT_SANDBOX` and
`USDC_MINT_MAINNET`, each with its program — and test vectors cover both.

**Rent exemption.** Covered above. Shown to the owner before signing, as a separate line.

**Priority fees.** When the network is congested, a transaction without a priority fee may not
land. `BuildCreate`, `BuildModify` and `BuildRevoke` include a compute-budget instruction with a
fee derived from a percentile of recent fees.

The **draw** is submitted by the endpoint or its facilitator, not by us — so its priority is
outside our control, and a draw that does not land is exactly what the `unknown` state and
`payment-sweep` exist to handle ([07-indexer-and-jobs.md](07-indexer-and-jobs.md)).

## Reading the chain

```go
ReadAllowance(ctx, addr) (AllowanceState, error)
// → cap, drawn, expiry, state, slot
```

Called only by the indexer, and by the signer's snapshot refresher. Never by a request handler:
the signing path makes no outbound call at all ([06-signer.md](06-signer.md)), and the API's read
paths serve the cache with its `last_read_slot` rather than blocking on an RPC.

**Every returned value carries the slot it was read at**, and that slot is written alongside the
numbers. This is invariant I2 at its narrowest: a balance without a slot is a balance we cannot
justify, and the schema refuses an `active` allowance that has no `last_read_slot`.

## RPC clients

Per network, primary and fallback:

```
RPC_SANDBOX_URL           RPC_SANDBOX_FALLBACK_URL
RPC_MAINNET_URL           RPC_MAINNET_FALLBACK_URL
```

**There is deliberately no `RPC_URL`.** A single global endpoint is the one variable capable of
sending a sandbox payment to mainnet, so it does not exist, and a configuration test reads the
config source to make sure it never quietly reappears (I8).

Failover is automatic and raises the degraded banner. An RPC outage makes data **late, never
wrong**, because `unknown` is a first-class state and nothing is inferred from silence.

If mainnet is in the configured network list and `RPC_MAINNET_URL` is unset, **the process
refuses to boot**, naming the variable. Half-configured is a boot failure — the alternative is
mainnet enabled and silently pointing somewhere else.

## Testing the adapter

The adapter is the one place where a unit test proves very little, because what is being tested
is an assumption about somebody else's program.

- **Golden transaction vectors.** Build each of the four transactions and compare the serialised
  message against a committed fixture. Catches an accidental change to instruction order,
  account ordering or a program identifier.
- **Both token programs.** Vectors for classic SPL and Token-2022, so the mint configuration path
  is exercised rather than assumed.
- **The sandbox end-to-end run, in CI.** Create an agent, delegate, read back, pay through
  `demo-402`, and assert the six-phase timeline. This is the test that would actually catch a
  wrong account layout, and the deck makes it a merge gate.
- **The over-cap proof.** Attempt a draw beyond the cap and assert **the chain** rejects it — not
  our policy engine. This is one of the four pieces of evidence in
  [13-testing.md](13-testing.md), and it is the only one that demonstrates the hard tier is real.

**Any change to the adapter must attach the results of the sandbox end-to-end run.** It is on the
pull-request rules in [15-roadmap.md](15-roadmap.md), because this is the file where a wrong
assumption costs money rather than a test failure.
