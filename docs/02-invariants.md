# 02 · Invariants

Eight rules. Every other chapter is subordinate to them. A reviewer rejects a pull request by
citing a number, and that is meant to end the discussion rather than start one.

They are numbered as the deck numbers them (chapter 3). I1–I7 were audited in version 1 and are
unchanged. I8 is new, and exists because the sandbox became the default place a customer starts.

The table below is the summary. The sections after it give, for each invariant, the mechanism
that enforces it — because an invariant with no mechanism is an intention, and intentions do not
survive a deadline.

| # | The rule | What breaking it costs |
|---|---|---|
| **I1** | **Non-custodial with the treasury.** Every transaction touching the treasury — delegate, top-up, revoke — is signed by the owner's wallet. Leash holds neither funds nor owner keys | Money transmission, and the loss of the product's whole position |
| **I2** | **The chain is the source of truth for the budget.** The database is a cache carrying `last_read_slot`. Any number the interface shows must be able to say which slot it was read at | The owner trusts a control that does not exist |
| **I3** | **Honest enforcement tiering.** Hard, on-chain: cap and revoke. Soft, at the signer: allow-list, per-transaction max, velocity, expiry. The interface must label which | The first incident destroys the credibility of a safety product |
| **I4** | **Never report success early; `unknown` is not `failed`.** Every chain write passes through a waiting state, left only by read-back **by signature**. A retry that skips the read is forbidden | Paying twice for one challenge |
| **I5** | **The kill switch survives Leash being down.** Revoke is an owner-signed transaction straight to the chain, and the instructions are published where a customer can reach them without us | The agent runs loose at exactly the moment the leash is needed |
| **I6** | **Money is an integer base unit.** `int64` micro-USDC in Go, `long` in MongoDB, `u64` in the protocol, a string on the wire. Floats forbidden at every layer | The audit trail stops adding up, from accumulated drift |
| **I7** | **Idempotency is enforced at storage.** Outward: `challenge_hash` is unique. Inward: `Idempotency-Key` is unique. A retry returns the original result | Signing one challenge twice is paying twice |
| **I8** | **Sandbox and mainnet are absolutely isolated.** The network is fixed at creation and immutable; API keys are prefixed `lk_test_` / `lk_live_`; the signer refuses a challenge whose network differs; the RPC endpoint is chosen per record | A test key spends real money — a disaster of both trust and money |

## Four axes that must never be merged

The deck is explicit, and it is worth repeating because collapsing two of these into one enum is
the most tempting bad idea in the data model:

```
allowance state   pending · active · exhausted · expired · revoked
payment state     signed · submitted · confirmed · failed · unknown
verdict           allowed · blocked
network           sandbox · mainnet
```

They are independent. A `blocked` verdict produces no payment at all, so it has no payment state.
An `unknown` payment has a perfectly healthy allowance. `exhausted` is derived, not stored. And
`network` is orthogonal to all three. **Merging any two of them is a data-model bug**, not a
simplification.

---

## I1 · Non-custodial with the treasury

**The rule.** Delegate, top-up and revoke are signed by the owner's wallet, in the owner's
browser. Leash builds the transaction, unsigned, and hands it over. Leash never receives an owner
private key, a seed phrase, or a signed blank.

**Why the wording is careful.** Leash *does* hold agent keys — that is the product. What it must
never hold is a key that can move the owner's money outside an allowance. The distinction is the
whole legal and architectural position, and the blast radius of losing an agent key is bounded by
one allowance's remaining balance ([12-security.md](12-security.md)).

**Mechanism.**

- The signing of owner transactions happens in the wallet adapter, in the browser. The API's job
  ends at returning base64.
- `internal/kms` — the only package that can decrypt an agent key — is imported by
  `internal/signer` and `cmd/leash-signer` and by nothing else. **`cmd/leash-indexer` must not
  reach it transitively**, asserted by `leashctl verify-architecture` over the real package graph.
- **Nothing under `frontend/website` may import a KMS client at all**, asserted by
  `dependency-cruiser`. The deployable that serves the dashboard and the API contains no key
  material and no means of decrypting any — a compile fact rather than a reviewer's opinion.
- A CI job greps the repository for anything shaped like a committed owner secret.
- Both gates have self-tests, and **the self-tests run first**. A gate that has never been
  watched reject something is a comment: settlement's equivalent once passed everything because a
  regex error made `grep` exit non-zero, which reads identically to "no match".

**Where it shows in the interface.** The sign-in screen: *"Connecting only reads your address.
Nothing moves without a transaction you sign."* And the wallet modal: *"Leash never asks for your
seed phrase."*

## I2 · The chain is the source of truth for the budget

**The rule.** The database holds a cache of on-chain state, and that cache carries the slot it
was read at. Any number the interface displays about money must be able to name its slot.

**Why.** A budget number that came from our own arithmetic rather than from the chain is a number
we made up. If it drifts high, the owner believes in a control that is not there.

**Mechanism.**

