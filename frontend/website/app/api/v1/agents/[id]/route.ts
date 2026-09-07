import { requireOwner, unauthenticated, json, fail, Unauthenticated } from "@/lib/auth/require";
import { getAgent, getPolicy, liveAllowance, listPayments } from "@/lib/store/queries";

export const dynamic = "force-dynamic";

export async function GET(_req: Request, ctx: { params: Promise<{ id: string }> }) {
  try {
    const caller = await requireOwner();
    const { id } = await ctx.params;

    const agent = await getAgent(caller.org, id);
    // Another organisation's agent returns 404, never 403. A 403 confirms the identifier exists,
    // which is an enumeration oracle.
    if (!agent) return fail("NOT_FOUND", "not found", 404);

    return json({
      ...agent,
      policy: await getPolicy(agent.id),
      allowance: await liveAllowance(agent.id),
      payments: await listPayments(caller.org, agent.network, { agentId: agent.id, limit: 50 }),
    });
  } catch (e) {
    if (e instanceof Unauthenticated) return unauthenticated();
    throw e;
  }
}
