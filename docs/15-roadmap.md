# 15 · Roadmap

Four weeks, one builder. Each week ships something that can be opened and shown.

## The four weeks

### W1 — The chain works

| | |
|---|---|
| ☐ | **Verify `[Q1]` and the one-delegate finding on day one.** Read the Subscriptions & Allowances IDL; confirm whether SPL `approve` forces a token account per agent ([08-onchain-adapter.md](08-onchain-adapter.md)). Lock the adapter and the fallback |
| ☐ | The repository skeleton, the migrations, and `demo-402` |
| ☐ | Delegate and revoke working on Surfnet |
| ☐ | Read-back activation — `pending → active` only through a slot |

**Output:** create an agent, delegate, revoke in the sandbox. The explorer and the dashboard agree
on all three states.

### W2 — The loop works

| | |
|---|---|
| ☐ | Signer: S0, S1, S5, S6 |
| ☐ | The P2 loop end to end against `demo-402` |
| ☐ | `handshake_events` and the timeline endpoint |
| ☐ | A mainnet smoke test with $1 |

**Output:** the nine-line test payment runs as real SSE inside onboarding, and the drawer shows a
six-phase timeline.

### W3 — The controls work

| | |
|---|---|
| ☐ | S2, S3, S4, S7, and the kill switch |
| ☐ | Recovery: `allow-hosts` and suppressions |
| ☐ | P4 modify intents — top-up and extend |
| ☐ | Alerts, the audit 202, and the sweep |

**Output:** a video of blocked → one tap → the next attempt passes. And a cap going $10 → $20 with
no dead window.

### W4 — It is a product

| | |
|---|---|
| ☐ | Every state in the [10-ui-spec.md](10-ui-spec.md) table, ported |
| ☐ | [if-leash-is-down.md](if-leash-is-down.md), correct and public |
| ☐ | An end-to-end MCP demonstration on mainnet |
| ☐ | The pitch |

**Output:** a brand-new wallet to an agent paying a real API within a $10 budget, in under ten
minutes of setup.

## Definition of done

- The W4 demonstration runs, from a wallet that did not exist that morning.
- Every collection matches [03-data-model.md](03-data-model.md).
- `make e2e-sandbox` is green in CI.
- The four pieces of evidence below can be produced on demand.

## The evidence

Three of the four are negative claims, which are the ones worth proving.

| Claim | The proof |
|---|---|
| **Hard enforcement is real** | An over-cap draw is rejected **by the chain** — not by our policy engine. Visible in an explorer |
| **We never pay twice** | The same challenge submitted twice yields exactly one signature |
| **We are non-custodial** | No code path in the repository receives an owner key. Every treasury transaction has the owner's wallet as its signer |
| **The networks are isolated** | An `lk_test_` key with a mainnet challenge returns `403 NETWORK_MISMATCH`, and zero transactions exist anywhere |

Run the first one first for anyone sceptical that the cap is more than a policy check.

## Reject the merge if

A reviewer rejects by citing a line here. These are not style preferences.

- **A float appears anywhere in a money path** (I6).
- **A chain write happens outside the signer or the owner's wallet** (I1).
- **A success state renders without its read-back slot** (I2, I4).
- **An outbound HTTP call appears in the signing path** ([04-policy-engine.md](04-policy-engine.md)).
- **A policy is displayed without its tier label** (I3).
- **A `payments` or `allowances` query is not filtered by network** (I8).
- **A recovery button adds more than exactly one host** (P3).
- **A handshake phase is written before the verdict is returned** ([06-signer.md](06-signer.md)).
- **`unknown` is merged with `failed`, or with `confirmed`** (I4).
- **A verification gate is added without a self-test**, or its self-test does not run first.

### Two pull-request rules

1. **Any pull request touching a money path must name the invariant it relates to** in its
   description. Not a formality: writing "this touches I5" is the moment you notice it also
   touches I4.
2. **Any pull request changing `internal/chain` must attach the results of `make e2e-sandbox`.**
   That package is where a wrong assumption costs money rather than a test failure.

## Risks

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| **SPL `approve` forces a token account per agent** | high — it is how the instruction works | High. Changes the onboarding copy, adds per-agent rent, makes revoke two instructions, and makes the revoke guide's correctness a money question | Designed for in [08-onchain-adapter.md](08-onchain-adapter.md). **Confirm on day one**, beside `[Q1]` |
| `[Q1]` — the primitive's real account layout differs from the assumption | medium | High, and it blocks W1 | The SPL fallback is designed and is what we ship. A controlled downgrade, not a delay |
| `reserved_base` drifts | medium | Medium to high — a refused payment, or an admitted one that should not have been | The validator refuses over-admission; the 60-second refresh repairs and logs the delta ([04-policy-engine.md](04-policy-engine.md)) |
| The hosting tier forbids custom database roles | medium | High — append-only becomes application-enforced only, a materially weaker claim | **Verify the tier before W1 ends.** If it cannot, say so publicly rather than quietly |
| `[Q5]` — Surfnet has no stable public RPC | medium | Medium. Breaks sandbox-first | Fall back to devnet with its USDC faucet. The adapter is URL-agnostic, so it is a configuration change |
| `[Q4]` — x402 canonicalisation drifts between versions | medium | Medium. It is the idempotency key | Key by `(version, hash)`; refuse an unknown version rather than guessing ([11-errors-and-codes.md](11-errors-and-codes.md)) |
| Two runtimes drift on the wire contract | medium | Medium | `contracts/` is committed and both sides are checked against it ([13-testing.md](13-testing.md)) |
| Change-stream resume tokens expire across a deploy | low | Medium — a stalled onboarding wizard | Size the oplog; the client falls back to polling |
| An agent key leaks | low | Bounded by that allowance's remainder | KMS, one key per agent, the runbook. **This bound is the product** |
| The four new chapters delay W1 | medium | Medium | `demo-402` and the faucet are a day between them. Cut optional items first, **never the P1 test payment** |

## Open questions

| # | Question | Decider · by | Blocks |
|---|---|---|---|
| **new** | Does SPL `approve` force a token account per agent, and does the S&A primitive avoid it? | Founder · W1 day 1 | The adapter, onboarding copy, the revoke guide, rent |
| **Q1** | The real instruction and account layout of the Subscriptions & Allowances primitive | Founder · W1 day 1 | The adapter, N-06 and N-08, P4 |
| **Q2** | Which facilitator verifies and settles for `demo-402` — self-verify, or Kora? | Founder · before W2 | The P1 test loop, the W2 happy path |
| **Q4** | Which fields enter the canonical hash across x402 versions? | Founder · W2, with real vectors | I7 on the external path |
| **Q5** | A public Surfnet RPC and faucet for our users, or do we run our own instance? | Founder · W1 | Sandbox-first, N-01 |
| **new** | Does the hosting tier support custom database roles? | Founder · W1 | The append-only claim |

**Q3 is resolved:** FIFO by signing time, operationally defined against the `sign_requests` ULID
([04-policy-engine.md](04-policy-engine.md)).

## Out of scope, and staying that way

Custody · fiat on-ramp and off-ramp · KYC and KYB · confidential amounts and human payroll (V2) ·
EVM, Base and multi-chain · a marketplace or endpoint discovery · merchant billing · mobile ·
multi-user roles · the self-hosted signer sidecar (V2).

If work starts on any of these, either it moved and [16-deck-conformance.md](16-deck-conformance.md)
was not updated, or it did not move.
