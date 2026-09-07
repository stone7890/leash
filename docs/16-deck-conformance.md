# 16 · Deck conformance

**The audit record.** The only chapter organised by the deck rather than by the system.

Every place this specification departs from `resources/specs/`, every contradiction found in the
source material and how it was resolved, and every gap the deck leaves that had to be filled. If
you are checking whether the build meets the specification, start here.

The rule that makes this file worth keeping: **if you are about to implement something and the
documentation does not answer your question, add the answer here rather than inventing it in
code.**

---

## §A · What is met, unchanged

Recorded so that the deviations below are visible against a background of conformance rather than
appearing to be the whole story.

| Deck | Status |
|---|---|
| I1–I8, all eight invariants | Met, each with a named enforcement mechanism ([02-invariants.md](02-invariants.md)) |
| S0–S7, the codes, the tiers, the defaults, which cannot be disabled | Met ([04-policy-engine.md](04-policy-engine.md)) |
| The latency budget: <50 ms p50, <300 ms p99, no outbound HTTP, phases 1–3 asynchronous | Met, and machine-checked |
| The thirteen tables, their fields, their uniqueness and their append-only rules | Met, mapped to MongoDB ([03-data-model.md](03-data-model.md)) |
| The seventeen endpoints, and the status discipline 202/403/409/422 | Met ([05-api-contract.md](05-api-contract.md)) |
| The four reconciliation jobs and their cadences | Met ([07-indexer-and-jobs.md](07-indexer-and-jobs.md)) |
| P1–P6, with the N-xx step numbering | Met ([09-flows.md](09-flows.md)) |
| The nine screens, the design tokens, the state rules, the verbatim copy | Met ([10-ui-spec.md](10-ui-spec.md)) |
| Q3 — FIFO when racing for the remainder | Met, with an operational definition — §C-3 |
| The scope boundary: no custody, no fiat, no KYC, no EVM, no RBAC | Met ([00-overview.md](00-overview.md)) |

---

## §B · Deviations

Deliberate departures. Each states what the deck says, what we build, and why.

### B-1 · The stack, and the split

**The deck says** (ch. 5, ch. 16): Node 22 + Fastify + zod + PostgreSQL 16, as a pnpm monorepo of
`apps/web`, `apps/api`, `apps/signer`, `apps/demo-402` and `packages/core`.

**We build:** the API as **Next.js route handlers in the same deployable as the dashboard**; the
signer, the indexer and `demo-402` in **Go**; **MongoDB 8.0** throughout.

**Why the API moved into Next.js.** Fifteen of the seventeen endpoints are ordinary
request/response over the database, consumed by exactly one client — the dashboard. Putting them
in the same application removes a network hop, a CORS configuration and a type boundary.

**Why three components did not.** They cannot be request handlers at all, and the reason is
runtime rather than language:

- The **signer** meets its budget only because the snapshot cache, the unwrapped KMS key, the
  blockhash and a change stream live in process memory. Invocation-scoped execution has none of
  them, and the kill switch's *"instantly"* would degrade to a 60-second TTL.
- The **indexer** is four loops, and `alert-eval` runs every 30 seconds against an interface
  promise of thirty seconds. A scheduler with a one-minute floor cannot keep it.
- The **test-payment stream** is server-sent events over a resumable change stream, held for a
  whole onboarding session.

**What the deck's reasoning bought that we lose, and what replaces it:**

| The deck's reason | What we lose | Replacement |
|---|---|---|
| zod types shared between frontend and backend | The frontend shares types with the Next.js API but **not** with the Go signer | `contracts/` — committed, language-neutral: `error-codes.json`, `endpoints.json`, the S0–S7 vectors, money vectors. Both runtimes are checked against it in CI |
| `packages/core` reusable in the V2 self-hosted sidecar | The sidecar inherits a **Go** core | Acceptable: the sidecar is a separate binary anyway, and the policy engine is a pure package with no I/O, which is what actually makes it portable |
| One language, less context-switching for a solo builder | Two languages | Accepted deliberately. The three Go components are the three that need a long-running process, so the boundary is meaningful rather than arbitrary |

