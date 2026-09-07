# 10 · Interface specification

**The prototype is the specification.** `resources/specs/leash-app-prototype-v2.html` is a
clickable, QA'd application with nine screens and four modals, and the deck says in as many words
that it is the source of truth for the interface. This chapter holds only what a developer needs
*beyond* opening that file: the tokens, the state rules, the copy that must be used verbatim, and
the handful of places where building it for real changes it.

Stack: Next.js 15 with the App Router, TypeScript, Tailwind, shadcn/ui, `@solana/wallet-adapter`.

## Design tokens

Taken from the prototype and used unchanged.

| Token | Value | For |
|---|---|---|
| `--bg` | `#12101F` | The page |
| `--panel` | `#1A1830` | Cards |
| `--panel2` | `#211E3B` | Raised surfaces |
| `--line` | `#2E2A52` | Borders |
| `--ink` | `#E9E7F4` | Primary text |
| `--mut` | `#9B96BE` | Secondary text |
| `--grn` | `#14F195` | **Money, on-chain, and the primary call to action. Nothing else** |
| `--pur` | `#9C6BFF` | Navigation, focus rings |
| `--amb` | `#FFC14D` | Waiting, and the signer tier |
| `--red` | `#FF6B81` | Danger, blocked |
| `--grn-d` / `--red-d` | `#0E3A2B` / `#3A1620` | Pill backgrounds |
| radius | `10px` | Everything |

**Fonts:** Space Grotesk 400/500/700 for text; **IBM Plex Mono for every amount, address, slot and
key**. A monospace address is not decoration — it is what makes `3xk…9Qe` comparable against a
block explorer at a glance.

The green is reserved. Using it for a generic success state would erode the one signal that means
*this is real money, and the chain agrees*.

## The two-tier visual language

This is invariant I3 made visible, and it is the most important rule in the interface.

```
solid border  + filled  green dot      Enforced by Solana
dashed border + hollow  amber dot      Enforced by the Leash signer
```

It appears **everywhere a policy is shown**: the Rules tab, the onboarding fields, the delegation
review card, the payment timeline. Omitting a tier label is on the reject-merge-if list in
[15-roadmap.md](15-roadmap.md).

**The tier is read from data, never from a constant.** `allowances.expiry_tier` says whether
expiry is chain-enforced, and it is `"signer"` under the shipping SPL implementation
([08-onchain-adapter.md](08-onchain-adapter.md)). A hardcoded table in the frontend would become a
lie the moment the adapter changed, and the adapter is expected to change.

Colour is never the only carrier. Every pill states its word — `active`, `pending`, `blocked`,
`unknown`, `revoked` — and every tier card carries its heading, *"Enforced by Solana"* or
*"Enforced by Leash signer"*.

## The state rules

The **Forbidden** column is what a reviewer checks. It is not advice.

| State | Show | **Forbidden** |
|---|---|---|
| loading / empty | A skeleton, or a call to action to create the first agent | A silent empty table |
| `pending` · `submitted` · `unknown` | A dedicated spinner and *"waiting for on-chain confirmation"* | Success colours or success screens; merging any of them with `confirmed` |
| `blocked` | The timeline stopped at phase 2, the rule's name, and both recovery buttons | Hiding the reason; hiding the next action; a button that adds a wildcard |
| `revoking` | *"revoking…"* until the read-back lands | Saying "revoked" before the read-back — the P5 trap |
| degraded | The banner, with the lag as a number | Hiding the banner because it looks untidy |
| sandbox | The purple chip, the banner, and the faucet link, always visible | **Any screen on which you cannot tell which network you are on** (I8) |

## The nine screens

| Screen | Flows | What must survive the port |
|---|---|---|
| Sign-in + wallet modal | F1 | *"Connecting only reads your address"*; the sandbox-first note |
| Onboarding, five steps | F2, F9 | The wallet-rejection branch; the key shown once; **the step-5 log is real SSE** |
| Dashboard | F3 | The slot chip; `pending` rows have no action; the bar turns red at ≥95% |
| Agent detail, four tabs | F3–F6 | A tier label on every policy (I3); the Add budget and extend buttons |
| Payments explorer + drawer | F3, F4 | Three drawer variants — confirmed, blocked, unknown — on the six-phase timeline |
| Alerts · Audit · Plan · Settings | F7, F8, I5 | Audit returns 202; the *"If Leash is ever down"* section kept verbatim |

