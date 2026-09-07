import { NextRequest } from "next/server";
import { requireOwner, unauthenticated, json, fail, Unauthenticated } from "@/lib/auth/require";
import { getAgent, liveAllowance } from "@/lib/store/queries";
import { killAgent } from "@/lib/store/writes";
import { buildIntent, ServiceError } from "@/lib/services";

export const dynamic = "force-dynamic";

/**
 * The kill switch, flow P5. Two tiers, in this order.
 *
 * FIRST the flag: `killed = true` is written before the transaction is even built, so the signer
 * starts refusing while the wallet prompt is still open — and even if the owner then abandons it.
 * A change stream evicts the cached snapshot within milliseconds, because rule S7's promise, the
 * sentence the owner reads before confirming, is that the signer refuses *instantly*.
 *
 * THEN the chain: an owner-signed revoke, which holds whatever happens to Leash afterwards.
 *
 * The window between the two is the nature of the chain, not a bug. The interface keeps saying
 * "revoking…" until the read-back lands, and saying "revoked" before that is the trap the
 * specification names for this flow.
 */
export async function POST(req: NextRequest, ctx: { params: Promise<{ id: string }> }) {
  try {
    const caller = await requireOwner();
    const { id } = await ctx.params;

    if (!req.headers.get("idempotency-key")?.trim()) {
      return fail("MISSING_IDEMPOTENCY_KEY", "an Idempotency-Key header is required", 400);
    }

    const agent = await getAgent(caller.org, id);
    if (!agent) return fail("NOT_FOUND", "not found", 404);

    const allowance = await liveAllowance(agent.id);

    // Instantly, and first. Even if everything below fails, this agent stops spending.
    await killAgent(agent.id);

    if (!allowance) {
      // Nothing on-chain to revoke — the flag is the whole of it.
      return json({ killed: true, transaction: null,
        note: "the signer is already refusing; there is no on-chain allowance to revoke" }, 202);
    }

    const intent = await buildIntent({
      kind: "revoke",
      owner: caller.wallet,
      agent: agent.pubkey,
      network: agent.network,
      allowance_account: allowance.onchainAddr,
      // Two instructions: revoke AND sweep. Revoking alone leaves the remaining balance in an
      // account the owner will not think to look at.
      sweep_to: caller.wallet,
    });

    return json({ killed: true, ...intent }, 202);
  } catch (e) {
    if (e instanceof Unauthenticated) return unauthenticated();
    if (e instanceof ServiceError) {
      // The flag is already set, so the agent is stopped even though the transaction could not be
      // built. Saying so matters: the owner needs to know the soft tier engaged.
      return json({
        killed: true, transaction: null,
        error: { code: e.code, message: e.message, retriable: true },
      }, 202);
    }
    throw e;
  }
}
