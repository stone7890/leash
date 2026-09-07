import { requireOwner, unauthenticated, json, fail, Unauthenticated } from "@/lib/auth/require";
import { getAgent, getPolicy, liveAllowance, listPayments } from "@/lib/store/queries";
import { agentResponse, allowanceResponse, paymentResponse, policyResponse } from "@/lib/api/shapes";

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
      ...agentResponse(agent),
      policy: policyResponse(await getPolicy(agent.id)),
      allowance: allowanceResponse(await liveAllowance(agent.id)),
      payments: (await listPayments(caller.org, agent.network, { agentId: agent.id, limit: 50 }))
        .map(paymentResponse),
    });
  } catch (e) {
    if (e instanceof Unauthenticated) return unauthenticated();
    throw e;
  }
}
