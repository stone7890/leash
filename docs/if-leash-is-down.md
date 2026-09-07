# If Leash is ever down

**Your control doesn't depend on us.** Every allowance you created can be revoked directly from
your wallet or the Solana CLI, with no part of Leash involved.

This page is public and is written for you, not for our engineers. It is linked from the Settings
screen, and it is kept correct as the product changes.

## What keeps working when we don't

| | Still enforced if Leash is offline? |
|---|---|
| **The spending cap** | **Yes.** Solana enforces it. An agent cannot exceed it, with or without us |
| **Revoking** | **Yes.** It is a transaction you sign, straight to the chain. This page tells you how |
| Allow-list, per-payment maximum, ten-minute limit, expiry | No — but they are checked by our signer before anything is signed, **and a signer that is unreachable signs nothing.** An outage stops payments; it does not let them through |

The short version: **an outage on our side is a stop, not a leak.**

## What to do, in order

If you suspect an agent is misbehaving or its key has leaked and you cannot reach the dashboard:

1. **Revoke the delegation.** Below. This stops the agent immediately and permanently.
2. **Sweep the remaining balance back.** Below, and **do not skip it** — see the warning.
3. Come back to the dashboard when we are up, and rotate the agent's key before reusing it.

## Two instructions, not one

> **Important.** Each agent has its own token account, which you own. Revoking the delegation
> stops the agent from spending — but any balance left in that account **stays there** until you
> move it back.
>
> Revoking without sweeping does not lose your money, but it does leave it somewhere you are
> unlikely to look. Do both.

## Revoke and sweep, with the Solana CLI

You need the wallet that created the allowance.

**Find the agent's token account.** It is shown on the agent's Overview tab as the allowance
address, and it is in your audit export. If you have neither, list your token accounts:

```bash
spl-token accounts --owner <YOUR_WALLET> --verbose
```

**Revoke the delegation.** This is the step that stops the agent.

```bash
spl-token revoke <AGENT_TOKEN_ACCOUNT> --owner <YOUR_WALLET>
```

From this moment the agent cannot spend, whatever it does, and whoever holds its key.

**Sweep the balance back.**

```bash
# what is left
spl-token balance --address <AGENT_TOKEN_ACCOUNT>

# move it to your main USDC account
spl-token transfer <USDC_MINT> <AMOUNT> <YOUR_MAIN_USDC_ACCOUNT> \
  --from <AGENT_TOKEN_ACCOUNT> --owner <YOUR_WALLET>
```

**Optional — close the account** and recover its rent, once it is empty:

```bash
spl-token close --address <AGENT_TOKEN_ACCOUNT> --owner <YOUR_WALLET>
```

Do not close an account you intend to reuse for the same agent.

## Revoke from a wallet, without the CLI

Phantom, Backpack and Solflare all list token approvals and offer to revoke them.

1. Open your wallet and find its **token approvals** or **connected apps / delegations** section.
   The wording differs between wallets.
2. Find the approval on the agent's token account. The delegate is the agent's public key, shown
   on the agent's page in Leash and in your audit export.
3. Revoke it.
4. Then send the remaining balance from that account back to your main account, as an ordinary
   transfer.

A wallet's revoke handles step 1 above. **It does not do the sweep** — that is an ordinary
transfer you make yourself.

## Which network?

Sandbox and mainnet are entirely separate, and an agent belongs to exactly one for its whole life.
You can tell from the agent's identifier:

```
agt_test_…    sandbox   — play money. Nothing here is real
agt_live_…    mainnet   — real USDC
```

Point the CLI at the matching cluster with `--url`. **A sandbox agent cannot hold real money**, so
if you are dealing with an incident, look for `agt_live_`.

## How to check it worked

```bash
spl-token account-info --address <AGENT_TOKEN_ACCOUNT>
```

The delegate should be gone and the delegated amount zero. Any block explorer shows the same
thing, and the transaction is public.

You do not need us to confirm it. **The chain is the record**, and that is the entire point of
this page.

## Afterwards

When the dashboard is reachable again:

- The agent will show as `revoked` once we have read the chain back. If it still says
  `revoking…`, we simply have not caught up yet — your revoke has already taken effect.
- **Rotate the agent's key** before creating a new allowance for it, if the reason for revoking
  was a suspected leak. An old key cannot spend against a revoked allowance, but there is no
  reason to keep it alive.
- Your audit trail is unchanged. Nothing you did here is invisible to it.

## What we will never ask you for

Your seed phrase. Your private key. A signed blank transaction.

Leash holds no funds and no key of yours, and there is no situation — including an outage,
including a support conversation — in which any of those is the answer. Anyone asking is not us.
