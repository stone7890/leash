import { NextRequest } from "next/server";
import { requireOwner, unauthenticated, json, fail, Unauthenticated } from "@/lib/auth/require";
import { getAgent } from "@/lib/store/queries";
import { startTestPayment, ServiceError } from "@/lib/services";

export const dynamic = "force-dynamic";

/**
 * Onboarding step 5.
 *
 * It runs the REAL P2 loop against the sample endpoint: a real 402, a real evaluation of the eight
 * rules, a real signature, a real settlement. The wizard then streams the handshake rows those
 * components wrote.
 *
 * The specification is emphatic that this is the most often botched step, and about why. A
 * scripted animation here would be a demonstration that the product works, shown to a user for
 * whom it might not.
 */
export async function POST(req: NextRequest) {
  try {
    const caller = await requireOwner();

    if (!req.headers.get("idempotency-key")?.trim()) {
      return fail("MISSING_IDEMPOTENCY_KEY", "an Idempotency-Key header is required", 400);
    }

    const body = (await req.json().catch(() => null)) as
      { agent_id?: string; api_key?: string } | null;
    if (!body?.agent_id || !body.api_key) {
      return fail("MALFORMED_REQUEST", "an agent and its API key are required", 400);
    }

    const agent = await getAgent(caller.org, body.agent_id);
    if (!agent) return fail("NOT_FOUND", "not found", 404);
    if (agent.network !== "sandbox") {
      // Not forbidden — nonsensical. A "test" payment on mainnet would spend real money.
      return fail("MAINNET_ONLY_OPERATION", "a test payment runs on the sandbox only", 422);
    }

    const { payment_id } = await startTestPayment(body.api_key, agent.network);
    return json({ payment_id }, 202);
  } catch (e) {
    if (e instanceof Unauthenticated) return unauthenticated();
    if (e instanceof ServiceError) {
      return fail(e.code, e.message, 422, { retriable: false });
    }
    throw e;
  }
}
