# Flows — the diagrams

Every flow in the system, drawn. [09-flows.md](../09-flows.md) is the prose; this directory is
the picture that goes with it, and the two use **the same N-xx step numbers as the deck**
(`resources/specs/leash-technical-deck-v3-1.html`, chapters 8–11) so that a conversation about
"N-12" means the same thing in the deck, in the prose, in a diagram and in a pull request.

Diagrams are Mermaid, rendered inline by GitHub and by the VS Code preview.

## The six business phases

| | Flow | Steps | What it answers |
|---|---|---|---|
| **P1** | [p1-onboard.md](p1-onboard.md) | N-01 … N-12 | A new wallet to an agent paying a real API, in five steps |
| **P2** | [p2-agent-pays.md](p2-agent-pays.md) | N-10 … N-19 | The 402 handshake — the loop that runs unattended, thousands of times |
| **P3** | [p3-recovery.md](p3-recovery.md) | N-30 … N-35 | A blocked payment, turned from a dead end into a one-tap fix |
| **P4** | [p4-budget-lifecycle.md](p4-budget-lifecycle.md) | N-40 … N-45 | Top up a cap, extend an expiry, with no dead window |
| **P5** | [p5-kill-revoke.md](p5-kill-revoke.md) | N-50 … N-55 | The two-tier kill switch, and the window between the tiers |
| **P6** | [p6-audit-export.md](p6-audit-export.md) | — | Always asynchronous, always `202` |

## The machinery underneath

| Diagram | Covers |
|---|---|
| [lanes.md](lanes.md) | The five components, the lanes, and **the edges that are forbidden** |
| [policy-engine.md](policy-engine.md) | S0–S7 as a decision tree, and the signer's ordered signing path |
| [state-machines.md](state-machines.md) | Allowance, payment, and the six-phase handshake |
| [reconciliation.md](reconciliation.md) | The four indexer loops, and how a payment escapes `unknown` |

## How to read a diagram here

Three conventions, all of them load-bearing:

- **A dashed arrow crosses a boundary Leash does not control.** The agent runtime and the x402
  endpoint belong to other people; Leash cannot make them retry, and does not try.
- **`read-back` is always drawn as its own step.** Never as a return arrow from a submit. That
  distinction is invariant I4, and collapsing it in a diagram is how it gets collapsed in code.
- **Tier is labelled wherever a rule appears** — *on-chain* (hard) or *signer* (soft), per I3.
  A diagram that does not say which tier is enforcing is a diagram that lies about the product.