**And a cost the deck never had to consider:** two MongoDB access layers, where the discipline is
*all database access in one place*. Survivable because the **enforcement point is the server** —
validators, partial unique indexes and role privileges constrain both runtimes identically, and
`verify-constraints` proves them against MongoDB rather than against a client. Ownership stays
single: each collection has exactly one writer. §D-6.

### B-2 · `agents.network` immutability

**The deck says** (ch. 12): `network network_t NOT NULL` with *"a trigger forbidding UPDATE on
this column"*.

**We build:** the network is a **component of the document `_id`** — `agt_test_…` / `agt_live_…` —
which MongoDB refuses to update at the storage engine, plus an `$expr` validator pinning the
`network` field to the prefix.

**Why this is stronger, not a substitute.** A trigger is code that runs; an immutable `_id` is a
property of the engine. And because the same validator ties a *referenced* identifier's prefix to
the referencing document's network, a mainnet payment cannot point at a sandbox agent — the
cross-table check PostgreSQL could only have had as a composite foreign key. It is also visible to
the naked eye in a URL, a log line and a support ticket.

The one thing MongoDB supposedly could not do turns out to be the thing it does best here.

### B-3 · The budget race, and `reserved_base`

**The deck assumes** (ch. 11, ch. 12): `SELECT … FOR UPDATE` over a live
`Σ amount_base(payments ∈ {signed, submitted, unknown})`.

**We build:** a conditional `findOneAndUpdate` whose predicate is in the **filter**, deciding S4
and S5 in one atomic act, with the in-flight total **materialised** as `allowances.reserved_base`.

**Why the mechanism changed.** MongoDB has no row lock. A `findOneAndUpdate` is a better primitive
for this problem — the storage engine retries write conflicts server-side, whereas a transaction
aborts and must be retried whole, turning the contended case into a retry storm in exactly the
scenario the design exists for.

**What it costs, and this is the deviation to worry about first.** A live `SUM()` cannot drift
because it is never stored. A materialised total can. Every drift is either a refused payment that
should have gone through or, worse, an admitted one that should not.

Three defences, none of which removes the risk:

1. The validator's `drawn + reserved ≤ cap` refuses an over-admitting document outright — the
   admission filter cannot over-admit even if it is wrong.
2. `allowance-refresh` recomputes from the payments every 60 seconds and logs any delta as a
   warning **with the amount**.
3. The decrement happens only in the same atomic update that writes a fresh `drawn_onchain_base`
   and its slot, so every window is conservative — counted twice, never zero times.

### B-4 · Expiry is not enforced on-chain

**The deck says** (ch. 3 I3, ch. 11): *"Hard, on-chain: cap, expiry, revoke."*

**We build:** cap and revoke on-chain. **Expiry is a signer-tier rule**, and
`allowances.expiry_tier` stores which tier applies so the interface renders from data rather than
a constant.

**Why.** The shipping adapter is the SPL `approve`/`revoke` delegate, which has a delegated amount
and no expiry. The deck anticipated this as its `[Q1]` fallback and said the interface must
relabel the tier; this specification does that, and makes the tier data so the relabelling is
automatic when `chain/subs` ships.

**What it costs.** If the signer were bypassed, an expired allowance could still be drawn against,
up to its cap. The signer cannot be bypassed by the agent, which never holds a key — so the
exposure is a compromise of the signer, already the worst case in
[12-security.md](12-security.md). The cap still binds and the owner can still revoke.

**A consequence in the interface that must not be missed:** the Settings copy the deck requires
verbatim says *"Caps and expiries keep being enforced by Solana no matter what happens to
Leash."* **That sentence is false under this implementation**, and I3 does not permit shipping it.
It becomes: *"Caps keep being enforced by Solana, and revocation works from your wallet, no matter
what happens to Leash."* §C-5.

### B-5 · `bigserial` becomes a ULID

**The deck says:** `handshake_events` and `policy_revisions` have `id bigserial PK`.

**We build:** ULIDs — time-sortable, monotonic within a millisecond, no counter document and no
hot spot.

**What it costs:** the sequence is **gapped**, so an audit trail's completeness cannot be proved
by counting. If a compliance conversation needs gapless numbering it needs a counter document or
an external notary. Decide before that conversation, not during it.

