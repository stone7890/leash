import { NextRequest } from "next/server";
import { requireOwner, unauthenticated, json, fail, Unauthenticated } from "@/lib/auth/require";
import { getAgent, getPolicy } from "@/lib/store/queries";
import { updatePolicyLimits } from "@/lib/store/writes";
import { parseMoney } from "@/lib/domain/money";
import { policyResponse } from "@/lib/api/shapes";

export const dynamic = "force-dynamic";

/**
 * The two signer-tier limits an owner can change after the fact.
 *
 * `contracts/endpoints.json` has carried this path since the beginning; the implementation is new.
 * Until now the only way to change a per-payment maximum was to create another agent, which is a
 * strange thing to ask of somebody whose agent is merely too tightly capped.
 *
 * What it does NOT touch: the on-chain cap and the expiry. Those live in the SPL delegation and
 * changing them means a transaction the owner signs in their wallet — no PATCH can move them, and
 * pretending otherwise would put a signer-tier promise on a screen that says "enforced by Solana".
 */
export async function PATCH(req: NextRequest, ctx: { params: Promise<{ id: string }> }) {
  try {
    const caller = await requireOwner();
    const { id } = await ctx.params;

    const agent = await getAgent(caller.org, id);
    if (!agent) return fail("NOT_FOUND", "not found", 404);

    const body = (await req.json().catch(() => null)) as {
      per_tx_max?: string; velocity_max?: string; velocity_window_s?: number;
    } | null;
    if (!body) return fail("MALFORMED_REQUEST", "a body is required", 400);

    const current = await getPolicy(agent.id);
    if (!current) return fail("NOT_FOUND", "this agent has no policy", 404);

    // Absent means unchanged, and that is not the same as zero: a PATCH that silently reset the
    // velocity limit because the caller only meant to raise the per-payment one would be a
    // widening nobody asked for.
    // Parsed one at a time, so the `field` in a 422 names the amount that was actually wrong. One
    // try around both reports whichever was merely PRESENT, which sends a caller to fix a value
    // that parsed perfectly well.
    let perTxMax = current.perTxMax;
    let velocityMax = current.velocityMax;
    try {
      if (body.per_tx_max !== undefined) perTxMax = parseMoney(body.per_tx_max);
    } catch (e) {
      return fail("INVALID_AMOUNT", e instanceof Error ? e.message : "invalid amount", 422,
        { field: "per_tx_max" });
    }
    try {
      if (body.velocity_max !== undefined) velocityMax = parseMoney(body.velocity_max);
    } catch (e) {
      return fail("INVALID_AMOUNT", e instanceof Error ? e.message : "invalid amount", 422,
        { field: "velocity_max" });
    }

    let windowS = current.velocityWindowS;
    if (body.velocity_window_s !== undefined) {
      windowS = Number(body.velocity_window_s);
      // The schema's own bounds, checked here so the answer is a sentence rather than a write
      // rejected by the database.
      if (!Number.isInteger(windowS) || windowS < 1 || windowS > 86_400) {
        return fail("INVALID_WINDOW", "the window must be a whole number of seconds between 1 " +
          "and 86400 (24 hours)", 422, { field: "velocity_window_s" });
      }
    }

    // A per-payment maximum above the velocity limit is not an error the database can catch, and
    // it is not harmless: it reads as "up to $1 per payment" on a screen where $0.50 is the real
    // answer, because the window refuses the rest. Refusing it keeps the two numbers honest.
    if (perTxMax > velocityMax) {
      return fail("INVALID_AMOUNT",
        "the per-payment maximum cannot exceed the limit for the whole window — the window would " +
        "refuse the payment the per-payment maximum just allowed", 422, { field: "per_tx_max" });
    }

    const ok = await updatePolicyLimits(
      agent.id, { perTxMax, velocityMax, velocityWindowS: windowS }, caller.wallet,
    );
    if (!ok) return fail("NOT_FOUND", "this agent has no policy", 404);

    return json(policyResponse(await getPolicy(agent.id)), 200);
  } catch (e) {
    if (e instanceof Unauthenticated) return unauthenticated();
    throw e;
  }
}
