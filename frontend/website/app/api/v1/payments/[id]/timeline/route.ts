import { signRequestResponse, timelineEventResponse } from "@/lib/api/shapes";
import { requireOwner, unauthenticated, json, Unauthenticated } from "@/lib/auth/require";
import { timeline, verdictFor } from "@/lib/store/queries";

export const dynamic = "force-dynamic";

export async function GET(_req: Request, ctx: { params: Promise<{ id: string }> }) {
  try {
    await requireOwner();
    const { id } = await ctx.params;
    // A phase that has not happened is ABSENT, not null. The interface renders absence differently
    // for a blocked payment ("not reached") and one still in flight (a spinner) — and phase 4
    // happens inside the customer's agent, where we cannot see it at all.
    return json({
      payment_id: id,
      phases: (await timeline(id)).map(timelineEventResponse),
      verdict: signRequestResponse(await verdictFor(id)),
    });
  } catch (e) {
    if (e instanceof Unauthenticated) return unauthenticated();
    throw e;
  }
}