---

## §C · Contradictions in the source, and their resolutions

### C-1 · The check count — "6 of 6" against "7/7" against eight rules

The prototype's step-5 log and its payment drawer say **"6 of 6 checks"** and name six:
allow-list, per-payment, velocity, budget, expiry, mint. The deck's mock of the *same screen*
says **"rules passed (7/7)"**. The rule set S0–S7 is **eight**.

**Resolution:** the interface renders the count and the names from the `checks` array returned by
the API, never from a literal. `sign_requests.checks` is the authoritative record of what was
evaluated. The number cannot drift from the engine again, and a rule added or removed appears in
the log without a second edit.

### C-2 · Chapter numbering on the cover

The deck's cover calls the three new developer chapters *"Stack (6), Contract design (13), Docs
guide (16)"*. Stack is chapter **5**; chapter 6 is the lane model. Recorded so cross-references
resolve.

### C-3 · "FIFO by signing time" is not a total order

Q3 is resolved in the deck as *FIFO by signing time*. BSON dates are **millisecond** resolution
where `timestamptz` is microsecond, so under contention this does not define an order.

**Resolution:** FIFO is the order in which requests reach the allowance compare-and-swap on the
primary, reconstructible from the ULID `_id` of the `sign_requests` documents. This is a
redefinition, not a restatement.

### C-4 · The prototype's version label

The filename says `v3-1`; the document title says `v3.0`. Immaterial, recorded for completeness.

### C-5 · The Settings copy contradicts the shipping adapter

Covered in §B-4. The deck requires the *"If Leash is ever down"* section verbatim, and its claim
about expiry is false under the SPL implementation. The corrected sentence is in
[10-ui-spec.md](10-ui-spec.md), and the public page is
[if-leash-is-down.md](if-leash-is-down.md).

### C-6 · Onboarding step 3's copy contradicts what the transaction does

The prototype says *"Funds stay put — you're granting a capped permission, not sending money."*
Under the per-agent token account design (§D-1) the owner's USDC **moves** into an account they
still own.

**Resolution:** the copy is corrected to describe what happens, and the review card gains a rent
line. Invariant I1 is unaffected — the account is owner-owned throughout — but the sentence as
written would be false, and this is a product whose whole claim is that it does not mislead you
about where your money is.

---

## §D · Gaps the deck leaves, and how they are filled

### D-1 · SPL `approve` allows exactly one delegate per token account

**The largest finding, and it is a product problem before it is a coding problem.**

`approve` sets **the** delegate on a token account — singular. A second `approve` replaces the
first. So an owner with three agents cannot delegate three allowances from one USDC account:
agent two silently takes agent one's delegation. The deck's fallback plan does not mention this
and the naive reading of it does not work.

**Resolution:** a token account per agent, owner-owned, funded to the cap.
[08-onchain-adapter.md](08-onchain-adapter.md). Four consequences, none optional: the owner's
funds move (§C-6); rent is per agent and shown before signing; revoke becomes two instructions —
revoke **and sweep** — and the public revoke guide must show both or an owner following it strands
their own money; and a top-up replaces rather than adds, which is why `allowances.epoch` exists.

**Verify on day one, beside `[Q1]`.** If the S&A primitive supports concurrent allowances against
one account, most of this reverts.

### D-2 · SIWS has no nonce store

The deck specifies Sign-In With Solana and specifies nothing to prevent replay. Without a nonce
store, a signed sign-in message is replayable for as long as it exists.

**Resolution:** the `siws_nonces` collection. Issued, consumed by deletion in one operation, TTL
of five minutes. A genuine gap in the specification, not an artefact of the stack.

### D-3 · Handshake phase 4 is unobservable

`replayed` happens inside the customer's agent runtime, which we do not run.

**Resolution:** the row is absent when it cannot be observed, and the interface renders it dimmed
— not as a failure, and not as a gap to apologise for. Fabricating a timestamp would be inventing
evidence in an audit trail, which is the one thing an audit trail may not do.

### D-4 · Nothing says how the kill switch beats its own cache

S7 must take effect *"instantly"*, and the snapshot cache has a 60-second TTL. The deck does not
reconcile the two.