### Sign-in

Two columns. The brand side carries the headline — *"Give every agent a budget. Not your
wallet."* — a live gauge of three agents with progress bars, and the footer promise:

> Non-custodial. Your funds stay in your wallet; Leash holds keys to nothing.

The auth card: *"Your wallet is your account. Sign a message to prove it's you — no password, no
email."* and two notes that are not optional:

> Connecting only reads your address. Nothing moves without a transaction you sign.

> First time? You'll start in the sandbox — a test network with play-money USDC. Nothing real
> until you switch.

The wallet modal offers Phantom, Backpack and Solflare, and says: *"Leash never asks for your seed
phrase."*

### The shell

A 216-pixel left rail — Agents · Payments · Alerts · Audit, then a spacer, then Plan · Settings —
collapsing to 64 pixels of icons below 900 pixels wide.

The top bar carries the breadcrumb, the **environment chip** (Sandbox ⇄ Mainnet, clickable), the
**slot chip** (`slot 356 442 108`, titled *"latest indexed slot"*), the wallet chip, and Sign out.

The slot chip is always present. It is invariant I2 made into furniture: the interface can always
answer *"as of when?"*.

Switching to mainnet says: *"Mainnet. Delegations here move real USDC — caps still protect you."*

### Dashboard

Four statistics — spent today, active agents, payments in 24 hours, blocked by rules in 24 hours
— then the agents table and the live feed.

The agents table shows budget as a bar, **red at ≥95%**, with the runtime and endpoints as a
sub-label. **A `pending` row has no action and is not clickable**, and its sub-label reads
*"awaiting confirmation"*. An agent whose delegation has not been read back yet cannot be
operated on, and offering a button that would fail is worse than offering none.

The live feed prepends rows and caps at twelve. Rows open the drawer.

### Agent detail

Four tabs.

**Overview** — three cards. The allowance card has the chain-styled border and the on-chain
badge, shows `$7.42 of $10.00`, and carries **`Read at slot 356 442 108`**. The 24-hour card. The
spend-by-endpoint card, with the burn-rate projection: *"At this pace the cap lasts ~9 more days —
past expiry."*

**Payments** — a table where blocked rows show `not signed` instead of a signature, and a note
explaining `unknown` verbatim.

**Rules** — the two tier cards, side by side. *"Enforced by Solana"* with a solid border:
*"Cannot be bypassed — not by the agent, not by Leash."* *"Enforced by Leash signer"* with a
dashed border: *"Checked before every signature. Applies to the next payment after you save."*

**Agent settings** — the masked API key with **Rotate key**: *"…rotating it invalidates the old
one immediately."* The new key toast: *"New key generated. Copy it now — it will not be shown
again."*

### The payment drawer — three variants

**Confirmed.** The key-value block, then the six-phase timeline with its slots and timings.

**Blocked.** `Result: blocked — nothing was signed, nothing moved`. The timeline stops at phase 2:
*"Blocked by rule: endpoint not allowed"* — *"api.unknown.xyz is not on this agent's allow list ·
5 other checks not reached"*. Then the two recovery buttons:

- **Add api.unknown.xyz to allow list** → *"…added to research-bot-01's allow list. In effect from
  the next payment."*
- **This block is correct** → *"Marked as expected block. We won't alert on this endpoint again."*

The second button silences the alert and **does not stop the blocking**. The interface must say
so, because a customer who believes they whitelisted the host will be confused by the next
refusal.

**Unknown.** Phases 1–2 complete, phase 3 spinning: *"Outcome unknown — re-checking Solana"* —
*"endpoint went silent after signing · next read-back in 4 min"*.

### Alerts · Audit · Plan · Settings

Alerts: the thresholds, the three checkboxes, Telegram and webhook delivery, and the note *"Alerts
fire from the indexer within 30 seconds of the on-chain event they describe."* — a promise the
cadence in [07-indexer-and-jobs.md](07-indexer-and-jobs.md) has to keep.

Audit: a date range and **Prepare export**. Always 202. The exports table flips `preparing` →
`ready`.