- `allowances.last_read_slot` and `last_read_at`, written only by the indexer.
- The schema validator refuses an allowance in state `active` that has no `last_read_slot` — you
  cannot claim the chain agrees without saying when you asked.
- The dashboard shows a **slot chip** in the top bar at all times, and the allowance card states
  `Read at slot 356 442 108`.
- When the read-back falls behind, the **degraded banner** appears — *"On-chain data is lagging
  14s behind — balances may be stale. Payments are unaffected."* Hiding that banner for
  aesthetics is listed in [10-ui-spec.md](10-ui-spec.md) as forbidden.

## I3 · Honest enforcement tiering

**The rule.** Some rules the chain enforces and nobody can bypass. Others the signer enforces,
which means they hold exactly as long as the signer is the only route to a signature. The
interface must always say which is which.

| Tier | Rules | Holds when Leash is down? |
|---|---|---|
| **Hard — on-chain** | cap (S5's ceiling), revoke (S7's landing) | **Yes.** Solana enforces it with or without us |
| **Soft — at the signer** | network match (S0), allowance liveness (S1), allow-list (S2), per-transaction max (S3), velocity (S4), terms (S6), kill flag (S7 before the revoke lands), **and expiry** | No — but an unreachable signer signs nothing, so the failure is safe |

**Expiry is in the soft column, and that is a change from the deck.** The SPL delegate
implementation has no expiry; only the cap and the revoke are on-chain. Under this invariant we
are not permitted to draw it as an on-chain rule anyway. See
[08-onchain-adapter.md](08-onchain-adapter.md) and §B-4 of the conformance record.

**Mechanism.** The tier is **data, not a constant.** `allowances.expiry_tier` is stored, and the
interface renders from it. A hardcoded tier table in the frontend would be a lie the moment the
adapter changed, and the adapter is expected to change.

**The visual language**, from the prototype, used everywhere a policy is shown:

```
solid border  + filled  green dot   =  enforced by Solana
dashed border + hollow  amber dot   =  enforced by the Leash signer
```

Omitting the label is on the reject-merge-if list in [15-roadmap.md](15-roadmap.md).

## I4 · Never report success early

**The rule.** Every write to the chain passes through a waiting state. The only exit is a
read-back **by signature**. A retry that skips the read is forbidden. `unknown` is a first-class
state and is not `failed`.

**Why.** The alternative is inferring "confirmed" from an HTTP response. An endpoint that returns
200 and then goes silent, a timeout after a successful broadcast, an RPC that lags — each
produces a state where the money may or may not have moved. Guessing "failed" and retrying is how
one challenge gets paid twice.

**Mechanism.**

- `payments.state` runs `signed → submitted → confirmed`, with `failed` and `unknown` as
  first-class outcomes. Every exit from `signed`, `submitted` or `unknown` is a read-back.
- **Only the indexer may write `confirmed`, and only with a slot.** This is a schema constraint,
  not a convention: `handshake_events` refuses a `confirmed` row whose `writer` is not `indexer`
  or whose `read_back_slot` is missing.
- `payment-sweep` re-checks by signature on a cadence, for as long as it takes. Past 24 hours
  with no trace it becomes `failed`, with an alert.
- `verify-architecture` asserts the sweep package never calls a signing function.
- The interface has a dedicated `unknown` presentation, and the copy is fixed:
  > This payment timed out before we saw a result. It is neither failed nor confirmed — we keep
  > checking the chain, and it will never be signed twice.

**The subtlety worth knowing.** The signing path deliberately runs without a database
transaction, for latency reasons argued in [06-signer.md](06-signer.md). It is nonetheless safe
across a crash between signing and recording, because Ed25519 is deterministic and the blockhash
is pinned into the claim record — so re-signing the same challenge produces **byte-identical**
output. There is one signature, produced twice, not two signatures.

## I5 · The kill switch survives Leash being down

**The rule.** Revoking an allowance is an owner-signed transaction straight to Solana. It does
not pass through us, and the instructions for doing it without us are public.

**Why.** A kill switch that needs its vendor to be healthy is not a kill switch. The scenario it
exists for — a leaked agent key, a runaway loop — has no reason to avoid coinciding with our
outage.

**Mechanism.**

- [if-leash-is-down.md](if-leash-is-down.md) is a public page, written for a customer, with the
  wallet and CLI steps to revoke without touching Leash. It is kept correct as the adapter
  changes — in particular it must include the **sweep** as well as the revoke, or an owner
  following it strands their own balance ([08-onchain-adapter.md](08-onchain-adapter.md)).
- The Settings screen carries the section *"If Leash is ever down"* and links to it. The deck
  requires that page verbatim; [10-ui-spec.md](10-ui-spec.md) reproduces the copy.
- The kill switch is two-tier and the interface says so, in this order: *"Instantly: the signer
  refuses every new payment from this agent. On-chain: your wallet signs a revoke — once
  confirmed, the allowance is gone even if Leash is offline."*