**Resolution:** a change stream on `agents`, filtered to updates touching `killed`, evicting
within tens of milliseconds. When it drops, the TTL is the floor and the degradation is logged and
visible — never silent. [06-signer.md](06-signer.md).

### D-5 · Nothing says what happens between signing and recording

The deck requires no double-signing (I7) and a sub-300 ms path, but a transaction across the
allowance and the records would be too slow, and without one there is a window where a signature
exists and no record does.

**Resolution:** Ed25519 is deterministic and the blockhash is **pinned into the claim document**,
so re-signing the same challenge produces byte-identical output. One signature, produced twice.
No transaction needed on the hot path.

### D-6 · Nothing anticipates two runtimes sharing the database

A consequence of §B-1, and the deck could not have foreseen it.

**Resolution:** the enforcement point is the server; each collection has exactly one writer;
`contracts/` holds the shared truth and both sides are checked against it; the golden vectors are
executed in both languages. [13-testing.md](13-testing.md).

### D-7 · Who pays the network fee on a draw

The specification names a gasless facilitator (Kora) as an option and leaves the choice open as its
`[Q2]`. It does not say what happens before that is settled — and a draw has to be paid for by
somebody, or it does not land.

**Found by running it.** The signer produced a valid signature and the endpoint refused to submit
it: `Attempt to debit an account but found no record of a prior credit`. The agent was the fee
payer and held no SOL.

**Resolution:** the owner funds a small SOL float into the agent's own account at delegation time —
`agentFeeFloat`, 0.02 SOL, enough for thousands of draws and not worth stealing. It appears in the
transaction summary alongside the rent, so the owner sees it before signing.

It is SOL for fees, not the customer's USDC, so the custody position is unchanged. When `[Q2]`
settles on a sponsoring facilitator the float becomes unnecessary and the instruction is removed.

### D-8 · A per-agent account cannot be a program-derived address

The per-agent token account (§D-1) has to be created by the system program, and `CreateAccount`
requires the new account to sign. **A program-derived address cannot sign**, so the obvious
derivation does not work.

**Found by running it**, as `signer key ... not found. Ensure all the signer keys are in the vault`.

**Resolution:** `CreateAccountWithSeed`, with the owner as the base. It keeps the property that
matters — the same owner and agent always yield the same address, so a delegation whose
confirmation we missed is recognised rather than duplicated — while needing only the owner's
signature. The seed is a hash of the agent and the mint, because Solana caps a seed at 32 bytes and
an agent key is 44 characters, and because an agent may hold one allowance per mint.

### D-9 · The Node driver breaks I6 by default

`bsonType: "long"` guarantees an amount is stored as an integer. It does not guarantee it is READ
as one: the Node MongoDB driver promotes int64 to a JavaScript `number` unless told otherwise, and
`number` is an IEEE double. Every amount above 2^53 loses precision on the way out of the database,
silently, with a correct schema and a correct write.

**Found by the guard that exists for it.** `toBig()` refuses to convert a `number` rather than
rounding it, so the dashboard failed loudly — *"an amount was read as a JavaScript number"* —
instead of showing a plausible wrong balance.

**Resolution:** `promoteLongs: false` on the client, so int64 arrives as a `Long` and converts to
`bigint` exactly. The guard stays, and its message now names the setting, because it is the only
thing standing between a driver default and a wrong number on a spending control.

Worth stating generally: the Go side needed a custom BSON codec for the same reason. **Neither
language reads money correctly by default**, and both required deliberate work at that one
boundary.

### D-10 · The dashboard cannot mint a key, and must not build a transaction

Two of the deck's own rules collide with the split in §B-1, and the resolution is worth recording
because it looks like a deviation and is not.

**Key minting.** The deck gives the API `POST /v1/agents`, and creating an agent means generating an
Ed25519 keypair. But the signer is *"the only holder of keys"*, and nothing under
`frontend/website` may import a KMS client — a rule this repository enforces over the build graph.
So the dashboard writes the agent and its policy, and asks the signer, on an internal path, to mint
the key. If that call fails the agent is removed: one without a key is one the signer would refuse
for a reason nobody could diagnose.

