import { NextRequest } from "next/server";
import { requireOwner, unauthenticated, json, fail, Unauthenticated } from "@/lib/auth/require";
import { getAgent, liveAllowance } from "@/lib/store/queries";
import { buildIntent, ServiceError } from "@/lib/services";

export const dynamic = "force-dynamic";

/**
 * Build the unsigned delegation for the owner's wallet.
 *
 * Leash never signs this and could not: it is returned as base64 and signed in the browser, by the
 * wallet, under the owner's own key. That is invariant I1, and it is why this endpoint returns a
 * transaction rather than a result.
 *
 * 202, never 200. Nothing has happened on-chain yet, and there is no path by which a client can
 * read "the API returned OK" as "the budget exists".
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

    // One live allowance per agent and mint. The database enforces it too, but refusing here
    // means the owner is told before a wallet prompt rather than after it.
    const live = await liveAllowance(agent.id);
    if (live) {
      return fail("ALLOWANCE_ALREADY_LIVE",
        "this agent already has a live allowance — top it up instead", 409);
    }

    const body = (await req.json().catch(() => null)) as
      { cap?: string; expiry_days?: number } | null;

    const intent = await buildIntent({
      kind: "delegation",
      owner: caller.wallet,
      agent: agent.pubkey,
      network: agent.network,
      cap: body?.cap ?? "10.000000",
      expiry_days: body?.expiry_days ?? 7,
    });

    return json(intent, 202);
  } catch (e) {
    if (e instanceof Unauthenticated) return unauthenticated();
    if (e instanceof ServiceError) {
      return fail(e.code, e.message, e.status === 0 ? 503 : e.status, { retriable: true });
    }
    throw e;
  }
}
