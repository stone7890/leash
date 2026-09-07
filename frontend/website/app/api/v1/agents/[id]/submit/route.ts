import { NextRequest } from "next/server";
import { requireOwner, unauthenticated, json, fail, Unauthenticated } from "@/lib/auth/require";
import { getAgent, liveAllowance } from "@/lib/store/queries";
import { submitSigned, ServiceError } from "@/lib/services";
import { createPendingAllowance } from "@/lib/store/writes";
import { parseMoney } from "@/lib/domain/money";
import type { Tier } from "@/lib/domain/state";

export const dynamic = "force-dynamic";

/**
 * Broadcast the delegation the owner has just signed, and record the allowance as PENDING.
 *
 * Pending is the whole point. The allowance becomes active only when the indexer reads the account
 * back and writes the slot it read it at — the schema refuses an active allowance without one — so
 * there is no code path here, or anywhere, that can shortcut to "live" (I4).
 */
export async function POST(req: NextRequest, ctx: { params: Promise<{ id: string }> }) {
  try {
    const caller = await requireOwner();
    const { id } = await ctx.params;

    const agent = await getAgent(caller.org, id);
    if (!agent) return fail("NOT_FOUND", "not found", 404);

    const body = (await req.json().catch(() => null)) as {
      transaction?: string;
      allowance_account?: string;
      mint?: string;
      token_program?: string;
      cap?: string;
      expiry_days?: number;
      expiry_tier?: Tier;
    } | null;
    if (!body?.transaction || !body.allowance_account || !body.mint) {
      return fail("MALFORMED_REQUEST", "a signed transaction and its account are required", 400);
    }

    if (await liveAllowance(agent.id)) {
      return fail("ALLOWANCE_ALREADY_LIVE", "this agent already has a live allowance", 409);
    }

    let cap: bigint;
    try {
      cap = parseMoney(body.cap ?? "10.000000");
    } catch {
      return fail("INVALID_AMOUNT", "the cap is not a decimal amount", 422, { field: "cap" });
    }

    let signature: string;
    try {
      ({ signature } = await submitSigned(body.transaction, agent.network));
    } catch (e) {
      const message = e instanceof ServiceError ? e.message : "the chain refused the transaction";
      // A wallet rejection never reaches here — the browser catches that. This is the chain
      // itself refusing, and nothing was created.
      return fail("CHAIN_UNREACHABLE", message, 422, { retriable: true });
    }

    await createPendingAllowance({
      agentId: agent.id,
      network: agent.network,
      onchainAddr: body.allowance_account,
      mint: body.mint,
      tokenProgram: body.token_program ?? "TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA",
      cap,
      expiryDays: body.expiry_days ?? 7,
      // From the intent's summary, which came from the adapter's own capabilities. Never a
      // constant: under the SPL delegate, expiry is signer-enforced (I3).
      expiryTier: body.expiry_tier ?? "signer",
    });

    // 202: submitted is not confirmed. The interface shows `pending` with a spinner and no action
    // until the read-back lands.
    return json({ signature, state: "pending" }, 202);
  } catch (e) {
    if (e instanceof Unauthenticated) return unauthenticated();
    throw e;
  }
}
