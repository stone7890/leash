import { NextRequest } from "next/server";
import { requireOwner, unauthenticated, json, fail, Unauthenticated } from "@/lib/auth/require";
import { getAgent } from "@/lib/store/queries";
import { suppressAlert } from "@/lib/store/writes";

export const dynamic = "force-dynamic";

/**
 * "This block is correct."
 *
 * It silences the ALERT for one (agent, host) pair and does not stop the blocking. The interface
 * says so in as many words, because a customer who believes they whitelisted the host will
 * otherwise be confused by the next refusal — and a safety product that quietly stops refusing is
 * the worst possible outcome of a button labelled "correct".
 */
export async function POST(req: NextRequest, ctx: { params: Promise<{ id: string }> }) {
  try {
    const caller = await requireOwner();
    const { id } = await ctx.params;

    const agent = await getAgent(caller.org, id);
    if (!agent) return fail("NOT_FOUND", "not found", 404);

    const body = (await req.json().catch(() => null)) as { host?: string } | null;
    const host = body?.host?.trim().toLowerCase();
    if (!host) return fail("MALFORMED_REQUEST", "a host is required", 400, { field: "host" });

    await suppressAlert(agent.id, host);
    return json({ host, alerts: "silenced", blocking: "unchanged" }, 201);
  } catch (e) {
    if (e instanceof Unauthenticated) return unauthenticated();
    throw e;
  }
}