**Transaction building.** Same shape. The dashboard owns the endpoint — it authenticates the owner
and decides what may be built — but the construction happens in the indexer, which owns the
adapter. There must be exactly one implementation of what a delegation looks like: chapter 13 is
where a wrong assumption costs money rather than a test failure, which is why a pull request
touching it must attach a sandbox end-to-end run. Two copies in two languages would double that
risk and halve the confidence in both.

Both internal paths require a shared token **and** are unreachable through nginx, which exposes
`/v1/sign` of the signer and the SSE stream of the indexer and nothing else. Both, not either: a
network boundary one misconfigured proxy away from being public is not a boundary.

### D-11 · A brand-new wallet cannot complete onboarding

Step N-01 says the workspace starts on the sandbox and the faucet grants 100 test USDC. Nothing
says what happens if it does not — and the answer, found by running it, is that step 3 builds a
transaction the owner cannot pay for and cannot fund, and the wizard stops with a chain error
naming an account rather than the cause.

**Resolution:** the faucet is part of the wizard, before the wallet prompt, and grants SOL as well
as USDC — a wallet holding USDC it cannot move is a worse experience than one holding nothing. One
grant per wallet per hour, recorded in `faucet_grants` rather than in memory, because an in-memory
limit survives neither a restart nor a second instance and fails silently in both cases.

### D-12 · The wire format was invented, and had to be replaced

The specification says *"chuẩn 402/x402"* and cites pay-kit's playground, but does not carry the
format itself. An earlier version of this build therefore invented one — and it was wrong in seven
ways at once. A real agent using `pay` could not have paid our endpoint, and our signer could not
have signed a real challenge.

| We had | x402 actually says |
|---|---|
| `version: "x402/1"` per offer | `x402Version: 1` or `2`, a number, at the top level |
| `amount: "0.010000"` | `maxAmountRequired` / `amount`, in **base units**: `"10000"` |
| `mint` | `asset` |
| `pay_to` | `payTo` |
| `network: "sandbox"` | `"solana-devnet"` (v1) or CAIP-2 (v2) |
| `nonce` in the challenge | **no such field** — the replay nonce is the transaction's memo |
| — | `resource`, `maxTimeoutSeconds`, `extra.{feePayer,decimals,tokenProgram}`, `protocol` |

**Resolution:** the format now follows pay-kit's normative spec, vendored under `contracts/x402/`
along with its cross-SDK conformance vectors, and the tests in `internal/domain/challenge` run
against those vectors — including their two REJECT cases. If the protocol moves and we do not, a
test fails instead of a payment.

**pay-kit has a Go SDK**, at `github.com/solana-foundation/pay-kit/go`. We do not use it, for one
specific reason: its client derives the payment's source as `ATA(signer, mint)` — it assumes
whoever signs owns the money. Leash's entire product is a *delegated* transfer, where the source is
the owner's per-agent account and the agent is only its delegate. Their **verifier** does not
constrain the source, so our transaction satisfies it; their **builder** simply cannot express one.
So we build, and check ourselves against their vectors.

### D-13 · Who pays for gas — the answer, replacing §D-7

§D-7 recorded that an agent needs SOL of its own and had the owner fund a float. **That was wrong,
and reading the protocol settled it in the other direction.**

The challenge carries `extra.feePayer`: the facilitator sponsors the fee. And pay-kit's verifier
REFUSES a transaction whose fee payer is also the transfer authority — the defence against a
fee-payer drain, which their own `docs/security/fee-payer-drain.md` sets out. So an agent paying
its own fee is not merely wasteful, it is a shape the protocol rejects.

The float is gone, and with it a per-agent cost the owner was being asked to carry for nothing.

### D-14 · The transaction shape is not ours to choose

pay-kit's verifier reads instructions POSITIONALLY, so the layout is part of the contract:

```
[0] ComputeBudget SetComputeUnitLimit
[1] ComputeBudget SetComputeUnitPrice     <= 5,000,000 microlamports
[2] transferChecked  ->  ATA(payTo, mint, tokenProgram)
[3] Memo                                   exactly one, always
    ... only Memo or Lighthouse may follow; total in [3, 6]
```