Plan: Free, Pro at $29, Team at $149, billed on-chain in USDC through a subscription the owner
approves from their wallet.

**Settings** carries the section the deck requires verbatim:

> ### If Leash is ever down
>
> Your control doesn't depend on us. Every allowance can be revoked directly from your wallet or
> the Solana CLI — no Leash involved.
>
> Caps and expiries keep being enforced by Solana no matter what happens to Leash. Only
> signer-level rules pause if the signer is unreachable — and an unreachable signer signs
> nothing.

It links to [if-leash-is-down.md](if-leash-is-down.md).

> **One correction is required here.** Under the shipping SPL implementation, expiry is *not*
> enforced by Solana ([08-onchain-adapter.md](08-onchain-adapter.md)). The sentence *"Caps and
> expiries keep being enforced by Solana"* is false as written, and I3 does not permit shipping
> it. It becomes: *"Caps keep being enforced by Solana, and revocation works from your wallet, no
> matter what happens to Leash."* Registered in
> [16-deck-conformance.md](16-deck-conformance.md).

### The modals

**Wallet** · **Faucet** (*"Test USDC on the Surfnet sandbox. No real value — spend it freely."*) ·
**Kill switch** · **Top-up / extend**.

The kill-switch modal states the two tiers in order, and the top-up modal states: *"Raising the
cap or extending expiry is a new transaction your wallet signs. The current allowance keeps
working until it confirms."*

## Copy that must be used verbatim

| Situation | Copy |
|---|---|
| Blocked | *"Leash blocked $0.31 to api.unknown.xyz — not on the allow list. Nothing was signed, nothing moved."* |
| Unknown | *"This payment timed out before we saw a result. It is neither failed nor confirmed — we keep checking the chain, and it will never be signed twice."* |
| Wallet rejected | *"The wallet rejected the transaction. Nothing was created and nothing moved."* |
| Switching to mainnet | *"Mainnet. Delegations here move real USDC — caps still protect you."* |

These four are load-bearing. Each tells a user something true that they would otherwise assume
wrongly, and each was written to close a specific misunderstanding.

## The check count

The prototype's step-5 log and its payment drawer say **"6 of 6 checks"**. The deck's mock of the
same screen says **"7/7"**. The rule set S0–S7 is **eight**.

**Resolution: the interface renders the count from the `checks` array returned by the API**, never
from a literal. `sign_requests.checks` is the authoritative record of what was evaluated
([04-policy-engine.md](04-policy-engine.md)), and rendering from it means the number cannot drift
from the engine again.

The named list in the step-5 log — allow-list, per-payment, velocity, budget, expiry, mint — is
also rendered from the array, so a rule added or removed appears there without a second edit.

## Rules this code follows

Modelled on the rule lists in the sibling repository, and each enforced by something.

- **No amount is ever a JavaScript `number`.** Everything is a string, and the cap input is
  `type="text"`, never `type="number"`. JSON numbers are IEEE doubles, and a spending cap that
  has been through one is not a spending cap (I6).
- **Colour is never the only carrier.** Every status pill states its word.
- **Nothing shows success before a read-back.** Every chain-touching endpoint returns 202, and the
  interface has no code path that renders a confirmed state without a slot (I2, I4).
- **`unknown` offers "Check again", never "Send again".** Offering a resend after a timeout is how
  a challenge gets paid twice.
- **Terminal states hide their actions rather than disabling them.** A disabled button implies it
  could be unlocked.
- **Every policy shows its tier**, read from data (I3).
- **You can always tell which network you are on** (I8).
- **The idempotency key is generated when the form opens**, not when the button is clicked, so two
  clicks create one agent.
- **There is no `NEXT_PUBLIC_*` variable.** Everything is read server-side at runtime, so a
  rebuild is never required to change an endpoint.

## Accessibility

- `@media (prefers-reduced-motion: reduce)` disables every animation and transition. The interface
  is full of spinners for states that legitimately wait; a user who has asked for stillness gets
  the state as text.
- The focus ring is `2px solid var(--pur)`, and it is never removed.
- The rail collapses at 900 pixels. There is no mobile application, and the dashboard is usable
  on a tablet.
- Every status is a word as well as a colour, which is the same rule as above arriving from a
  different direction.
