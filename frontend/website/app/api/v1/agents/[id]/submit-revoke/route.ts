import { NextRequest } from "next/server";
import { requireOwner, unauthenticated, json, fail, Unauthenticated } from "@/lib/auth/require";
import { getAgent } from "@/lib/store/queries";
import { submitSigned, ServiceError } from "@/lib/services";

export const dynamic = "force-dynamic";

/**
 * Broadcast the owner-signed revoke.
 *
 * The allowance is NOT marked revoked here. It becomes revoked when the indexer reads the account
 * back and finds the delegation gone — telling an owner that a compromised agent is contained,
 * before the chain agrees, is the trap flow P5 names.
 */
export async function POST(req: NextRequest, ctx: { params: Promise<{ id: string }> }) {
  try {
    const caller = await requireOwner();
    const { id } = await ctx.params;

    const agent = await getAgent(caller.org, id);
    if (!agent) return fail("NOT_FOUND", "not found", 404);

    const body = (await req.json().catch(() => null)) as { transaction?: string } | null;
    if (!body?.transaction) {
      return fail("MALFORMED_REQUEST", "a signed transaction is required", 400);
    }

    const { signature } = await submitSigned(body.transaction, agent.network);
    // 202 and `revoking`: submitted is not confirmed, and the interface keeps saying so.
    return json({ signature, state: "revoking" }, 202);
  } catch (e) {
    if (e instanceof Unauthenticated) return unauthenticated();
    if (e instanceof ServiceError) return fail(e.code, e.message, 422, { retriable: true });
    throw e;
  }
}