Three of those cost us a debugging session each, and are recorded so they cost nobody another:

- **`transferChecked`, not `transfer`.** It carries the mint and decimals, so the chain refuses an
  amount computed against the wrong precision.
- **The destination is the recipient's ATA, not the recipient.** Sending to `payTo` directly fails
  with `InvalidAccountData` — after the compute-budget instructions have already succeeded.
- **The memo is mandatory and is the replay protection.** x402 on Solana has no nonce field, so
  two otherwise identical payments would be the same transaction without it.

The compute LIMIT is ours: pay-kit's client uses 20,000, which is not enough here — a delegated
`transferChecked` costs about 6,300 and the memo then exhausts what is left, failing *after* the
payment instruction succeeded. We use 60,000. The verifier caps the price, not the limit.

### D-15 · Idempotency without a nonce

Invariant I7 says `challenge_hash` is unique. That assumed the challenge carried a nonce. It does
not — two purchases of the same resource at the same price are **byte-identical**, so hashing the
offer alone made the second purchase collide with the first and receive its signature.

**Found by running it twice**: the second demo returned the first's signature, for a different
agent.

**Resolution:** the claim is keyed on `(agent, attempt, offer)`, where `attempt` is an
`Idempotency-Key` the agent supplies — the same discipline every other creating POST in this API
follows, and the agent is the only party that knows whether it is retrying. The offer hash is
recorded alongside, because "which offer was this" is what an audit asks.

### D-16 · A sponsored payment settles under a signature we never see

When a facilitator pays the fee, the transaction's on-chain identity is the FEE PAYER's signature
— the first one — and that does not exist when we sign. Looking a payment up by the signature we
produced finds nothing, ever, and it would sit in `unknown` until the sweep abandoned it a day
later, holding budget the whole time.

**Resolution:** the memo. It is the protocol's own replay nonce, unique per payment, and Solana
returns it on `getSignaturesForAddress` — so the indexer finds a sponsored payment by scanning the
account it was drawn from, with no cooperation from the agent. That matters: an agent using a stock
x402 client has no idea Leash exists and cannot report anything back. The settled signature is then
recorded alongside the agent's, so the audit trail names a transaction an explorer can show.

### D-17 · Nothing says whether the hosting tier can express append-only

The deck's `REVOKE UPDATE, DELETE` assumes a database where you can. MongoDB can, through a
custom role — but some managed shared tiers forbid custom roles entirely.

**Resolution:** verify the tier before W1 ends. If it cannot, append-only degrades to
application-enforced only, which is a **materially weaker claim to make to a customer** and must
be stated publicly rather than quietly accepted.

---

## §E · Open questions, carried forward

| # | Question | Decider · by | Blocks | State |
|---|---|---|---|---|
| **new** | Does SPL `approve` force a token account per agent? Does S&A avoid it? | Founder · W1 day 1 | The adapter, onboarding copy, the revoke guide, rent | **open** |
| **new** | Does the hosting tier support custom database roles? | Founder · W1 | The append-only claim | **open** |
| Q1 | The real instruction and account layout of the Subscriptions & Allowances primitive | Founder · W1 day 1 | The adapter, N-06/N-08, P4 | open |
| Q2 | Which facilitator verifies and settles for `demo-402`? | Founder · before W2 | The P1 test loop | open |
| Q4 | Which fields enter the canonical hash across x402 versions? | Founder · W2 | I7 on the external path | open — an unknown version is **refused**, not guessed |
| Q5 | A public Surfnet RPC and faucet, or our own instance? | Founder · W1 | Sandbox-first, N-01 | open |
| Q3 | Order when racing for the remainder | — | — | **resolved:** FIFO, defined in §C-3 |

---

## How to add to this record

1. **Name it.** §B for a deliberate deviation, §C for a contradiction in the source, §D for a gap,
   §E for something still undecided.
2. **Quote the deck**, with its chapter.
3. **Say what we build instead**, and **why** — the reasoning is the point, not the decision.
4. **Say what it costs.** A deviation with no stated cost has not been thought through.
5. **Link the chapter** that carries the detail.

A deviation that is not in this file is not a deviation. It is a bug.