**The window between the two tiers is the nature of the chain, not a bug.** The interface must
keep saying `revoking…` until the read-back lands. Saying "revoked" early is specifically called
out as the trap in flow P5.

## I6 · Money is an integer base unit

**The rule.** Micro-USDC, as an integer, everywhere. USDC has six decimal places, so one dollar
is 1,000,000. No floating point in any money path, at any layer.

**Mechanism.**

- `money.Base` is an `int64` with hand-written codecs. It marshals to BSON as `int64` only.
- Reading a `double` or an `int32` back **fails loudly rather than coercing.** A double in the
  database means money has already been lost and a coercion would hide it; an `int32` means
  something wrote a raw document and bypassed the schema. Both are incidents, not values.
- On the wire it is a **string**, in both directions. `{"amount":"0.31"}` is accepted;
  `{"amount":0.31}` is rejected with `INVALID_AMOUNT`, because JSON numbers are IEEE doubles.
- `bsonType: "long"` in every validator, on every money field and every slot. This is the actual
  guarantee — it holds regardless of which driver, which shell session, or which future
  contributor wrote the document.
- A schema `maximum` of 10^15 micro-USDC on every amount, so an aggregation `$sum` in the budget
  comparison can never reach the range where it promotes to a double.
- `verify-architecture` fails the build on `float64`, `strconv.ParseFloat`, or a
  `map[string]any` decode anywhere in a money path, and on a log line that renders an amount as
  an integer rather than through its string form.

**In the interface**: no amount is ever a JavaScript `number`. The cap input is
`type="text"`, not `type="number"`.

## I7 · Idempotency is enforced at storage

**The rule.** Uniqueness that matters is a database constraint, not an application check. A
retry returns the original result rather than doing the work again.

| Path | Key | Constraint |
|---|---|---|
| Outward — an agent retrying `/v1/sign` | `challenge_hash` | `sign_claims._id`, and `sign_requests.challenge_hash` unique |
| Inward — the dashboard retrying a create | `Idempotency-Key` | `agents.idempotency_key` unique |
| One challenge, one payment | `sign_request_id` | `payments.sign_request_id` unique |
| One phase, once | `(payment_id, phase)` | compound unique |

**Why storage and not code.** Two signer processes, or one process retried by a load balancer,
defeat an application-level check the first time they race. A unique index does not race.

**The claim is separate from the verdict.** `sign_claims` exists as its own collection because
the verdict is not known at the moment you must win the race, and `sign_requests` is append-only.
Trying to make one document be both the race token and the immutable record is where this design
goes wrong. See [06-signer.md](06-signer.md).

## I8 · Sandbox and mainnet are absolutely isolated

**The rule.** The network is attached to an agent, an allowance, a sign request and a payment at
the moment it is created, and it can never change. Keys are prefixed. The signer refuses a
challenge whose network differs from the key's. The RPC endpoint comes from the record.

**Why.** A `lk_test_` key that spends real USDC is the worst incident this product can have. It
is simultaneously a money loss and a total loss of trust, and it is the kind of bug that arrives
through a helpful default.

**Mechanism** — this is the invariant with the most machinery behind it, deliberately.

- **The network is part of the identifier.** `agt_test_01J8XK…` and `agt_live_01J8XK…`. MongoDB
  refuses any update that modifies `_id`, so the network component of identity is immutable at
  the storage engine — **stronger than the trigger the deck asked for**, and visible to the naked
  eye in a URL, a log line and a support ticket.
- A validator pins the `network` field to the `_id` prefix, so the two cannot drift apart. An
  update setting `network: "mainnet"` on a `agt_test_…` document fails with
  `DocumentFailedValidation`. There is no aggregation-pipeline update or `$rename` that gets
  around it.
- The same validator on `allowances`, `payments`, `sign_requests` and `handshake_events` ties the
  **referenced** identifier's prefix to the row's own network — the cross-collection check that
  PostgreSQL could only have had as a composite foreign key.
- **Every network-scoped store method takes `network.Network` as a parameter**, asserted on the
  AST by `verify-architecture`. This is how *"a query not filtered by network rejects the pull
  request"* stops being a checklist item nobody remembers.
- **There is deliberately no `RPC_URL`.** Only `RPC_SANDBOX_URL` and `RPC_MAINNET_URL`, with
  fallbacks. A configuration test reads the config source and fails if a generic one ever
  appears, because a single global endpoint is the one variable capable of sending a sandbox
  payment to mainnet.
- Background jobs are spawned **once per network**, with the network bound in the closure and in
  every log line. A job that iterates networks internally is one `if` away from reading a sandbox
  record with a mainnet client.
- API keys carry the prefix, and S0 checks it before anything else runs.

**In the interface**: the environment chip and the sandbox banner are always visible. A screen on
which you cannot tell which network you are looking at is forbidden.

**The evidence we present**: an `lk_test_` key plus a mainnet challenge returns
`403 NETWORK_MISMATCH` and produces zero transactions.
